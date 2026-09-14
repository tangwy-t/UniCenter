# ============================================================
# WebManagerFramework — 项目统一 Makefile
# 单入口同时管理 server(Go) 与 web(Vue3/pnpm) 两端 + Docker 编排。
#
# 用法:
#   make help           查看全部目标
#   make server         构建后端(server/) 执行 go build
#   make web            构建前端(web/)   执行 pnpm build
#   make all            构建两端
#   make test           全量测试(server go test + web vitest)
#   make docker-build   构建全部镜像(server + web)
# ============================================================

SHELL := /bin/bash
.DEFAULT_GOAL := help

# ── 目录 ────────────────────────────────────────────────────
SERVER_DIR     := server
WEB_DIR        := web

# ── Go 模块与版本注入 ───────────────────────────────────────
GO_MODULE      := github.com/tangwy-t/WebManagerFramework/server
# 用 ?= 允许 CI/命令行覆盖;但 ?= 对「已定义但为空」的环境变量不会赋值,
# 故追加 strip 空值兜底,保证空值也回退到 git/date 推算,避免注入 empty。
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
COMMIT_HASH   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

ifeq ($(strip $(VERSION)),)
  VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
endif
ifeq ($(strip $(BUILD_TIME)),)
  BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
endif
ifeq ($(strip $(COMMIT_HASH)),)
  COMMIT_HASH := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
endif

LDFLAGS = -X '$(GO_MODULE)/internal/pkg/version.Version=$(VERSION)' \
          -X '$(GO_MODULE)/internal/pkg/version.BuildTime=$(BUILD_TIME)' \
          -X '$(GO_MODULE)/internal/pkg/version.CommitHash=$(COMMIT_HASH)'

# ── 前端包管理器 ─────────────────────────────────────────────
PM             ?= pnpm

# ── swag 版本(与 server/go.mod 的 swaggo/swag 保持一致) ──────
SWAG_VERSION   ?= v1.16.6

.PHONY: help all \
        server-run server-build server-test server-lint server-vet server-fmt \
        server-swagger server-clean \
        web-install web-dev web-build web-serve web-test web-lint web-fix web-fmt \
        test lint fmt build run clean \
        docker-build docker-up docker-down docker-logs docker-ps \
        docker-build-tracing docker-up-tracing docker-down-tracing

## ───────────────────────────────────────────────────────────
## 帮助
## ───────────────────────────────────────────────────────────
help: ## 显示本帮助
	@printf "WebManagerFramework 统一构建入口\n\n"
	@printf "后端(server) : make server-run | server-build | server-test | server-lint | server-vet | server-fmt | server-swagger | server-clean\n"
	@printf "前端(web)    : make web-install | web-dev | web-build | web-serve | web-test | web-lint | web-fix | web-fmt\n"
	@printf "全量         : make all | test | lint | fmt | build | run | clean\n"
	@printf "Docker       : make docker-build | docker-up | docker-down | docker-logs | docker-ps\n"
	@printf "\n详细目标:\n"
	@grep -E '^[a-zA-Z_%-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

## ───────────────────────────────────────────────────────────
## 后端 server
## ───────────────────────────────────────────────────────────
server-run: ## 本地运行后端(go run)
	cd $(SERVER_DIR) && go run ./cmd/server

server-build: ## 构建后端二进制到 server/bin/server
	cd $(SERVER_DIR) && go build -ldflags "${LDFLAGS}" -o bin/server ./cmd/server

server-test: ## 后端单元测试(go test)
	cd $(SERVER_DIR) && go test ./... -v -count=1

# 静态检查:优先 golangci-lint,未安装时降级 go vet。
# 注意 if/else 显式分支:不能写成 A && B || C —— golangci-lint 检出问题(非零退出)
# 也会触发 go vet,vet 通过会掩盖 lint 失败退出码。
server-lint: ## 后端静态检查(golangci-lint,缺省降级 go vet)
	@cd $(SERVER_DIR) && \
	if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint 未安装,降级 go vet"; \
		go vet ./...; \
	fi

server-vet: ## 后端 go vet
	cd $(SERVER_DIR) && go vet ./...

server-fmt: ## 后端 gofmt 格式化(检查模式,列差异)
	cd $(SERVER_DIR) && gofmt -l -w internal/ cmd/

