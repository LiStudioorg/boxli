// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package compose

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// 本文件实现一个零依赖的 YAML 子集解析器，只覆盖 compose 文件需要的语法。
//
// 支持的语法：
//   - 块映射 `key: value`，用空格缩进嵌套（缩进宽度任意，同级必须一致）
//   - 块序列 `- item`、`- key: value`（映射序列）以及 `-` 后跟嵌套块
//   - 标量：裸标量、单引号、双引号（支持 \" \\ \n \t 转义）、布尔 true/false、
//     空值（空 / null / Null / NULL / ~）、十进制整数、浮点数
//   - 注释 `#`（行首，或前面是空白；引号内的 # 属于内容）
//   - 空行
//
// 明确不支持并显式报错的语法（宁可失败也不猜测）：
// 锚点 `&`、别名 `*`、标签 `!`、多文档 `---` / `...`、流式集合 `{}` / `[]`、
// 块标量 `|` / `>`、复合键 `?`、指令 `%`、制表符缩进、非 2 的幂次可用的
// 纯空白行以外的行尾多余内容。

// maxYAMLBytes 是单个 YAML 文档的体积上限（16 MiB），防止误读巨型文件。
const maxYAMLBytes = 16 << 20

// Parse 解析 YAML 子集文本，返回 Go 原生值：
//
//	map[string]any（映射）、[]any（序列）、string、bool、int64、float64 或 nil。
//
// 任何本包不支持的语法都会以 ErrUnsupported 包装错误返回；
// 语法错误以 ErrYAML 包装错误返回，且错误信息带行号列号。
func Parse(data []byte) (any, error) {
	if len(data) > maxYAMLBytes {
		return nil, fmt.Errorf("YAML 体积 %d 字节 > 上限 %d: %w", len(data), maxYAMLBytes, ErrYAML)
	}
	lines, err := scanLines(data)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}
	p := &yamlParser{lines: lines}
	v, err := p.parseBlock(lines[0].indent)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		return nil, fmt.Errorf("第 %d 行第 %d 列: 缩进与上层不匹配: %w", ln.num, ln.indent+1, ErrYAML)
	}
	return v, nil
}

// ParseFile 读取文件并调用 Parse。
// 读取失败、体积超限或解析失败都会以 fmt.Errorf 包装上下文返回。
func ParseFile(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 YAML 文件 %s: %w", path, err)
	}
	v, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("解析 YAML 文件 %s: %w", path, err)
	}
	return v, nil
}

// yamlLine 是一条已预处理（去注释、去尾部空白）的 YAML 行。
type yamlLine struct {
	num     int    // 1 起的行号，用于报错
	indent  int    // 前导空格数
	content string // 前导空格之后的内容，已去掉行尾空白与注释
}

// scanLines 把原始文本切分为有效行，跳过空行与整行注释，并拒绝制表符缩进。
func scanLines(data []byte) ([]yamlLine, error) {
	raw := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	lines := make([]yamlLine, 0, len(raw))
	for i, s := range raw {
		num := i + 1
		indent := 0
		for indent < len(s) && s[indent] == ' ' {
			indent++
		}
		body := strings.TrimRight(s[indent:], " \t")
		if body == "" || strings.HasPrefix(body, "#") {
			// 空行 / 整行空白（含含制表符的空白）或整行注释：跳过。
			continue
		}
		if indent < len(s) && s[indent] == '\t' {
			return nil, fmt.Errorf("第 %d 行第 %d 列: 缩进禁止使用制表符（Tab），请改用空格: %w",
				num, indent+1, ErrYAML)
		}
		if strings.HasPrefix(body, "\t") {
			return nil, fmt.Errorf("第 %d 行: 内容中禁止出现制表符: %w", num, ErrYAML)
		}
		lines = append(lines, yamlLine{num: num, indent: indent, content: body})
	}
	return lines, nil
}

// yamlParser 持有待解析的行序列与当前游标。
type yamlParser struct {
	lines []yamlLine
	pos   int
}

// yamlIndent 是本 YAML 子集里一级缩进的空格数：
// 嵌套块必须恰好比父级深 2 个空格，过深/过浅都在报错中明确提示期望值。
const yamlIndent = 2

