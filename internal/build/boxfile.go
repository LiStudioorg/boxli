// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Boxfile 的体积与行数上限：Boxfile 是构建描述，不允许无限膨胀。
const (
	// MaxBoxfileBytes 是 Boxfile 的体积上限（1 MiB）。
	MaxBoxfileBytes = 1 << 20
	// MaxBoxfileLines 是 Boxfile 的行数上限。
	MaxBoxfileLines = 4096
)

// 指令操作符。统一大写；解析器只认这些，其余一律拒绝。
const (
	// OpFrom 指定基础镜像，形如 FROM alice/myapp:v1。必须且只能出现一次，且为首条指令。
	OpFrom = "FROM"
	// OpCopy 从构建上下文拷贝文件到 rootfs，形如 COPY src dst，两参数必填。
	OpCopy = "COPY"
	// OpEnv 设置环境变量，支持 ENV KEY=VALUE 与 ENV KEY VALUE 两种写法。
	OpEnv = "ENV"
	// OpWorkdir 设置工作目录，必须是绝对路径。
	OpWorkdir = "WORKDIR"
	// OpEntrypoint 设置入口命令，只接受 JSON 数组形式。
	OpEntrypoint = "ENTRYPOINT"
	// OpCmd 设置默认参数，只接受 JSON 数组形式。
	OpCmd = "CMD"
	// OpExpose 声明暴露端口，形如 EXPOSE 8080/tcp。
	OpExpose = "EXPOSE"
	// OpVolume 声明卷挂载点。
	OpVolume = "VOLUME"
	// OpLabel 设置镜像标签，形如 LABEL key=value。
	OpLabel = "LABEL"
	// OpUser 设置运行用户，形如 USER 1000:1000。
	OpUser = "USER"
	// OpArg 声明构建参数及其默认值，形如 ARG NAME=default；仅供 ${NAME} 展开使用。
	OpArg = "ARG"
	// OpRun 尚未实现：构建期执行命令需要 rootfs 内运行容器（阶段 3 后续项）。
	OpRun = "RUN"
	// OpAdd 尚未实现：远程 tar 自动解包属于网络能力，不在 v1 Boxfile 范围。
	OpAdd = "ADD"
)

// Boxfile 是一份解析完成的构建描述。
type Boxfile struct {
	// From 是基础镜像引用，形如 alice/myapp:v1，与镜像格式的 name:version 一致。
	From string
	// Instructions 是除 FROM 以外的全部指令，严格保持文件中的先后顺序。
	Instructions []Instruction
}

// Instruction 是一条构建指令。
type Instruction struct {
	// Op 是指令操作符，取值为 OpFrom / OpCopy 等大写常量。
	Op string
	// Args 是该指令的独立参数，各指令的个数与含义由解析器保证。
	Args []string
	// Line 是指令在 Boxfile 中的起始行号（从 1 开始，续行取首行行号）。
	Line int
	// Raw 是原始文本（已剥掉注释与续行转义，保留行内多余空白）。
	Raw string
}

// ParseBoxfile 解析内存中的 Boxfile 文本。
// 语法为行式：以 # 开头（允许前导空白）的行为注释，空行忽略，
// 行尾反斜杠表示与下一行拼接为同一条指令。
// 解析严格不做猜测：未知指令、非法参数、未声明的 ${NAME} 一律报错并给出行号。
func ParseBoxfile(data []byte) (*Boxfile, error) {
	if int64(len(data)) > MaxBoxfileBytes {
		return nil, fmt.Errorf("Boxfile %d 字节 > 上限 %d: %w", len(data), MaxBoxfileBytes, ErrBadBoxfile)
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return nil, fmt.Errorf("Boxfile 含空字节: %w", ErrBadBoxfile)
	}
	p := &parser{bf: &Boxfile{Instructions: []Instruction{}}, args: map[string]string{}}
	for _, l := range joinContinuations(string(data)) {
		if err := p.instruction(l); err != nil {
			return nil, err
		}
	}
	if !p.seenFrom {
		return nil, fmt.Errorf("Boxfile 未声明 FROM: %w", ErrNoFrom)
	}
	return p.bf, nil
}

