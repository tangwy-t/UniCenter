# ============================================================
# uni_core — 多阶段构建
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

# 先拷贝依赖清单**与离线模块载荷**，利用构建缓存。
#
# 为什么需要 .goproxy：uni_core 现在处于「发布态」——go.mod 自带
# `require github.com/tangwy-t/UniCenter/uni_protocol`（不再依赖仓库根 go.work，
# 容器里也没有 go.work）。该模块尚未推送到远端，无法从任何公共 proxy 取，
# 故仓库内烘焙了一份**仅含该模块**的离线载荷（4 文件/106K），此处作为
# GOPROXY 的第一顺位；若把它换成真实 proxy，构建阶段无需其它改动。
#
# **改了 uni_protocol 就必须重出载荷**（否则镜像构建会以「undefined: 新符号」
# 失败 —— 那不是代码问题，是载荷还是旧版本）。做法（v1.1.0 即如此产出，
# 历史版本保持不可变、只追加）：把 uni_protocol 打成一个
# `<module>@<新版本>/` 前缀的 zip 放进 @v/，同时补 `<版本>.mod`、`<版本>.info`、
# 在 list 里追加版本，然后 bump uni_core/go.mod 的 require 并用
# `GOWORK=off GOPROXY=file://$PWD/uni_core/.goproxy,off go mod download` 补 go.sum；
# 最后 `make release-precheck` 断言发布态可构建（它就是这条流程的门禁）。
COPY go.mod go.sum ./
COPY .goproxy ./.goproxy
RUN GOPROXY="file:///src/.goproxy,${GOPROXY}" GOSUMDB=off GOFLAGS=-mod=mod go mod download

# 再拷贝源码（含 docs 包，main.go 编译依赖）
COPY cmd ./cmd
COPY internal ./internal
COPY docs ./docs

# ldflags 注入版本信息（与 Makefile 一致）。
# RUN 指令不做 Dockerfile 解析期变量替换,${VAR:-default} 全部由 shell 求值;
# ARG 值以环境变量形式进入 RUN:非空 → 用传入值,空/未传 → 走兜底。
# 注意 GOPROXY 同样要带上本地载荷（上一步的 ENV 不会带到此处）。
RUN VERSION="${VERSION:-docker}" && \
    BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" && \
    COMMIT_HASH="${COMMIT_HASH:-unknown}" && \
    GOPROXY="file:///src/.goproxy,${GOPROXY}" GOSUMDB=off GOFLAGS=-mod=mod go build \
      -ldflags "-s -w \
        -X 'github.com/tangwy-t/UniCenter/uni_core/internal/pkg/version.Version=${VERSION}' \
        -X 'github.com/tangwy-t/UniCenter/uni_core/internal/pkg/version.BuildTime=${BUILD_TIME}' \
        -X 'github.com/tangwy-t/UniCenter/uni_core/internal/pkg/version.CommitHash=${COMMIT_HASH}'" \
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