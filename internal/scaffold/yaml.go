// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package scaffold

import (
	"strings"
)

// 本文件实现 lint 需要的极简 YAML 子集扫描器。
//
// licore 禁止第三方依赖，标准库也没有 YAML 解析器，因此这里只实现编排文件
// 实际用到的子集：块映射、块序列、引号标量（含跨行双引号）、流式 [] / {}
// 标量、注释与文档分隔符。任何超出子集的写法（Tab 缩进、锚点、块标量、
// 多文档）都产出 compose/parse 诊断，而不是被静默忽略——"不猜测"与
// internal/compose 的严格解析保持同一取向。

// yamlKind 是扫描出的节点种类。
type yamlKind int

const (
	yamlScalar yamlKind = iota
	yamlMap
	yamlSeq
	yamlBad
)

// yamlNode 是扫描出的一个 YAML 节点。行号均为 1 起算。
type yamlNode struct {
	kind     yamlKind
	scalar   string // 标量原文（引号已去掉，流式集合保留 [ ] / { }）
	quote    byte   // 标量被引号包裹时为 '"' 或 '\''，否则 0
	line     int    // 键（映射值）或条目（序列项）所在行
	valueLn  int    // 值的起始行，跨行标量时与 line 不同
	children []kv   // 映射条目
	items    []*yamlNode
}

// kv 是块映射的一个条目。
type kv struct {
	key     string
	keyLine int
	val     *yamlNode
}

// yamlProblem 是扫描期发现的一处语法问题。
type yamlProblem struct {
	line    int
	message string
}

// get 按键取映射子节点。
func (n *yamlNode) get(key string) (*yamlNode, bool) {
	if n == nil || n.kind != yamlMap {
		return nil, false
	}
	for i := range n.children {
		if n.children[i].key == key {
			return n.children[i].val, true
		}
	}
	return nil, false
}

// keyLines 返回映射中每个键所在行，供未知键诊断定位。
func (n *yamlNode) keyLines() map[string]int {
	out := make(map[string]int, len(n.children))
	for _, c := range n.children {
		out[c.key] = c.keyLine
	}
	return out
}

// scalarStrings 把节点展平成字符串列表：标量本身，或序列中每个标量的原文。
func (n *yamlNode) scalarStrings() []string {
	if n == nil {
		return nil
	}
	if n.kind == yamlSeq {
		out := make([]string, 0, len(n.items))
		for _, it := range n.items {
			if it != nil && it.kind == yamlScalar {
				out = append(out, it.scalar)
			}
		}
		return out
	}
	if n.kind == yamlScalar {
		return []string{n.scalar}
	}
	return nil
}

// flowItems 解析流式序列 `[a, "b:c", c]`，返回各标量原文（引号保留的信息靠调用方）。
// 非流式序列返回 nil。
func flowItems(raw string) ([]string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return nil, false
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	if inner == "" {
		return []string{}, true
	}
	var (
		out     []string
		cur     strings.Builder
		quote   byte
		escaped bool
	)
	flush := func() {
		out = append(out, strings.Trim(strings.TrimSpace(cur.String()), `"'`))
		cur.Reset()
	}
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case escaped:
			cur.WriteByte(c)
			escaped = false
		case quote == '"' && c == '\\':
			cur.WriteByte(c)
			escaped = true
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			cur.WriteByte(c)
			quote = c
		case c == ',':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out, true
}

// flowKeys 解析流式 / 内联映射 `{a: b, c: d}`，返回按键与值的原文。
// 非映射形态返回 nil（调用方按标量处理）。
func flowKeys(raw string) ([][2]string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") || !strings.HasSuffix(raw, "}") {
		return nil, false
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	if inner == "" {
		return [][2]string{}, true
	}
	parts, _ := flowItems("[" + inner + "]")
	out := make([][2]string, 0, len(parts))
	for _, p := range parts {
		idx := strings.Index(p, ":")
		if idx < 0 {
			out = append(out, [2]string{strings.TrimSpace(p), ""})
			continue
		}
		out = append(out, [2]string{
			strings.TrimSpace(p[:idx]),
			strings.TrimSpace(p[idx+1:]),
		})
	}
	return out, true
}

// stripComment 去掉行尾注释。引号内的 # 不算注释。
func stripComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i]
		}
	}
	return line
}

