// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package scaffold 负责 licore 项目的脚手架（`licore init`）与静态检查（`licore lint`）。
//
// 脚手架产出两个文件：`Boxfile`（构建描述）与 `licore-compose.yml`（本地编排），
// 它们必须能被 internal/build 与 internal/compose 直接解析，并由本包自己的
// lint 规则检查出零个 error 级诊断——"init 产出的项目开箱即 lint 干净"是硬约束。
//
// 静态检查不依赖内部解析器：parse 错误以 Diagnostic 形式上报（带行号），
// 且任一问题都不会中断后续规则，保证一次运行报全所有问题。
package scaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// BoxfileName 是脚手架生成的构建描述文件名。
const BoxfileName = "Boxfile"

// ComposeFileName 是脚手架生成的编排文件名（首选 .yml 扩展名）。
const ComposeFileName = "licore-compose.yml"

// ComposeFileNameAlt 是编排文件的备用扩展名，LintProject 会识别但 Init 不生成。
const ComposeFileNameAlt = "licore-compose.yaml"

// BaseImage 是模板默认的基础镜像引用（自带 /bin/sh 的最小 rootfs）。
const BaseImage = "licore/base:latest"

// DefaultProjectName 是 ComposeTemplate 收到空项目名时使用的名字。
const DefaultProjectName = "myapp"

// DefaultServiceName 是 ComposeTemplate 收到空服务名时使用的名字。
const DefaultServiceName = "app"

// ignoreDirectives 是模板 dev.ignore 中使用的目录名。
// 这些目录在构建期由 .licoreignore 排除、在开发期由 dev watch 忽略，
// 两处必须保持一致，故用同一个变量渲染，避免模板漂移。
var ignoreDirectives = []string{".git", "bin", "node_modules", "tmp"}

// BoxfileTemplate 返回一份可直接构建的 Boxfile 模板：FROM/COPY/ENV/WORKDIR/
// ENTRYPOINT/CMD 各一行，每行都带 `#` 说明，COPY 之前还有一段被注释掉的
// .licoreignore 清单——构建期忽略规则写在 Boxfile 里（`licore` 不读 .dockerignore）。
//
// service 为空时使用 DefaultServiceName（仅用于注释中的提示文本）。
// 模板里的 COPY 源路径都是 `boxfile-src/...`，与项目根目录互不冲突。
func BoxfileTemplate(service string) string {
	if strings.TrimSpace(service) == "" {
		service = DefaultServiceName
	}
	var b strings.Builder

	b.WriteString("# Boxfile —— licore 的构建描述文件，语法与 Dockerfile 类似但语义自研。\n")
	b.WriteString("# 用法：在 licore-compose.yml 同一目录下执行 `licore build`（或 `licore run` 触发构建）。\n")
	fmt.Fprintf(&b, "# 本模板为服务 %q 生成；每个指令一行，'#' 开头为注释行。\n\n", service)

	b.WriteString("# FROM 指定基础镜像，必须是第一条指令，且只能出现一次。\n")
	fmt.Fprintf(&b, "FROM %s\n\n", BaseImage)

	b.WriteString("# ENV 声明构建期与运行期都生效的环境变量（KEY=VALUE，支持 ${VAR} 引用）。\n")
	b.WriteString("ENV APP_ENV=production\n")
	b.WriteString("ENV APP_HOME=/app\n\n")

	b.WriteString("# WORKDIR 设置后续 COPY/RUN 与容器 1 号进程的工作目录，必须是绝对路径。\n")
	b.WriteString("WORKDIR /app\n\n")

	b.WriteString("# .licoreignore —— 构建期忽略规则，直接写在 Boxfile 里（licore 不读取 Docker 的 .dockerignore）。\n")
	for _, d := range ignoreDirectives {
		fmt.Fprintf(&b, "# %s/\n", d)
	}
	b.WriteString("\n")

	b.WriteString("# COPY 把构建上下文中的文件拷进镜像；源路径相对构建上下文，不可越界。\n")
	b.WriteString("COPY boxfile-src/ /app/\n\n")

	b.WriteString("# ENTRYPOINT 是固定入口（exec 形式），CMD 是它的默认参数；运行时 CMD 可被覆盖。\n")
	b.WriteString("ENTRYPOINT [\"/bin/sh\"]\n")
	b.WriteString("CMD [\"-c\", \"echo \\\"hello from ${APP_HOME}\\\" && exec sleep infinity\"]\n")

	return b.String()
}

