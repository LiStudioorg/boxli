# Copyright (C) 2026 LiStudioorg
# SPDX-License-Identifier: AGPL-3.0-only
#
# LiCore 构建入口。设计要点：
#   - 所有目标都显式设置 CGO_ENABLED，产出可预期的静态二进制；
#   - Android 的 cgo 构建（`licore exec` 需要）依赖 NDK：有就编 cgo，
#     没有就自动降级为纯 Go 并**明确警告**（exec 不可用，其余功能正常）；
#   - 不隐式下载工具链：缺什么就报错或降级，绝不静默产出坏二进制。

BIN      := licore
DIST     := dist
GO       ?= go
# 版本号：make VERSION=0.7.0 all，或直接 -ldflags 覆盖。
VERSION  ?= 0.0.0-dev
LDFLAGS  := -s -w -X main.version=$(VERSION)
# 纯 Go 目标统一带 nocgo_exec：execns 走 stub，保证 CGO_ENABLED=0 也能编。
NOTAGS   := -tags nocgo_exec

# Android NDK：ANDROID_NDK_HOME 优先，兼容旧名 ANDROID_NDK_ROOT。
NDK      ?= $(if $(ANDROID_NDK_HOME),$(ANDROID_NDK_HOME),$(ANDROID_NDK_ROOT))
# 交叉编译工具链前缀（arm64）。NDK r23+ 使用 llvm 预编译目录。
NDK_HOST := $(shell uname -s | tr 'A-Z' 'a-z')-x86_64
NDK_BIN  := $(NDK)/toolchains/llvm/prebuilt/$(NDK_HOST)/bin
NDK_CC   := $(NDK_BIN)/aarch64-linux-android21-clang

.PHONY: all linux android android-nocgo test vet fmt clean install help

# 默认目标：服务器三件套（amd64 + arm64 + android arm64）。
all: linux android
	@echo "构建完成，产物在 $(DIST)/"

## linux: 桌面 Linux（amd64 + arm64），纯 Go 静态二进制
linux:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(NOTAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(NOTAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-linux-arm64 .
	@echo "  → $(DIST)/$(BIN)-linux-amd64, $(DIST)/$(BIN)-linux-arm64"

## android: Android ARM64，带 cgo（exec 可用）；无 NDK 时降级为纯 Go
android:
	@mkdir -p $(DIST)
	@if [ -n "$(NDK)" ] && [ -x "$(NDK_CC)" ]; then \
	  echo "检测到 NDK：$(NDK)"; \
	  echo "  → 使用 cgo 构建，licore exec 可用"; \
	  CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$(NDK_CC)" \
	    $(GO) build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-android-arm64 . || exit 1; \
	else \
	  echo "警告：未检测到可用的 Android NDK（ANDROID_NDK_HOME / ANDROID_NDK_ROOT）。" >&2; \
	  if [ -n "$(NDK)" ]; then \
	    echo "警告：已设置 NDK 路径，但找不到编译器：$(NDK_CC)" >&2; \
	  fi; \
	  echo "警告：降级为纯 Go 构建 —— licore exec 将不可用（返回 ErrNoCgoExec），其余功能正常。" >&2; \
	  echo "警告：需要 exec 请安装 NDK 并设置 ANDROID_NDK_HOME，然后重跑 make android。" >&2; \
	  CGO_ENABLED=0 GOOS=android GOARCH=arm64 $(GO) build $(NOTAGS) \
	    -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-android-arm64 . || exit 1; \
	fi
	@echo "  → $(DIST)/$(BIN)-android-arm64"

## android-nocgo: Android ARM64 纯 Go 构建（exec 不可用，其余功能正常）
android-nocgo:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=android GOARCH=arm64 $(GO) build $(NOTAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-android-arm64 .
	@echo "  → $(DIST)/$(BIN)-android-arm64（纯 Go：licore exec 不可用）"

## test: 运行全部测试
test:
	CGO_ENABLED=0 $(GO) test ./... -count=1

## vet: 静态检查
vet:
	CGO_ENABLED=0 $(GO) vet ./...

## fmt: 格式检查（有未格式化文件则失败，不自动改写）
fmt:
	@out="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$out" ]; then echo "以下文件未格式化:"; echo "$$out"; exit 1; fi; \
	echo "gofmt 干净"

## clean: 清理构建产物
clean:
	rm -rf $(DIST)
	@echo "已清理 $(DIST)/"

## install: 装到 /usr/local/bin（默认装当前平台的 amd64 服务器版）
install: 
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(NOTAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$(BIN)-linux-amd64 .
	install -m 0755 $(DIST)/$(BIN)-linux-amd64 /usr/local/bin/$(BIN)
	@echo "已安装到 /usr/local/bin/$(BIN)"

## help: 显示本帮助
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
