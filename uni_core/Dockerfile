# ============================================================
# webmanager-server — 多阶段构建
# 构建阶段：golang:1.26-alpine（CGO 禁用，静态链接）
# 运行阶段：alpine 最小镜像
# 配置由 docker-compose 挂载到 /app/configs/config.yaml，镜像内不含配置文件。
# ============================================================

# ---- 构建阶段 ----
FROM golang:1.26-alpine AS builder

# Go 模块代理；默认 goproxy.cn（本环境 proxy.golang.org 不可达），构建时可覆盖：
#   docker compose build --build-arg GOPROXY=https://proxy.golang.org,direct
ARG GOPROXY=https://goproxy.cn,direct
# 版本信息三件套：构建方（Makefile docker-build / CI）应显式传入；
# 未传时 RUN 内 shell 兜底 —— BUILD_TIME 落到构建时刻（自动、无需人工），
# VERSION/COMMIT_HASH 回退 docker/unknown（.dockerignore 排除了 .git，
# 镜像内无法自查 commit hash，只能由构建方注入）。
ARG VERSION=
ARG BUILD_TIME=
ARG COMMIT_HASH=

WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

# 先拷贝依赖清单，利用构建缓存
COPY go.mod go.sum ./
RUN go mod download

# 再拷贝源码（含 docs 包，main.go 编译依赖）
COPY cmd ./cmd
COPY internal ./internal
COPY docs ./docs

# ldflags 注入版本信息（与 Makefile 一致）。
# RUN 指令不做 Dockerfile 解析期变量替换,${VAR:-default} 全部由 shell 求值;
# ARG 值以环境变量形式进入 RUN:非空 → 用传入值,空/未传 → 走兜底。
RUN VERSION="${VERSION:-docker}" && \
    BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" && \
    COMMIT_HASH="${COMMIT_HASH:-unknown}" && \
    go build \
      -ldflags "-s -w \
        -X 'github.com/tangwy-t/webmanager-server/internal/pkg/version.Version=${VERSION}' \
        -X 'github.com/tangwy-t/webmanager-server/internal/pkg/version.BuildTime=${BUILD_TIME}' \
        -X 'github.com/tangwy-t/webmanager-server/internal/pkg/version.CommitHash=${COMMIT_HASH}'" \
      -o /out/server ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app

WORKDIR /app

# 上传、日志目录交给 app 用户（日志已配置为 stdout，这里仅为文件后端兜底）
RUN mkdir -p /app/uploads /app/logs /app/configs \
    && chown -R app:app /app

COPY --from=builder /out/server /app/server

USER app

EXPOSE 8088

ENTRYPOINT ["/app/server"]