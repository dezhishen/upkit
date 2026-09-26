# upkit 构建入口
#
# 所有命令都基于 POSIX shell 编写，可在 Linux / macOS / Git Bash / WSL 下运行，
# 不依赖 PowerShell。需要完整发行包（zip + 校验值）时请直接使用：
#
#     bash scripts/build.sh

SHELL   := /bin/sh
GO      ?= go
PKG     := ./cmd/upkit
BINARY  := upkit.exe
DIST    := dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# 内置订阅地址：留空则用代码里的默认值（官方源 upkit-hub 仓库）。
# 用法：make build-all FEED_URL=https://mirror.internal/upkit/feed.yaml
FEED_URL ?=
FEED_URL_ARGS = $(if $(FEED_URL),--feed-url $(FEED_URL))

.PHONY: all build build-windows build-dev build-all test test-short bench vet fmt fmt-check headers-check tidy clean run run-dev check release next-version help

all: check build

build: ## 构建 windows/amd64 的 dist/upkit.exe
	@mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) $(PKG)
	@echo "已生成 $(DIST)/$(BINARY)（$(VERSION)）"

build-windows: ## 构建 windows/amd64 与 windows/arm64（发行目标）
	@mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/upkit-windows-amd64.exe $(PKG)
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/upkit-windows-arm64.exe $(PKG)
build-dev: ## 构建当前平台产物（仅开发机冒烟用，不是发行目标）
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/upkit-dev $(PKG)

build-all: ## 交叉编译全部发行目标（FEED_URL=... 可覆盖内置订阅地址）
	VERSION="$(VERSION)" bash scripts/build.sh --no-zip $(FEED_URL_ARGS)

test: ## 运行全部单元测试
	$(GO) test ./...

test-short: ## 运行单元测试（跳过耗时用例）
	$(GO) test -short ./...

bench: ## 运行基准测试
	$(GO) test -bench . -benchmem ./...

vet: ## go vet 静态检查
	$(GO) vet ./...

fmt: ## 格式化代码
	$(GO) fmt ./...

fmt-check: ## 检查代码是否已格式化
	@if ! unformatted=$$(gofmt -l .); then \
		echo "gofmt 解析失败（通常是语法错误，比如重复的 package 行）"; \
		exit 1; \
	fi; \
	if [ -n "$$unformatted" ]; then \
		echo "以下文件未格式化，请运行 make fmt："; echo "$$unformatted"; exit 1; \
	fi

headers-check: ## 检查文件头（package 唯一、//go:build 在第 1 行）
	@bash scripts/check-headers.sh

tidy: ## 整理 go.mod / go.sum
	$(GO) mod tidy

clean: ## 清理构建产物
	rm -rf $(DIST) release coverage.out

check: fmt-check headers-check vet test ## 本地质量门禁

run: ## 启动 TUI（开发调试用）
	$(GO) run $(PKG)

run-dev: ## 构建并启动当前平台的 TUI（开发机上验证界面用）
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/upkit-dev $(PKG)
	$(DIST)/upkit-dev

release: ## 生成完整发行包（zip + sha256sums.txt；FEED_URL=... 可覆盖订阅地址）
	bash scripts/build.sh -v "$(VERSION)" $(FEED_URL_ARGS)

BUMP ?= patch
STAGE ?= stable
next-version: ## 计算下一个版本号（BUMP=patch|minor|major|hotfix STAGE=stable|rc|beta）
	@bash scripts/next-version.sh "$(BUMP)" "$(STAGE)"

help: ## 显示本帮助
	@echo "可用目标："
	@echo "  build          构建 $(DIST)/$(BINARY)"
	@echo "  build-windows  构建 windows/amd64 与 windows/arm64（发行目标）"
	@echo "  build-dev      构建当前平台产物（仅开发机冒烟）"
	@echo "  build-all      交叉编译全部发行目标"
	@echo "  run-dev        构建并启动当前平台的 TUI（开发机验证界面用）"
	@echo "  test           运行单元测试"
	@echo "  vet            go vet 静态检查"
	@echo "  fmt            格式化代码"
	@echo "  next-version   计算下一个版本号（BUMP/STAGE 可覆盖）"
	@echo "  fmt-check      检查格式化"
	@echo "  headers-check  检查文件头（package 唯一、//go:build 在第 1 行）"
	@echo "  tidy           整理依赖"
	@echo "  clean          清理产物"
	@echo "  check          fmt-check + vet + test"
	@echo "  release        生成完整发行包（走 scripts/build.sh）"