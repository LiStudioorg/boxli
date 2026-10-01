// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package compose 解析 Boxli 的自研 compose 文件（零第三方依赖的 YAML 子集）
// 并提供项目模型、严格校验与依赖拓扑排序。
//
// 设计原则与 internal/image 一致：严格解析、未知键一律拒绝、禁止"尽力猜测"；
// 所有错误都用 fmt.Errorf 包装上下文，并可用 errors.Is 匹配本包的哨兵。
//
// 典型用法：
//
//	p, err := compose.LoadFile("boxli-compose.yaml")
//	if err != nil {
//		return err
//	}
//	if err := p.Validate(); err != nil {
//		return err
//	}
//	order, err := p.Resolve() // 按依赖拓扑顺序启动
package compose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 复用的哨兵别名，保证 errors.Is(err, ErrBadProject) 与
// errors.Is(err, ErrBadService) 在项目级、服务级错误上语义一致。
var (
	// ErrBadImageRef 表示服务字段 image 的镜像引用不合法。
	ErrBadImageRef = ErrBadProject

	// ErrServiceNotFound 表示 depends_on 引用了项目里不存在的服务。
	ErrServiceNotFound = ErrBadProject

	// errListSeparator 仅用于错误聚合的可读性，不参与判定。
	_ = errors.Is
)

// 命名规则来自 internal/image：镜像名 ^[a-z0-9][a-z0-9._/-]{0,254}$，版本名 ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$。
const (
	maxImageNameLen = 255
	maxImageTagLen  = 128
	maxServiceName  = 255
)

// 支持的顶层键（严格：不在此列的键一律报错）。
var allowedTopKeys = map[string]bool{
	"version":  true,
	"name":     true,
	"services": true,
}

// 支持的服务键（严格：不在此列的键一律报错）。
var allowedServiceKeys = map[string]bool{
	"image":       true,
	"boxfile":     true,
	"build":       true,
	"command":     true,
	"entrypoint":  true,
	"environment": true,
	"ports":       true,
	"volumes":     true,
	"depends_on":  true,
	"restart":     true,
	"hostname":    true,
	"working_dir": true,
	"user":        true,
	"dev":         true,
	"replicas":    true,
	"labels":      true,
}

// 支持的 dev 块键（严格）。
var allowedDevKeys = map[string]bool{
	"watch":   true,
	"ignore":  true,
	"rebuild": true,
}

// 支持的 restart 策略，与 store.Restart 取值一致。
var allowedRestart = map[string]bool{
	"no":             true,
	"always":         true,
	"unless-stopped": true,
	"on-failure":     true,
}

// Project 是一个已解析的 compose 项目。
type Project struct {
	// Version 是 compose 文件声明的格式版本，例如 "1"；为空表示未声明。
	Version string `yaml:"version"`
	// Name 是项目名，默认取 compose 文件所在目录的 basename。
	Name string `yaml:"name"`
	// Services 是服务表，键为服务名。
	Services map[string]*Service `yaml:"services"`
	// Path 是 compose 文件路径；通过 ParseProject 构造时为空。
	Path string `yaml:"-"`
	// Dir 是 compose 文件所在目录，服务里的相对路径都相对它解析。
	// 通过 ParseProject 构造时为空，此时 Dir 为 "."。
	Dir string `yaml:"-"`
}