// unquote 去掉成对引号并解掉双引号内的反斜杠转义。
func unquote(s string) (value string, quote byte) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			body := s[1 : len(s)-1]
			var b strings.Builder
			for i := 0; i < len(body); i++ {
				if body[i] == '\\' && i+1 < len(body) {
					i++
					switch body[i] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(body[i])
					}
					continue
				}
				b.WriteByte(body[i])
			}
			return b.String(), '"'
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), '\''
		}
	}
	return s, 0
}

// splitKey 把一行（已去注释、已去缩进）拆成 key 与值原文。
// 缩进过的 `- key: value` 视为没有键。
func splitKey(content string) (key, rest string, ok bool) {
	var quote byte
	for i := 0; i < len(content); i++ {
		c := content[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ':':
			if i+1 < len(content) && content[i+1] != ' ' && content[i+1] != '\t' {
				continue // `8080:8080` 这类标量里的冒号不是键分隔符
			}
			return strings.TrimSpace(content[:i]), strings.TrimSpace(content[i+1:]), true
		}
	}
	return "", "", false
}

// yamlLine 是扫描期的一行。
type yamlLine struct {
	no      int
	indent  int
	content string
	raw     string
}

// scanYAML 扫描 YAML 子集，返回根节点与全部语法问题。
func scanYAML(data []byte) (*yamlNode, []yamlProblem) {
	lines := make([]yamlLine, 0, 64)
	var problems []yamlProblem

	// 1) 逐行预处理：跳过空行/注释/文档分隔符/指令行，Tab 缩进与制表符报错。
	var pendingQuoteLine int
	for i, raw := range strings.Split(string(data), "\n") {
		no := i + 1
		raw = strings.TrimRight(raw, "\r")
		if pendingQuoteLine != 0 {
			// 上一行打开了一个跨行双引号标量，本行整体属于该标量。
			if strings.Contains(raw, "\"") {
				pendingQuoteLine = 0
			}
			continue
		}
		if strings.ContainsRune(raw, '\t') {
			problems = append(problems, yamlProblem{no, "缩进必须使用空格，不允许 Tab"})
			continue
		}
		content := raw
		if idx := strings.Index(content, "#"); idx == 0 {
			continue
		}
		indent := len(content) - len(strings.TrimLeft(content, " "))
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "...") {
			problems = append(problems, yamlProblem{no, "不支持多文档 YAML（--- / ... 分隔符）"})
			continue
		}
		if strings.HasPrefix(trimmed, "%") {
			problems = append(problems, yamlProblem{no, "不支持 YAML 指令行"})
			continue
		}
		if strings.HasPrefix(trimmed, "&") || strings.HasPrefix(trimmed, "*") {
			problems = append(problems, yamlProblem{no, "不支持锚点与别名（& / *）"})
			continue
		}
		if strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, ">") {
			problems = append(problems, yamlProblem{no, "不支持块标量（| / >）"})
			continue
		}
		body := stripComment(content)
		if strings.TrimSpace(body) == "" {
			continue
		}
		// 值以双引号起头且本行没有闭合引号：标量跨行，跳过后继行。
		if _, rest, ok := splitKey(strings.TrimSpace(body)); ok {
			r := strings.TrimSpace(rest)
			if strings.HasPrefix(r, "\"") && strings.Count(r, "\"")%2 == 1 {
				pendingQuoteLine = no
			}
		}
		lines = append(lines, yamlLine{no: no, indent: indent, content: strings.TrimSpace(body), raw: content})
	}

	// 2) 递归下降：按缩进切分块结构。
	pos := 0
	root := parseBlock(lines, &pos, 0, problems)
	if root == nil {
		root = &yamlNode{kind: yamlMap}
	}
	return root, problems
}

