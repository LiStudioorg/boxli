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

// ---------- 标量 ----------

// TestParseScalars 覆盖裸标量、引号标量、布尔、空值与数字的字面量转换。
func TestParseScalars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]any
	}{
		{"纯字符串", "k: hello\n", map[string]any{"k": "hello"}},
		{"带空格字符串", "k: hello world\n", map[string]any{"k": "hello world"}},
		{"单引号不做转义", `k: 'a\nb'`, map[string]any{"k": `a\nb`}},
		{"单引号内双写转义", "k: 'it''s'\n", map[string]any{"k": "it's"}},
		{"双引号换行转义", `k: "a\nb"`, map[string]any{"k": "a\nb"}},
		{"双引号引号转义", `k: "say \"hi\""`, map[string]any{"k": `say "hi"`}},
		{"双引号反斜杠转义", `k: "a\\b"`, map[string]any{"k": `a\b`}},
		{"双引号制表符转义", `k: "a\tb"`, map[string]any{"k": "a\tb"}},
		{"空值nil", "k:\n", map[string]any{"k": nil}},
		{"null字面量", "k: null\n", map[string]any{"k": nil}},
		{"波浪号", "k: ~\n", map[string]any{"k": nil}},
		{"NULL大写", "k: NULL\n", map[string]any{"k": nil}},
		{"true", "k: true\n", map[string]any{"k": true}},
		{"false", "k: false\n", map[string]any{"k": false}},
		{"True首字母大写", "k: True\n", map[string]any{"k": true}},
		{"正整数", "k: 42\n", map[string]any{"k": int64(42)}},
		{"负整数", "k: -7\n", map[string]any{"k": int64(-7)}},
		{"零", "k: 0\n", map[string]any{"k": int64(0)}},
		{"正浮点", "k: 3.5\n", map[string]any{"k": 3.5}},
		{"负浮点", "k: -0.25\n", map[string]any{"k": -0.25}},
		{"带冒号的裸标量不是键", "k: 12:30\n", map[string]any{"k": "12:30"}},
		{"字符串保持原样", "k: v1.2.3\n", map[string]any{"k": "v1.2.3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.in))
			if err != nil {
				t.Fatalf("Parse(%q) 返回错误: %v", tc.in, err)
			}
			if want, ok := tc.want["k"]; ok || len(tc.want) == 1 {
				m, ok := got.(map[string]any)
				if !ok {
					t.Fatalf("Parse(%q) = %T，期望 map[string]any", tc.in, got)
				}
				if !reflect.DeepEqual(m["k"], want) {
					t.Fatalf("Parse(%q)[k] = %#v (%T)，期望 %#v (%T)", tc.in, m["k"], m["k"], want, want)
				}
			}
		})
	}
}

// TestParseQuoteIsLiteral 确认引号内的 # 与 : 等记号不会被当作语法记号。
func TestParseQuoteIsLiteral(t *testing.T) {
	got, err := Parse([]byte(`k: "a # b: c"`))
	if err != nil {
		t.Fatalf("Parse 返回错误: %v", err)
	}
	m := got.(map[string]any)
	if m["k"] != "a # b: c" {
		t.Fatalf("引号内内容 = %#v，期望 %q", m["k"], "a # b: c")
	}
}

// ---------- 嵌套结构 ----------

// TestParseNestedMapping 覆盖两层与四层嵌套映射。
func TestParseNestedMapping(t *testing.T) {
	src := "a:\n  b:\n    c:\n      d: deep\n"
	got, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse 返回错误: %v", err)
	}
	want := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": map[string]any{"d": "deep"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("四层嵌套 = %#v，期望 %#v", got, want)
	}
}

// TestParseSequence 覆盖标量序列、序列下的键以及序列中的映射。
func TestParseSequence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want any
	}{
		{
			"标量序列",
			"list:\n  - alpha\n  - beta\n",
			map[string]any{"list": []any{"alpha", "beta"}},
		},
		{
			"单个标量序列项",
			"list:\n  - only\n",
			map[string]any{"list": []any{"only"}},
		},
		{
			"带引号的序列项",
			"ports:\n  - \"80:80\"\n  - \"81:81\"\n",
			map[string]any{"ports": []any{"80:80", "81:81"}},
		},
		{
			"数字序列项",
			"nums:\n  - 1\n  - -2\n  - 3.5\n",
			map[string]any{"nums": []any{int64(1), int64(-2), 3.5}},
		},
		{
			"顶层序列",
			"- a\n- b\n",
			[]any{"a", "b"},
		},
		{
			"序列项的续行键",
			"list:\n  - name: a\n    v: 1\n  - name: b\n",
			map[string]any{"list": []any{
				map[string]any{"name": "a", "v": int64(1)},
				map[string]any{"name": "b"},
			}},
		},
		{
			"序列项下的嵌套映射",
			"list:\n  - name: a\n    dev:\n      watch:\n        - src\n",
			map[string]any{"list": []any{
				map[string]any{"name": "a", "dev": map[string]any{"watch": []any{"src"}}},
			}},
		},
		{
			"空序列项值为空",
			"list:\n  - a\n  -\n  - c\n",
			map[string]any{"list": []any{"a", nil, "c"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.in))
			if err != nil {
				t.Fatalf("Parse(%q) 返回错误: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Parse(%q) = %#v，期望 %#v", tc.in, got, tc.want)
			}
		})
	}
}

// ---------- 注释与空行 ----------

