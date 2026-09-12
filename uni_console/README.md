# 后台管理系统 (Web Admin)

基于 Vue 3 + TypeScript + Vite 7 + Element Plus + Tailwind CSS 4 的插件化后台管理框架前端。

> 视觉与组件精华提炼自 [art-design-pro](https://github.com/Daymychen/art-design-pro)，完整原始工程作为只读参考保留在仓库根目录 `reference/`。

## 技术栈

- Vue 3.5 + TypeScript
- Vite 7 + Vue Router (hash) + Pinia + vue-i18n + axios
- Element Plus + Tailwind CSS 4
- 自研插件框架：`src/modules/<name>/` 目录即插件，增删文件夹即启用/停用

## 目录结构

```
uni_console/
├── src/
│   ├── framework/     # 框架核心：插件运行时、路由注册、生命周期
│   ├── core/          # 框架骨架：Shell、Store、Router、Config
│   ├── components/    # 精简组件库（art-*）
│   ├── modules/       # 业务插件（一个目录 = 一个模块/插件）
│   └── utils/         # 数据访问层（http）等
└── reference/         # art-design-pro 只读参考（不参与构建）
```

## 快速开始

```bash
pnpm install
pnpm dev        # http://localhost:3006
pnpm build      # 类型检查 + 产物构建
```

## 说明

- 后端：对接 Go 服务 `uni_core/`（默认 `http://127.0.0.1:9999`，API 前缀 `/api/v1`）。
- 权限模式：`VITE_ACCESS_MODE=frontend`（菜单/路由由插件声明，后端返回 `permissions` 过滤）。