// Service 是 compose 文件里的一个服务定义。
type Service struct {
	// Name 是服务名（Services 映射的键），解析后回填，便于 Resolve 结果自描述。
	Name string `yaml:"-"`
	// Image 是引用的 .boxli 镜像，形如 name:version；与 boxfile/build 三选一。
	Image string `yaml:"image"`
	// Boxfile 是 .boxli 镜像文件路径（相对 compose 文件所在目录）；三选一。
	Boxfile string `yaml:"boxfile"`
	// Build 是构建上下文路径（相对 compose 文件所在目录）；三选一。
	Build string `yaml:"build"`
	// Command 覆盖镜像的启动命令，为空表示沿用镜像内配置。
	Command []string `yaml:"command"`
	// Entrypoint 覆盖镜像的入口点，为空表示沿用镜像内配置。
	Entrypoint []string `yaml:"entrypoint"`
	// Environment 是注入容器的环境变量。
	Environment map[string]string `yaml:"environment"`
	// Ports 是端口映射，形如 "8080:80"。
	Ports []string `yaml:"ports"`
	// Volumes 是卷挂载，形如 "./data:/data"。
	Volumes []string `yaml:"volumes"`
	// DependsOn 是本服务启动前必须先启动的服务名列表。
	DependsOn []string `yaml:"depends_on"`
	// Restart 是重启策略，取值 no|always|unless-stopped|on-failure；默认 no。
	Restart string `yaml:"restart"`
	// Hostname 是容器 UTS 名。
	Hostname string `yaml:"hostname"`
	// WorkingDir 是容器工作目录。
	WorkingDir string `yaml:"working_dir"`
	// User 是容器内运行身份。
	User string `yaml:"user"`
	// Dev 是开发模式（文件监听 + 自动重建）配置，nil 表示未开启。
	Dev *DevSpec `yaml:"dev"`
	// Replicas 是副本数，0 或 1 表示单副本。
	Replicas int `yaml:"replicas"`
	// Labels 是附加到容器的标签。
	Labels map[string]string `yaml:"labels"`
}

// DevSpec 描述服务的开发模式：监听哪些路径、忽略哪些路径、是否自动重建。
type DevSpec struct {
	// Watch 是要监听的路径列表（相对 compose 文件所在目录）。
	Watch []string `yaml:"watch"`
	// Ignore 是监听时要忽略的路径列表。
	Ignore []string `yaml:"ignore"`
	// Rebuild 表示文件变化时是否重建镜像。
	Rebuild bool `yaml:"rebuild"`
}

// ResolvedService 是解析后的服务视图：路径已相对 compose 文件目录展开，可直接驱动运行。
type ResolvedService struct {
	// Name 是服务名。
	Name string
	// Image 是镜像引用 name:version；仅当服务声明了 image 时非空。
	Image string
	// Boxfile 是 .boxli 文件路径：声明了就是绝对路径（或相对 Dir 的干净路径），
	// 未声明时为空。
	Boxfile string
	// Build 是构建上下文路径：声明了就是绝对路径（或相对 Dir 的干净路径），未声明时为空。
	Build string
	// Command 是启动命令覆盖。
	Command []string
	// Entrypoint 是入口点覆盖。
	Entrypoint []string
	// Environment 是环境变量。
	Environment map[string]string
	// Ports 是端口映射。
	Ports []string
	// Volumes 是卷挂载。
	Volumes []string
	// DependsOn 是依赖的服务名（去重后按字典序）。
	DependsOn []string
	// Restart 是重启策略（已填默认值 no）。
	Restart string
	// Hostname 是容器 UTS 名。
	Hostname string
	// WorkingDir 是容器工作目录。
	WorkingDir string
	// User 是容器内运行身份。
	User string
	// Dev 是开发模式配置，nil 表示未开启。
	Dev *DevSpec
	// Replicas 是副本数。
	Replicas int
	// Labels 是标签。
	Labels map[string]string
	// Service 指回原始服务定义，方便调用方读取未展开的字段。
	Service *Service
}

// LoadFile 读取并解析 compose 文件，路径已相对文件所在目录展开。
// 文件不存在或非法时返回包装后的错误。
func LoadFile(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 compose 文件 %s: %w", path, err)
	}
	p, err := ParseProject(data)
	if err != nil {
		return nil, fmt.Errorf("解析 compose 文件 %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析 compose 文件路径 %s: %w", path, err)
	}
	p.Path = abs
	p.Dir = filepath.Dir(abs)
	if p.Name == "" {
		if base := filepath.Base(p.Dir); base != "" && base != "." && base != string(filepath.Separator) {
			p.Name = base
		}
	}
	if p.Name == "" {
		p.Name = "boxli-compose"
	}
	return p, nil
}

