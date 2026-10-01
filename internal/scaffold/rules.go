// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package scaffold

import (
	"fmt"
	"strconv"
	"strings"
)

// 本文件实现 boxli-compose.yml 与 Boxfile 的全部 lint 规则。规则之间互不依赖，
// 单条规则发现的问题不会中断其他规则；语法层问题先产出 parse 诊断，语义规则
// 有能力时仍在已解析出的节点上继续执行。

// composeOptions 调整编排文件规则的行为。
type composeOptions struct {
	// skipMissingTargets 为真时抑制 compose/missing-target：同目录没有 Boxfile，
	// 目标文件可能由构建流程后置生成。
	skipMissingTargets bool
}

// lintCompose 以默认选项检查编排文件内容。
func lintCompose(file string, data []byte) *Result {
	return lintComposeWithOptions(file, data, false)
}

// lintComposeWithOptions 检查编排文件内容。
func lintComposeWithOptions(file string, data []byte, skipMissingTargets bool) *Result {
	res := &Result{}
	root, problems := scanYAML(data)
	for _, p := range problems {
		res.add(file, p.line, SeverityError, RuleComposeParse, "%s", p.message)
	}
	checkCompose(file, root, &composeOptions{skipMissingTargets: skipMissingTargets}, res)
	return res
}

// checkCompose 在已解析的根节点上跑全部编排规则。
func checkCompose(file string, root *yamlNode, opts *composeOptions, res *Result) {
	if root == nil || root.kind != yamlMap {
		res.add(file, 1, SeverityError, RuleComposeParse, "编排文件顶层必须是键值映射")
		return
	}
	topLines := root.keyLines()
	for _, c := range root.children {
		if !contains(composeTopLevelKeys, c.key) {
			res.add(file, c.keyLine, SeverityError, RuleComposeUnknownKey,
				"顶层未知键 %q（支持 %s）", c.key, strings.Join(composeTopLevelKeys, ", "))
		}
	}

	services, ok := root.get("services")
	if !ok {
		res.add(file, 1, SeverityError, RuleComposeValidate,
			"缺少顶层 services（至少需要定义一个服务）")
		return
	}
	if services == nil || services.kind != yamlMap {
		res.add(file, lineOr(services, topLines["services"]), SeverityError, RuleComposeParse,
			"services 必须是服务名的映射")
		return
	}

	known := make(map[string]bool, len(services.children))
	for _, s := range services.children {
		known[s.key] = true
	}
	for _, s := range services.children {
		if s.val == nil || s.val.kind != yamlMap {
			res.add(file, s.keyLine, SeverityError, RuleComposeParse,
				"服务 %q 的配置必须是键值映射", s.key)
			continue
		}
		checkComposeService(file, s.key, s.val, known, opts, res)
	}
}