// seqChildIndent 是序列项内容相对 `-` 的缩进量（`- ` 两个字符）。
const seqChildIndent = 2

// parseBlock 解析缩进严格等于 indent 的一个块（映射或序列）。
func (p *yamlParser) parseBlock(indent int) (any, error) {
	if p.pos >= len(p.lines) {
		return nil, nil
	}
	if isSeqItem(p.lines[p.pos].content) {
		return p.parseSequence(indent)
	}
	return p.parseMapping(indent)
}

// parseSequence 解析块序列：所有 `-` 项必须缩进一致。
func (p *yamlParser) parseSequence(indent int) ([]any, error) {
	out := []any{}
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent != indent {
			break
		}
		if !isSeqItem(ln.content) {
			return nil, fmt.Errorf("第 %d 行: 序列项必须以 \"- \" 开头: %w", ln.num, ErrYAML)
		}
		rest, restCol := trimSeqMarker(ln.content)
		if rest == "" {
			// `-` 单独一行：值必须在下一行，且只允许缩进一级。
			p.pos++
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				child := p.lines[p.pos]
				if child.indent != indent+seqChildIndent {
					return nil, fmt.Errorf("第 %d 行第 %d 列: 序列项值缩进非法（期望 %d 个空格）: %w",
						child.num, child.indent+1, indent+seqChildIndent, ErrYAML)
				}
				v, err := p.parseNode(child.indent)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
				continue
			}
			out = append(out, nil)
			continue
		}
		if err := rejectUnsupported(rest, ln.num, restCol); err != nil {
			return nil, err
		}
		if rest == "-" || isSeqItem(rest) {
			// `- - x` 的嵌套序列。
			p.lines[p.pos].content = rest
			v, err := p.parseSequence(ln.indent + seqChildIndent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		key, rawVal, ok, err := splitKey(rest)
		if err != nil {
			return nil, at(ln.num, restCol, err)
		}
		if !ok {
			// 标量项：取值文本是去掉 `- ` 后的 rest 本身。
			p.pos++
			v, err := p.parseInline(rest, ln.indent, restCol)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		// `- key: value`：把第一对键值当作内联映射首行，其余键在更深缩进里。
		if err := checkKey(key, ln.num, restCol); err != nil {
			return nil, err
		}
		m := map[string]any{}
		v, err := p.parseInline(rawVal, ln.indent, restCol+len(key)+1)
		if err != nil {
			return nil, err
		}
		m[key] = v
		p.pos++
		if seqItemHasChildMapping(p, ln.indent) {
			// 项的其余键写在更深一层缩进里。
			more, err := p.parseMappingExtend(ln.indent+yamlIndent, m, ln.num)
			if err != nil {
				return nil, err
			}
			m = more
		}
		out = append(out, m)
	}
	return out, nil
}

// parseNode 解析缩进严格等于 indent 的任意节点（映射 / 序列 / 单行标量）。
// indent 由调用方按"父级 + 一级缩进"给出，因此这里的标量行缩进必须与之一致：
// 过深缩进的孤立标量属于歧义写法，直接拒绝而不是猜测。
func (p *yamlParser) parseNode(indent int) (any, error) {
	ln := p.lines[p.pos]
	if ln.indent != indent {
		return nil, fmt.Errorf("第 %d 行第 %d 列: 缩进非法（期望 %d 个空格）: %w",
			ln.num, ln.indent+1, indent, ErrYAML)
	}
	if isSeqItem(ln.content) {
		return p.parseSequence(indent)
	}
	if _, _, ok, err := splitKey(ln.content); err != nil {
		return nil, at(ln.num, ln.indent+1, err)
	} else if ok {
		return p.parseMapping(indent)
	}
	// 裸标量行：整行内容就是标量。
	p.pos++
	return p.parseInline(ln.content, ln.indent, ln.indent+1)
}

// parseMapping 解析块映射：所有同级键必须缩进一致。
func (p *yamlParser) parseMapping(indent int) (map[string]any, error) {
	return p.parseMappingExtend(indent, nil, 0)
}

// parseMappingExtend 在已有映射 base 上继续解析同级键；base 为 nil 时新建。
// seqOwnerLine 非 0 时表示基础键来自第 seqOwnerLine 行的 `- key: value`，
// 用于把重复键错误归属到正确行号。
func (p *yamlParser) parseMappingExtend(indent int, base map[string]any, seqOwnerLine int) (map[string]any, error) {
	m := base
	if m == nil {
		m = map[string]any{}
	}
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, fmt.Errorf("第 %d 行第 %d 列: 缩进超出当前层级（期望 %d 个空格）: %w",
				ln.num, ln.indent+1, indent, ErrYAML)
		}
		if err := rejectUnsupported(ln.content, ln.num, ln.indent+1); err != nil {
			return nil, err
		}
		if isSeqItem(ln.content) {
			return nil, fmt.Errorf("第 %d 行: 映射中不允许直接出现序列项: %w", ln.num, ErrYAML)
		}
		key, rawVal, ok, err := splitKey(ln.content)
		if err != nil {
			return nil, at(ln.num, 1, err)
		}
		if !ok {
			return nil, fmt.Errorf("第 %d 行: 期望 \"key: value\"，实际是 %q: %w", ln.num, ln.content, ErrYAML)
		}
		if err := checkKey(key, ln.num, ln.indent+1); err != nil {
			return nil, err
		}
		if _, dup := m[key]; dup {
			if seqOwnerLine != 0 {
				return nil, fmt.Errorf("第 %d 行: 键 %q 与第 %d 行重复: %w", ln.num, key, seqOwnerLine, ErrYAML)
			}
			return nil, fmt.Errorf("第 %d 行: 键 %q 重复: %w", ln.num, key, ErrYAML)
		}
		if rawVal == "" {
			// 值在下一层缩进里。
			p.pos++
			childIndent, has, err := p.peekIndent(indent)
			if err != nil {
				return nil, err
			}
			if !has {
				m[key] = nil
				continue
			}
			v, err := p.parseNode(childIndent)
			if err != nil {
				return nil, err
			}
			m[key] = v
			continue
		}
		v, err := p.parseInline(rawVal, ln.indent, ln.indent+len(key)+1)
		if err != nil {
			return nil, err
		}
		m[key] = v
		p.pos++
	}
	return m, nil
}