// ParseBoxfileFile 读取并解析指定路径的 Boxfile。
// 读取失败（不存在 / 无权限 / 非普通文件）包装为 ErrBadBoxfile。
func ParseBoxfileFile(path string) (*Boxfile, error) {
	if path == "" {
		return nil, fmt.Errorf("Boxfile 路径为空: %w", ErrBadBoxfile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 Boxfile %s: %w（%w）", path, err, ErrBadBoxfile)
	}
	bf, err := ParseBoxfile(data)
	if err != nil {
		return nil, fmt.Errorf("解析 Boxfile %s: %w", path, err)
	}
	return bf, nil
}

// CheckContext 校验构建上下文：必须是存在且可读的目录。
// COPY 的源路径相对上下文解析，因此任何含 COPY 的构建都必须先通过本检查。
func CheckContext(dir string) error {
	if dir == "" {
		return fmt.Errorf("构建上下文为空: %w", ErrNoContext)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("构建上下文 %s: %w（%w）", dir, err, ErrNoContext)
	}
	if !fi.IsDir() {
		return fmt.Errorf("构建上下文 %s 不是目录: %w", dir, ErrNoContext)
	}
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("构建上下文 %s 不可读: %w（%w）", dir, err, ErrNoContext)
	}
	return f.Close()
}

// ResolveContextPath 把构建上下文内的相对路径解析为宿主绝对路径，并保证不逃逸出上下文。
// 绝对路径、反斜杠路径、以及含 "." / ".." 段的路径一律拒绝。
func ResolveContextPath(contextDir, rel string) (string, error) {
	if err := checkContextRel(rel); err != nil {
		return "", err
	}
	root, err := filepath.Abs(contextDir)
	if err != nil {
		return "", fmt.Errorf("解析构建上下文 %s: %w（%w）", contextDir, err, ErrNoContext)
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	relBack, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("路径 %q 相对 %s: %w（%w）", rel, contextDir, err, ErrNoContext)
	}
	if relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("COPY 源路径 %q 逃逸出构建上下文: %w", rel, ErrNoContext)
	}
	return full, nil
}

// ResolveRootfsPath 把 rootfs 内的相对路径解析为宿主绝对路径（rootfsDir 为宿主机上的 rootfs 目录）。
// 语法与逃逸校验和 ResolveContextPath 一致，仅错误对象换成 BuildOptions 侧关注的上下文错误。
func ResolveRootfsPath(rootfsDir, rel string) (string, error) {
	if err := checkContextRel(rel); err != nil {
		return "", err
	}
	root, err := filepath.Abs(rootfsDir)
	if err != nil {
		return "", fmt.Errorf("解析 rootfs %s: %w（%w）", rootfsDir, err, ErrNoContext)
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	relBack, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("路径 %q 相对 %s: %w（%w）", rel, rootfsDir, err, ErrNoContext)
	}
	if relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("目标路径 %q 逃逸出 rootfs: %w", rel, ErrNoContext)
	}
	return full, nil
}

// checkContextRel 校验构建上下文 / rootfs 内的相对路径：非空、非绝对、无反斜杠、无 "." / ".." 段。
func checkContextRel(rel string) error {
	switch {
	case rel == "":
		return fmt.Errorf("空路径: %w", ErrNoContext)
	case strings.HasPrefix(rel, "/"):
		return fmt.Errorf("路径 %q 必须是相对路径: %w", rel, ErrNoContext)
	case strings.Contains(rel, "\\"):
		return fmt.Errorf("路径 %q 含反斜杠: %w", rel, ErrNoContext)
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "":
			return fmt.Errorf("路径 %q 含空路径段: %w", rel, ErrNoContext)
		case ".", "..":
			return fmt.Errorf("路径 %q 含 %q 段: %w", rel, seg, ErrNoContext)
		}
	}
	return nil
}

// parser 是解析过程的可变状态。
type parser struct {
	bf       *Boxfile
	seenFrom bool
	args     map[string]string
}

// instruction 解析一条逻辑指令行；空行与注释行直接跳过。
func (p *parser) instruction(l logicalLine) error {
	text := strings.TrimSpace(l.text)
	if text == "" || strings.HasPrefix(text, "#") {
		return nil
	}
	op, rest := splitOp(text)
	if op == "" {
		return fmt.Errorf("第 %d 行 %q: 缺少指令名: %w", l.line, l.raw, ErrBadInstruction)
	}
	if err := p.dispatch(l, op, rest); err != nil {
		if l.line <= 0 || strings.HasPrefix(err.Error(), "第 ") {
			return err
		}
		return fmt.Errorf("第 %d 行 %s: %w", l.line, op, err)
	}
	return nil
}