// checkComposeService 检查单个服务的全部字段。
func checkComposeService(file, name string, svc *yamlNode, known map[string]bool, opts *composeOptions, res *Result) {
	lines := svc.keyLines()
	for _, c := range svc.children {
		if !contains(composeServiceKeys, c.key) {
			res.add(file, c.keyLine, SeverityError, RuleComposeServiceUnknown,
				"服务 %q 的未知键 %q", name, c.key)
		}
	}

	// restart 策略。
	if restart, ok := svc.get("restart"); ok {
		value := scalarText(restart)
		if !contains(composeRestarts, value) {
			res.add(file, lineOr(restart, lines["restart"]), SeverityError, RuleComposeRestartInvalid,
				"服务 %q 的 restart=%q 非法（可选 %s）", name, value, strings.Join(composeRestarts, " / "))
		}
	}

	// 构建来源与镜像来源。
	image, hasImage := svc.get("image")
	boxfile, hasBoxfile := svc.get("boxfile")
	build, hasBuild := svc.get("build")
	if !hasImage && !hasBoxfile && !hasBuild {
		res.add(file, lineOr(svc, 0), SeverityError, RuleComposeMissingTarget,
			"服务 %q 必须提供 image、boxfile 或 build 之一", name)
	} else if !opts.skipMissingTargets {
		if hasImage && strings.TrimSpace(scalarText(image)) == "" {
			res.add(file, lineOr(image, lines["image"]), SeverityError, RuleComposeMissingTarget,
				"服务 %q 的 image 为空", name)
		}
		if hasBoxfile && strings.TrimSpace(scalarText(boxfile)) == "" {
			res.add(file, lineOr(boxfile, lines["boxfile"]), SeverityError, RuleComposeMissingTarget,
				"服务 %q 的 boxfile 为空", name)
		}
		if hasBuild && strings.TrimSpace(scalarText(build)) == "" {
			res.add(file, lineOr(build, lines["build"]), SeverityError, RuleComposeMissingTarget,
				"服务 %q 的 build 为空", name)
		}
	}

	// 端口格式。
	if ports, ok := svc.get("ports"); ok {
		entries := nodeEntries(ports)
		if len(entries) == 0 {
			res.add(file, lineOr(ports, lines["ports"]), SeverityError, RuleComposePortFormat,
				"服务 %q 的 ports 必须是非空列表", name)
		}
		for _, e := range entries {
			line := e.line
			if line == 0 {
				line = lineOr(ports, lines["ports"])
			}
			if msg := portProblem(e.text, e.numeric); msg != "" {
				res.add(file, line, SeverityError, RuleComposePortFormat,
					"服务 %q 的端口 %q %s", name, e.text, msg)
			}
		}
	}

	// 卷格式。
	if volumes, ok := svc.get("volumes"); ok {
		entries := nodeEntries(volumes)
		if len(entries) == 0 {
			res.add(file, lineOr(volumes, lines["volumes"]), SeverityError, RuleComposeVolumeFormat,
				"服务 %q 的 volumes 必须是非空列表", name)
		}
		for _, e := range entries {
			line := e.line
			if line == 0 {
				line = lineOr(volumes, lines["volumes"])
			}
			if msg := volumeProblem(e.text); msg != "" {
				res.add(file, line, SeverityError, RuleComposeVolumeFormat,
					"服务 %q 的卷 %q %s", name, e.text, msg)
			}
		}
	}

	// depends-on 引用完整性。
	if dep, ok := svc.get("depends-on"); ok {
		entries := nodeEntries(dep)
		if len(entries) == 0 {
			res.add(file, lineOr(dep, lines["depends-on"]), SeverityError, RuleComposeDependsUnknown,
				"服务 %q 的 depends-on 必须是非空列表", name)
		}
		for _, e := range entries {
			line := e.line
			if line == 0 {
				line = lineOr(dep, lines["depends-on"])
			}
			if !known[e.text] {
				res.add(file, line, SeverityError, RuleComposeDependsUnknown,
					"服务 %q 依赖了不存在的服务 %q", name, e.text)
			}
		}
	}

	// dev 块。
	if dev, ok := svc.get("dev"); ok {
		checkComposeDev(file, name, dev, lines["dev"], res)
	}
}

// checkComposeDev 检查 dev 热重载配置。
func checkComposeDev(file, name string, dev *yamlNode, fallbackLine int, res *Result) {
	if dev == nil || dev.kind != yamlMap {
		res.add(file, lineOr(dev, fallbackLine), SeverityError, RuleComposeParse,
			"服务 %q 的 dev 必须是键值映射", name)
		return
	}
	lines := dev.keyLines()
	for _, c := range dev.children {
		if !contains(composeDevKeys, c.key) {
			res.add(file, c.keyLine, SeverityError, RuleComposeServiceUnknown,
				"服务 %q 的 dev 未知键 %q（支持 %s）", name, c.key, strings.Join(composeDevKeys, ", "))
		}
	}
	rebuild, ok := dev.get("rebuild")
	if !ok {
		return
	}
	value := scalarText(rebuild)
	if value != "true" && value != "false" {
		res.add(file, lineOr(rebuild, lines["rebuild"]), SeverityError, RuleComposeDevRebuild,
			"服务 %q 的 dev.rebuild=%q 非法（只接受 true / false）", name, value)
	}
}

// entry 是一个待校验的序列条目。
type entry struct {
	text    string
	line    int
	numeric bool // 未被引号包裹，宿主端口可据此保证为纯数字
}

// nodeEntries 把序列/标量节点展平成条目列表；映射或非法条目返回空文本条目。
func nodeEntries(n *yamlNode) []entry {
	if n == nil {
		return nil
	}
	if n.kind == yamlScalar {
		return []entry{{text: n.scalar, line: n.line, numeric: n.quote == 0}}
	}
	if n.kind != yamlSeq {
		return nil
	}
	out := make([]entry, 0, len(n.items))
	for _, it := range n.items {
		if it == nil {
			continue
		}
		out = append(out, entry{text: it.scalar, line: it.line, numeric: it.quote == 0})
	}
	return out
}

// scalarText 返回标量节点的文本；非标量返回空串。
func scalarText(n *yamlNode) string {
	if n == nil || n.kind != yamlScalar {
		return ""
	}
	return n.scalar
}