// ParseProject 解析 compose 文件内容；未知顶层键、未知服务键都返回错误。
// 返回的项目 Dir 为空（等价于当前目录），Name 可能为空（未声明 name 且无文件路径）。
func ParseProject(data []byte) (*Project, error) {
	root, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("compose 文件为空: %w", ErrBadProject)
	}
	m, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("compose 文件顶层必须是映射，实际是 %T: %w", root, ErrBadProject)
	}
	p := &Project{Dir: "."}
	for _, k := range sortedKeys(m) {
		v := m[k]
		if !allowedTopKeys[k] {
			return nil, unknownKeyErr("顶层", k, sortedKeys(m), ErrBadProject)
		}
		switch k {
		case "version":
			s, err := scalarString(v)
			if err != nil {
				return nil, fmt.Errorf("version: %w", err)
			}
			p.Version = s
		case "name":
			s, err := scalarString(v)
			if err != nil {
				return nil, fmt.Errorf("name: %w", err)
			}
			if s != "" {
				if err := validateProjectName(s); err != nil {
					return nil, err
				}
			}
			p.Name = s
		case "services":
			svcs, err := parseServices(v)
			if err != nil {
				return nil, err
			}
			p.Services = svcs
		}
	}
	if len(p.Services) == 0 {
		return nil, fmt.Errorf("services 不能为空: %w", ErrBadProject)
	}
	return p, nil
}

// parseServices 解析 services 映射，逐个服务做严格字段检查。
func parseServices(v any) (map[string]*Service, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("services 必须是映射，实际是 %T: %w", v, ErrBadProject)
	}
	out := make(map[string]*Service, len(m))
	for _, name := range sortedKeys(m) {
		raw := m[name]
		sm, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("服务 %s 的定义必须是映射，实际是 %T: %w", name, raw, ErrBadService)
		}
		svc, err := parseService(name, sm)
		if err != nil {
			return nil, err
		}
		out[name] = svc
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("services 不能为空: %w", ErrBadProject)
	}
	return out, nil
}

// parseService 解析单个服务定义；未知键一律报错并提示可用键。
func parseService(name string, m map[string]any) (*Service, error) {
	if err := validateServiceName(name); err != nil {
		return nil, err
	}
	svc := &Service{Name: name}
	for _, k := range sortedKeys(m) {
		v := m[k]
		if !allowedServiceKeys[k] {
			return nil, unknownKeyErr(fmt.Sprintf("服务 %s", name), k, sortedKeys(m), ErrBadService)
		}
		var err error
		switch k {
		case "image":
			svc.Image, err = scalarString(v)
		case "boxfile":
			svc.Boxfile, err = scalarString(v)
		case "build":
			svc.Build, err = scalarString(v)
		case "command":
			svc.Command, err = stringList(k, v)
		case "entrypoint":
			svc.Entrypoint, err = stringList(k, v)
		case "environment":
			svc.Environment, err = stringMap(k, v)
		case "ports":
			svc.Ports, err = stringList(k, v)
		case "volumes":
			svc.Volumes, err = stringList(k, v)
		case "depends_on":
			svc.DependsOn, err = stringList(k, v)
		case "restart":
			svc.Restart, err = scalarString(v)
		case "hostname":
			svc.Hostname, err = scalarString(v)
		case "working_dir":
			svc.WorkingDir, err = scalarString(v)
		case "user":
			svc.User, err = scalarString(v)
		case "dev":
			svc.Dev, err = parseDev(name, v)
		case "replicas":
			svc.Replicas, err = scalarInt(v)
		case "labels":
			svc.Labels, err = stringMap(k, v)
		}
		if err != nil {
			return nil, fmt.Errorf("服务 %s 的 %s: %w", name, k, err)
		}
	}
	if svc.Restart == "" {
		svc.Restart = "no"
	}
	return svc, nil
}