// dispatch 按操作符分派到具体解析函数。
// 返回的错误统一在 instruction 中补上"第 N 行 <OP>"上下文，保证任何拒绝都能定位到源码。
func (p *parser) dispatch(l logicalLine, op, rest string) error {
	switch op {
	case OpFrom, OpCopy, OpEnv, OpWorkdir, OpEntrypoint, OpCmd, OpExpose, OpVolume, OpLabel, OpUser, OpArg:
		return p.parse(l, op, rest)
	case OpRun, OpAdd:
		return fmt.Errorf("阶段 3 尚未实现该指令: %w", ErrBadInstruction)
	default:
		return fmt.Errorf("未知指令 %q（合法指令见 Boxfile 规范）: %w", op, ErrBadInstruction)
	}
}

// parse 按操作符解析指令参数。
func (p *parser) parse(l logicalLine, op, rest string) error {
	switch op {
	case OpFrom:
		return p.doFrom(l, rest)
	case OpCopy:
		return p.doCopy(l, rest)
	case OpEnv:
		return p.doEnv(l, rest)
	case OpWorkdir:
		return p.doWorkdir(l, rest)
	case OpEntrypoint, OpCmd:
		return p.doJSONArgs(l, op, rest)
	case OpExpose:
		return p.doExpose(l, rest)
	case OpVolume:
		return p.doVolume(l, rest)
	case OpLabel:
		return p.doLabel(l, rest)
	case OpUser:
		return p.doUser(l, rest)
	case OpArg:
		return p.doArg(l, rest)
	}
	return fmt.Errorf("未知指令 %q: %w", op, ErrBadInstruction)
}

// add 追加一条已解析的指令，并统一校验参数展开、个数与记录行号 / 原文。
func (p *parser) add(l logicalLine, op string, args []string) error {
	if !l.expanded {
		if err := p.expandArgs(args); err != nil {
			return err
		}
	}
	p.bf.Instructions = append(p.bf.Instructions, Instruction{
		Op:   op,
		Args: args,
		Line: l.line,
		Raw:  l.raw,
	})
	return nil
}

func (p *parser) doFrom(l logicalLine, rest string) error {
	if p.seenFrom {
		return fmt.Errorf("FROM 重复出现: %w", ErrFromNotFirst)
	}
	if len(p.bf.Instructions) != 0 {
		return fmt.Errorf("FROM 之前已有指令，必须是首条: %w", ErrFromNotFirst)
	}
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return fmt.Errorf("FROM 需要且仅需要一个镜像引用，得到 %d 个: %w", len(fields), ErrBadInstruction)
	}
	if err := checkImageRef(fields[0]); err != nil {
		return fmt.Errorf("FROM: %w", err)
	}
	p.bf.From = fields[0]
	p.seenFrom = true
	return nil
}

func (p *parser) doCopy(l logicalLine, rest string) error {
	fields := strings.Fields(rest)
	if len(fields) != 2 {
		return fmt.Errorf("COPY 需要 <src> <dst> 两个参数，得到 %d 个: %w", len(fields), ErrBadInstruction)
	}
	return p.add(l, OpCopy, fields)
}

func (p *parser) doEnv(l logicalLine, rest string) error {
	if strings.TrimSpace(rest) == "" {
		return fmt.Errorf("ENV 需要 KEY=VALUE 或 KEY VALUE: %w", ErrBadInstruction)
	}
	var key, val string
	if i := strings.IndexByte(rest, '='); i >= 0 {
		// KEY=VALUE 形式：值可取任意非空文本，允许含空格，但不允许再出现 '='。
		key, val = rest[:i], rest[i+1:]
		if strings.Contains(val, "=") {
			return fmt.Errorf("ENV %q 含多个 '=': %w", rest, ErrBadInstruction)
		}
	} else if i := strings.IndexAny(rest, " \t"); i >= 0 {
		// KEY VALUE 形式：值取到行尾，允许含空格。
		key, val = rest[:i], strings.TrimSpace(rest[i:])
	} else {
		return fmt.Errorf("ENV 需要 KEY=VALUE 或 KEY VALUE 两个参数: %w", ErrBadInstruction)
	}
	if err := checkEnvKey(key); err != nil {
		return err
	}
	if strings.ContainsAny(val, "\r\n") {
		return fmt.Errorf("ENV %s 的值含换行: %w", key, ErrBadInstruction)
	}
	return p.add(l, OpEnv, []string{key, val})
}