# swag 生成 docs;版本与 go.mod 的 swaggo/swag 保持一致,go run 钉版保证可复现
server-swagger: ## 后端重新生成 swagger 文档(docs/)
	cd $(SERVER_DIR) && go run github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION) init -g cmd/server/main.go -o docs

server-clean: ## 清理后端产物(bin/ 与 docs/)
	rm -rf $(SERVER_DIR)/bin/ $(SERVER_DIR)/docs/

## ───────────────────────────────────────────────────────────
## 前端 web
## ───────────────────────────────────────────────────────────
web-install: ## 安装前端依赖(pnpm install)
	cd $(WEB_DIR) && $(PM) install

web-dev: ## 启动前端开发服务器(vite dev)
	cd $(WEB_DIR) && $(PM) dev

web-build: ## 构建前端(类型检查 + vite build)
	cd $(WEB_DIR) && $(PM) build

web-serve: ## 本地预览前端构建产物(vite preview)
	cd $(WEB_DIR) && $(PM) serve

web-test: ## 前端单元测试(vitest run)
	cd $(WEB_DIR) && $(PM) test

web-lint: ## 前端 ESLint 检查
	cd $(WEB_DIR) && $(PM) lint

web-fix: ## 前端 ESLint 自动修复
	cd $(WEB_DIR) && $(PM) fix

web-fmt: ## 前端 Prettier 格式化
	cd $(WEB_DIR) && $(PM) lint:prettier

## ───────────────────────────────────────────────────────────
## 全量(两端)
## ───────────────────────────────────────────────────────────
all: server-build web-build ## 构建后端 + 前端

build: server-build web-build ## 同 all

run: server-run ## 运行后端(带前端时请配合 web-dev)

test: server-test web-test ## 全量测试(server + web)

lint: server-lint web-lint ## 全量静态检查(server + web)

fmt: server-fmt web-fmt ## 全量格式化(server + web)

clean: server-clean ## 清理(后端产物;前端 dist 请在 web/ 内单独处理或全局 git clean)

## ───────────────────────────────────────────────────────────
## Docker 编排(根目录 docker-compose.yml)
## ───────────────────────────────────────────────────────────
# docker-build: 计算版本三件套并注入 server 镜像(监控页「构建时间/提交 Hash」
# 数据源)。直接 docker compose build 不传参时,Dockerfile 兜底为
# BUILD_TIME=构建时刻、VERSION=docker、COMMIT_HASH=unknown。
# 本部署环境 BuildKit 不可用时,DOCKER_BUILDKIT=0 构建:
#   DOCKER_BUILDKIT=0 make docker-build
docker-build: ## 构建全部镜像(server + web),注入版本信息
	docker compose build \
		--build-arg VERSION="$(VERSION)" \
		--build-arg BUILD_TIME="$(BUILD_TIME)" \
		--build-arg COMMIT_HASH="$(COMMIT_HASH)" \
		server web

docker-up: docker-build ## 构建并启动全部服务(docker compose up -d)
	docker compose up -d

docker-down: ## 停止并移除全部服务
	docker compose down

docker-logs: ## 跟踪查看全部服务日志( Ctrl+C 退出)
	docker compose logs -f

docker-ps: ## 查看服务状态
	docker compose ps

# ── 可选追踪(需 --profile tracing,默认 up 不启动) ──
# 前置:在 .env 开启 OBSERVABILITY_TRACING_ENABLED=true 并设
#   OBSERVABILITY_TRACING_ENDPOINT=jaeger:4317
# 再执行本目标,一次命令完成「构建 + 起业务 + 起追踪侧(Jaeger v2)」,
# 保证后端按最新 .env 追踪变量重建并上报。
# 注意:docker compose up --build 不支持 --build-arg,版本三件套只能经
# docker compose build 注入;故 up 前必须先 build(参照 docker-build)。
docker-build-tracing: ## 构建全部镜像(含追踪 profile),注入版本信息
	docker compose --profile tracing build \
		--build-arg VERSION="$(VERSION)" \
		--build-arg BUILD_TIME="$(BUILD_TIME)" \
		--build-arg COMMIT_HASH="$(COMMIT_HASH)" \
		server web

docker-up-tracing: docker-build-tracing ## 构建(注入版本)+启动全部服务 + 追踪(Jaeger v2)
	docker compose --profile tracing up -d

docker-down-tracing: ## 停止并移除全部服务(含追踪)
	docker compose --profile tracing down