// parseDev 解析 dev 块（严格字段集）。
func parseDev(svcName string, v any) (*DevSpec, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("dev 必须是映射，实际是 %T: %w", v, ErrBadService)
	}
	d := &DevSpec{}
	for _, k := range sortedKeys(m) {
		raw := m[k]
		if !allowedDevKeys[k] {
			return nil, unknownKeyErr(fmt.Sprintf("服务 %s 的 dev", svcName), k, sortedKeys(m), ErrBadService)
		}
		switch k {
		case "watch":
			l, err := stringList(k, raw)
			if err != nil {
				return nil, err
			}
			d.Watch = l
		case "ignore":
			l, err := stringList(k, raw)
			if err != nil {
				return nil, err
			}
			d.Ignore = l
		case "rebuild":
			b, ok := raw.(bool)
			if !ok {
				return nil, fmt.Errorf("rebuild 必须是布尔值，实际是 %T: %w", raw, ErrBadService)
			}
			d.Rebuild = b
		}
	}
	return d, nil
}

// Validate 汇总项目所有问题后一次性返回：错误信息逐条带服务名，
// 用 errors.Is 可匹配 ErrBadProject（含服务级问题的 ErrBadService 别名）。
func (p *Project) Validate() error {
	var problems []string
	if p == nil {
		return fmt.Errorf("项目为空: %w", ErrBadProject)
	}
	if p.Name != "" {
		if err := validateProjectName(p.Name); err != nil {
			problems = append(problems, fmt.Sprintf("项目名 %q 非法: %v", p.Name, err))
		}
	}
	if len(p.Services) == 0 {
		problems = append(problems, "services 为空：至少定义一个服务")
	}
	for _, name := range p.serviceNames() {
		svc := p.Services[name]
		problems = append(problems, p.validateService(name, svc)...)
	}
	if len(problems) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "compose 校验失败，共 %d 项：", len(problems))
	for i, s := range problems {
		fmt.Fprintf(&b, "\n  %d) %s", i+1, s)
	}
	return fmt.Errorf("%s: %w", b.String(), ErrBadProject)
}

// validateService 校验单个服务，返回该服务的问题清单（不含服务名前缀之外的格式）。
func (p *Project) validateService(name string, svc *Service) []string {
	var out []string
	bad := func(format string, args ...any) {
		out = append(out, fmt.Sprintf("服务 %s: %s", name, fmt.Sprintf(format, args...)))
	}
	if svc == nil {
		bad("定义缺失")
		return out
	}
	set := 0
	for _, f := range []struct {
		key string
		val string
	}{{"image", svc.Image}, {"boxfile", svc.Boxfile}, {"build", svc.Build}} {
		if f.val != "" {
			set++
		}
	}
	switch {
	case set == 0:
		bad("必须设置 image、boxfile、build 之一")
	case set > 1:
		bad("image、boxfile、build 只能三选一，当前设置了 %d 个", set)
	}
	if svc.Image != "" {
		if err := ValidateImageRef(svc.Image); err != nil {
			bad("image=%q 非法: %v", svc.Image, err)
		}
	}
	if svc.Boxfile != "" && isDirLike(svc.Boxfile) {
		bad("boxfile=%q 需指向 .boxli 文件", svc.Boxfile)
	}
	if !allowedRestart[svc.Restart] {
		bad("restart=%q 非法（可选 no|always|unless-stopped|on-failure）", svc.Restart)
	}
	if svc.Replicas < 0 {
		bad("replicas=%d 不能为负", svc.Replicas)
	}
	for _, d := range svc.DependsOn {
		if d == name {
			bad("depends_on 不能依赖自身")
			continue
		}
		if _, ok := p.Services[d]; !ok {
			bad("depends_on 引用了不存在的服务 %q", d)
		}
	}
	for _, key := range sortedMapKeys(svc.Environment) {
		if key == "" {
			bad("environment 含空键名")
		}
	}
	if svc.Dev != nil {
		if len(svc.Dev.Watch) == 0 {
			bad("dev.watch 不能为空")
		}
	}
	return out
}

// Resolve 返回按依赖拓扑排序的服务列表：被依赖的服务在前，
// 同层按服务名字典序，保证结果完全确定。depends_on 成环时返回 ErrCycle。
func (p *Project) Resolve() ([]*ResolvedService, error) {
	order, err := p.topologicalOrder()
	if err != nil {
		return nil, err
	}
	out := make([]*ResolvedService, 0, len(order))
	for _, name := range order {
		out = append(out, p.resolveService(name, p.Services[name]))
	}
	return out, nil
}