func (p *parser) doWorkdir(l logicalLine, rest string) error {
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return fmt.Errorf("WORKDIR 需要且仅需要一个绝对路径，得到 %d 个: %w", len(fields), ErrBadInstruction)
	}
	// 先展开 ${NAME} 再校验，否则带变量的绝对路径会被误判为相对路径。
	if err := p.expandArgs(fields); err != nil {
		return err
	}
	if err := checkAbsPath(fields[0]); err != nil {
		return fmt.Errorf("WORKDIR: %w", err)
	}
	l.expanded = true
	return p.add(l, OpWorkdir, fields)
}

// doJSONArgs 解析 ENTRYPOINT / CMD，只接受 JSON 数组形式。
func (p *parser) doJSONArgs(l logicalLine, op, rest string) error {
	if strings.TrimSpace(rest) == "" {
		return fmt.Errorf("%s 需要 JSON 数组形式的参数: %w", op, ErrBadInstruction)
	}
	if !strings.HasPrefix(strings.TrimSpace(rest), "[") {
		return fmt.Errorf("%s 只支持 exec 形式（JSON 数组，如 %s [\"/bin/sh\",\"-c\"]），shell 形式不支持: %w", op, op, ErrBadInstruction)
	}
	var argv []string
	if err := json.Unmarshal([]byte(rest), &argv); err != nil {
		return fmt.Errorf("%s JSON 数组解析失败: %w（%w）", op, err, ErrBadInstruction)
	}
	if len(argv) == 0 {
		return fmt.Errorf("%s 的 JSON 数组不能为空: %w", op, ErrBadInstruction)
	}
	for i, a := range argv {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("%s 第 %d 个元素为空: %w", op, i+1, ErrBadInstruction)
		}
	}
	return p.add(l, op, argv)
}

func (p *parser) doExpose(l logicalLine, rest string) error {
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return fmt.Errorf("EXPOSE 需要且仅需要一个端口，得到 %d 个: %w", len(fields), ErrBadInstruction)
	}
	if err := checkPort(fields[0]); err != nil {
		return fmt.Errorf("EXPOSE: %w", err)
	}
	return p.add(l, OpExpose, fields)
}

func (p *parser) doVolume(l logicalLine, rest string) error {
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return fmt.Errorf("VOLUME 需要且仅需要一个容器内绝对路径，得到 %d 个: %w", len(fields), ErrBadInstruction)
	}
	if err := checkAbsPath(fields[0]); err != nil {
		return fmt.Errorf("VOLUME: %w", err)
	}
	return p.add(l, OpVolume, fields)
}

func (p *parser) doLabel(l logicalLine, rest string) error {
	if strings.TrimSpace(rest) == "" {
		return fmt.Errorf("LABEL 需要 key=value: %w", ErrBadInstruction)
	}
	if strings.Count(rest, "=") != 1 {
		return fmt.Errorf("LABEL %q 需要恰好一个 '=': %w", rest, ErrBadInstruction)
	}
	key, val, _ := strings.Cut(rest, "=")
	if err := checkLabelKey(key); err != nil {
		return err
	}
	if val == "" || strings.Contains(val, "=") {
		return fmt.Errorf("LABEL %s 的值非法: %w", key, ErrBadInstruction)
	}
	return p.add(l, OpLabel, []string{key, val})
}

func (p *parser) doUser(l logicalLine, rest string) error {
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return fmt.Errorf("USER 需要且仅需要一个用户或 用户:组，得到 %d 个: %w", len(fields), ErrBadInstruction)
	}
	if err := checkUser(fields[0]); err != nil {
		return fmt.Errorf("USER: %w", err)
	}
	return p.add(l, OpUser, fields)
}

func (p *parser) doArg(l logicalLine, rest string) error {
	if strings.TrimSpace(rest) == "" {
		return fmt.Errorf("ARG 需要 NAME 或 NAME=默认值: %w", ErrBadInstruction)
	}
	if strings.Count(rest, "=") > 1 {
		return fmt.Errorf("ARG %q 含多个 '=': %w", rest, ErrBadInstruction)
	}
	name, def, hasDef := strings.Cut(rest, "=")
	if err := checkArgName(name); err != nil {
		return err
	}
	if hasDef {
		if def == "" || strings.IndexAny(def, " \t\r\n") >= 0 {
			return fmt.Errorf("ARG %s 的默认值 %q 非法: %w", name, def, ErrBadInstruction)
		}
	}
	if _, dup := p.args[name]; dup {
		return fmt.Errorf("ARG %s 重复声明: %w", name, ErrBadInstruction)
	}
	p.args[name] = def
	return p.add(l, OpArg, []string{name, def})
}

