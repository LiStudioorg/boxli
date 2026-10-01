// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compose

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fullComposeYAML 覆盖 Service 的每一个字段（外加顶层 version/name）。
const fullComposeYAML = `
version: "1"
name: demo
services:
  serviceA:
    image: alice/myapp:v1
    command:
      - /bin/sh
      - -c
      - echo hi
    entrypoint:
      - /entry
    environment:
      LOG_LEVEL: debug
      PORT: "8080"
    ports:
      - "8080:80"
      - "9090:90"
    volumes:
      - ./data:/data
    depends_on:
      - serviceB
    restart: unless-stopped
    hostname: app-a
    working_dir: /srv
    user: 1000:1000
    replicas: 3
    labels:
      tier: frontend
    dev:
      watch:
        - ./src
      ignore:
        - ./src/vendor
      rebuild: true
  serviceB:
    build: ./svc-b
    command: ["run", "--fast"]
`

// TestParseProjectFull 断言完整项目的每一个字段都被精确解码。
func TestParseProjectFull(t *testing.T) {
	p, err := ParseProject([]byte(fullComposeYAML))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	if p.Version != "1" {
		t.Fatalf("Version = %q，期望 %q", p.Version, "1")
	}
	if p.Name != "demo" {
		t.Fatalf("Name = %q，期望 %q", p.Name, "demo")
	}
	if len(p.Services) != 2 {
		t.Fatalf("服务数 = %d，期望 2（%v）", len(p.Services), p.Services)
	}

	a := p.Services["serviceA"]
	if a == nil {
		t.Fatal("serviceA 缺失")
	}
	if a.Name != "serviceA" {
		t.Fatalf("serviceA.Name = %q", a.Name)
	}
	if a.Image != "alice/myapp:v1" {
		t.Fatalf("serviceA.Image = %q", a.Image)
	}
	if want := []string{"/bin/sh", "-c", "echo hi"}; !reflect.DeepEqual(a.Command, want) {
		t.Fatalf("serviceA.Command = %#v，期望 %#v", a.Command, want)
	}
	if want := []string{"/entry"}; !reflect.DeepEqual(a.Entrypoint, want) {
		t.Fatalf("serviceA.Entrypoint = %#v，期望 %#v", a.Entrypoint, want)
	}
	if want := map[string]string{"LOG_LEVEL": "debug", "PORT": "8080"}; !reflect.DeepEqual(a.Environment, want) {
		t.Fatalf("serviceA.Environment = %#v，期望 %#v", a.Environment, want)
	}
	if want := []string{"8080:80", "9090:90"}; !reflect.DeepEqual(a.Ports, want) {
		t.Fatalf("serviceA.Ports = %#v，期望 %#v", a.Ports, want)
	}
	if want := []string{"./data:/data"}; !reflect.DeepEqual(a.Volumes, want) {
		t.Fatalf("serviceA.Volumes = %#v，期望 %#v", a.Volumes, want)
	}
	if want := []string{"serviceB"}; !reflect.DeepEqual(a.DependsOn, want) {
		t.Fatalf("serviceA.DependsOn = %#v，期望 %#v", a.DependsOn, want)
	}
	if a.Restart != "unless-stopped" {
		t.Fatalf("serviceA.Restart = %q", a.Restart)
	}
	if a.Hostname != "app-a" {
		t.Fatalf("serviceA.Hostname = %q", a.Hostname)
	}
	if a.WorkingDir != "/srv" {
		t.Fatalf("serviceA.WorkingDir = %q", a.WorkingDir)
	}
	if a.User != "1000:1000" {
		t.Fatalf("serviceA.User = %q", a.User)
	}
	if a.Replicas != 3 {
		t.Fatalf("serviceA.Replicas = %d，期望 3", a.Replicas)
	}
	if want := map[string]string{"tier": "frontend"}; !reflect.DeepEqual(a.Labels, want) {
		t.Fatalf("serviceA.Labels = %#v，期望 %#v", a.Labels, want)
	}
	if a.Dev == nil {
		t.Fatal("serviceA.Dev 为 nil")
	}
	if want := []string{"./src"}; !reflect.DeepEqual(a.Dev.Watch, want) {
		t.Fatalf("serviceA.Dev.Watch = %#v，期望 %#v", a.Dev.Watch, want)
	}
	if want := []string{"./src/vendor"}; !reflect.DeepEqual(a.Dev.Ignore, want) {
		t.Fatalf("serviceA.Dev.Ignore = %#v，期望 %#v", a.Dev.Ignore, want)
	}
	if !a.Dev.Rebuild {
		t.Fatal("serviceA.Dev.Rebuild = false，期望 true")
	}

	b := p.Services["serviceB"]
	if b == nil {
		t.Fatal("serviceB 缺失")
	}
	if b.Build != "./svc-b" {
		t.Fatalf("serviceB.Build = %q", b.Build)
	}
	if b.Image != "" || b.Boxfile != "" {
		t.Fatalf("serviceB 只应设置 build，实际 image=%q boxfile=%q", b.Image, b.Boxfile)
	}
	if want := []string{"run", "--fast"}; !reflect.DeepEqual(b.Command, want) {
		t.Fatalf("serviceB.Command = %#v，期望 %#v", b.Command, want)
	}
	// restart 默认 no，Dev 默认 nil。
	if b.Restart != "no" {
		t.Fatalf("serviceB.Restart = %q，期望默认 %q", b.Restart, "no")
	}
	if b.Dev != nil {
		t.Fatalf("serviceB.Dev = %#v，期望 nil", b.Dev)
	}
}