// ReverseOrder 返回停止顺序（启动顺序的逆序）：先停叶子服务，最后停被依赖的服务。
// 不返回错误：环已在 Resolve/Validate 阶段被拒绝；本方法对环做截断处理，
// 保证调用方永远拿得到一个可用顺序。
func (p *Project) ReverseOrder() []*ResolvedService {
	if p == nil {
		return nil
	}
	order, err := p.topologicalOrder()
	if err != nil {
		order = p.serviceNames()
	}
	out := make([]*ResolvedService, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		out = append(out, p.resolveService(order[i], p.Services[order[i]]))
	}
	return out
}

// resolveService 把服务定义展开为可执行的视图（相对路径按 Dir 解析）。
func (p *Project) resolveService(name string, svc *Service) *ResolvedService {
	r := &ResolvedService{Name: name, Service: svc}
	if svc == nil {
		return r
	}
	r.Image = svc.Image
	r.Boxfile = p.resolvePath(svc.Boxfile)
	r.Build = p.resolvePath(svc.Build)
	r.Command = cloneStrings(svc.Command)
	r.Entrypoint = cloneStrings(svc.Entrypoint)
	r.Environment = cloneStringMap(svc.Environment)
	r.Ports = cloneStrings(svc.Ports)
	r.Volumes = cloneStrings(svc.Volumes)
	r.DependsOn = uniqueSorted(svc.DependsOn)
	r.Restart = svc.Restart
	if r.Restart == "" {
		r.Restart = "no"
	}
	r.Hostname = svc.Hostname
	r.WorkingDir = svc.WorkingDir
	r.User = svc.User
	if svc.Dev != nil {
		dev := &DevSpec{
			Watch:   cloneStrings(svc.Dev.Watch),
			Ignore:  cloneStrings(svc.Dev.Ignore),
			Rebuild: svc.Dev.Rebuild,
		}
		if p.DevWatchesPaths() {
			for i, w := range dev.Watch {
				dev.Watch[i] = p.resolvePath(w)
			}
			for i, ig := range dev.Ignore {
				dev.Ignore[i] = p.resolvePath(ig)
			}
		}
		r.Dev = dev
	}
	r.Replicas = svc.Replicas
	r.Labels = cloneStringMap(svc.Labels)
	return r
}

// DevWatchesPaths 报告服务路径是否按 compose 文件目录展开。
// 恒为 true：Resolve 结果里的相对路径一律已相对 Project.Dir 解析。
func (p *Project) DevWatchesPaths() bool { return true }

// topologicalOrder 返回服务名的确定性拓扑序：Kahn 算法 + 每轮取字典序最小的就绪服务；
// 剩余节点非空即为环，环中服务名按字典序列出。
func (p *Project) topologicalOrder() ([]string, error) {
	names := p.serviceNames()
	indeg := make(map[string]int, len(names))
	dependents := make(map[string][]string, len(names))
	for _, n := range names {
		indeg[n] = 0
	}
	for _, n := range names {
		svc := p.Services[n]
		if svc == nil {
			continue
		}
		for _, d := range uniqueSorted(svc.DependsOn) {
			if _, ok := p.Services[d]; !ok || d == n {
				continue
			}
			indeg[n]++
			dependents[d] = append(dependents[d], n)
		}
	}
	ready := make([]string, 0, len(names))
	for _, n := range names {
		if indeg[n] == 0 {
			ready = append(ready, n)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(names))
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		var freed []string
		for _, dep := range dependents[n] {
			indeg[dep]--
			if indeg[dep] == 0 {
				freed = append(freed, dep)
			}
		}
		if len(freed) > 0 {
			ready = append(ready, freed...)
			sort.Strings(ready)
		}
	}
	if len(order) != len(names) {
		stuck := make([]string, 0, len(names))
		for _, n := range names {
			if indeg[n] > 0 {
				stuck = append(stuck, n)
			}
		}
		return nil, fmt.Errorf("depends_on 存在环，涉及服务 %s: %w",
			strings.Join(stuck, ", "), ErrCycle)
	}
	return order, nil
}