// peekIndent 返回下一个有效行的缩进；行已耗尽或缩进不深于 parent 时返回 ok=false。
func (p *yamlParser) peekIndent(parent int) (int, bool, error) {
	if p.pos >= len(p.lines) {
		return 0, false, nil
	}
	ln := p.lines[p.pos]
	if ln.indent <= parent {
		return 0, false, nil
	}
	return ln.indent, true, nil
}

// parseInline 解析写在 key 或 `-` 之后的标量：先剥掉行尾注释，再判定标量类型。
// baseIndent/col 用于报错定位。取值只能在同一行内结束：多行标量一律拒绝。
func (p *yamlParser) parseInline(s string, baseIndent, col int) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	// 行尾注释：`#` 前面必须是空白（或整行以 # 开头），引号内的 # 属于内容。
	s = stripComment(s)
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if err := rejectUnsupported(s, baseIndent, col); err != nil {
		return nil, err
	}
	if s[0] == '"' {
		v, rest, err := unquoteDouble(s)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行第 %d 列: %w", baseIndent+1, col, err)
		}
		rest = strings.TrimSpace(stripComment(rest))
		if rest != "" {
			return nil, fmt.Errorf("第 %d 行第 %d 列: 双引号标量后有多余内容 %q: %w",
				baseIndent+1, col, rest, ErrYAML)
		}
		return v, nil
	}
	if s[0] == '\'' {
		v, rest, err := unquoteSingle(s)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行第 %d 列: %w", baseIndent+1, col, err)
		}
		rest = strings.TrimSpace(stripComment(rest))
		if rest != "" {
			return nil, fmt.Errorf("第 %d 行第 %d 列: 单引号标量后有多余内容 %q: %w",
				baseIndent+1, col, rest, ErrYAML)
		}
		return v, nil
	}
	return parsePlainScalar(s)
}