// TestParseProjectBoxfile 覆盖第三个互斥字段 boxfile。
func TestParseProjectBoxfile(t *testing.T) {
	p, err := ParseProject([]byte("services:\n  app:\n    boxfile: base.boxli\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	if got := p.Services["app"].Boxfile; got != "base.boxli" {
		t.Fatalf("Boxfile = %q，期望 %q", got, "base.boxli")
	}
}

// TestParseProjectEnvironmentListForm 覆盖 environment 的 KEY=VALUE 列表写法。
func TestParseProjectEnvironmentListForm(t *testing.T) {
	p, err := ParseProject([]byte("services:\n  app:\n    image: a/b:1\n    environment:\n      - A=1\n      - B=two\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	want := map[string]string{"A": "1", "B": "two"}
	if got := p.Services["app"].Environment; !reflect.DeepEqual(got, want) {
		t.Fatalf("Environment = %#v，期望 %#v", got, want)
	}
}

// ---------- LoadFile ----------

// TestLoadFileResolvesDirAndName 覆盖 Dir、相对路径展开与项目名默认值。
func TestLoadFileResolvesDirAndName(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "myproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, "boxli-compose.yaml")
	src := "services:\n  app:\n    build: ./ctx\n    dev:\n      watch:\n        - ./src\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile 返回错误: %v", err)
	}
	if p.Dir != projDir {
		t.Fatalf("Dir = %q，期望 %q", p.Dir, projDir)
	}
	if p.Path != path {
		t.Fatalf("Path = %q，期望 %q", p.Path, path)
	}
	// 未声明 name → 取 compose 文件所在目录的 basename。
	if p.Name != "myproj" {
		t.Fatalf("Name = %q，期望 %q（目录 basename）", p.Name, "myproj")
	}

	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	app := order[0]
	if want := filepath.Join(projDir, "ctx"); app.Build != want {
		t.Fatalf("Resolve 的 Build = %q，期望 %q", app.Build, want)
	}
	if app.Dev == nil || len(app.Dev.Watch) != 1 {
		t.Fatalf("Resolve 的 Dev = %#v", app.Dev)
	}
	if want := filepath.Join(projDir, "src"); app.Dev.Watch[0] != want {
		t.Fatalf("Resolve 的 Dev.Watch[0] = %q，期望 %q", app.Dev.Watch[0], want)
	}
}

// TestLoadFileExplicitNameWins 确认显式 name 不被目录名覆盖。
func TestLoadFileExplicitNameWins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dirname")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "boxli-compose.yaml")
	if err := os.WriteFile(path, []byte("name: explicit\nservices:\n  app:\n    image: a/b:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile 返回错误: %v", err)
	}
	if p.Name != "explicit" {
		t.Fatalf("Name = %q，期望 %q", p.Name, "explicit")
	}
}