// lineOr 返回节点的行号，缺失时回落到 fallback。
func lineOr(n *yamlNode, fallback int) int {
	if n != nil && n.line > 0 {
		return n.line
	}
	return fallback
}

// contains 报告候选集中是否存在目标串。
func contains(set []string, want string) bool {
	for _, s := range set {
		if s == want {
			return true
		}
	}
	return false
}

// portProblem 校验 `[host:]container[/proto]` 形式，返回问题描述（空串表示合法）。
func portProblem(raw string, numeric bool) string {
	if strings.TrimSpace(raw) == "" {
		return "为空"
	}
	spec := raw
	if idx := strings.LastIndex(spec, "/"); idx >= 0 {
		switch spec[idx+1:] {
		case "tcp", "udp":
			spec = spec[:idx]
		default:
			return "的协议后缀不是 tcp / udp"
		}
	}
	parts := strings.Split(spec, ":")
	if len(parts) > 2 {
		return "格式非法（应为 \"宿主端口:容器端口\"）"
	}
	host := ""
	container := parts[0]
	if len(parts) == 2 {
		host, container = parts[0], parts[1]
	}
	if !validPortNumber(container) {
		return "的容器端口不是 1-65535 的整数"
	}
	if host != "" {
		if !validPortNumber(host) {
			return "的宿主端口不是 1-65535 的整数"
		}
	} else if numeric {
		return "未写宿主端口时必须是带引号的字符串（YAML 会把 8080:8080 解析成数字）"
	}
	return ""
}

// validPortNumber 报告端口串是否为 1-65535 的十进制整数。
func validPortNumber(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return false
	}
	return n >= 1 && n <= 65535
}

// volumeProblem 校验 `源:容器路径[:ro|:rw]` 形式，返回问题描述（空串表示合法）。
func volumeProblem(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "为空"
	}
	if !strings.Contains(raw, ":") {
		return "必须是 \"宿主路径:容器路径\" 形式"
	}
	if !strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "./") &&
		!strings.HasPrefix(raw, "../") && !strings.HasPrefix(raw, "~") {
		return "的宿主路径必须是绝对路径或以 ./ ../ ~ 开头"
	}
	if _, _, ok := splitVolume(raw); !ok {
		return "必须包含容器路径"
	}
	_, container, _ := splitVolume(raw)
	if !strings.HasPrefix(container, "/") {
		return "的容器路径必须是绝对路径"
	}
	return ""
}

// splitVolume 拆出宿主机侧与容器侧路径。源路径不得含冒号，故按第一个冒号切分。
func splitVolume(raw string) (host, container string, ok bool) {
	idx := strings.Index(raw, ":")
	if idx < 0 {
		return "", "", false
	}
	return raw[:idx], raw[idx+1:], true
}

// buildShaped 报告 Boxfile 路径是否具备扩展名（形如 `dir/Name.boxfile`）。
func buildShaped(raw string) bool {
	base := raw
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return strings.Contains(base, ".")
}