// resolvePath 把服务里的相对路径解析为相对 Project.Dir 的干净路径；空值返回空串。
func (p *Project) resolvePath(s string) string {
	if s == "" {
		return ""
	}
	dir := p.Dir
	if dir == "" {
		dir = "."
	}
	if filepath.IsAbs(s) {
		return filepath.Clean(s)
	}
	return filepath.Clean(filepath.Join(dir, s))
}

// serviceNames 返回按字典序排列的服务名。
func (p *Project) serviceNames() []string {
	if p == nil {
		return nil
	}
	names := make([]string, 0, len(p.Services))
	for n := range p.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ValidateImageRef 校验 name:version 形式的镜像引用，规则与 internal/image 一致：
// 名字 ^[a-z0-9][a-z0-9._/-]{0,254}$，版本 ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$。
func ValidateImageRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("镜像引用为空: %w", ErrBadImageRef)
	}
	name, tag, found := strings.Cut(ref, ":")
	if !found {
		return fmt.Errorf("镜像引用 %q 缺少 :version 部分: %w", ref, ErrBadImageRef)
	}
	if err := validateImageName(name); err != nil {
		return err
	}
	if err := validateImageTag(tag); err != nil {
		return err
	}
	return nil
}

// validateImageName 校验镜像名：小写字母数字开头，其后允许 a-z0-9._/-，最长 255 字节。
func validateImageName(name string) error {
	if name == "" {
		return fmt.Errorf("镜像名为空: %w", ErrBadImageRef)
	}
	if len(name) > maxImageNameLen {
		return fmt.Errorf("镜像名 %q 超过 %d 字节: %w", name, maxImageNameLen, ErrBadImageRef)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '/' || c == '-'
		if !ok {
			return fmt.Errorf("镜像名 %q 含非法字符 %q: %w", name, string(c), ErrBadImageRef)
		}
	}
	if c := name[0]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
		return fmt.Errorf("镜像名 %q 必须以小写字母或数字开头: %w", name, ErrBadImageRef)
	}
	return nil
}

// validateImageTag 校验版本名：字母数字开头，其后允许 A-Za-z0-9._-，最长 128 字节。
func validateImageTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("镜像版本为空: %w", ErrBadImageRef)
	}
	if len(tag) > maxImageTagLen {
		return fmt.Errorf("镜像版本 %q 超过 %d 字节: %w", tag, maxImageTagLen, ErrBadImageRef)
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("镜像版本 %q 含非法字符 %q: %w", tag, string(c), ErrBadImageRef)
		}
	}
	if c := tag[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
		return fmt.Errorf("镜像版本 %q 必须以字母或数字开头: %w", tag, ErrBadImageRef)
	}
	return nil
}

// validateProjectName 校验项目名：字母数字开头，允许 a-z0-9._-，最长 63 字节。
func validateProjectName(name string) error {
	if name == "" || len(name) > 63 {
		return fmt.Errorf("项目名 %q 需为 1..63 字节: %w", name, ErrBadProject)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("项目名 %q 含非法字符 %q（只允许字母数字 . _ -）: %w", name, string(c), ErrBadProject)
		}
	}
	if c := name[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
		return fmt.Errorf("项目名 %q 必须以字母或数字开头: %w", name, ErrBadProject)
	}
	return nil
}

// validateServiceName 校验服务名：字母数字开头，允许 a-z0-9._-，最长 255 字节。
func validateServiceName(name string) error {
	if name == "" || len(name) > maxServiceName {
		return fmt.Errorf("服务名 %q 需为 1..%d 字节: %w", name, maxServiceName, ErrBadService)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("服务名 %q 含非法字符 %q（只允许字母数字 . _ -）: %w", name, string(c), ErrBadService)
		}
	}
	if c := name[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
		return fmt.Errorf("服务名 %q 必须以字母或数字开头: %w", name, ErrBadService)
	}
	return nil
}

