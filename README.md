# WebManagerFramework

> 企业级 RBAC 权限管理系统 · Go + Vue 3 全栈方案

An enterprise-grade RBAC (Role-Based Access Control) administration framework, built with Go (Gin) on the backend and Vue 3 + TypeScript on the frontend. It ships with a complete RBAC system, row-level data permission auto-injection, and a plugin-based frontend architecture — a solid foundation for building enterprise management consoles.

```
简体中文 / English  ·  分段双语对照  ·  Section-by-section bilingual
```

---

## 目录 / Table of Contents

- [项目简介 / Overview](#项目简介--overview)
- [特性 / Features](#特性--features)
- [技术栈 / Tech Stack](#技术栈--tech-stack)
- [目录结构 / Project Structure](#目录结构--project-structure)
- [快速开始 / Quick Start](#快速开始--quick-start)
- [配置说明 / Configuration](#配置说明--configuration)
- [权限体系 / Permission Model](#权限体系--permission-model)
- [内置业务模块 / Built-in Modules](#内置业务模块--built-in-modules)
- [响应契约 / Response Contract](#响应契约--response-contract)
- [API 文档 / API Documentation](#api-文档--api-documentation)
- [开发规范 / Development Conventions](#开发规范--development-conventions)
- [License 与致谢 / License & Acknowledgements](#license-与致谢--license--acknowledgements)

---

## 项目简介 / Overview

**WebManagerFramework** 是一套面向企业管理后台的全栈 Web 管理框架，后端采用 Go (Gin) 分层架构，前端采用 Vue 3 + TypeScript 的插件化架构。核心能力是完整的 RBAC 权限体系与数据权限（DataScope）自动注入，开箱即可作为各类管理系统的基座。

**WebManagerFramework** is a full-stack administration framework targeting enterprise management consoles. The backend follows a layered Go (Gin) architecture; the frontend follows a plugin-based Vue 3 + TypeScript architecture. Its core strengths are a complete RBAC system and row-level DataScope auto-injection, ready to serve as the foundation for a wide range of management systems.

---

## 特性 / Features

### 后端 / Backend (`uni_core/`)

- **RBAC 权限体系 / RBAC model**：用户 / 角色 / 菜单 / 部门 / 数据权限（DataScope）五位一体，支持菜单、按钮、接口三级鉴权。
  Users, roles, menus, departments and DataScope in one unified model, with menu / button / API three-level authorization.
- **数据权限自动注入 / Row-level DataScope injection**：基于 GORM 插件的 DataScope，随实体定义注册，按角色配置自动过滤数据行（全部 / 本部门 / 本部门及以下 / 仅本人）。
  A GORM-plugin-based DataScope registered per entity, automatically filtering rows by role configuration (All / Department / Department & Children / Self).
- **多端安全认证 / Robust authentication**：JWT + Redis 会话、bcrypt 密码哈希、登录限流、验证码、账号锁定。
  JWT + Redis sessions, bcrypt password hashing, login rate-limiting, captcha and account locking.
- **可观测性 / Observability**：OpenTelemetry 分布式追踪（Jaeger v2 all-in-one）、Zap 结构化日志、pprof、慢查询统计、服务端 / SQL 监控指标。
  OpenTelemetry distributed tracing (Jaeger v2 all-in-one), Zap structured logging, pprof, slow-query stats, and server / SQL metrics.
- **基础设施完备 / Complete infrastructure**：Snowflake 雪花 ID、Cron 定时任务、WebSocket 实时推送、结构化版本迁移（v001 ~ v007）。
  Snowflake IDs, cron scheduling, WebSocket push, and structured versioned migrations (v001 ~ v007).
- **优雅停机 / Graceful shutdown**：分阶段 drain → cleanup 生命周期管理，编排 HTTP / DB / Redis / 调度器 / 连接的有序关闭。
  Staged drain → cleanup lifecycle with ordered shutdown of HTTP / DB / Redis / scheduler / connections.

### 前端 / Frontend (`uni_console/`)

- **插件化架构 / Plugin architecture**：`src/modules/<name>/` 一个目录即一个业务插件，增删文件夹即启用 / 停用，开箱即用。
  Each directory under `src/modules/<name>/` is a business plugin — add or remove a folder to enable or disable a module.
- **现代化技术栈 / Modern stack**：Vue 3.5 + TypeScript + Vite 7 + Pinia + Element Plus + Tailwind CSS 4。
- **丰富的企业级能力 / Enterprise capabilities**：动态路由与权限守卫、主题 / 布局 / 菜单样式切换、国际化、多 Tab 工作台、锁屏、水印。
  Dynamic routing & guards, theme / layout / menu style switching, i18n, multi-tab workspace, lock screen and watermark.
- **内置业务模块 / Built-in modules**：仪表盘、用户 / 角色 / 菜单 / 部门 / 字典 / 配置 / 文件 / 定时任务 / 日志 / 通知 / 系统监控。
  Dashboard, User / Role / Menu / Department / Dictionary / Config / File / Job / Log / Notice / System Monitor.

### 工程化 / Engineering

- **一键部署 / One-click deployment**：Docker Compose 编排前后端，健康检查、依赖启动顺序、数据卷持久化。
  Docker Compose orchestrates both ends with health checks, startup ordering and volume persistence.
- **数据库版本迁移 / Versioned migrations**：启动时自动执行版本化迁移，随二进制交付。
  Versioned migrations run automatically at startup and ship with the binary.
- **Swagger 文档 / Swagger docs**：API 自动生成文档，`/api/v1` 前缀统一。
  Auto-generated API docs under a unified `/api/v1` prefix.

---

## 技术栈 / Tech Stack

| 层 / Layer | 技术 / Technology |
|-----------|-------------------|
| 后端语言 / Language | Go 1.26 |
| Web 框架 / Web framework | Gin 1.12 |
| ORM | GORM（MySQL / SQLite） |
| 缓存 / 会话 / Cache & session | Redis（go-redis v9） |
| 认证 / Auth | JWT（golang-jwt/v5）+ bcrypt |
| 配置 / Config | Viper（环境变量覆盖 / env override） |
| 日志 / Logging | Zap + lumberjack（日志轮转 / rotation） |
| 可观测 / Observability | OpenTelemetry + pprof + gopsutil |
| 前端框架 / Frontend | Vue 3.5 + TypeScript |
| 构建工具 / Builder | Vite 7 |
| UI 组件库 / UI library | Element Plus 2.11 + Tailwind CSS 4 |
| 状态管理 / State | Pinia + pinia-plugin-persistedstate |
| 图表 / Charts | ECharts 6 |

---

## 目录结构 / Project Structure

```
WebManagerFramework/
├── uni_core/                    # Go 后端 / Backend
│   ├── cmd/server/            # 入口（main.go，生命周期编排 / lifecycle orchestration）
│   ├── configs/               # 配置文件（本地 / docker 环境 / local & docker env）
│   ├── docs/                  # Swagger 生成物（docs.go / swagger.json / yaml）
│   └── internal/
│       ├── handler/           # HTTP 处理层 / Handler layer
│       ├── middleware/        # 中间件（鉴权 / 限流 / 日志 / 数据范围）
│       ├── service/           # 业务逻辑层 / Service layer
│       ├── repository/        # 数据访问层（GORM）/ Repository layer
│       ├── model/             # entity / dto
│       ├── router/            # 路由注册 / Route registration
│       ├── scheduler/         # 定时任务调度 / Job scheduler
│       ├── task/              # 任务注册表 / Task registry
│       └── pkg/               # 基础设施（jwt / redis / datascope / migration …）
├── uni_console/                       # Vue 3 前端 / Frontend
│   ├── src/
│   │   ├── framework/         # 插件运行时 · 路由注册 · 生命周期 / Plugin runtime
│   │   ├── components/        # 组件库（art-*）/ Component library
│   │   ├── modules/           # 业务插件（一目录 = 一模块 / one dir = one module）
│   │   ├── router/            # 动态路由 · 权限守卫 / Dynamic routes & guards
│   │   └── utils/             # http / storage / socket 等
│   └── public/
├── data/uploads/              # 上传文件持久化目录（运行时 / runtime）
├── docker-compose.yml         # 前后端容器编排 / Full-stack orchestration
└── .env.example               # 环境变量模板 / Env template
```

---

## 快速开始 / Quick Start

### 环境要求 / Prerequisites

- **Go** ≥ 1.26
- **Node.js** ≥ 20.19 + **pnpm** ≥ 8.8
- **MySQL** 5.7+ / 8.x、**Redis** 5+（需现存实例 / pre-existing instances）

### 本地开发 / Local Development

```bash
# ── 后端 / Backend ──
cd uni_core
cp configs/config.yaml configs/config.local.yaml   # （可选）覆盖本地配置 / (optional) local override
go mod download
make run          # 启动于 http://localhost:9999

# 常用命令 / Common tasks
make build        # 编译（注入版本 / 构建时间 / 提交 Hash）/ build (injects version/time/commit)
make test         # 运行测试 / run tests
make lint         # 静态检查（golangci-lint，缺失时降级 go vet）/ lint
make swagger      # 重新生成 API 文档（swag v1.16.6）/ regenerate API docs

# ── 前端 / Frontend ──
cd uni_console
pnpm install
pnpm dev          # http://localhost:3006
pnpm build        # 类型检查 + 产物构建 / type-check + build
pnpm test         # Vitest 单元测试 / unit tests
```

> 前端开发环境通过 Vite 代理把 `/api` 转发到 `http://127.0.0.1:9999`（见 `uni_console/.env.example` 的 `VITE_API_PROXY_URL`）。
> The dev frontend proxies `/api` to `http://127.0.0.1:9999` via Vite (see `VITE_API_PROXY_URL` in `uni_console/.env.example`).

### Docker Compose 一键部署 / One-Click Deployment

> MySQL / Redis 复用宿主机现存实例（经 `host.docker.internal` 访问），compose 不启动数据库服务。
> MySQL / Redis are reused from the host (via `host.docker.internal`); compose does not start database services.

```bash
# 1. 准备环境变量（可选，覆盖默认值）/ Prepare env (optional)
cp .env.example .env

# 2. 构建并启动 / Build & start
docker compose up -d --build

# 3. 查看状态与日志 / Status & logs
docker compose ps
docker compose logs -f uni_core
docker compose logs -f uni_console

# 4. 停止 / Stop
docker compose down
```

| 端口 / Port | 服务 / Service | 说明 / Description |
|------|------|------|
| `8088` | 后端 API / Backend API | 可由 `SERVER_PORT` 覆盖 / override via `SERVER_PORT` |
| `80` | 前端 / Frontend | nginx 托管 dist + `/api` 反代到后端，可由 `WEB_PORT` 覆盖 |

启动后访问 / After startup:

- 前端管理后台 / Admin console：http://localhost
- 后端健康检查 / Health check：http://localhost:8088/api/v1/health

---

## 配置说明 / Configuration

配置采用 **配置文件 + 环境变量覆盖** 双轨制（Viper，键名点号 → 下划线映射，环境变量优先级更高）。
Configuration follows a **file + environment override** dual approach (Viper; dot-key → underscore mapping, env has higher priority).

关键配置项 / Key options（见 `.env.example`）:

| 环境变量 / Env | 默认值 / Default | 说明 / Description |
|----------------|------------------|--------------------|
| `DATABASE_HOST` | `host.docker.internal` | MySQL 地址 / host |
| `DATABASE_PORT` | `3306` | MySQL 端口 / port |
| `DATABASE_USER` | `root` | MySQL 用户 / user |
| `DATABASE_PASSWORD` | `password` | MySQL 密码 / password |
| `DATABASE_DBNAME` | `web_manager_framework` | 数据库名 / database name |
| `REDIS_ADDR` | `host.docker.internal:6379` | Redis 地址 / address |
| `SERVER_PORT` | `8088` | 后端映射端口 / backend mapped port |
| `WEB_PORT` | `80` | 前端映射端口 / frontend mapped port |

> ⚠️ 生产环境请通过环境变量注入真实密钥，切勿在配置文件或仓库中硬编码敏感信息。
> ⚠️ In production, inject real secrets via environment variables; never hard-code sensitive information in config files or the repository.

---

## 权限体系 / Permission Model

系统采用经典的 RBAC 模型，并扩展了数据权限（DataScope）：
The system uses the classic RBAC model, extended with DataScope:

```
用户 (User)
  └─ 多角色 (Role)
        ├─ 菜单权限 (Menu / Permission)     —— 决定"能看什么页面、按钮"
        │                                   determines which pages/buttons are visible
        └─ 数据权限 (DataScope)             —— 决定"能看哪些数据行"
                                            determines which data rows are visible
              ├─ 全部数据 / All data
              ├─ 本部门数据 / Own department
              ├─ 本部门及以下数据 / Department & children
              └─ 仅本人数据 / Self only
```

前端动态路由由后端下发的菜单 + 权限标识生成，按钮级权限通过指令（`v-auth` / `v-perm` / `v-roles`）控制。
Frontend dynamic routes are generated from backend-delivered menus + permission tags; button-level permissions are controlled via directives (`v-auth` / `v-perm` / `v-roles`).

---

## 内置业务模块 / Built-in Modules

| 模块 / Module | 目录 / Directory | 说明 / Description |
|------|------|------|
| 仪表盘 / Dashboard | `uni_console/src/modules/dashboard` | 数据总览 / overview |
| 用户管理 / Users | `system-user` | 用户 CRUD、部门树、角色分配 / CRUD, dept tree, role assignment |
| 角色管理 / Roles | `system-role` | 角色、菜单 / 数据权限分配 / role & menu/data perms |
| 菜单管理 / Menus | `system-menu` | 菜单 / 路由 / 权限点 / menus, routes, permission points |
| 部门管理 / Departments | `system-dept` | 组织架构树 / org tree |
| 字典管理 / Dictionary | `system-dict` | 数据字典类型 / 数据 / dict types & data |
| 配置管理 / Config | `system-config` | 系统参数（可热更）/ params (hot-reload) |
| 文件管理 / Files | `system-file` | 上传 / 下载 / 分片 / 缩略图 / upload, download, chunked, thumbnail |
| 定时任务 / Jobs | `system-job` | Cron 任务、执行日志 / cron jobs & logs |
| 日志管理 / Logs | `system-log` | 登录日志 / 操作日志 / login & operation logs |
| 通知公告 / Notices | `system-notice` | 公告发布、已读聚合 / announces & read aggregation |
| 系统监控 / Monitor | `system-monitor` | 服务端 / SQL / Redis 缓存 / pprof / 在线用户 / server, SQL, Redis, pprof, online users |
| 个人中心 / Profile | `system-user-center` | 账号设置、登录活动 / account settings, login activity |

---

## 响应契约 / Response Contract

所有接口统一返回 `{ code, msg, data }` 信封，**同时**携带 HTTP 状态码与业务码。
两者是**互不相交**的两个命名空间，消费时必须分开使用：

| | HTTP 状态码 | 业务码 |
|---|---|---|
| 取值 | 200 / 400 / 401 / 403 / 404 / 500 … | 0 / 10001 / 10002 / 40000 / 40400 / 50000 … |
| 读取处 | `error.response.status`（前端）、响应行（后端） | 信封 `code` 字段 |
| 前端对应 | `ApiStatus.*`（`utils/http/status.ts`） | `BizCode.*`（同文件） |
| 后端对应 | `AppError.HTTPStatus` | `apperror.Code*` 常量 |

- **成功**恒为 HTTP 200 + `code: 0`（见 `app.Success`）。
- **失败**由 `app.Error` 同时给出两者：HTTP 状态取 `AppError.HTTPStatus`，
  信封 `code` 取 `AppError.Code`。
- 因此不存在"二者只能选一"的情况 —— 按手上的字段选对应枚举即可。

⚠️ **不要把两者混用。** 例如把成功判定写成 `code === ApiStatus.unauthorized`
是恒不成立的：后端未授权业务码是 `10001`，永不等于 HTTP `401`，
该分支会让**每一个成功响应都被误判为失败**。
`uni_console/src/utils/http/status.test.ts` 与 `interceptor.test.ts` 已用断言锁定这一点。

All endpoints share one envelope, `{ code, msg, data }`, carrying **both** an HTTP
status and a business code. These are two **disjoint** namespaces and must be used
separately: HTTP status via `error.response.status` (frontend) / the status line
(backend), and the business code via the envelope's `code` field. Success is
always HTTP 200 with `code: 0`; failures carry both, set from `AppError.HTTPStatus`
and `AppError.Code` respectively. Mixing them is a bug — comparing a business code
against an HTTP status is always false.

---

## API 文档 / API Documentation

后端基于 [swag](https://github.com/swaggo/swag) 自动生成 Swagger 文档（`uni_core/docs/`），运行时在 Swagger UI 中访问。接口统一前缀 `/api/v1`，采用 RESTful 风格，Bearer JWT 鉴权。
The backend auto-generates Swagger docs (`uni_core/docs/`) via [swag](https://github.com/swaggo/swag), served through Swagger UI at runtime. All endpoints share the `/api/v1` prefix, follow RESTful conventions, and use Bearer JWT auth.

---

## 开发规范 / Development Conventions

- 后端采用 **分层架构**：`handler → service → repository → model`，依赖注入经 `wireup` 装配。
  The backend follows a layered architecture (`handler → service → repository → model`) wired together via `wireup` DI.
- 前端业务以 **插件目录** 组织，`index.ts` 声明模块元信息与路由。
  Frontend features are organized as plugin directories; `index.ts` declares module metadata and routes.
- 提交遵循 Conventional Commits 规范。
  Commits follow the Conventional Commits specification.
- 代码格式化：Go `gofmt` / `go vet` / `golangci-lint`；前端 ESLint + Prettier。
  Formatting: Go `gofmt` / `go vet` / `golangci-lint`; frontend ESLint + Prettier.

---

## License 与致谢 / License & Acknowledgements

本项目仅供学习与参考。UI 视觉风格借鉴自 [art-design-pro](https://github.com/Daymychen/art-design-pro)，特此致谢。
This project is for learning and reference only. Its UI visual style draws inspiration from [art-design-pro](https://github.com/Daymychen/art-design-pro) — many thanks to the original authors.