// expandArgs 就地展开参数中的 ${NAME}，NAME 必须已由 ARG 声明。
func (p *parser) expandArgs(args []string) error {
	for i, a := range args {
		v, err := p.expand(a)
		if err != nil {
			return err
		}
		args[i] = v
	}
	return nil
}

func (p *parser) expand(s string) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	var sb strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			sb.WriteString(s)
			return sb.String(), nil
		}
		sb.WriteString(s[:i])
		s = s[i+2:]
		j := strings.IndexByte(s, '}')
		if j < 0 {
			return "", fmt.Errorf("%q 中的 ${ 缺少配对的 '}': %w", s, ErrBadInstruction)
		}
		name := s[:j]
		if name == "" {
			return "", fmt.Errorf("${} 中变量名为空: %w", ErrBadInstruction)
		}
		v, ok := p.args[name]
		if !ok {
			return "", fmt.Errorf("未声明的变量 ${%s}（先用 ARG 声明，可使用默认值 ${%s:-默认值}）: %w", name, name, ErrBadInstruction)
		}
		sb.WriteString(v)
		s = s[j+1:]
	}
}

// logicalLine 是一条完成注释剥离与续行拼接后的逻辑行。
type logicalLine struct {
	line     int    // 起始行号，从 1 开始
	text     string // 已拼接的逻辑文本
	raw      string // 原始文本（仅去掉续行标记），用于报错与 Raw 字段
	expanded bool   // 参数中的 ${NAME} 是否已在解析阶段展开
}

// joinContinuations 按物理行切分，剥离注释、拼接续行，产出逻辑行。
// 空行与注释行不产出逻辑行，因此行号始终是物理行号，报错能精确定位到源码。
// 行尾反斜杠表示续行；续行之间的空白折叠为单个空格，续行标记本身不出现在文本中。
func joinContinuations(src string) []logicalLine {
	phys := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	out := make([]logicalLine, 0, len(phys))
	var text []string
	var raw []string
	start := 0
	inCont := false
	flush := func() {
		out = append(out, logicalLine{
			line: start,
			text: strings.TrimSpace(strings.Join(text, " ")),
			raw:  strings.TrimSpace(strings.Join(raw, " ")),
		})
		text, raw = nil, nil
		start = 0
	}
	for i, pl := range phys {
		ln := i + 1
		if !inCont && isCommentLine(pl) {
			continue
		}
		if !inCont {
			start = ln
		}
		body, cont := splitContinuation(pl)
		text = append(text, strings.TrimSpace(body))
		raw = append(raw, strings.TrimSpace(body))
		inCont = cont
		if !cont {
			flush()
		}
	}
	if inCont {
		flush()
	}
	return out
}

// isCommentLine 判断物理行是否为注释或空行（允许前导空白）。
func isCommentLine(pl string) bool {
	t := strings.TrimSpace(pl)
	return t == "" || strings.HasPrefix(t, "#")
}

// splitContinuation 去掉该物理行的行尾注释，并报告是否为续行（行尾反斜杠）。
// 先剥注释再判续行：`ENV K=V \ # 注释` 是续行，而 `ENV K=V # 注释` 就地结束。
// 反斜杠的续行职责优先于其转义职责，正文中的反斜杠一律原样保留，避免静默改写。
func splitContinuation(pl string) (body string, cont bool) {
	s := pl
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, " \t")
	if s == "" {
		return "", false
	}
	if strings.HasSuffix(s, "\\") {
		return strings.TrimRight(s[:len(s)-1], " \t"), true
	}
	return s, false
}

// splitOp 切出指令操作符与其余参数文本。
func splitOp(text string) (op, rest string) {
	i := strings.IndexAny(text, " \t")
	if i < 0 {
		return strings.ToUpper(text), ""
	}
	return strings.ToUpper(text[:i]), strings.TrimSpace(text[i:])
}