// stripComment 删除标量文本中的行尾注释。
// 规则与 YAML 一致：`#` 位于行首，或前面紧跟空白时才开始注释；
// 单/双引号内的 `#` 属于内容，不参与判定。
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if quote == '"' && c == '\\' {
				i++ // 跳过被转义的字符
				continue
			}
			if c == quote {
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++ // 单引号内的 '' 是一个字面单引号
					continue
				}
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return strings.TrimRight(s[:i], " \t")
			}
		}
	}
	return s
}

// parsePlainScalar 把裸标量转换为 bool / int64 / float64 / nil / string。
func parsePlainScalar(s string) (any, error) {
	switch s {
	case "~", "null", "Null", "NULL":
		return nil, nil
	case "true", "True", "TRUE":
		return true, nil
	case "false", "False", "FALSE":
		return false, nil
	}
	if isIntLiteral(s) {
		n, err := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 10, 64)
		if err == nil {
			return n, nil
		}
	}
	if isFloatLiteral(s) {
		f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
		if err == nil {
			return f, nil
		}
	}
	return s, nil
}

// isIntLiteral 判断是否为十进制整数字面量（允许前导 +/-, 不允许前导 0 的多位数字）。
func isIntLiteral(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isFloatLiteral 判断是否为十进制浮点字面量。
func isFloatLiteral(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	dot, digit := false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digit = true
		case c == '.':
			if dot {
				return false
			}
			dot = true
		default:
			return false
		}
	}
	return dot && digit
}

// unquoteSingle 解析单引号标量，返回其值与剩余内容；连续两个单引号表示一个字面单引号。
func unquoteSingle(s string) (string, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), s[i+1:], nil
		default:
			b.WriteByte(s[i])
		}
	}
	return "", "", fmt.Errorf("单引号未闭合: %w", ErrYAML)
}

// unquoteDouble 解析双引号标量，支持 \" \\ \n \t \r \0 转义；返回其值与剩余内容。
func unquoteDouble(s string) (string, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			return b.String(), s[i+1:], nil
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", "", fmt.Errorf("双引号未闭合: %w", ErrYAML)
		}
		switch s[i] {
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		default:
			return "", "", fmt.Errorf("双引号内不支持的转义 \\%c: %w", s[i], ErrYAML)
		}
	}
	return "", "", fmt.Errorf("双引号未闭合: %w", ErrYAML)
}

// splitKey 判断内容是否为 `key:` 或 `key: value`。
// 返回 key、冒号后的原始值文本（未 trim）与是否命中；未命中时不是键值行。
func splitKey(s string) (key, rawVal string, ok bool, err error) {
	if s == "" {
		return "", "", false, nil
	}
	switch s[0] {
	case '&':
		return "", "", false, fmt.Errorf("不支持锚点 &（YAML 子集不含锚点/别名）: %w", ErrUnsupported)
	case '*':
		return "", "", false, fmt.Errorf("不支持别名 *（YAML 子集不含锚点/别名）: %w", ErrUnsupported)
	case '!':
		return "", "", false, fmt.Errorf("不支持标签 !（YAML 子集不含标签）: %w", ErrUnsupported)
	case '?':
		return "", "", false, fmt.Errorf("不支持复合键 ?（YAML 子集不含复合键）: %w", ErrUnsupported)
	case '%':
		return "", "", false, fmt.Errorf("不支持 YAML 指令 %%: %w", ErrUnsupported)
	case '{', '[':
		return "", "", false, fmt.Errorf("不支持流式集合 %c（请改用块式映射/序列）: %w", s[0], ErrUnsupported)
	case '|', '>':
		return "", "", false, fmt.Errorf("不支持块标量 %c（请改用单行标量）: %w", s[0], ErrUnsupported)
	case '"', '\'':
		q := s[0]
		i, end := 1, -1
		for i < len(s) {
			if s[i] == '\\' && q == '"' {
				i += 2
				continue
			}
			if s[i] == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i += 2
					continue
				}
				end = i
				break
			}
			i++
		}
		if end < 0 {
			return "", "", false, fmt.Errorf("引号未闭合: %w", ErrYAML)
		}
		k := s[1:end]
		if k == "" {
			return "", "", false, fmt.Errorf("空键名: %w", ErrYAML)
		}
		rest := strings.TrimLeft(s[end+1:], " \t")
		if !strings.HasPrefix(rest, ":") {
			return "", "", false, nil
		}
		return k, strings.TrimSpace(rest[1:]), true, nil
	}
	// 裸键：最多 1024 字节，冒号必须在行尾或后面跟空白/引号/流式记号。
	for i := 0; i < len(s); i++ {
		if s[i] != ':' {
			continue
		}
		if i == 0 {
			return "", "", false, fmt.Errorf("空键名: %w", ErrYAML)
		}
		if i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\t' {
			// 形如 "12:30" 或 "a:b" 的内容属于标量，不是键。
			return "", "", false, nil
		}
		return s[:i], strings.TrimSpace(s[i+1:]), true, nil
	}
	return "", "", false, nil
}