// isDirLike 判断路径看起来像目录（以 / 或 . 结尾），boxfile 必须指向文件。
func isDirLike(s string) bool {
	return strings.HasSuffix(s, "/") || s == "." || s == ".."
}

// scalarString 把 YAML 标量转为字符串；映射/序列报类型错误。
// 布尔与数字按 YAML 语义转为字面量，例如 true → "true"。
func scalarString(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	case int64:
		return fmt.Sprintf("%d", t), nil
	case float64:
		return fmt.Sprintf("%v", t), nil
	default:
		return "", fmt.Errorf("需要标量，实际是 %T: %w", v, ErrBadService)
	}
}

// scalarInt 解析非负整数字段；布尔/字符串/浮点等非整数一律报错。
func scalarInt(v any) (int, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case int64:
		if t < 0 || t > 1<<31 {
			return 0, fmt.Errorf("整数 %d 超出范围: %w", t, ErrBadService)
		}
		return int(t), nil
	case float64:
		if t != float64(int64(t)) {
			return 0, fmt.Errorf("需要整数，实际是 %v: %w", t, ErrBadService)
		}
		return int(t), nil
	default:
		return 0, fmt.Errorf("需要整数，实际是 %T: %w", v, ErrBadService)
	}
}

// stringList 把标量或标量序列转为 []string；nil 返回 nil（不是空切片）。
func stringList(key string, v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case []any:
		out := make([]string, 0, len(t))
		for i, it := range t {
			s, err := scalarString(it)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", key, i, err)
			}
			out = append(out, s)
		}
		return out, nil
	case string, bool, int64, float64:
		s, err := scalarString(t)
		if err != nil {
			return nil, err
		}
		return []string{s}, nil
	default:
		return nil, fmt.Errorf("%s 需要标量或标量序列，实际是 %T: %w", key, v, ErrBadService)
	}
}

// stringMap 解析字符串映射：值允许标量，null 值按空串处理（退化为"取父进程环境变量"的语义）。
func stringMap(key string, v any) (map[string]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		out := make(map[string]string, len(t))
		for _, k := range sortedKeys(t) {
			s, err := scalarString(t[k])
			if err != nil {
				return nil, fmt.Errorf("%s[%q]: %w", key, k, err)
			}
			out[k] = s
		}
		return out, nil
	case []any:
		// 兼容 `KEY=VALUE` 列表写法。
		out := make(map[string]string, len(t))
		for i, it := range t {
			s, err := scalarString(it)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", key, i, err)
			}
			k, val, found := strings.Cut(s, "=")
			if !found || k == "" {
				return nil, fmt.Errorf("%s[%d]=%q 需为 KEY=VALUE: %w", key, i, s, ErrBadService)
			}
			out[k] = val
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s 需要映射或 KEY=VALUE 列表，实际是 %T: %w", key, v, ErrBadService)
	}
}

// unknownKeyErr 生成未知键错误，并列出该层级允许的键，便于用户改正。
func unknownKeyErr(where, key string, present []string, sentinel error) error {
	allowed := make([]string, 0, len(allowedTopKeys))
	if strings.HasPrefix(where, "顶层") {
		allowed = append(allowed, sortedKeys(allowedTopKeys)...)
	} else if strings.Contains(where, "dev") {
		allowed = append(allowed, sortedKeys(allowedDevKeys)...)
	} else {
		allowed = append(allowed, sortedKeys(allowedServiceKeys)...)
	}
	return fmt.Errorf("%s 含未知键 %q（此处出现：%s；允许的键：%s）: %w",
		where, key, strings.Join(present, ", "), strings.Join(allowed, ", "), sentinel)
}

// sortedKeys 返回映射的键，按字典序排列（保证报错信息可复现）。
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedMapKeys 只接受 string 值映射，避免与泛型版本冲突。
func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// uniqueSorted 去重并按字典序排序；nil 输入返回 nil。
func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// cloneStrings 复制字符串切片，避免调用方改到项目内部状态。
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

// cloneStringMap 复制字符串映射。
func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// errNotExist 便于调用方判断 compose 文件缺失（保持 errors.Is 语义）。
func errNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