// ComposeTemplate 返回一份可直接 `licore up` 的 licore-compose.yml 模板。
//
// 结构包含顶层 version/name，以及单个服务 app（boxfile 构建来源、restart 策略、
// 端口、环境变量、卷）与 dev 块（watch/ignore/rebuild 热重载）。全文中文注释，
// 缩进固定两格；project/service 为空时回落到 DefaultProjectName/DefaultServiceName。
func ComposeTemplate(project string) string {
	if strings.TrimSpace(project) == "" {
		project = DefaultProjectName
	}
	var b strings.Builder

	b.WriteString("# licore-compose.yml —— licore 的本地编排文件（YAML）。\n")
	b.WriteString("# 用法：与本文件同目录执行 `licore up` 启动，`licore lint` 做静态检查。\n")
	b.WriteString("# 注意：licore 自研生态，不兼容 docker-compose 的扩展字段，未知键会被拒绝。\n\n")

	b.WriteString("# version 声明编排文件格式版本，当前只支持 1。\n")
	b.WriteString("version: 1\n\n")

	b.WriteString("# name 是项目名：容器名、网络名、卷名都会以它为前缀。\n")
	fmt.Fprintf(&b, "name: %s\n\n", project)

	b.WriteString("# services 下的每一项是一个服务（= 一组同配置容器）。\n")
	b.WriteString("services:\n")

	b.WriteString("  # app 是模板自带的服务名，可改成业务名（同步修改 dev 相关的引用即可）。\n")
	b.WriteString("  app:\n")

	b.WriteString("    # boxfile 指定服务用哪个 Boxfile 构建；与 image 二选一（用现成镜像就写 image）。\n")
	b.WriteString("    boxfile: Boxfile\n")

	b.WriteString("    # restart 是容器退出后的重启策略：no / always / unless-stopped / on-failure。\n")
	b.WriteString("    restart: unless-stopped\n\n")

	b.WriteString("    # command 覆盖镜像 CMD；entrypoint 覆盖镜像 ENTRYPOINT（都是数组形式）。\n")
	b.WriteString("    # command: [\"/bin/sh\", \"-c\", \"exec sleep infinity\"]\n\n")

	b.WriteString("    # environment 注入环境变量，按 KEY: VALUE 逐行书写。\n")
	b.WriteString("    environment:\n")
	b.WriteString("      APP_ENV: dev\n\n")

	b.WriteString("    # ports 端口映射，格式 \"宿主端口:容器端口\"（容器端口必填），可加 /tcp、/udp 后缀。\n")
	b.WriteString("    ports:\n")
	b.WriteString("      - \"8080:8080\"\n\n")

	b.WriteString("    # volumes 卷挂载，格式 \"宿主路径:容器路径[:ro]\"；容器路径必须是绝对路径。\n")
	b.WriteString("    volumes:\n")
	b.WriteString("      - \"./data:/app/data\"\n\n")

	b.WriteString("    # depends-on 声明启动顺序，值必须是本文件里存在的服务名。\n")
	b.WriteString("    # depends-on:\n")
	b.WriteString("    #   - db\n\n")

	b.WriteString("    # dev 块开启开发模式：文件变更后自动同步 / 重建并重启容器。\n")
	b.WriteString("    dev:\n")
	b.WriteString("      # watch 是要监听的目录（相对项目根目录）。\n")
	b.WriteString("      watch:\n")
	b.WriteString("        - ./boxfile-src\n")
	b.WriteString("      # ignore 是监听排除列表；建议与 Boxfile 里的 .licoreignore 保持一致。\n")
	b.WriteString("      ignore:\n")
	for _, d := range ignoreDirectives {
		fmt.Fprintf(&b, "        - ./%s\n", d)
	}
	b.WriteString("      # rebuild 表示变更后是否需要重新构建镜像：true 重建，false 只重启容器。\n")
	b.WriteString("      rebuild: true\n")

	return b.String()
}

// InitResult 汇总一次 `licore init` 的落盘结果。
type InitResult struct {
	// Dir 是脚手架实际写入的目录（绝对路径）。
	Dir string
	// Files 是本次实际写入的文件绝对路径，按写入顺序排列。
	Files []string
	// Skipped 是因已存在（且未指定 force）而保留原样的文件绝对路径。
	Skipped []string
}

// Init 在 dir 下生成 Boxfile 与 licore-compose.yml。dir 为空表示当前目录；
// 目录不存在时自动创建。任一目标文件已存在且 force 为 false 时立即返回
// 包装了 ErrFileExists 的错误，且不写入任何文件（要么全写、要么不动）。
//
// force 为 true 时覆盖已存在的文件（被覆盖的路径记入 Files，不记入 Skipped）。
func Init(dir string, force bool) (*InitResult, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建目录 %s: %w", dir, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析绝对路径 %s: %w", dir, err)
	}

	res := &InitResult{Dir: abs}
	// 先检查全部目标：存在冲突就一个文件都不写，避免留下半成品项目。
	if !force {
		for _, name := range []string{BoxfileName, ComposeFileName} {
			target := filepath.Join(abs, name)
			_, statErr := os.Stat(target)
			switch {
			case statErr == nil:
				res.Skipped = append(res.Skipped, target)
			case errors.Is(statErr, fs.ErrNotExist):
			default:
				return nil, fmt.Errorf("检查 %s: %w", target, statErr)
			}
		}
		if len(res.Skipped) > 0 {
			return nil, fmt.Errorf("文件已存在，使用 --force 覆盖: %s: %w",
				strings.Join(res.Skipped, ", "), ErrFileExists)
		}
	}

	project := filepath.Base(abs)
	contents := []struct {
		name string
		data string
	}{
		{BoxfileName, BoxfileTemplate(DefaultServiceName)},
		{ComposeFileName, ComposeTemplate(project)},
	}
	for _, f := range contents {
		target := filepath.Join(abs, f.name)
		if err := writeFile(target, f.data); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, target)
	}
	slog.Debug("scaffold 写入完成", "dir", abs, "files", len(res.Files), "force", force)
	return res, nil
}

// writeFile 以 0644 覆盖写入文本文件。
func writeFile(path, data string) error {
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		return fmt.Errorf("写入 %s: %w", path, err)
	}
	return nil
}
