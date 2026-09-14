# ============================================================
# uni-console — 多阶段构建
# 构建阶段：node:22-alpine + pnpm（vue-tsc 类型检查 + vite 构建）
# 运行阶段：nginx 托管 dist 静态资源，/api 反向代理到 compose 服务 uni_core:8088
# （nginx.conf 见 ./nginx.conf，为生产镜像专用配置）
# ============================================================

# ---- 构建阶段 ----
FROM node:22-alpine AS builder

# npm/pnpm 镜像源；默认 npmmirror（本环境 proxy.golang.org 不可达，网络环境类似），
# 如需官方源：docker compose build --build-arg NPM_REGISTRY=https://registry.npmjs.org
ARG NPM_REGISTRY=https://registry.npmmirror.com

RUN npm config set registry "$NPM_REGISTRY" \
    && corepack enable

WORKDIR /app

# 先拷贝依赖清单，利用构建缓存
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

# 再拷贝源码；.dockerignore 已排除 node_modules/dist 等
COPY . .

# 生产构建环境变量（可在 docker compose 的 build.args 或根 .env 覆盖）。
# 注意：环境变量优先级高于 uni_console/.env.production，故以下 ENV 会覆盖仓库内
# .env.production 的同名值，实现「根 .env / compose 一层管全部 VITE_*」。
#
# VITE_API_URL：不能用 /api/v1 —— API 模块 url 自带 VITE_API_PREFIX，
#   baseURL 再带前缀会拼成 /api/v1/api/v1/...；nginx 把 /api/ 原样转发给
#   uni_core，路径以 /api/v1 开头即可命中 Go 路由，故保持 /。
ARG VITE_API_URL=/
ENV VITE_API_URL=$VITE_API_URL

# 部署基础路径（如子目录部署 /admin/）
ARG VITE_BASE_URL=/
ENV VITE_BASE_URL=$VITE_BASE_URL

# 应用版本（注入 __APP_VERSION__）
ARG VITE_VERSION=0.1.0
ENV VITE_VERSION=$VITE_VERSION

# API 前缀（后端统一 /api/v1）
ARG VITE_API_PREFIX=/api/v1
ENV VITE_API_PREFIX=$VITE_API_PREFIX

# 权限模式 frontend | backend
ARG VITE_ACCESS_MODE=backend
ENV VITE_ACCESS_MODE=$VITE_ACCESS_MODE

# 跨域请求是否携带 Cookie
ARG VITE_WITH_CREDENTIALS=false
ENV VITE_WITH_CREDENTIALS=$VITE_WITH_CREDENTIALS

# 是否打开路由信息
ARG VITE_OPEN_ROUTE_INFO=false
ENV VITE_OPEN_ROUTE_INFO=$VITE_OPEN_ROUTE_INFO

# 生产构建删除 console（默认 true，与 .env.production 一致）
ARG VITE_DROP_CONSOLE=true
ENV VITE_DROP_CONSOLE=$VITE_DROP_CONSOLE

RUN pnpm build

# ---- 运行阶段 ----
FROM nginx:1.27-alpine

# 生产镜像专用 nginx 配置（托管 dist + /api 反代 uni_core:8088）
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /usr/share/nginx/html

EXPOSE 80