// lintBoxfile 检查 Boxfile 文本。
func lintBoxfile(file string, data []byte) *Result {
	res := &Result{}
	lines := strings.Split(string(data), "\n")
	var (
		fromLine     int
		fromCount    int
		fromValue    string
		firstOp      = true
		instructions []string
		declaredArgs = map[string]int{}
		usedArgs     = map[string]int{}
	)
	// 逐行扫描：语法问题只影响当前行，其余行继续检查。
	for i, raw := range lines {
		no := i + 1
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.ContainsRune(line, '\t') {
			continue // 指令参数里的制表符不影响语义
		}
		fields := strings.Fields(trimmed)
		op := strings.ToUpper(fields[0])
		argText := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))

		if !contains(boxfileOps, op) {
			res.add(file, no, SeverityError, RuleBoxfileUnknownInstruct,
				"未知指令 %q（支持 %s）", fields[0], strings.Join(boxfileOps, ", "))
			continue
		}
		instructions = append(instructions, op)
		if op == "ARG" {
			name := argName(argText)
			if name != "" {
				declaredArgs[name] = no
			}
		}
		if op == "FROM" {
			fromCount++
			if fromCount == 1 {
				fromLine, fromValue = no, argText
			}
			if !firstOp {
				res.add(file, no, SeverityError, RuleBoxfileFromRequired,
					"FROM 必须是第一条指令（非注释行必须在它之后）")
			}
		}
		if firstOp && op != "FROM" {
			// 首条非注释指令不是 FROM：由 from-required 统一报告位置。
			res.add(file, no, SeverityError, RuleBoxfileFromRequired,
				"FROM 必须是第一条指令，实际首条是 %s", op)
		}
		firstOp = false

		if op == "WORKDIR" {
			if msg := workdirProblem(argText); msg != "" {
				res.add(file, no, SeverityError, RuleBoxfileWorkdirRelative, "%s", msg)
			}
		}
		if op == "ENTRYPOINT" || op == "CMD" {
			if msg := shellFormProblem(op, argText); msg != "" {
				res.add(file, no, SeverityWarning, RuleBoxfileEntrypointShell, "%s", msg)
			}
		}
		if op == "RUN" || op == "HEALTHCHECK" || op == "ONBUILD" {
			// boxli 构建器不执行任何容器内命令（无 docker 式 RUN 阶段），
			// 这些指令写在 Boxfile 里不会生效，必须提示而不是静默忽略。
			res.add(file, no, SeverityWarning, RuleBoxfileRunUnsupported,
				"%s 在 boxli 中不会执行（boxli 构建不做容器内命令执行），请把准备工作写进基础镜像", op)
		}
		// ARG 变量的使用情况：按名字边界匹配 ${NAME} / $NAME。
		for name, declLine := range declaredArgs {
			if declLine == no {
				continue
			}
			if usesArg(argText, name) {
				usedArgs[name] = no
			}
		}
	}

	if fromCount == 0 {
		res.add(file, 1, SeverityError, RuleBoxfileFromRequired, "缺少 FROM 指令，构建无法确定基础镜像")
	} else if strings.TrimSpace(fromValue) == "" {
		res.add(file, fromLine, SeverityError, RuleBoxfileFromRequired, "FROM 缺少基础镜像名")
	}

	// 未被任何后续指令引用的 ARG。
	names := make([]string, 0, len(declaredArgs))
	for name := range declaredArgs {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		if _, ok := usedArgs[name]; !ok {
			res.add(file, declaredArgs[name], SeverityWarning, RuleBoxfileArgUnused,
				"ARG %s 声明后未被任何指令引用", name)
		}
	}

	return res
}

// argName 从 `ARG NAME=value` / `ARG NAME` 取出变量名。
func argName(argText string) string {
	argText = strings.TrimSpace(argText)
	if argText == "" {
		return ""
	}
	if idx := strings.Index(argText, "="); idx >= 0 {
		argText = argText[:idx]
	}
	return strings.TrimSpace(argText)
}

// usesArg 判断文本是否引用了变量 name（$NAME 或 ${NAME}），按边界匹配避免误判。
func usesArg(text, name string) bool {
	if name == "" {
		return false
	}
	if strings.Contains(text, "${"+name+"}") {
		return true
	}
	needle := "$" + name
	for i := 0; ; {
		idx := strings.Index(text[i:], needle)
		if idx < 0 {
			return false
		}
		end := i + idx + len(needle)
		if end >= len(text) {
			return true
		}
		c := text[end]
		if !(c == '_' || c == '-' || (c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return true
		}
		i = end
	}
}

// workdirProblem 检查 WORKDIR 是否为绝对路径，返回问题描述（空串表示合法）。
func workdirProblem(argText string) string {
	value, _ := firstToken(argText)
	if value == "" {
		return "WORKDIR 缺少路径参数"
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		return fmt.Sprintf("WORKDIR %s 使用了 ~，容器内不会展开为 home 目录", value)
	}
	if !strings.HasPrefix(value, "/") {
		return fmt.Sprintf("WORKDIR %s 必须是绝对路径", value)
	}
	return ""
}

// shellFormProblem 检查 ENTRYPOINT/CMD 是否使用了 shell 形式。
func shellFormProblem(op, argText string) string {
	value := strings.TrimSpace(argText)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "[") {
		return ""
	}
	return fmt.Sprintf("%s 使用了 shell 形式（%s ...），建议改为 exec 数组形式 [\"...\"] 以便正确转发信号", op, op)
}

// firstToken 取出参数的首个空白分隔片段。
func firstToken(argText string) (string, string) {
	fields := strings.Fields(argText)
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], strings.TrimSpace(argText[strings.Index(argText, fields[0])+len(fields[0]):])
}

// sortStrings 是插入排序：内置 sort 也可用，这里避免为一处小切片引入额外依赖感
// （保持 lint.go 的 import 面最小）。切片规模等于 ARG 数量，足够小。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