// TestLoadFileMissing 确认缺失文件返回错误。
func TestLoadFileMissing(t *testing.T) {
	_, err := LoadFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("缺失文件未报错")
	}
}

// ---------- Validate ----------

// TestValidateCollectsAllProblems 确认 Validate 一次性汇总所有问题且逐条带服务名。
func TestValidateCollectsAllProblems(t *testing.T) {
	// badBoth: image 与 build 同时设置；badRestart: restart 非法；
	// badDep: depends_on 指向不存在的服务。
	src := "" +
		"services:\n" +
		"  badBoth:\n" +
		"    image: alice/app:v1\n" +
		"    build: ./ctx\n" +
		"  badRestart:\n" +
		"    image: alice/app:v1\n" +
		"    restart: sometimes\n" +
		"  badDep:\n" +
		"    image: alice/app:v1\n" +
		"    depends_on:\n" +
		"      - ghost\n"
	p, err := ParseProject([]byte(src))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	err = p.Validate()
	if err == nil {
		t.Fatal("Validate 未报错")
	}
	msg := err.Error()
	// 必须一次性报出全部三个问题，而不是只报第一个。
	for _, name := range []string{"badBoth", "badRestart", "badDep"} {
		if !strings.Contains(msg, name) {
			t.Fatalf("Validate 错误未提到服务 %s:\n%s", name, msg)
		}
	}
	if !strings.Contains(msg, "ghost") {
		t.Fatalf("Validate 错误未提到缺失依赖 ghost:\n%s", msg)
	}
	if !errors.Is(err, ErrBadProject) {
		t.Fatalf("Validate 错误 = %v，期望可匹配 ErrBadProject", err)
	}
}

// TestValidateCleanProject 确认合法项目零问题。
func TestValidateCleanProject(t *testing.T) {
	p, err := ParseProject([]byte(fullComposeYAML))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("合法项目 Validate 报错: %v", err)
	}
}

// TestValidateServiceTargets 覆盖 image/boxfile/build 三选一的缺失与多选。
func TestValidateServiceTargets(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"三选一都未设置",
			"services:\n  app:\n    restart: always\n",
			"必须设置 image、boxfile、build 之一",
		},
		{
			"image 与 boxfile 同时设置",
			"services:\n  app:\n    image: a/b:1\n    boxfile: b.boxli\n",
			"只能三选一",
		},
		{
			"非法镜像引用缺少版本",
			"services:\n  app:\n    image: a/b\n",
			"image=",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseProject([]byte(tc.src))
			if err != nil {
				t.Fatalf("ParseProject 返回错误: %v", err)
			}
			err = p.Validate()
			if err == nil {
				t.Fatal("Validate 未报错")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate 错误 = %v，期望包含 %q", err, tc.want)
			}
		})
	}
}

