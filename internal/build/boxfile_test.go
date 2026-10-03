// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package build

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestParseBoxfileOK 覆盖全部合法指令、注释、空行与续行。
func TestParseBoxfileOK(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want *Boxfile
	}{
		{
			name: "全部指令各一条",
			in: `# 注释行
FROM alice/myapp:v1

COPY app /app
COPY conf/app.yaml /etc/app.yaml
ENV LANG=C.UTF-8
ENV PATH /usr/local/bin
WORKDIR /app
ENTRYPOINT ["/usr/bin/myapp"]
CMD ["--config", "/etc/app.yaml"]
EXPOSE 8080/tcp
EXPOSE 9090
VOLUME /data
LABEL org.licore.maintainer=alice
USER 1000:1000
ARG VERSION=1.2.3
`,
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpCopy, Args: []string{"app", "/app"}, Line: 4, Raw: "COPY app /app"},
					{Op: OpCopy, Args: []string{"conf/app.yaml", "/etc/app.yaml"}, Line: 5, Raw: "COPY conf/app.yaml /etc/app.yaml"},
					{Op: OpEnv, Args: []string{"LANG", "C.UTF-8"}, Line: 6, Raw: "ENV LANG=C.UTF-8"},
					{Op: OpEnv, Args: []string{"PATH", "/usr/local/bin"}, Line: 7, Raw: "ENV PATH /usr/local/bin"},
					{Op: OpWorkdir, Args: []string{"/app"}, Line: 8, Raw: "WORKDIR /app"},
					{Op: OpEntrypoint, Args: []string{"/usr/bin/myapp"}, Line: 9, Raw: `ENTRYPOINT ["/usr/bin/myapp"]`},
					{Op: OpCmd, Args: []string{"--config", "/etc/app.yaml"}, Line: 10, Raw: `CMD ["--config", "/etc/app.yaml"]`},
					{Op: OpExpose, Args: []string{"8080/tcp"}, Line: 11, Raw: "EXPOSE 8080/tcp"},
					{Op: OpExpose, Args: []string{"9090"}, Line: 12, Raw: "EXPOSE 9090"},
					{Op: OpVolume, Args: []string{"/data"}, Line: 13, Raw: "VOLUME /data"},
					{Op: OpLabel, Args: []string{"org.licore.maintainer", "alice"}, Line: 14, Raw: "LABEL org.licore.maintainer=alice"},
					{Op: OpUser, Args: []string{"1000:1000"}, Line: 15, Raw: "USER 1000:1000"},
					{Op: OpArg, Args: []string{"VERSION", "1.2.3"}, Line: 16, Raw: "ARG VERSION=1.2.3"},
				},
			},
		},
		{
			name: "仅 FROM",
			in:   "FROM alice/myapp:v1\n",
			want: &Boxfile{From: "alice/myapp:v1"},
		},
		{
			name: "行内注释与多余空白",
			in: "   FROM    alice/myapp:v1   # 基础镜像\n" +
				"\t# 缩进注释\n" +
				"    \n" +
				"COPY  app   /app\n",
			want: &Boxfile{
				From:         "alice/myapp:v1",
				Instructions: []Instruction{{Op: OpCopy, Args: []string{"app", "/app"}, Line: 4, Raw: "COPY  app   /app"}},
			},
		},
		{
			name: "反斜杠续行",
			in: "FROM alice/myapp:v1\n" +
				"COPY app \\\n" +
				"     /app\n" +
				"WORKDIR \\\n" +
				"  /opt/app\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpCopy, Args: []string{"app", "/app"}, Line: 2, Raw: "COPY app /app"},
					{Op: OpWorkdir, Args: []string{"/opt/app"}, Line: 4, Raw: "WORKDIR /opt/app"},
				},
			},
		},
		{
			name: "续行后紧跟注释行时注释不参与正文",
			in: "FROM alice/myapp:v1\n" +
				"COPY app \\\n" +
				"     /app # 尾注释\n",
			want: &Boxfile{
				From:         "alice/myapp:v1",
				Instructions: []Instruction{{Op: OpCopy, Args: []string{"app", "/app"}, Line: 2, Raw: "COPY app /app"}},
			},
		},
		{
			name: "反斜杠后跟 # 仍是续行，注释被剥离",
			in: "FROM alice/myapp:v1\n" +
				"ENV FOO=bar \\ # 尾注释\n" +
				"  baz\n" +
				"ENV BAZ=qux\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpEnv, Args: []string{"FOO", "bar baz"}, Line: 2, Raw: "ENV FOO=bar baz"},
					{Op: OpEnv, Args: []string{"BAZ", "qux"}, Line: 4, Raw: "ENV BAZ=qux"},
				},
			},
		},
		{
			name: "CRLF 行尾",
			in:   "FROM alice/myapp:v1\r\nCOPY app /app\r\n",
			want: &Boxfile{
				From:         "alice/myapp:v1",
				Instructions: []Instruction{{Op: OpCopy, Args: []string{"app", "/app"}, Line: 2, Raw: "COPY app /app"}},
			},
		},
		{
			name: "指令大小写不敏感",
			in: "from alice/myapp:v1\n" +
				"Workdir /app\n" +
				"env LANG=C.UTF-8\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpWorkdir, Args: []string{"/app"}, Line: 2, Raw: "Workdir /app"},
					{Op: OpEnv, Args: []string{"LANG", "C.UTF-8"}, Line: 3, Raw: "env LANG=C.UTF-8"},
				},
			},
		},
		{
			name: "ENTRYPOINT 与 CMD 的 JSON 形态",
			in: "FROM alice/myapp:v1\n" +
				"ENTRYPOINT [\"/bin/sh\",\"-c\"]\n" +
				"CMD [\"echo\",\"a b\"]\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpEntrypoint, Args: []string{"/bin/sh", "-c"}, Line: 2, Raw: `ENTRYPOINT ["/bin/sh","-c"]`},
					{Op: OpCmd, Args: []string{"echo", "a b"}, Line: 3, Raw: `CMD ["echo","a b"]`},
				},
			},
		},
		{
			name: "ARG 默认值展开",
			in: "FROM alice/myapp:v1\n" +
				"ARG BASE=/opt\n" +
				"ARG TAG=latest\n" +
				"WORKDIR ${BASE}/app\n" +
				"ENV VERSION=${TAG}\n" +
				"LABEL app.version=${TAG}\n" +
				"COPY ${BASE}/src /src\n" +
				"ARG BARE\n" +
				"ENV BARE_VALUE=${BARE}\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpArg, Args: []string{"BASE", "/opt"}, Line: 2, Raw: "ARG BASE=/opt"},
					{Op: OpArg, Args: []string{"TAG", "latest"}, Line: 3, Raw: "ARG TAG=latest"},
					{Op: OpWorkdir, Args: []string{"/opt/app"}, Line: 4, Raw: "WORKDIR ${BASE}/app"},
					{Op: OpEnv, Args: []string{"VERSION", "latest"}, Line: 5, Raw: "ENV VERSION=${TAG}"},
					{Op: OpLabel, Args: []string{"app.version", "latest"}, Line: 6, Raw: "LABEL app.version=${TAG}"},
					{Op: OpCopy, Args: []string{"/opt/src", "/src"}, Line: 7, Raw: "COPY ${BASE}/src /src"},
					{Op: OpArg, Args: []string{"BARE", ""}, Line: 8, Raw: "ARG BARE"},
					{Op: OpEnv, Args: []string{"BARE_VALUE", ""}, Line: 9, Raw: "ENV BARE_VALUE=${BARE}"},
				},
			},
		},
		{
			name: "占位符风格不展开",
			in: "FROM alice/myapp:v1\n" +
				"ARG NAME=alice\n" +
				"ENV GREETING=$NAME\n" +
				"ENV BRACES=${NAME}\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpArg, Args: []string{"NAME", "alice"}, Line: 2, Raw: "ARG NAME=alice"},
					{Op: OpEnv, Args: []string{"GREETING", "$NAME"}, Line: 3, Raw: "ENV GREETING=$NAME"},
					{Op: OpEnv, Args: []string{"BRACES", "alice"}, Line: 4, Raw: "ENV BRACES=${NAME}"},
				},
			},
		},
		{
			name: "ENV 值允许含空格",
			in: "FROM alice/myapp:v1\n" +
				"ENV GREETING=hello world\n" +
				"ENV EMPTY_VALUE=\n" +
				"ENV MOTD say hi there\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpEnv, Args: []string{"GREETING", "hello world"}, Line: 2, Raw: "ENV GREETING=hello world"},
					{Op: OpEnv, Args: []string{"EMPTY_VALUE", ""}, Line: 3, Raw: "ENV EMPTY_VALUE="},
					{Op: OpEnv, Args: []string{"MOTD", "say hi there"}, Line: 4, Raw: "ENV MOTD say hi there"},
				},
			},
		},
		{
			name: "USER 与端口的各种合法形态",
			in: "FROM alice/myapp:v1\n" +
				"USER root\n" +
				"USER 1000\n" +
				"USER app:app\n" +
				"EXPOSE 1\n" +
				"EXPOSE 65535/udp\n" +
				"LABEL a_b-c.d=1\n",
			want: &Boxfile{
				From: "alice/myapp:v1",
				Instructions: []Instruction{
					{Op: OpUser, Args: []string{"root"}, Line: 2, Raw: "USER root"},
					{Op: OpUser, Args: []string{"1000"}, Line: 3, Raw: "USER 1000"},
					{Op: OpUser, Args: []string{"app:app"}, Line: 4, Raw: "USER app:app"},
					{Op: OpExpose, Args: []string{"1"}, Line: 5, Raw: "EXPOSE 1"},
					{Op: OpExpose, Args: []string{"65535/udp"}, Line: 6, Raw: "EXPOSE 65535/udp"},
					{Op: OpLabel, Args: []string{"a_b-c.d", "1"}, Line: 7, Raw: "LABEL a_b-c.d=1"},
				},
			},
		},
		{
			name: "行号始终是物理行号（注释与空行不占号）",
			in:   "\n\n# 头注释\nFROM alice/myapp:v1\n\n# 中注释\n\nCOPY app /app\n",
			want: &Boxfile{
				From:         "alice/myapp:v1",
				Instructions: []Instruction{{Op: OpCopy, Args: []string{"app", "/app"}, Line: 8, Raw: "COPY app /app"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseBoxfile([]byte(tt.in))
			if err != nil {
				t.Fatalf("ParseBoxfile() 返回错误: %v", err)
			}
			if got.From != tt.want.From {
				t.Errorf("From = %q, 期望 %q", got.From, tt.want.From)
			}
			if len(got.Instructions) != len(tt.want.Instructions) {
				t.Fatalf("指令条数 = %d, 期望 %d: %+v", len(got.Instructions), len(tt.want.Instructions), got.Instructions)
			}
			for i, want := range tt.want.Instructions {
				g := got.Instructions[i]
				if g.Op != want.Op || g.Line != want.Line || g.Raw != want.Raw {
					t.Errorf("指令[%d] = {Op:%q Line:%d Raw:%q}, 期望 {Op:%q Line:%d Raw:%q}",
						i, g.Op, g.Line, g.Raw, want.Op, want.Line, want.Raw)
				}
				if want.Args == nil {
					want.Args = []string{}
				}
				if !reflect.DeepEqual(g.Args, want.Args) {
					t.Errorf("指令[%d] Args = %#v, 期望 %#v", i, g.Args, want.Args)
				}
			}
		})
	}
}