// checkKey 校验键名合法性与长度（与 compose 规范一致：不含控制字符）。
func checkKey(key string, line, col int) error {
	if key == "" {
		return fmt.Errorf("第 %d 行第 %d 列: 空键名: %w", line, col, ErrYAML)
	}
	if len(key) > 1024 {
		return fmt.Errorf("第 %d 行第 %d 列: 键名过长（%d 字节）: %w", line, col, len(key), ErrYAML)
	}
	for i := 0; i < len(key); i++ {
		if c := key[i]; c < 0x20 || c == 0x7f {
			return fmt.Errorf("第 %d 行第 %d 列: 键名含控制字符: %w", line, col, ErrYAML)
		}
	}
	return nil
}

// isSeqItem 判断内容是否是序列项（`-` 或 `- xxx`）。
func isSeqItem(s string) bool {
	return s == "-" || strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "-\t")
}

// trimSeqMarker 去掉序列项的 `-` 前缀，返回剩余内容与剩余内容的起始列（1 起）。
func trimSeqMarker(s string) (rest string, col int) {
	if s == "-" {
		return "", 2
	}
	i := 1
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:], i + 1
}

// rejectUnsupported 对文本做一次"不支持特性"体检，保证这些语法在
// 任何位置（键、值、映射值、序列项、嵌套层）都显式报 unsupported 错误，
// 而不是被当成普通字符串静默接受。
func rejectUnsupported(s string, line, col int) error {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	if trimmed == "---" || trimmed == "..." || strings.HasPrefix(trimmed, "--- ") {
		return fmt.Errorf("第 %d 行: 不支持多文档分隔符 %q（YAML 子集只允许单文档）: %w",
			line, trimmed, ErrUnsupported)
	}
	switch trimmed[0] {
	case '%':
		return unsupportedAt(line, col, "YAML 指令 %")
	case '&':
		return unsupportedAt(line, col, "锚点 &")
	case '*':
		return unsupportedAt(line, col, "别名 *")
	case '!':
		return unsupportedAt(line, col, "标签 !")
	case '{', '[':
		return unsupportedAt(line, col, fmt.Sprintf("流式集合 %c（请改用块式映射/序列）", trimmed[0]))
	case '|', '>':
		return unsupportedAt(line, col, fmt.Sprintf("块标量 %c（请改用单行标量）", trimmed[0]))
	}
	return nil
}

// unsupportedAt 生成带定位的"不支持"错误。
func unsupportedAt(line, col int, what string) error {
	return fmt.Errorf("第 %d 行第 %d 列: 不支持%s: %w", line, col, what, ErrUnsupported)
}

// seqItemHasChildMapping 判断 `- key:` 之后的下一个有效行是否缩进到 keyIndent+seqChildIndent，
// 即它属于本序列项的内联映射（`- key:` 的续行），而不是同级的兄弟序列项。
// 行已耗尽，或缩进不是 keyIndent+2 时都返回 false。
func seqItemHasChildMapping(p *yamlParser, keyIndent int) bool {
	if p.pos >= len(p.lines) {
		return false
	}
	return p.lines[p.pos].indent == keyIndent+seqChildIndent
}

// at 给错误补上"第 N 行第 M 列"前缀。
func at(line, col int, err error) error {
	return fmt.Errorf("第 %d 行第 %d 列: %w", line, col, err)
}