// parseBlock 解析同一缩进层级上的一段块，返回其节点。
func parseBlock(lines []yamlLine, pos *int, indent int, problems []yamlProblem) *yamlNode {
	if *pos >= len(lines) {
		return nil
	}
	if strings.HasPrefix(lines[*pos].content, "- ") || lines[*pos].content == "-" {
		node := &yamlNode{kind: yamlSeq}
		for *pos < len(lines) {
			ln := lines[*pos]
			if ln.indent != indent || !(strings.HasPrefix(ln.content, "- ") || ln.content == "-") {
				break
			}
			item := strings.TrimSpace(strings.TrimPrefix(ln.content, "-"))
			*pos++
			if key, rest, ok := splitKey(item); ok && item != "" {
				// 序列项是内联映射起点：`- key: value`，其子键缩进更深。
				m := &yamlNode{kind: yamlMap, line: ln.no, valueLn: ln.no}
				child := parseValue(rest, ln.no)
				m.children = append(m.children, kv{key: key, keyLine: ln.no, val: child})
				if rest == "" {
					if *pos < len(lines) && lines[*pos].indent > indent {
						childIndent := lines[*pos].indent
						sub := parseBlock(lines, pos, childIndent, problems)
						if sub != nil {
							m.children[len(m.children)-1].val = sub
						}
					} else {
						// 无子块的 `key:` 视为空映射，交由规则层判断。
						m.children[len(m.children)-1].val = &yamlNode{kind: yamlMap, line: ln.no}
					}
				}
				for *pos < len(lines) && lines[*pos].indent > indent {
					c := lines[*pos]
					ckey, crest, cok := splitKey(c.content)
					*pos++
					if !cok {
						problems = append(problems, yamlProblem{c.no, "序列项内的映射条目缺少 `key: value` 形式"})
						continue
					}
					cval := parseValue(crest, c.no)
					if crest == "" && ckey != "" {
						if *pos < len(lines) && lines[*pos].indent > c.indent {
							cval = parseBlock(lines, pos, lines[*pos].indent, problems)
						} else {
							cval = &yamlNode{kind: yamlMap, line: c.no}
						}
					}
					m.children = append(m.children, kv{key: ckey, keyLine: c.no, val: cval})
				}
				node.items = append(node.items, m)
				continue
			}
			node.items = append(node.items, parseValue(item, ln.no))
			// 标量序列项之后若还有更深缩进，属于非法结构。
			if *pos < len(lines) && lines[*pos].indent > indent {
				problems = append(problems, yamlProblem{lines[*pos].no, "标量序列项后不应再有缩进内容"})
				for *pos < len(lines) && lines[*pos].indent > indent {
					*pos++
				}
			}
		}
		return node
	}

	node := &yamlNode{kind: yamlMap}
	for *pos < len(lines) {
		ln := lines[*pos]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			problems = append(problems, yamlProblem{ln.no, "缩进不一致：该行比同级条目更深"})
			*pos++
			continue
		}
		if strings.HasPrefix(ln.content, "- ") || ln.content == "-" {
			break
		}
		key, rest, ok := splitKey(ln.content)
		if !ok {
			problems = append(problems, yamlProblem{ln.no, "该行不是 `key: value` 形式"})
			*pos++
			continue
		}
		*pos++
		val := parseValue(rest, ln.no)
		if rest == "" && key != "" {
			if *pos < len(lines) && lines[*pos].indent > indent {
				val = parseBlock(lines, pos, lines[*pos].indent, problems)
			} else if *pos < len(lines) && lines[*pos].indent == indent &&
				(strings.HasPrefix(lines[*pos].content, "- ") || lines[*pos].content == "-") {
				// 同缩进的序列归属该键（YAML 允许序列与键同缩进）。
				val = parseBlock(lines, pos, indent, problems)
			} else {
				val = &yamlNode{kind: yamlMap, line: ln.no, valueLn: ln.no}
			}
		}
		if val == nil {
			val = &yamlNode{kind: yamlMap, line: ln.no, valueLn: ln.no}
		}
		node.children = append(node.children, kv{key: key, keyLine: ln.no, val: val})
	}
	return node
}

// parseValue 把一行里 `key:` 之后的原文转成节点。
func parseValue(rest string, line int) *yamlNode {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil
	}
	if items, ok := flowItems(rest); ok {
		n := &yamlNode{kind: yamlSeq, line: line, valueLn: line}
		for _, it := range items {
			n.items = append(n.items, &yamlNode{kind: yamlScalar, scalar: it, line: line, valueLn: line})
		}
		return n
	}
	if entries, ok := flowKeys(rest); ok {
		n := &yamlNode{kind: yamlMap, line: line, valueLn: line}
		for _, e := range entries {
			n.children = append(n.children, kv{
				key:     e[0],
				keyLine: line,
				val:     &yamlNode{kind: yamlScalar, scalar: e[1], line: line, valueLn: line},
			})
		}
		return n
	}
	value, quote := unquote(rest)
	return &yamlNode{kind: yamlScalar, scalar: value, quote: quote, line: line, valueLn: line}
}