// checkImageRef 校验 FROM 的镜像引用，与镜像格式一致：name:version，name 允许小写字母 / 数字 / . _ - /。
func checkImageRef(ref string) error {
	name, ver, ok := strings.Cut(ref, ":")
	if !ok {
		return fmt.Errorf("镜像引用 %q 缺少 :version: %w", ref, ErrBadInstruction)
	}
	if name == "" || ver == "" {
		return fmt.Errorf("镜像引用 %q 的 name / version 不能为空: %w", ref, ErrBadInstruction)
	}
	if strings.ContainsAny(name, " \t") || strings.ContainsAny(ver, " \t") {
		return fmt.Errorf("镜像引用 %q 含空白: %w", ref, ErrBadInstruction)
	}
	return nil
}

// checkEnvKey 校验环境变量名：字母、数字、下划线，且不以数字开头。
func checkEnvKey(k string) error {
	if k == "" {
		return fmt.Errorf("ENV 变量名为空: %w", ErrBadInstruction)
	}
	for i, c := range k {
		switch {
		case c == '_':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("ENV 变量名 %q 非法: %w", k, ErrBadInstruction)
		}
	}
	return nil
}

// checkLabelKey 校验标签键：字母、数字、点、下划线、短横线，且以字母或下划线开头。
func checkLabelKey(k string) error {
	if k == "" {
		return fmt.Errorf("LABEL 键为空: %w", ErrBadInstruction)
	}
	for i, c := range k {
		switch {
		case c == '.' || c == '_' || c == '-':
			if i == 0 {
				return fmt.Errorf("LABEL 键 %q 不能以 %q 开头: %w", k, c, ErrBadInstruction)
			}
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("LABEL 键 %q 非法: %w", k, ErrBadInstruction)
		}
	}
	return nil
}

// checkAbsPath 校验容器内绝对路径：以 / 开头、无空白、无 "." / ".." 段与空段。
func checkAbsPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("空路径: %w", ErrBadInstruction)
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("路径 %q 不是绝对路径: %w", p, ErrBadInstruction)
	case strings.ContainsAny(p, " \t\\"):
		return fmt.Errorf("路径 %q 含空白或反斜杠: %w", p, ErrBadInstruction)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case ".", "..":
			return fmt.Errorf("路径 %q 含 %q 段: %w", p, seg, ErrBadInstruction)
		}
	}
	if strings.Contains(p, "//") {
		return fmt.Errorf("路径 %q 含空路径段: %w", p, ErrBadInstruction)
	}
	return nil
}

// checkPort 校验 EXPOSE 的端口：PORT 或 PORT/proto，proto 仅 tcp / udp。
func checkPort(s string) error {
	spec, proto, ok := strings.Cut(s, "/")
	if ok && proto != "tcp" && proto != "udp" {
		return fmt.Errorf("EXPOSE %q 的协议只支持 tcp / udp: %w", s, ErrBadInstruction)
	}
	if !ok {
		proto = "tcp"
	}
	n, err := strconv.Atoi(spec)
	if err != nil {
		return fmt.Errorf("EXPOSE %q 的端口不是数字: %w", s, ErrBadInstruction)
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("EXPOSE %q 的端口需在 1..65535: %w", s, ErrBadInstruction)
	}
	_ = proto
	return nil
}

// checkUser 校验运行用户：name、uid、name:group、uid:gid 四种形态。
func checkUser(s string) error {
	u, g, hasGroup := strings.Cut(s, ":")
	if !hasGroup {
		if s == "" {
			return fmt.Errorf("USER 为空: %w", ErrBadInstruction)
		}
		u = s
	} else if u == "" || g == "" {
		return fmt.Errorf("USER %q 的用户或组为空: %w", s, ErrBadInstruction)
	}
	for _, part := range []string{u, g} {
		if part == "" {
			continue
		}
		for i, c := range part {
			switch {
			case c >= '0' && c <= '9':
			case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
			case (c == '_' || c == '-' || c == '.') && i > 0:
			default:
				return fmt.Errorf("USER %q 非法: %w", s, ErrBadInstruction)
			}
		}
	}
	return nil
}

// checkArgName 校验 ARG 名：字母、数字、下划线，且不以数字开头。
func checkArgName(name string) error {
	if name == "" {
		return fmt.Errorf("ARG 名为空: %w", ErrBadInstruction)
	}
	for i, c := range name {
		switch {
		case c == '_':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return fmt.Errorf("ARG 名 %q 非法: %w", name, ErrBadInstruction)
		}
	}
	return nil
}