// TestParseCommentsAndBlankLines 覆盖注释、行尾注释与空行。
func TestParseCommentsAndBlankLines(t *testing.T) {
	src := "" +
		"# 整行注释\n" +
		"\n" +
		"a: 1 # 行尾注释\n" +
		"   \n" +
		"b: two\n" +
		"# 结尾注释\n"
	got, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse 返回错误: %v", err)
	}
	want := map[string]any{"a": int64(1), "b": "two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("带注释的解析结果 = %#v，期望 %#v", got, want)
	}
}

// TestParseHashInsideQuotesIsContent 确认引号里的 # 不是注释起点。
func TestParseHashInsideQuotesIsContent(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`k: "a#b"`, "a#b"},
		{"k: 'a#b'", "a#b"},
		{`k: "a # b"`, "a # b"},
		{"k: v#notcomment\n", "v#notcomment"},
	}
	for _, tc := range tests {
		got, err := Parse([]byte(tc.in))
		if err != nil {
			t.Fatalf("Parse(%q) 返回错误: %v", tc.in, err)
		}
		m := got.(map[string]any)
		if m["k"] != tc.want {
			t.Fatalf("Parse(%q)[k] = %#v，期望 %q", tc.in, m["k"], tc.want)
		}
	}
}

// ---------- 空文档 ----------

// TestParseEmptyDocument 确认只有注释或空白的文档解析为 nil。
func TestParseEmptyDocument(t *testing.T) {
	for _, in := range []string{"", "\n\n", "# 只有注释\n", "   \n\t\n"} {
		got, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("Parse(%q) 返回错误: %v", in, err)
		}
		if got != nil {
			t.Fatalf("Parse(%q) = %#v，期望 nil", in, got)
		}
	}
}

// TestParseOversize 确认超过体积上限的输入被拒绝。
func TestParseOversize(t *testing.T) {
	big := strings.Repeat("k: v\n", (maxYAMLBytes/5)+2)
	if _, err := Parse([]byte(big)); err == nil {
		t.Fatal("超过体积上限的输入未报错")
	} else if !errors.Is(err, ErrYAML) {
		t.Fatalf("体积超限错误 = %v，期望可匹配 ErrYAML", err)
	}
}

// ---------- 错误 ----------

// TestParseErrors 覆盖语法错误与"明确不支持"的 YAML 特性。
func TestParseErrors(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		unsupport bool // true 表示期望 errors.Is(err, ErrUnsupported)
	}{
		{"制表符缩进", "a:\n\tb: 1\n", false},
		{"重复键", "a: 1\na: 2\n", false},
		{"嵌套层重复键", "a:\n  b: 1\n  b: 2\n", false},
		{"缩进超出层级", "a:\n  b: 1\n   c: 2\n", false},
		{"缩进回退到不存在的层级", "a:\n    b: 1\n  c: 2\n", false},
		{"锚点", "a: &anchor 1\n", true},
		{"锚点定义在键上", "&a: 1\n", true},
		{"别名", "a: *ref\n", true},
		{"标签", "a: !!str 1\n", true},
		{"文档开始标记", "---\na: 1\n", true},
		{"文档结束标记", "a: 1\n...\n", true},
		{"流式映射", "a: {b: 1}\n", true},
		{"流式序列", "a: [1, 2]\n", true},
		{"块标量竖线", "a: |\n  text\n", true},
		{"块标量折叠", "a: >\n  text\n", true},
		{"YAML 指令", "%YAML 1.2\na: 1\n", true},
		{"双引号未闭合", `a: "unterminated` + "\n", false},
		{"单引号未闭合", "a: 'unterminated\n", false},
		{"双引号后的多余内容", `a: "x" y` + "\n", false},
		{"双引号内非法转义", `a: "x\q"` + "\n", false},
		{"空键名", ": 1\n", false},
		{"映射中直接出现序列项", "a: 1\n- b\n", false},
		{"期望键值但只有标量", "a: 1\nb\n", false},
		{"序列项下的标量缩进非法", "a:\n  -\n      x\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.in))
			if err == nil {
				t.Fatalf("Parse(%q) 未报错，返回 %#v", tc.in, got)
			}
			if tc.unsupport {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("Parse(%q) 错误 = %v，期望可匹配 ErrUnsupported", tc.in, err)
				}
				return
			}
			if !errors.Is(err, ErrYAML) {
				t.Fatalf("Parse(%q) 错误 = %v，期望可匹配 ErrYAML", tc.in, err)
			}
		})
	}
}

// TestParseErrorMentionsLine 确认语法错误带行号（便于用户定位）。
func TestParseErrorMentionsLine(t *testing.T) {
	_, err := Parse([]byte("a: 1\nb: 2\n\tc: 3\n"))
	if err == nil {
		t.Fatal("未报错")
	}
	if !strings.Contains(err.Error(), "第 3 行") {
		t.Fatalf("错误未指出第 3 行: %v", err)
	}
}

// ---------- ParseFile ----------

// TestParseFile 覆盖文件解析与读文件失败。
func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(path, []byte("a: 1\nb:\n  - x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile 返回错误: %v", err)
	}
	want := map[string]any{"a": int64(1), "b": []any{"x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseFile = %#v，期望 %#v", got, want)
	}

	if _, err := ParseFile(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("缺失文件未报错")
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("a: &x 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(bad); err == nil {
		t.Fatal("非法 YAML 未报错")
	} else if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("错误 = %v，期望可匹配 ErrUnsupported", err)
	}
}