// TestParseBoxfileErrors 覆盖全部拒绝规则，并断言哨兵错误。
func TestParseBoxfileErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
		// contains 是希望在错误信息中出现的关键字（小写匹配），用于确认报错可读。
		contains []string
		// noLine 表示该错误是文件级错误，不携带行号。
		noLine bool
	}{
		{name: "缺少 FROM", in: "# 只有注释\n\n", want: ErrNoFrom, contains: []string{"from"}, noLine: true},
		{name: "没有任何 FROM", in: "COPY app /app\n", want: ErrNoFrom, contains: []string{"FROM"}, noLine: true},
		{name: "空文件", in: "", want: ErrNoFrom, noLine: true},
		{name: "FROM 不是首条", in: "WORKDIR /app\nFROM alice/myapp:v1\n", want: ErrFromNotFirst, contains: []string{"第 2 行", "首条"}},
		{name: "FROM 重复", in: "FROM alice/myapp:v1\nFROM bob/base:v2\n", want: ErrFromNotFirst, contains: []string{"第 2 行", "重复"}},
		{name: "FROM 引用为空", in: "FROM   \n", want: ErrBadInstruction, contains: []string{"第 1 行"}},
		{name: "FROM 缺少参数", in: "FROM\n", want: ErrBadInstruction, contains: []string{"第 1 行"}},
		{name: "FROM 缺少版本", in: "FROM alice/myapp\n", want: ErrBadInstruction, contains: []string{":version"}},
		{name: "FROM 版本为空", in: "FROM alice/myapp:\n", want: ErrBadInstruction, contains: []string{"version"}},
		{name: "FROM 参数过多", in: "FROM alice/myapp:v1 extra\n", want: ErrBadInstruction, contains: []string{"第 1 行"}},
		{name: "未知指令", in: "FROM alice/myapp:v1\nFROB nicate\n", want: ErrBadInstruction, contains: []string{"第 2 行", "未知指令"}},
		{name: "RUN 未实现", in: "FROM alice/myapp:v1\nRUN echo hi\n", want: ErrBadInstruction, contains: []string{"第 2 行", "尚未实现"}},
		{name: "ADD 未实现", in: "FROM alice/myapp:v1\nADD http://x/y.tar.gz /y\n", want: ErrBadInstruction, contains: []string{"第 2 行", "尚未实现"}},
		{name: "COPY 参数不足", in: "FROM alice/myapp:v1\nCOPY app\n", want: ErrBadInstruction, contains: []string{"第 2 行", "两个参数"}},
		{name: "COPY 参数过多", in: "FROM alice/myapp:v1\nCOPY a b c\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "ENV 缺少值", in: "FROM alice/myapp:v1\nENV LANG\n", want: ErrBadInstruction, contains: []string{"第 2 行", "KEY=VALUE"}},
		{name: "ENV 键非法", in: "FROM alice/myapp:v1\nENV 1LANG=C\n", want: ErrBadInstruction, contains: []string{"第 2 行", "变量名"}},
		{name: "ENV 键缺失", in: "FROM alice/myapp:v1\nENV =value\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "ENV 值含多个等号", in: "FROM alice/myapp:v1\nENV LANG=a=b\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "WORKDIR 相对路径", in: "FROM alice/myapp:v1\nWORKDIR app\n", want: ErrBadInstruction, contains: []string{"第 2 行", "绝对路径"}},
		{name: "WORKDIR 含上跳段", in: "FROM alice/myapp:v1\nWORKDIR /app/../etc\n", want: ErrBadInstruction, contains: []string{"第 2 行", ".."}},
		{name: "WORKDIR 参数过多", in: "FROM alice/myapp:v1\nWORKDIR /a /b\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "ENTRYPOINT shell 形式", in: "FROM alice/myapp:v1\nENTRYPOINT /bin/sh -c echo\n", want: ErrBadInstruction, contains: []string{"第 2 行", "JSON 数组"}},
		{name: "CMD shell 形式", in: "FROM alice/myapp:v1\nCMD echo hi\n", want: ErrBadInstruction, contains: []string{"第 2 行", "JSON 数组"}},
		{name: "CMD 非数组 JSON", in: "FROM alice/myapp:v1\nCMD \"echo\"\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "CMD JSON 元素非字符串", in: "FROM alice/myapp:v1\nCMD [\"echo\", 1]\n", want: ErrBadInstruction, contains: []string{"第 2 行", "解析失败"}},
		{name: "CMD JSON 语法错误", in: "FROM alice/myapp:v1\nCMD [\"echo\",\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "CMD 缺参数", in: "FROM alice/myapp:v1\nCMD\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "EXPOSE 端口越界", in: "FROM alice/myapp:v1\nEXPOSE 70000\n", want: ErrBadInstruction, contains: []string{"第 2 行", "65535"}},
		{name: "EXPOSE 端口非数字", in: "FROM alice/myapp:v1\nEXPOSE http\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "EXPOSE 协议非法", in: "FROM alice/myapp:v1\nEXPOSE 8080/sctp\n", want: ErrBadInstruction, contains: []string{"第 2 行", "tcp"}},
		{name: "VOLUME 相对路径", in: "FROM alice/myapp:v1\nVOLUME data\n", want: ErrBadInstruction, contains: []string{"第 2 行", "绝对路径"}},
		{name: "VOLUME 缺参数", in: "FROM alice/myapp:v1\nVOLUME\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "LABEL 缺等号", in: "FROM alice/myapp:v1\nLABEL maintainer\n", want: ErrBadInstruction, contains: []string{"第 2 行", "="}},
		{name: "LABEL 键非法", in: "FROM alice/myapp:v1\nLABEL -bad=1\n", want: ErrBadInstruction, contains: []string{"第 2 行", "LABEL 键"}},
		{name: "LABEL 值为空", in: "FROM alice/myapp:v1\nLABEL k=\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "USER 非法字符", in: "FROM alice/myapp:v1\nUSER ad min\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "USER 组为空", in: "FROM alice/myapp:v1\nUSER app:\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "ARG 名非法", in: "FROM alice/myapp:v1\nARG 1BAD=x\n", want: ErrBadInstruction, contains: []string{"第 2 行", "ARG 名"}},
		{name: "ARG 重复声明", in: "FROM alice/myapp:v1\nARG A=1\nARG A=2\n", want: ErrBadInstruction, contains: []string{"第 3 行", "重复"}},
		{name: "ARG 缺参数", in: "FROM alice/myapp:v1\nARG\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "未声明变量", in: "FROM alice/myapp:v1\nENV V=${MISSING}\n", want: ErrBadInstruction, contains: []string{"第 2 行", "MISSING"}},
		{name: "ARG 之前使用变量", in: "FROM alice/myapp:v1\nWORKDIR ${LATE}/app\nARG LATE=/opt\n", want: ErrBadInstruction, contains: []string{"第 2 行"}},
		{name: "变量缺少右括号", in: "FROM alice/myapp:v1\nARG A=1\nENV V=${A\n", want: ErrBadInstruction, contains: []string{"第 3 行", "}"}},
		{name: "变量名为空", in: "FROM alice/myapp:v1\nENV V=${}\n", want: ErrBadInstruction, contains: []string{"第 2 行", "变量名"}},
		{name: "续行后的非法指令同样给出行号", in: "FROM alice/myapp:v1\n# 注释\nRUN x \\\n y\n", want: ErrBadInstruction, contains: []string{"第 3 行"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseBoxfile([]byte(tt.in))
			if err == nil {
				t.Fatalf("ParseBoxfile() 未报错，返回 %+v", got)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("错误 = %v, 期望 errors.Is(_, %v)", err, tt.want)
			}
			low := strings.ToLower(err.Error())
			if !tt.noLine && !strings.Contains(low, "第 ") {
				t.Errorf("错误信息 %q 未带行号", err.Error())
			}
			for _, c := range tt.contains {
				if !strings.Contains(low, strings.ToLower(c)) {
					t.Errorf("错误信息 %q 未包含 %q", err.Error(), c)
				}
			}
		})
	}
}

// TestParseBoxfileLimits 覆盖体积上限与空字节拒绝。
func TestParseBoxfileLimits(t *testing.T) {
	t.Parallel()

	t.Run("含空字节", func(t *testing.T) {
		t.Parallel()
		_, err := ParseBoxfile([]byte("FROM alice/myapp:v1\n\x00\n"))
		if !errors.Is(err, ErrBadBoxfile) {
			t.Fatalf("错误 = %v, 期望 %v", err, ErrBadBoxfile)
		}
	})

	t.Run("超出体积上限", func(t *testing.T) {
		t.Parallel()
		big := make([]byte, MaxBoxfileBytes+1)
		for i := range big {
			big[i] = '\n'
		}
		copy(big, "FROM alice/myapp:v1\n")
		_, err := ParseBoxfile(big)
		if !errors.Is(err, ErrBadBoxfile) {
			t.Fatalf("错误 = %v, 期望 %v", err, ErrBadBoxfile)
		}
	})
}

// TestParseBoxfileFile 覆盖文件读取路径与错误包装。
func TestParseBoxfileFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	good := filepath.Join(dir, "Boxfile")
	if err := os.WriteFile(good, []byte("FROM alice/myapp:v1\nCOPY app /app\n"), 0o600); err != nil {
		t.Fatalf("写入测试 Boxfile 失败: %v", err)
	}

	bf, err := ParseBoxfileFile(good)
	if err != nil {
		t.Fatalf("ParseBoxfileFile() 返回错误: %v", err)
	}
	if bf.From != "alice/myapp:v1" || len(bf.Instructions) != 1 || bf.Instructions[0].Op != OpCopy {
		t.Fatalf("解析结果异常: %+v", bf)
	}

	t.Run("路径为空", func(t *testing.T) {
		t.Parallel()
		if _, err := ParseBoxfileFile(""); !errors.Is(err, ErrBadBoxfile) {
			t.Fatalf("错误 = %v, 期望 %v", err, ErrBadBoxfile)
		}
	})

	t.Run("文件不存在", func(t *testing.T) {
		t.Parallel()
		_, err := ParseBoxfileFile(filepath.Join(dir, "nope"))
		if !errors.Is(err, ErrBadBoxfile) {
			t.Fatalf("错误 = %v, 期望 %v", err, ErrBadBoxfile)
		}
	})

	t.Run("目录不是文件", func(t *testing.T) {
		t.Parallel()
		if _, err := ParseBoxfileFile(dir); !errors.Is(err, ErrBadBoxfile) {
			t.Fatalf("错误 = %v, 期望 %v", err, ErrBadBoxfile)
		}
	})

	t.Run("内容非法时哨兵透传", func(t *testing.T) {
		t.Parallel()
		bad := filepath.Join(dir, "Boxfile.bad")
		if err := os.WriteFile(bad, []byte("COPY app /app\n"), 0o600); err != nil {
			t.Fatalf("写入测试 Boxfile 失败: %v", err)
		}
		_, err := ParseBoxfileFile(bad)
		if !errors.Is(err, ErrNoFrom) {
			t.Fatalf("错误 = %v, 期望 %v", err, ErrNoFrom)
		}
		if !strings.Contains(err.Error(), bad) {
			t.Errorf("错误信息 %q 未包含文件路径", err.Error())
		}
	})
}

// TestCheckContext 覆盖构建上下文校验。
func TestCheckContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	if err := CheckContext(dir); err != nil {
		t.Fatalf("CheckContext(目录) = %v, 期望 nil", err)
	}
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"空路径", ""},
		{"不是目录", file},
		{"不存在", filepath.Join(dir, "missing")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := CheckContext(tc.in); !errors.Is(err, ErrNoContext) {
				t.Fatalf("CheckContext(%q) = %v, 期望 %v", tc.in, err, ErrNoContext)
			}
		})
	}
}

// TestResolveContextPath 覆盖上下文内相对路径解析与逃逸拒绝。
func TestResolveContextPath(t *testing.T) {
	t.Parallel()

	ctx := t.TempDir()

	got, err := ResolveContextPath(ctx, "src/app/main.go")
	if err != nil {
		t.Fatalf("ResolveContextPath() 返回错误: %v", err)
	}
	want := filepath.Join(ctx, "src", "app", "main.go")
	if got != want {
		t.Fatalf("解析结果 = %q, 期望 %q", got, want)
	}

	abs, err := filepath.Abs(ctx)
	if err != nil {
		t.Fatalf("Abs 失败: %v", err)
	}
	for _, rel := range []string{
		"",
		"/etc/passwd",
		"..",
		"../outside",
		"a/../../outside",
		"a/./b",
		"a//b",
		"a\\b",
	} {
		t.Run("拒绝_"+rel, func(t *testing.T) {
			rel := rel
			t.Parallel()
			if _, err := ResolveContextPath(abs, rel); !errors.Is(err, ErrNoContext) {
				t.Fatalf("ResolveContextPath(%q) = %v, 期望 %v", rel, err, ErrNoContext)
			}
		})
	}
}

// TestResolveRootfsPath 覆盖 rootfs 内目标路径解析与逃逸拒绝。
func TestResolveRootfsPath(t *testing.T) {
	t.Parallel()

	rootfs := t.TempDir()

	got, err := ResolveRootfsPath(rootfs, "app/bin")
	if err != nil {
		t.Fatalf("ResolveRootfsPath() 返回错误: %v", err)
	}
	if want := filepath.Join(rootfs, "app", "bin"); got != want {
		t.Fatalf("解析结果 = %q, 期望 %q", got, want)
	}

	abs, err := filepath.Abs(rootfs)
	if err != nil {
		t.Fatalf("Abs 失败: %v", err)
	}
	for _, rel := range []string{"", "/app", "../escape", "a/../b", "a//b"} {
		t.Run("拒绝_"+rel, func(t *testing.T) {
			rel := rel
			t.Parallel()
			if _, err := ResolveRootfsPath(abs, rel); !errors.Is(err, ErrNoContext) {
				t.Fatalf("ResolveRootfsPath(%q) = %v, 期望 %v", rel, err, ErrNoContext)
			}
		})
	}
}

// TestJoinContinuations 直接覆盖行拼接与注释剥离的实现细节。
func TestJoinContinuations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []logicalLine
	}{
		{
			name: "空行与注释行不产出逻辑行",
			in:   "COPY a /a\n\n# c\nENV K=V\n",
			want: []logicalLine{
				{line: 1, text: "COPY a /a", raw: "COPY a /a"},
				{line: 4, text: "ENV K=V", raw: "ENV K=V"},
			},
		},
		{
			name: "连续三行续行",
			in:   "COPY a \\\n  b \\\n  c\n",
			want: []logicalLine{{line: 1, text: "COPY a b c", raw: "COPY a b c"}},
		},
		{
			name: "末尾悬空续行按一行处理",
			in:   "COPY a \\\n",
			want: []logicalLine{{line: 1, text: "COPY a", raw: "COPY a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := joinContinuations(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("joinContinuations() = %#v, 期望 %#v", got, tt.want)
			}
		})
	}
}