// TestValidateReplicasAndDev 覆盖 replicas 与 dev.watch 的取值校验。
func TestValidateReplicasAndDev(t *testing.T) {
	p, err := ParseProject([]byte("services:\n  app:\n    image: a/b:1\n    replicas: 0\n    dev:\n      rebuild: true\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	err = p.Validate()
	if err == nil {
		t.Fatal("dev 缺少 watch 时 Validate 未报错")
	}
	if !strings.Contains(err.Error(), "dev.watch") {
		t.Fatalf("Validate 错误 = %v，期望提到 dev.watch", err)
	}
}

// ---------- Resolve ----------

// TestResolveChainOrder 覆盖 3 服务链的拓扑顺序（被依赖者在前）。
func TestResolveChainOrder(t *testing.T) {
	src := "" +
		"services:\n" +
		"  c:\n" +
		"    image: a/c:1\n" +
		"    depends_on:\n" +
		"      - b\n" +
		"  b:\n" +
		"    image: a/b:1\n" +
		"    depends_on:\n" +
		"      - a\n" +
		"  a:\n" +
		"    image: a/a:1\n"
	p, err := ParseProject([]byte(src))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(resolvedNames(order), want) {
		t.Fatalf("Resolve 顺序 = %v，期望 %v", resolvedNames(order), want)
	}
	// ResolvedService 必须带原始服务指针与已展开字段。
	if order[0].Service == nil {
		t.Fatal("ResolvedService.Service 为 nil")
	}
	if order[2].Image != "a/c:1" {
		t.Fatalf("order[2].Image = %q", order[2].Image)
	}
}

// TestResolveTieBreakByName 确认同层按服务名字典序，结果完全确定。
func TestResolveTieBreakByName(t *testing.T) {
	src := "" +
		"services:\n" +
		"  zebra:\n" +
		"    image: a/z:1\n" +
		"  alpha:\n" +
		"    image: a/a:1\n" +
		"  middle:\n" +
		"    image: a/m:1\n"
	p, err := ParseProject([]byte(src))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	for i := 0; i < 20; i++ { // 多次运行防 map 迭代顺序泄漏
		order, err := p.Resolve()
		if err != nil {
			t.Fatalf("Resolve 返回错误: %v", err)
		}
		if want := []string{"alpha", "middle", "zebra"}; !reflect.DeepEqual(resolvedNames(order), want) {
			t.Fatalf("第 %d 次 Resolve 顺序 = %v，期望 %v", i, resolvedNames(order), want)
		}
	}
}

// TestResolveTieBreakAfterUnlock 覆盖解锁后进入就绪队列时的字典序重排。
func TestResolveTieBreakAfterUnlock(t *testing.T) {
	// base 被 b 与 a 依赖；base 完成后就绪集合是 {a, b}，必须按名字取 a 先。
	src := "" +
		"services:\n" +
		"  base:\n" +
		"    image: a/base:1\n" +
		"  b:\n" +
		"    image: a/b:1\n" +
		"    depends_on:\n" +
		"      - base\n" +
		"  a:\n" +
		"    image: a/a:1\n" +
		"    depends_on:\n" +
		"      - base\n"
	p, err := ParseProject([]byte(src))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	if want := []string{"base", "a", "b"}; !reflect.DeepEqual(resolvedNames(order), want) {
		t.Fatalf("Resolve 顺序 = %v，期望 %v", resolvedNames(order), want)
	}
}

// TestResolveCycle 确认 depends_on 成环返回 ErrCycle。
func TestResolveCycle(t *testing.T) {
	src := "" +
		"services:\n" +
		"  a:\n" +
		"    image: a/a:1\n" +
		"    depends_on:\n" +
		"      - b\n" +
		"  b:\n" +
		"    image: a/b:1\n" +
		"    depends_on:\n" +
		"      - a\n"
	p, err := ParseProject([]byte(src))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err == nil {
		t.Fatalf("成环未报错，返回 %v", resolvedNames(order))
	}
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("成环错误 = %v，期望可匹配 ErrCycle", err)
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("成环错误未列出涉及服务: %v", err)
	}
}

// TestResolveSelfDependency 确认自依赖不会让 Resolve 死循环。
func TestResolveSelfDependency(t *testing.T) {
	p, err := ParseProject([]byte("services:\n  a:\n    image: a/a:1\n    depends_on:\n      - a\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	if want := []string{"a"}; !reflect.DeepEqual(resolvedNames(order), want) {
		t.Fatalf("Resolve 顺序 = %v，期望 %v", resolvedNames(order), want)
	}
	if err := p.Validate(); err == nil {
		t.Fatal("自依赖未在 Validate 中报错")
	}
}

// TestResolveUnknownDependencyIgnored 确认指向未知服务的依赖不会卡住排序
// （该问题由 Validate 负责报告）。
func TestResolveUnknownDependencyIgnored(t *testing.T) {
	p, err := ParseProject([]byte("services:\n  a:\n    image: a/a:1\n    depends_on:\n      - ghost\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	if want := []string{"a"}; !reflect.DeepEqual(resolvedNames(order), want) {
		t.Fatalf("Resolve 顺序 = %v，期望 %v", resolvedNames(order), want)
	}
}

// TestResolveDependsOnDedupSorted 确认 Resolve 结果的 DependsOn 去重且有序。
func TestResolveDependsOnDedupSorted(t *testing.T) {
	src := "" +
		"services:\n" +
		"  top:\n" +
		"    image: a/t:1\n" +
		"    depends_on:\n" +
		"      - zeta\n" +
		"      - alpha\n" +
		"      - zeta\n" +
		"  zeta:\n" +
		"    image: a/z:1\n" +
		"  alpha:\n" +
		"    image: a/a:1\n"
	p, err := ParseProject([]byte(src))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	var top *ResolvedService
	for _, r := range order {
		if r.Name == "top" {
			top = r
		}
	}
	if top == nil {
		t.Fatal("top 缺失")
	}
	if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(top.DependsOn, want) {
		t.Fatalf("DependsOn = %#v，期望去重升序 %#v", top.DependsOn, want)
	}
}

// TestResolveIsolatesInternalState 确认 Resolve 返回的切片是副本，改动不影响项目。
func TestResolveIsolatesInternalState(t *testing.T) {
	p, err := ParseProject([]byte("services:\n  app:\n    image: a/b:1\n    command:\n      - one\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	order[0].Command[0] = "mutated"
	if p.Services["app"].Command[0] != "one" {
		t.Fatal("Resolve 返回的切片与项目内部状态共享底层数组")
	}
}

// ---------- ReverseOrder ----------

// TestReverseOrder 确认停止顺序恰好是启动顺序的逆序。
func TestReverseOrder(t *testing.T) {
	src := "" +
		"services:\n" +
		"  c:\n" +
		"    image: a/c:1\n" +
		"    depends_on:\n" +
		"      - b\n" +
		"  b:\n" +
		"    image: a/b:1\n" +
		"    depends_on:\n" +
		"      - a\n" +
		"  a:\n" +
		"    image: a/a:1\n" +
		"  d:\n" +
		"    image: a/d:1\n"
	p, err := ParseProject([]byte(src))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	up, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	down := p.ReverseOrder()
	if len(down) != len(up) {
		t.Fatalf("ReverseOrder 长度 = %d，Resolve 长度 = %d", len(down), len(up))
	}
	want := make([]string, 0, len(up))
	for i := len(up) - 1; i >= 0; i-- {
		want = append(want, up[i].Name)
	}
	if got := resolvedNames(down); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReverseOrder = %v，期望 Resolve 的逆序 %v", got, want)
	}
}

// TestReverseOrderNilProject 确认 nil 项目不会 panic。
func TestReverseOrderNilProject(t *testing.T) {
	var p *Project
	if got := p.ReverseOrder(); got != nil {
		t.Fatalf("nil 项目的 ReverseOrder = %#v，期望 nil", got)
	}
}

// ---------- 严格键检查 ----------

// TestParseProjectErrors 覆盖未知键、结构错误与非法取值。
func TestParseProjectErrors(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		contains string
	}{
		{
			"未知顶层键",
			"version: \"1\"\nservices:\n  app:\n    image: a/b:1\nvolumes:\n  data: {}\n",
			"未知键",
		},
		{
			"未知服务键",
			"services:\n  app:\n    image: a/b:1\n    privileged: true\n",
			"未知键",
		},
		{
			"未知 dev 键",
			"services:\n  app:\n    image: a/b:1\n    dev:\n      polling: true\n",
			"未知键",
		},
		{
			"顶层不是映射",
			"- a\n- b\n",
			"顶层必须是映射",
		},
		{
			"services 不是映射",
			"services:\n  - app\n",
			"services 必须是映射",
		},
		{
			"服务定义不是映射",
			"services:\n  app: just-a-string\n",
			"必须是映射",
		},
		{
			"services 为空",
			"version: \"1\"\n",
			"services 不能为空",
		},
		{
			"文件为空",
			"",
			"compose 文件为空",
		},
		{
			"replicas 不是整数",
			"services:\n  app:\n    image: a/b:1\n    replicas: many\n",
			"需要整数",
		},
		{
			"dev.rebuild 不是布尔",
			"services:\n  app:\n    image: a/b:1\n    dev:\n      watch:\n        - ./src\n      rebuild: sometimes\n",
			"rebuild 必须是布尔值",
		},
		{
			"environment 列表项缺少等号",
			"services:\n  app:\n    image: a/b:1\n    environment:\n      - NOEQUALS\n",
			"KEY=VALUE",
		},
		{
			"command 是映射",
			"services:\n  app:\n    image: a/b:1\n    command:\n      a: b\n",
			"需要标量或标量序列",
		},
		{
			"项目名非法",
			"name: Bad Name\nservices:\n  app:\n    image: a/b:1\n",
			"非法字符",
		},
		{
			"服务名非法",
			"services:\n  app one:\n    image: a/b:1\n",
			"非法字符",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseProject([]byte(tc.src))
			if err == nil {
				t.Fatalf("ParseProject(%q) 未报错，返回 %#v", tc.src, p)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("ParseProject 错误 = %v，期望包含 %q", err, tc.contains)
			}
			if !errors.Is(err, ErrBadProject) {
				t.Fatalf("错误 = %v，期望可匹配 ErrBadProject", err)
			}
		})
	}
}

// TestParseProjectEmptyDocIsNil 确认只有注释的 compose 内容被视为空文件而非顶层类型错误。
func TestParseProjectEmptyDocIsNil(t *testing.T) {
	_, err := ParseProject([]byte("# 只有注释\n"))
	if err == nil {
		t.Fatal("空 compose 未报错")
	}
	if !strings.Contains(err.Error(), "compose 文件为空") {
		t.Fatalf("错误 = %v，期望提示 compose 文件为空", err)
	}
}

// ---------- 服务名/镜像引用校验 ----------

// TestValidateImageRef 覆盖镜像引用的接受与拒绝规则。
func TestValidateImageRef(t *testing.T) {
	valid := []string{"alice/myapp:v1", "a:b", "app:1.2.3", "reg.example.com/ns/app:latest", "a_b/c.d-e:v1_2"}
	for _, ref := range valid {
		if err := ValidateImageRef(ref); err != nil {
			t.Errorf("ValidateImageRef(%q) = %v，期望通过", ref, err)
		}
	}
	invalid := []string{"", "a/b", ":v1", "App:v1", "a b:v1", "a/b:", "a/b:v 1", "-a/b:v1", "a/b/v:1.2.3"}
	for _, ref := range invalid {
		if err := ValidateImageRef(ref); err == nil {
			t.Errorf("ValidateImageRef(%q) 未报错", ref)
		}
	}
}

// TestResolveRestartDefaultsToNo 确认未声明 restart 时解析结果填默认值 no。
func TestResolveRestartDefaultsToNo(t *testing.T) {
	p, err := ParseProject([]byte("services:\n  app:\n    image: a/b:1\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	order, err := p.Resolve()
	if err != nil {
		t.Fatalf("Resolve 返回错误: %v", err)
	}
	if order[0].Restart != "no" {
		t.Fatalf("Restart = %q，期望 %q", order[0].Restart, "no")
	}
}

// TestValidateRestartOptions 覆盖四种合法 restart 与一种非法值。
func TestValidateRestartOptions(t *testing.T) {
	for _, r := range []string{"no", "always", "unless-stopped", "on-failure"} {
		src := "services:\n  app:\n    image: a/b:1\n    restart: " + r + "\n"
		p, err := ParseProject([]byte(src))
		if err != nil {
			t.Fatalf("restart=%s ParseProject 返回错误: %v", r, err)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("restart=%s Validate 返回错误: %v", r, err)
		}
	}
	p, err := ParseProject([]byte("services:\n  app:\n    image: a/b:1\n    restart: sometimes\n"))
	if err != nil {
		t.Fatalf("ParseProject 返回错误: %v", err)
	}
	if err := p.Validate(); err == nil {
		t.Fatal("非法 restart 未报错")
	}
}

// resolvedNames 提取服务名序列，便于断言顺序。
func resolvedNames(in []*ResolvedService) []string {
	out := make([]string, 0, len(in))
	for _, r := range in {
		out = append(out, r.Name)
	}
	return out
}
