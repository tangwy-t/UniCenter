# Docker 管理全部 UI 重构详细设计

> 交付类型：设计文档，不是已实施的 UI 改造报告。  
> 代码基线：UniCenter，`main`，提交 `2e0f936`；核查日期：2026-09-29。  
> 设计原则：以 UI 渲染和操作体验为中心，保留现有功能边界；接口仅做有明确渲染收益的适配；复用现有框架、权限、命令、日志和编辑能力。

## 0. 文档口径与边界

### 0.1 事实、设计与缺口

- **现状事实**：来自当前源码、路由注册、接口 DTO、执行链路和现有测试。历史设计文档仅作交叉参考；与实现冲突时，以本次代码基线为准。
- **本次设计**：下文明确提出的新布局、交互、组件职责与接口调整，是待实施方案，不代表当前系统已经具备，也不代表用户已经确认视觉偏好。
- **待补充信息**：无法从当前仓库核实的产品规则、真实环境和个人偏好，一律使用 `(待补充：…)`。不据此伪造线上截图、规模、性能指标或业务能力。
- **完整性口径**：路由只是一级清单；详情 Tab、展开行、弹窗、批量操作、公共主机条、日志、终端、Compose 双模式编辑和备份恢复都属于覆盖对象。
- **本次实际变更**：仅新增本设计文档。接口、业务源码、权限、部署和生产数据均未修改。

### 0.2 重构范围

| 范围 | 处理方式 |
| --- | --- |
| Docker 管理 5 个列表页、2 个详情页 | 全部重构 UI，保留原路由、可达性和功能 |
| 页面内所有 Tab、操作菜单、确认弹窗、批量反馈、日志、终端、Compose 编辑器和备份历史 | 纳入完整覆盖和验收 |
| Docker 专属状态、格式化、数据转换和接口调用层 | 仅为本次 UI 渲染和交互正确性进行整理 |
| 服务端 DTO、已有命令结果 | 有明确字段缺口才做最小兼容调整，不重写 Docker 执行架构 |
| 全站导航、主题、认证、设备管理、通用系统配置 | 复用并做关联回归，不重构这些模块 |
| 容器创建、镜像构建/推送、仓库管理、Swarm/Kubernetes、跨主机聚合、容器长期监控历史 | 当前 Docker UI 未提供的独立业务不纳入；不能因新布局顺手新增 |
| 日志/终端以外的新实时通道、全局任务中心 | 不凭视觉需要虚构后端能力或扩大产品范围 |

### 0.3 待补充清单与开发阻塞点

| 编号 | 待补充内容 | 影响及处理 |
| --- | --- | --- |
| U-01 | (待补充：认可的设计风格、品牌规范、参考页面或设计稿) | 视觉基线已锚定仓库既有主线（§2.1，数值取自服务监控 monitor-tokens.scss），风险从「凭空建议」降为「延续既有风格待签收」；视觉冻结前仍需核对 |
| U-02 | (待补充：实际部署版本、动态菜单与角色权限配置、是否存在仓库外 Docker 页面或定制入口) | 源码范围可完整核对，线上覆盖需部署清单复核，不能以单测代替 |
| U-03 | (待补充：目标浏览器、必须支持的终端尺寸和无障碍等级) | 本文提出推荐验收矩阵；最终兼容承诺以产品确认结果为准 |
| U-04 | (待补充：生产单主机容器/镜像/卷/网络/项目数量、典型日志吞吐、典型 Compose 文件规模) | 不擅自改为服务端分页或新流协议；容量结论通过测量确定 |
| U-05 | (待补充：真实页面截图及空态、异常态、权限态的线上样本) | 当前现状分析是源码审计，不是浏览器视觉走查 |
| U-06 | (待补充：验收测试账号、可操作的非生产 Docker 主机和可恢复测试资源) | 有副作用的功能仅在明确授权的测试环境验证 |
| U-07 | (待补充：发布窗口、负责人、视觉签收人与最终性能预算) | 不编造工期、人力与上线时间；以实施阶段的进入/退出条件推进 |

## 1. 现状分析

### 1.1 技术栈与复用基础

| 层次 | 已核实技术/位置 | 本次结论 |
| --- | --- | --- |
| 前端 | `uni_console/package.json`：Vue 3.5.22、TypeScript ~5.6.3、Vue Router ^4.5.1、Pinia ^3.0.3、Vite ^7.1.5 | 沿用 Vue Composition API；不更换框架或新增状态框架 |
| UI 与样式 | Element Plus 2.11.4、Tailwind CSS ^4.1.14、SCSS；已有 ArtTable、ArtSearchBar、表格列工具和主题变量 | 组合现有控件，Docker 样式局部收敛，不建立第二套基础组件库 |
| 日志/终端/编辑器 | 原生日志缓冲、xterm ^6.0.0、addon-fit、CodeMirror 6、YAML ^2.9.1 | 保留专业组件及领域逻辑，不用自制 textarea/terminal 替代 |
| 请求与类型 | Axios；`src/utils/http/index.ts`；`Api.Docker.*` 由后端生成 | 不在页面手写重复 DTO；不另建认证拦截器 |
| 测试 | Vitest ^4.1.11、Vue Test Utils、jsdom、vue-tsc、ESLint | 保留已有 15 个 Docker 测试文件并补充渲染/交互/竞态回归 |
| 后端 | `uni_core/go.mod`：Go 1.26.0、Gin、Redis；`uni_agent/internal/dockerops/` 负责主机侧执行 | 保持 Console → Core → Agent → Docker 的边界 |
| 协议 | `uni_protocol/docker.go`，快照、指令、指令结果和流帧 | UI 改版不能绕过白名单、权限、保护规则、确认和路径校验 |

### 1.2 已核实的公共规范

- 主题：`src/assets/styles/core/tailwind.css` 已有明暗模式背景、边框和灰阶；`el-light.scss` / `el-dark.scss` 负责 Element Plus 主题。
- 视觉主线：服务监控四视图（server/cache/sql/pprof，`monitor-tokens.scss`）与设备模块（overview/detail，`device-tokens.scss` 复刻其数值）已形成「hero 页头 + KPI 磁贴 + live-dot 呼吸圆点 + 四态」的成熟范式；Docker 镜像详情 `imd-hero` 与设备 `dd-hero` 骨架本已同构。新 UI 对齐这条主线而非另起炉灶（数值与出处见 §2.1、§4.4）。
- 字体：全站系统字体栈已包含中文字体，不为 Docker 单独下载字体。
- 断点：`src/config/breakpoints.ts` 与 `_breakpoints.scss` 已统一：500 / 640 / 768 / 1024 / 1180 / 1280 / 1440 / 1600 px；禁止另立一套接近但不相同的断点。
- 表格：`components/core/tables/art-table/index.vue` 已支持 loading、分页、空态和 `hideBelow` 响应式列；按需复用，不把整个 Docker 模块绑定到新的通用 CRUD 配置器。
- 认证：通用 HTTP 层负责 Bearer、401 刷新与重放；日志流使用独立流式读取，不能直接照搬整段响应的 Axios 用法。
- 可访问性：全局已处理 `prefers-reduced-motion`；新增交互仍需焦点管理、文本状态和键盘操作。

### 1.3 基线验证与结论边界

本次执行了 Docker 模块现有单元/组件测试：**15 个测试文件，268 项通过，退出码 0**。

```sh
VITE_API_PREFIX=/api/v1 VITE_API_URL=/ VITE_BASE_URL=/ pnpm --dir uni_console test src/modules/docker/__tests__
```

这里的环境值仅用于本地测试配置加载，不是生产地址。运行环境为 Node v24.15.0、pnpm 11.24.0。测试启动时提示 `package.json` 的 `pnpm.onlyBuiltDependencies` 配置被忽略；该工具链告警不是本次 UI 改造范围，不因此改动依赖配置。

**通过现有测试 ≠ 已完成新 UI 验收**：尚未实施新 UI，也未进行生产写操作、真实浏览器截图比对、后端端到端验证或容量测试。下文的验收标准是开发阶段必须完成的门禁。

### 1.4 页面与功能全清单

以下清单来自源码逐文件核对（文件与行号为 `main@2e0f936` 的现状）。Docker 模块共 **7 个视图、10 个组件、2 个 composable、10 个 utils、1 个 api 层，合计约 7700 行**，另有 15 个测试文件。

#### 1.4.1 路由与入口（`uni_console/src/modules/docker/index.ts`）

| 路由 | 视图 | 权限（authMark） | 菜单 | 备注 |
| --- | --- | --- | --- | --- |
| `/docker/containers` | views/containers.vue（590 行） | docker:list | 显示 | 路径须与后端 v015 种子菜单逐字一致 |
| `/docker/images` | views/images.vue（601 行） | docker:list | 显示 | 同上 |
| `/docker/volumes` | views/volumes.vue（345 行） | docker:list | 显示 | 同上 |
| `/docker/networks` | views/networks.vue（245 行） | docker:list | 显示 | 同上 |
| `/docker/projects` | views/projects.vue（1127 行） | docker:list | 显示 | 同上 |
| `/docker/container-detail/:id` | views/container-detail.vue（1080 行） | docker:inspect | 隐藏 | 列表页跳转进入 |
| `/docker/image-detail/:id` | views/image-detail.vue（843 行） | docker:inspect | 隐藏 | 列表页跳转进入 |

#### 1.4.2 页面功能点

**容器列表**（containers.vue）

| 功能块 | 现状 |
| --- | --- |
| 页面外壳 | docker-page：主机条（切换+刷新+同步文案+离线提示）、dockerOk=false 全页空态+重新检测 |
| 筛选 | ArtSearchBar：keyword（名称/镜像名）、state（select）、runningOnly（switch）；本地过滤 |
| 列 | 名称、镜像、状态、端口、运行时长、操作（固定右侧）；操作列含「日志」+ 注册表写动作 |
| 行操作 | action-menu + DockerActionConfirm（标准档/删除档；受保护显示锁与 force） |
| 批量 | 勾选后批量启动/停止/重启/删除；批量删除走独立批量确认弹窗；指令在途禁用整栏 |
| 主机切换 | 清空勾选 + 清空筛选（见 D-4：与规范「筛选保留」不一致） |
| 权限 | 列表= docker:list；写动作按注册表（启停=manage、删除=delete、终端=exec） |

**镜像列表**（images.vue）

| 功能块 | 现状 |
| --- | --- |
| 筛选 | keyword、仅悬空（danglingOnly）、未使用（unusedOnly） |
| 列 | repoTags（minWidth 240）、大小/创建时间（hideBelow desktop）、使用中、操作 |
| 页脚 | 合计（总数/总体积/未使用数）+ 写操作条（清理/拉取/打标签/导出/导入） |
| 确认 | 清理=删除档+all 复选框；删除=标准档；导出=文件名档，两段式（alreadyExists 后再确认覆盖） |
| 特殊 | 使用中的镜像不可删 → 页面自构 rowMenuItems 单条禁用（绕过 action-menu 整组禁用限制，见 D-7） |
| 输入 | 拉取镜像/打标签/导出文件名走 ElMessageBox.prompt |
| 引用键 | repoTags[0] ‖ image.id |

**卷列表**（volumes.vue）

| 功能块 | 现状 |
| --- | --- |
| 筛选 | keyword、仅未使用（unusedOnly） |
| 列 | 名称、驱动、大小（hideBelow desktop）、使用中（含挂载者）、受保护（🔒+结论）、操作（仅有删除权限才渲染） |
| 页脚 | 合计 + 「N 个卷大小未知」「未使用 N」结论行 |
| 确认 | volume:remove 标准档；volume:prune 删除档 + force 复选框 |

**网络列表**（networks.vue）

| 功能块 | 现状 |
| --- | --- |
| 筛选 | keyword、仅内部（internalOnly） |
| 提示行 | Docker 默认网络（bridge/host/none 等）的说明常驻 |
| 列 | 名称、驱动、范围（本机/集群，hideBelow tablet）、容器数（hideBelow desktop）、仅内部 |
| 确认 | network:remove 标准档（网络 DTO 无 protected 字段） |

**项目列表**（projects.vue，模块内最大页面）

| 功能块 | 现状 |
| --- | --- |
| 结构 | ElTable 展开行三层：项目头（名称/状态/元信息/受阻结论 + Up -d/停止/启动/重启/拉取/Down）→ 服务行（名称/容器 N/M/🔒 + 停止/启动/重启 + remove-containers/scale 菜单）→ 配置行（查看配置/编辑/＋添加服务 + 历史备份） |
| 数据拼装 | 服务树由容器快照的 composeProject/composeService 标签**反推**；统计「另有 N 个服务未在容器列表中」（missingServiceCount） |
| 循环操作 | protected 服务的启停/重启走逐容器循环（runContainerLoop）：顺序发指令、跳过无 force 的受保护项、收集失败、摘要显示前 3 条 |
| 确认弹窗 | 4 个 DockerActionConfirm：项目（含回收孤儿/删卷复选框）、服务、循环、回滚（带 override） |
| 配置查看 | ElDialog + `<pre>` 等宽展示；载入失败走 ElAlert+重试 |
| 备份 | backupState 按项目懒加载（COMPOSE_ACTIONS.read 的 backups）；回滚带 token+baseHash 乐观锁 |
| 编辑器 | ComposeEditor 集成（v-model/project/projectProtected/autoAdd；saved→重拉备份+快照，applied→重拉） |
| 主机切换 | 关闭全部弹窗 + 清空 backupState（全模块最完整的重置纪律） |

**容器详情**（container-detail.vue）

| 功能块 | 现状 |
| --- | --- |
| 头部 | 返回 + 容器名 + 短 id + 状态；动作：启动/停止/重启/删除/刷新（按权限与保护门控） |
| Tab 概览 | inspect 懒拉取 + 快照：挂载、健康、网络；快照在则先渲染快照态 |
| Tab 日志 | 行数 100/500/2000 + 一次性拉取 + Follow 开关（NDJSON 流 + log-viewer）；离开 Tab/页面断流 |
| Tab 终端 | 懒挂载 pty-terminal（组件生命周期=会话生命周期） |
| Tab 环境 | 明文环境变量 / 标签 / Entrypoint / Cmd 等宽展示 |
| 主机跟随 | watch ctx.hostId 重读 inspect/日志 |
| 写禁用 | 权限不足或保护门不满足时按钮不渲染或禁用+结论句 |

**镜像详情**（image-detail.vue）

| 功能块 | 现状 |
| --- | --- |
| 头部 | 返回 + 镜像名 + 短 id；打标签/导出 tar（manage）、删除（delete，使用中禁用+结论） |
| 摘要行 | 大小 / 创建于 / 使用（悬空警示色） |
| Tab 分层历史 | agent 顺序（自下而上）不重排；合计与镜像大小**不同口径**的说明常驻 |
| Tab 元数据 | 架构/系统/暴露端口/入口点/命令/标签表 |
| Tab 关联容器 | 从当前主机快照按镜像引用反查（不另开端点） |

#### 1.4.3 公共组件（`components/`，10 个）

| 组件 | 职责（现状已核实） |
| --- | --- |
| docker-page.vue | 页面外壳：主机条插槽（刷新+同步文案+陈旧样式+离线提示）、dockerOk=false 全页空态+重新检测、search/table/footer 插槽包 ElCard |
| host-switcher.vue | ElSelect 主机下拉：dockerOk 状态点 + 在线/离线副文案 |
| action-menu.vue | ArtButtonMore 包装：extraItems + 注册表动作；丢失注册的动作静默丢弃；blocked=protected&&guarded&&!canForce 时整组锁 |
| action-confirm.vue | 通用强确认弹窗：目标卡（危险/保护样式）、结论句、逐字输入校验、force 复选框（保护+有 exec 权限时） |
| log-viewer.vue | 日志渲染：Follow 开关、关键字过滤、复制/下载、滚动暂停/继续（8px 底部阈值）+暂停计数、**纯文本插值渲染（防 XSS，禁止 v-html）**、响应式高度 |
| pty-terminal.vue | xterm.js 容器终端：exec→轮询票据→WS 升级；票据过期重试（复用会话）、退避 3 次、resize 合帧、卸载显式 cancel |
| compose-editor/compose-editor.vue | 四步闭环容器：载入→编辑→保存（validate 预检→diff 预览→强确认→patch/write）→应用（独立强确认）；双模式共享文档模型；三条防注释丢失防线 |
| compose-editor/form-mode.vue | 表单模式：服务折叠卡（镜像/容器名/重启策略/CPU/内存 + 端口/卷/环境变量列表编辑）+ 模板新增服务 |
| compose-editor/yaml-mode.vue | YML 模式：CodeMirror 6 + 与文档模型同一解析器的语法校验（单一判定口径） |
| compose-editor/backup-history.vue | 历史备份：懒加载入口、时间/体积/同异结论、当前文件对照、回滚交调用方强确认 |

#### 1.4.4 数据与逻辑层

| 层 | 内容 |
| --- | --- |
| api.ts | 6 个端点封装（见 §3.1）；fetch+ReadableStream 日志流带 Authorization；WS 票据 URL 换算 |
| composables/useDockerHostState.ts | 主机清单+快照生命周期、seq 守卫、主机切换钩子、陈旧结论计算 |
| composables/useDockerCmds.ts | 指令通道收口：受理（错误四分类）→轮询（指数退避）→结构化结果；成功后立即重拉+落定重拉（1.5s）；pendingId/busy |
| utils/actions.ts | 21 个写动作注册表（label/icon/权限/危险度/确认档/保护门）+ splitDockerProjectService + expectedConfirm + protectedGate |
| utils/cmd.ts | 4 个只读动作（PHASE1_ACTIONS）、轮询节奏 pollDelay |
| utils/compose.ts | 1017 行：文档模型（建模键+_raw 保注释）、解析/序列化/最小 patch/diff、备份格式化 |
| utils/confirm.ts | 确认档类型与校验 |
| utils/display.ts / host.ts / log.ts / snapshot.ts / stream.ts | 展示口径、同步文案、日志行数上限、快照访问器、NDJSON/帧解码/行缓冲纯函数 |
| utils/host-context.ts | provideDockerHost/inject 上下文（含「同组件 provide 后 inject 拿不到」的踩坑记录） |

**动作总量 29**：只读 4（utils/cmd.ts）+ 写 21（utils/actions.ts 注册表）+ `container:exec` + Compose 配置 3（read/validate/patch-write，utils/compose.ts）。权限六档：docker:list / inspect / manage / delete / exec / config；确认三档：标准（无输入）/ 删除词（DELETE）/ 目标词（项目名、服务名、文件名）。

### 1.5 已核实的交互与状态缺陷

以下条目均经本次源码复核（附文件:行号）；子代理曾报告、但复核后属**设计意图而非缺陷**的项，单列在 1.5.2。

#### 1.5.1 缺陷与缺口

| 编号 | 现象 | 位置 | 影响 |
| --- | --- | --- | --- |
| D-1 | 快照拉取失败（网络错误）时 `state=null` → 派生值 stale=false、neverReported=false、ageSeconds=0 → 头部显示「刚刚同步」 | useDockerHostState.ts:71-74 + utils/host.ts:10-15 | 数据获取失败被渲染成「数据是新的」，误导信任 |
| D-2 | `pendingId` 是单值 ref：并发指令互相覆盖；且要等 inflight 归零才清除——指令 A 先完成时 A 的行仍显示在途直到 B 也结束 | useDockerCmds.ts:153, 221-223 | 行级在途指示失真（项目页逐容器循环、连续快速操作时） |
| D-3 | 容器列表「日志」菜单项无 auth 字段：无 docker:inspect 权限的用户可见该项，点击后才被路由/接口拒绝 | containers.vue:288（对照写动作均带注册表 auth） | 权限反馈滞后；与「不渲染≠禁用」的阶段门规范不一致 |
| D-4 | 主机切换重置纪律各页不一致：containers 清勾选**还清筛选**（规范 §11.0 与 useDockerHostState 注释均要求「筛选保留」）、且不复位已开的确认弹窗；projects 则关闭全部弹窗+备份态 | containers.vue:200-204 对照 projects.vue 同名钩子 | 同一交互在不同页面行为不同；containers 违反自家规范 |
| D-5 | 日志流消费端无收尾 API：createLogFeed 没有 end()——流断开时 NDJSON 残留半行被静默丢弃（parser.flush() 无处调用）；eof 后 pushRaw 也未关闸 | utils/stream.ts:241-277 | 最后一行日志可能缺失；eof 语义未收敛 |
| D-6 | `refresh` 只重拉快照不重拉主机清单：新增/离线主机在页面存活期间不会出现在切换器里 | useDockerHostState.ts:101（清单仅在 setup 拉一次） | 多主机环境清单漂移；离线提示依赖旧清单 |
| D-7 | action-menu 只支持整组禁用，不支持单条禁用：images.vue 为「使用中的镜像不可删」自构 rowMenuItems 绕行 | action-menu.vue（blocked 为整组）+ images.vue:427-434（模板 534） | 组件能力缺口逼出页内重复逻辑，重构时其他页会再抄一遍 |
| D-8 | 侧边栏切换列表页丢失主机选择：模块内跳转都带 `?host=`，但菜单进入新页无 query → hostId 落回第一台主机，多主机用户每次换页都要重选 | utils/host-context.ts:49（`route.query.host ?? hosts[0]`）；带 host 的模块内跳转见 containers.vue:299、images.vue:465、container-detail.vue:718、image-detail.vue:484 | 多主机环境操作连续性被菜单打断；模块代码改不了菜单链接，需前端兜底 |
| D-9 | 项目页是五个列表页中唯一无筛选的页面：无 keyword、无状态过滤，项目多时只能目视扫描 | views/projects.vue 全文无 ArtSearchBar/searchItems | 与其它四页交互不一致；规模增长后定位成本线性上升 |
| D-10 | 全模块无自动刷新：快照 30s 周期 + 手动刷新；同仓库服务监控/设备总览已有「自动刷新开关 + live-dot 呼吸圆点」成熟先例 | docker 模块 grep「autoRefresh/自动刷新」为空；先例 system-monitor/views/server.vue 页头、device/views/overview.vue | 观察态页面的「数据是活的」体感缺失，依赖用户手点刷新 |

#### 1.5.2 复核后确认的设计约束（不是缺陷，重构必须保留）

| 项 | 说明 |
| --- | --- |
| `:key="route.path"` 整页重建 | art-page-content 以路由路径为 key；同主机下不同容器/镜像间跳转必然重建组件。新 UI 不能依赖「同组件换 props」假设 |
| Transition `out-in` 单根 | 所有页面/弹窗根节点必须单一（single-root.test.ts 钉住）；双根会白屏 |
| Compose `_raw` 保注释 | 未建模键进 `_raw` 且 patch 只发被改键——这是「表单改配置不丢注释」的核心机制，不可简化成全量重写 |
| 分层历史顺序 | agent 已把 docker history 翻转为自下而上；页面不重排（两份顺序会分叉） |
| 日志纯文本插值 | log-viewer 用 `{{ }}` 渲染，明确禁止 v-html（防日志内容 XSS） |
| 日志 Follow 双次重拉 | 指令成功后立即重拉+1.5s 落定重拉，覆盖 agent push 帧与缓存更新的到达次序 |

#### 1.5.3 范围外关注（记录但不处理）

后端（uni_core/uni_agent）在流通道上的若干健壮性问题（NDJSON 写超时、WS 泵关闭、票据活性检查）曾由子代理报告，本次未逐条复核，不纳入 UI 重构范围；(待补充：后端侧复核结论——如后续确认，将作为 §2.3 流交互异常态的依据)。

## 2. UI 重构方案

### 2.1 设计方向与视觉语言

**关键词**：信息密度适中、操作就近、状态即事实、一眼分清「能做/不能做/为什么」。不追逐装饰性效果（玻璃拟态、大圆角、强投影），把视觉预算花在**状态表达与层级**上——运维工具的「美」是低噪音下的高确定性。

视觉基线（**锚定仓库既有主线**，非凭空建议）：布局与数值取自服务监控 `monitor-tokens.scss`，采用先例为设备模块（overview 的 kpi-grid/do-hero、detail 的 dd-hero）——`sv-hero`/`live-dot`/四态均已在产品内落地验证；是否最终签收仍见 U-01。

| 维度 | 方案 |
| --- | --- |
| 布局骨架 | 保留「页头 hero（图标 + 标题 + 实时状态 + 动作，对齐 sv-hero/do-hero）→ 内容卡片」两段式，由 docker-page 统一；主机条并入 hero 副标题位；统计改 KPI 磁贴（kpi-grid：2/3/5 列响应式栅格，同设备总览，且仅在数据就绪态渲染）；列表页操作条从表格底部上移进 hero 右侧（写操作与统计同区，减少视线跳动）；hero 动作区含刷新与 10s 自动刷新开关（D-10，sv-hero 同款） |
| 容器令牌 | 卡片圆角 14px、内边距 1rem、阴影 `0 1px 3px rgba(16,24,40,.05)`（暗色 `rgba(0,0,0,.4)`）；KPI 数值 20px/650（tabular-nums）、标签 13px、辅助说明 11px、图标方块 40px/圆角 12px——逐字取自 monitor-tokens.scss；注意其内部已漂移（cache 的 22px/44px 为少数派，**不采纳**，以多数派为准） |
| 色彩 | 全部走既有 CSS 变量（--art-\*、--default-box-color、EP 语义色）；Docker 模块新增 4 个模块级语义 class：状态点、保护锁、mono 字段、危险操作条（见 §4.4） |
| 状态可视化 | 容器/项目状态统一为「彩色圆点 + 文本」（StateDot：运行=success、停止=info、暂停=warning、异常=danger）；主机实时态复用 live-dot 呼吸圆点（2s）先例；受保护统一锁形图标 + 结论 tooltip；替代现有各页手写的文字/emoji 混用 |
| 密度 | 表格沿用全局 tableSize；操作列固定右侧统一 96~140px；详情页事实行（镜像页的「大小/创建/使用」模式）推广为 FactRow 组件 |
| 排版 | 名称/ID/镜像等标识类字段统一等宽字体（--art-font-family-mono）；长 ID 始终短显+title 全显+点击复制（dd-hero__copy 同款） |
| 明暗模式 | 任何新样式禁止写死颜色值，必须引用变量；日志/终端保留自带暗色画布（内容语义） |
| 动效 | hover/过渡 200ms（复用 .tad-200）+ 入场 0.3s 自下淡入（rise 先例）；live-dot 呼吸 2s；全部包 prefers-reduced-motion（沿用 monitor-tokens 的 reduced-motion mixin） |

### 2.2 各页面重构方案

每页给「结构改动 / 交互改动 / 视觉改动 / 修复缺陷」。**功能边界不变**：动作集合、权限、确认档、保护门全部沿用注册表与协议。

#### 2.2.1 容器列表

- 结构：页头改为「主机条 + 四格统计卡（总数/运行/停止/受保护）」一行；写操作条（批量栏）合并到页头右侧，勾选后原地展开；表格保持 ArtTable；hero 动作区加 10s 自动刷新开关（复用 loadState，默认关）。
- 交互：行菜单统一走 action-menu（补 D-7 的单条禁用能力后，「日志」项带 auth=inspect 修复 D-3）；批量确认弹窗展示目标清单（前 5+溢出计数）；主机切换重置对齐规范（保筛选、清勾选、**关弹窗**）修 D-4。
- 视觉：状态列改 StateDot；端口列紧凑分组（宿主:容器 → `0.0.0.0:80→80/tcp` 折叠展示）；镜像名等宽+去 tag 截断 tooltip。
- 缺陷修复：D-3、D-4、D-2（pendingIds 多槽）、D-10（自动刷新）。

#### 2.2.2 镜像列表

- 结构：页头统计卡（总数/总体积/悬空/未使用）；清理/拉取等写按钮归页头右侧操作区（现散在表尾）。
- 交互：行菜单统一 action-menu（删除项对使用中镜像**单条禁用**+结论，替代 rowMenuItems 绕行，删约 40 行页内逻辑）；导出两段式确认保留（协议语义）；prompt 输入统一改 action-confirm 的可选输入框形态（减少两种输入弹窗形态）。
- 视觉：repoTags 主 tag 加粗、其余 tag 折叠为 +N；大小列右对齐数字等宽；悬空/未用用 tag 徽标而不是文字。
- 缺陷修复：D-7。

#### 2.2.3 卷列表 / 网络列表

- 两页同构，重构后共享同一「简单资源页」骨架（统计行+筛选+表格+行菜单）。
- 卷：受保护列与使用中列合并为一列「状态」（锁/挂载者/未使用三态），页脚结论行保留（大小未知口径如实）。
- 网络：默认网络提示行收进页头说明位（不再占表格上方独立条）；范围列改徽标（本机/集群）。
- 确认档、force 选项、删除权限渲染逻辑全部不变。

#### 2.2.4 项目列表（重构重点）

- 结构：1127 行拆分为 project-row（项目头卡）、service-row、config-row 三个子组件 + 页面级弹窗编排（见 §4.2）；展开行改为**卡片流**（项目=一张卡，卡内三个分区：概要/服务列表/配置），不再用 ElTable 展开行承载非表格内容。
- 交互：服务树若采纳 §3.2 A-1（快照补服务清单）则删除反推与 missingServiceCount 提示；未采纳则保留现有反推+缺口说明（两案均可落地，接口层二选一）。逐容器循环反馈改为卡内进度行（N/M + 失败摘要），保留 hostAtStart 守卫与保护跳过语义。备份入口与编辑器集成不变。**补 keyword（项目名/服务名）+ 状态筛选**（本地过滤，与其它四页同构）修 D-9。
- 视觉：项目状态 StateDot+受阻结论条（warning 底）；服务行容器数 N/M 数字徽标；配置区等宽文件名+备份计数。
- 缺陷修复：D-2（循环期间的行级在途）、D-6（主机清单随刷新重拉）、D-9（补筛选）。

#### 2.2.5 容器详情

- 结构：1080 行拆 hero 头 + 4 个 Tab 子组件（见 §4.2）；Tab 懒挂载与现有 keep 语义保留。
- 交互：日志 Tab 的行数选择改分段控件（100/500/2000）+一次性/Follow 两模式明确分栏；Follow 断流给重连按钮（现仅结论句）；终端 Tab 保持懒挂载+组件生命周期=会话生命周期。修复 D-5：日志流收尾统一走 feed.end()（冲残留半行+eof 关闸）。
- 视觉：概览 Tab 的挂载/健康/网络改为分组描述列表（ElDescriptions 复用）；环境 Tab 的键值改为两列等宽对齐；hero 头状态点+保护锁。
- 缺陷修复：D-5。

#### 2.2.6 镜像详情

- 结构：hero 头+摘要行（FactRow 推广）+3 Tab 保留；关联容器 Tab 的表格复用列表页列配置子集。
- 交互：分层历史的「合计≠镜像大小」口径说明保留常驻（这是防误读的关键文案，重构不得删）；删除受阻结论展示位置随 hero 动作区。
- 视觉：分层表加序号列与累计大小列（数据已有）；元数据 Tab 的标签表键名等宽。

#### 2.2.7 Compose 编辑器与日志/终端组件

- 编辑器：双模式+四步闭环+防注释三防线**原样保留**（这是模块内成熟度最高的部分）；视觉统一弹窗骨架（宽度档 min(900px,94vw) 不变），diff 预览的行着色改用语义 class（现用 EP light-9 变量，保留）；表单模式折叠卡标题加「已修改」点状指示（tag 保留）。
- log-viewer：保留纯文本插值与滚动暂停机制；工具栏改图标+文字双形态（窄屏只留图标）；关键字过滤高亮**不做**（需要改渲染方式，防 XSS 约束下收益/风险比不成立，维持纯文本）。
- pty-terminal：连接/失败/断开三态浮层保留；主题色跟随暗色画布不变；补「会话已结束」后输入区不可点的可视觉状态（现仅静默丢弃）。

### 2.3 统一交互模式（全模块契约）

| 模式 | 规则 |
| --- | --- |
| 列表四态 | 加载（骨架）、空（ElEmpty+引导文案）、错误（结论句+重试）、主机不可用（dockerOk=false 全页空态+重新检测）——全部由 docker-page 承担，页面只提供数据 |
| 操作反馈链 | 受理（行 pending 多槽）→ 结论 toast（成功/失败均一句，服务端结论句优先）→ 立即重拉+落定重拉（1.5s，保留现状） |
| 确认 | 所有写操作统一 action-confirm；三档确认与 force 复选框逻辑来自注册表+协议，页面零自定义确认（现状 projects 的 4 个弹窗收口为 1 个组件实例+状态机） |
| 主机切换 | 统一纪律：保留筛选、清空勾选、关闭全部弹窗、断开流、清页面级缓存（backupState 等）——由 useDockerHostState 的 onHostSwitch 模板化（见 §4.3） |
| 保护门 | protected 且无 exec+force 的动作：不渲染或单条禁用+锁+结论句（「不渲染 ≠ 禁用」的阶段门规范维持） |
| 权限 | 菜单项/按钮/操作条三处同一来源（注册表 auth）；有 list 无 inspect 的用户不应看到任何进详情的入口 |
| 流通道 | 日志=NDJSON+AbortController；终端=票据 WS；断开必须给「已断开」与「已结束（eof）」两种区分结论（现状口径保留） |
| 自动刷新 | 列表页 hero 提供开关（默认关，开启后 10s 重拉快照、复用 loadState；离开页面/路由失活自动停）；「数据是活的」由 hero 副标题的 live-dot 承担（对齐服务监控先例，修 D-10） |
| 主机记忆 | 跨页（含菜单导航）保持上次选择：route query 优先、会话级兜底、首台垫底（修 D-8，见 §4.3） |
| 可访问性 | 图标按钮一律 aria-label（title 仅作 hover 提示）；弹窗焦点圈定与 Esc 走 EP 默认；「列表→详情→操作→确认」键盘链路纳入 §6 验收 |

## 3. 接口适配方案

### 3.1 现有接口清单（已核实，共 6 个 HTTP 端点）

| 端点 | 用途 | 前端封装 | 适配结论 |
| --- | --- | --- | --- |
| GET `/docker/hosts` | 可管主机清单（含 dockerOk/在线） | fetchDockerHosts | **不改** |
| GET `/docker/hosts/:id/state` | 资源快照（五类资源+陈旧结论+error 字段） | fetchDockerState | 可选增强 A-1 |
| POST `/docker/hosts/:id/cmds` | 受理指令（202+ref；403/409/400/500 错误口径） | sendDockerCmd | **不改**（A-2 可选） |
| GET `/docker/hosts/:id/cmds/:ref` | 轮询结果（status/error/detail/sessionId/alreadyExists/streamTicket/payload） | fetchDockerCmdResult | **不改** |
| GET `/docker/hosts/:id/cmds/:ref/stream` | 日志 Follow（NDJSON 流） | openDockerLogStream（fetch+ReadableStream） | **不改** |
| GET `/docker/hosts/:id/stream/exec?ticket=` | 终端 WS（票据 TTL 30s 单次） | dockerExecStreamUrl | **不改** |

类型唯一事实源是 `Api.Docker.*`（uni_core tools/apigen 生成，`pnpm check:api-types` 校验）。任何 DTO 调整都走「改 uni_protocol/uni_core → 重新生成 → 前端消费」链路，**禁止前端手写平行类型**。

### 3.2 建议调整的接口（最小化、可全部拒绝）

两条均为「渲染收益明确、向后兼容、不动执行架构」的可选增强；是否采纳在实施第 0 阶段定案：

| 编号 | 调整 | 修改点 | 理由（UI 渲染） | 不采纳的替代 |
| --- | --- | --- | --- | --- |
| A-1 | `DockerStateResp.projects[]` 增加服务清单字段（agent 从容器 compose 标签去重收集，快照已有原始数据） | uni_protocol DTO + uni_agent 快照组装 + apigen 重生成；前端删除 servicesByProject 反推与 missingServiceCount | 项目页核心 UI 是「项目→服务→容器」树；反推在服务无运行容器时**结构性缺行**，渲染层无法弥补 | 保留前端反推+缺口提示文案（现状），UI 方案 §2.2.4 已按两案设计 |
| A-2 | POST cmds 的 409 冲突响应体携带在途指令的 ref（现为纯错误信封） | uni_core service/docker_cmd.go 冲突分支加 data 字段 | 409 时 UI 可给「查看该目标的在途操作」入口，把「请稍候」变成可解释的状态 | 保留统一冲突文案（现状） |

**明确不做的接口改动**（防范围蔓延，对照约束「接口修改仅以适配 UI 渲染为目的」）：

- 不改信封/鉴权/错误码结构（全站契约）；
- 不增加服务端分页/排序/过滤参数（快照模型 + 本地过滤是既有架构，容量结论待 U-04 实测后再议）；
- 不新增「读备份正文」「跨主机聚合」「镜像构建/推送」等动作（原 spec §14 不做清单）；
- 不为 D-1~D-7 任何一条改接口——**全部是前端可修复**（见 §4.3），这是本次「接口最小动」的实测依据：state 响应已含 error 字段，D-1 只是前端没用它。

### 3.3 不需要改动的接口与原因

1. **6 端点形状完整覆盖新 UI 的数据需求**：列表四态、统计卡、详情 Tab、日志、终端、确认/保护/权限所需字段在现协议中齐备（含 alreadyExists 两段式、streamTicket、baseHash 乐观锁、backups）。
2. **指令-快照-流三通道模型与新 UI 正交**：新 UI 是渲染层重构，202+ref 轮询、NDJSON、票据 WS 的时序契约（含「成功后双次重拉」）不需要变化。
3. **改端点的成本不对称**：每条 DTO 变更横跨 protocol→core→agent→apigen→前端五处并有回归面；仅 A-1/A-2 的渲染收益配得上这个成本，其余一律用前端方案解决。

## 4. 代码设计方案

### 4.1 目录结构（目标态）

```
src/modules/docker/
├── index.ts               # 路由与清单（不变）
├── api.ts                 # 6 端点封装（不变）
├── styles.scss            # 新增：模块级语义 class + 视觉令牌（数值取自 monitor-tokens，见 §4.4）
├── composables/
│   ├── useDockerHostState.ts   # +错误显式化、清单重拉（D-1/D-6）
│   ├── useDockerCmds.ts        # pendingIds: Set（D-2）
│   └── useDockerReset.ts       # 新增：主机切换统一重置（D-4）
├── utils/                 # 10 个纯逻辑文件（不变；stream.ts +end()）
├── components/
│   ├── docker-page.vue        # +页头统计卡插槽
│   ├── host-switcher.vue / action-menu.vue / action-confirm.vue / log-viewer.vue / pty-terminal.vue
│   ├── state-dot.vue / fact-row.vue / stat-cards.vue   # 新增 3 个展示件
│   └── compose-editor/        # 4 件套（不变）
└── views/
    ├── containers.vue / images.vue / volumes.vue / networks.vue   # 各 <300 行
    ├── projects/
    │   ├── index.vue           # 页面编排 <200 行
    │   ├── project-card.vue / service-row.vue / config-row.vue
    │   └── dialogs.vue         # 4 类确认弹窗的单一实例
    ├── container-detail/
    │   ├── index.vue           # hero+Tab 编排
    │   └── tab-overview.vue / tab-logs.vue / tab-pty.vue / tab-env.vue
    └── image-detail/
        ├── index.vue
        └── tab-layers.vue / tab-meta.vue / tab-containers.vue
```

拆分原则：**页面文件只做编排**（数据上下文+布局+弹窗状态机），可复用交互与展示下沉组件/组合式；不按「每个按钮一个文件」过度切分。拆完后单文件 ≤300 行（现状 4 个文件超 800 行）。

### 4.2 组件职责与新增/拆分清单

| 动作 | 对象 | 说明 |
| --- | --- | --- |
| 新增 | state-dot、fact-row、stat-cards | §2.1 的三个通用展示件；只吃 props，无业务 |
| 新增 | useDockerReset | 把「保筛选/清勾选/关弹窗/断流/清缓存」做成声明式清单，各页传入自己的弹窗 refs |
| 拆分 | projects.vue → 3 行组件 + dialogs.vue | 展开行内容是卡片流；4 个确认弹窗收口为单实例+状态机（kind: project/service/loop/rollback） |
| 拆分 | container-detail.vue → hero + 4 Tab | Tab 独立文件后各自的懒加载/断流生命周期天然隔离 |
| 拆分 | image-detail.vue → hero + 3 Tab | 同上 |
| 增强 | docker-page | hero 化：KPI 磁贴插槽、10s 自动刷新开关（D-10）、hero 副标题 live-dot 实时态；主机条并入 hero（样式对齐 sv-hero 先例） |
| 增强 | action-menu | 支持 per-item disabled+结论（D-7），images.vue 删 rowMenuItems 绕行 |
| 增强 | log-viewer | 窄屏图标态；其余保留 |
| 保留 | compose-editor 全套、pty-terminal | 成熟度最高，仅视觉微调 |

### 4.3 状态与数据流修复（对应 1.5.1 缺陷）

| 缺陷 | 修复设计 |
| --- | --- |
| D-1 | useDockerHostState 增加 `error` ref；catch 分支置位且**不清空既有 state**（保留最后已知数据+错误结论行）；syncText 输入侧把 error 优先于新鲜度计算 |
| D-2 | pendingId: `ref<string>` → `pendingIds: ref<Set<string>>`；run 的 try/finally 各自增删自己的 key；页面判断 `pendingIds.has(key)`；busy 语义不变 |
| D-3 | 容器列表「日志」项补 auth=PermDockerInspect（与写动作同构） |
| D-4 | useDockerReset 统一实现；containers 的 onHostSwitch 删除 `searchForm.value = {}`（恢复「筛选保留」）并接入弹窗复位 |
| D-5 | createLogFeed 增加 `end()`：冲 parser.flush() 残留 + decoder.flush() + eof 关闸；调用方（tab-logs）在流关闭/Abort 后调用；对应纯函数单测先行 |
| D-6 | refresh 选项化 `reloadHosts: true`（节流 10s，避免每次指令成功都拉清单）；离线主机的切换器子文案由清单+快照双源判断 |
| D-7 | action-menu 的 items 增加可选 disabled/disabledReason，透传到 ElDropdownItem |
| D-8 | hostId 解析改三级兜底：`route.query.host → 会话级上次选择（sessionStorage，selectHost/reload 落定时写入）→ hosts[0]`。菜单导航进新页无 query 时不再掉回第一台；query 仍是首要事实源（深链/分享/刷新还原不受影响），会话值只作缺 query 时的兜底 |
| D-9 | projects 页补 keyword（项目名/服务名）+ 状态（运行/受阻/全部）筛选，ArtSearchBar + computed 本地过滤，与其它列表页同构 |
| D-10 | docker-page 内置 10s 自动刷新（默认关）：interval 调 loadState，路由离开/组件失活自动清除；开关态写 route query（`auto=1`）随 URL 还原 |

**必须保留的既有竞态防线**（新实现不得退化）：loadState 的 seq 守卫；run 入口固定 hostId；批量栏 busy 由 inflight 计数派生；Compose 编辑器写后以 agent 回读刷新基线；`provideDockerHost()` 返回值显式传给两个 composable（同组件 provide/inject 陷阱）。

### 4.4 样式与主题集成

- 命名维持 BEM（`imd-`、`fm-` 等现有前缀风格），新组件按容器名前缀；不引入 Tailwind 原子类与 BEM 混写在同一组件的同一区块（现状允许工具类做布局、BEM 做皮肤，延续该分工）。
- 断点一律 `respond-below(breakpoints.scss)` 与 `hideBelow`（ArtTable 列）；禁任意值断点。
- 设计令牌策略：`modules/docker/styles.scss` 承载 §2.1 的容器/KPI/圆点令牌，数值取自 `system-monitor/views/monitor-tokens.scss` 并注明出处——与 `device-tokens.scss` 同一「复制数值、不跨模块 @use」取舍（跨模块 @use 会把两个业务模块编译期绑死，这是设备模块已论证过的边界）。**如实记录代价**：这将是数值的第三处副本（monitor 内部已漂移：cache 22px/44px vs 其余 20px/40px；device 是第二份副本）。消除副本的正解是把令牌提升到 `@styles/` 层做单一事实源并让 monitor/device 迁移——那是一次跨模块重构，**超出本次 Docker 重构范围**，作为独立后续事项另行确认后实施（收益：一次修掉 cache 漂移 + 三份副本归一）。
- 新增 4 个模块级语义 class 放 `modules/docker/styles.scss`：`.docker-state-dot`（四色）、`.docker-protected`（锁）、`.docker-mono`、`.docker-danger-zone`（批量删除栏）——颜色全部引用 EP 变量。
- 明暗模式验收纳入 §6（每页两模式截图比对）。

### 4.5 测试策略

- **保留全部 15 个现有测试文件与 268 用例**（含 phase-gate、action 注册表计数 4+21+1+3=29、single-root、视口列集合守卫——这些是重构的护栏，改任何注册表/结构都必须让它们先绿）。
- 新增用例（按 §4.3 一一对应）：pendingIds 并发（A 完成清 A 行）、快照错误态文案（error 优先）、主机切换重置矩阵（筛选保留+弹窗关闭）、logFeed.end() 残留行冲刷、action-menu 单条禁用、拆分后 projects/container-detail 的渲染冒烟、主机记忆三级兜底（D-8）、自动刷新启停与离开清理（D-10）、项目页筛选（D-9）。
- 回归方式：每阶段跑 `pnpm --dir uni_console test src/modules/docker/__tests__` + `vue-tsc` + `eslint`（与 §1.3 同命令口径）。

## 5. 实施步骤

每阶段以「进入/退出条件」推进，不含工期与人力（U-07 待补充）；阶段间可按页并行，但 §4.3 的缺陷修复（P0）必须先行——它们是后续渲染的公共地基。

| 阶段 | 内容 | 进入条件 | 退出条件 |
| --- | --- | --- | --- |
| P0 地基 | §4.3 D-1~D-7 修复 + action-menu 单条禁用 + state-dot/fact-row/stat-cards 三件 + useDockerReset | 基线 268 用例绿（§1.3）；A-1/A-2 接口决策定案 | 新增缺陷修复用例全绿；现有用例零回退；五个列表页视觉未动但交互缺陷消失 |
| P1 列表页 | containers/images/volumes/networks 四页套新骨架（统计卡+页头操作区+StateDot+行菜单统一） | P0 退出 | 四页在 phone/tablet/desktop/wide 四档断点+明暗双模式走查通过；images 删除绕行代码 |
| P2 详情页 | container-detail/image-detail 拆分 hero+Tab 子组件；日志流 end() 接入；Follow 断流重连 | P0 退出（可与 P1 并行） | 两页四 Tab 全状态（加载/空/错误/离线）渲染正确；日志 eof 与断开结论区分可复现 |
| P3 项目页与编辑器 | projects 拆 project-card/service-row/config-row/dialogs；卡片流改版；编辑器视觉微调（逻辑不动） | P1/P2 退出 | 1127 行拆至编排 <200 行+子组件 ≤300 行；4 类确认弹窗收口为单实例；备份/回滚/应用全链路手测通过 |
| P4 收口 | 样式语义 class 收敛、明暗模式截图比对、全量回归、文档与验收表更新 | P1~P3 全退出 | §6 验收清单全项勾选 |

回滚策略：P0~P4 均为前端模块内改动（仅 A-1/A-2 例外，若采纳则后端先行独立合入）；任一阶段失败可整阶段 revert，不影响其他模块。写操作验证须在 U-06 授权的测试主机进行。

## 6. 验收标准

### 6.1 覆盖完整性（对照 §1.4 清单逐项勾选）

- [ ] 7 个路由页面全部按新骨架渲染；5 列表页 + 2 详情页 + 4 详情 Tab + 项目三层结构 + 4 类确认弹窗 + 批量操作 + 编辑器双模式 + 备份历史 + 日志（一次性/Follow）+ 终端逐项在验收表中有一条走查记录
- [ ] 29 个动作（4 只读+21 写+exec+3 compose）在注册表驱动下全部可从 UI 触发且走统一确认/保护/权限链路
- [ ] 六档权限 × 关键页面交叉走查：无权限时对应入口不渲染（不是禁用）；受保护目标显示锁与结论

### 6.2 交互与状态验收

- [ ] D-1~D-10 各有对应复现步骤与修复后行为的记录（快照失败显示错误结论而非「刚刚同步」等）
- [ ] 列表四态、指令反馈链（受理→pending→结论→双次重拉）、主机切换统一纪律在每页行为一致
- [ ] D-8/D-9/D-10 专项：菜单切换五个列表页主机保持不丢；项目页 keyword/状态过滤生效；自动刷新开关启停正确、离开页面自动停且无泄漏 interval
- [ ] 可访问性走查：图标按钮均有 aria-label；键盘可完成「列表→详情→行操作→强确认提交」全链路；正文与状态色对比度抽查 ≥4.5:1（明暗两模式）；kpi-sub 若沿用 11px 须作为 caption 豁免记录（或提至 12px）
- [ ] `:key="route.path"` 重建、单根约束、_raw 保注释、纯文本日志渲染四条设计约束的护栏测试仍然在库且绿

### 6.3 代码质量与回归

- [ ] 全量 Docker 测试（原 268 + 新增）+ vue-tsc + eslint 全绿，命令与 §1.3 一致
- [ ] 单文件 ≤300 行目标达成（compose utils 纯函数除外）；无页内重复的确认/菜单/重置逻辑
- [ ] 新样式零写死色值；断点全部来自单一事实源；视觉令牌数值与 monitor-tokens 一致且注明出处（第三副本与提升方案见 §4.4）；`pnpm check:api-types` 通过
- [ ] 手工走查：4 断点 × 2 明暗模式 × 7 页截图存档（U-05 样本库）

### 6.4 交付物

- [ ] 重构代码（P0~P4 阶段合入）+ 本文档随实施更新（标注「已实施/已偏离+原因」）
- [ ] 验收走查记录（含权限矩阵、四态截图、缺陷修复验证）
- [ ] (待补充：视觉签收人确认记录、性能与容量实测数据——后者依赖 U-04)

---

**文档自检**：重构范围与 §0.2 一致（Docker 管理 UI 全量，未扩未缩）；接口改动仅 §3.2 两条可选增强且给出不采纳替代；§1.5 缺陷 D-1~D-10 全部附文件:行号且经源码复核（D-8~D-10 来自 2026-09-29 的 UX/功能完整性/操作便捷度专项评审，规则基线含 ui-ux-pro-max 准则与仓库既有先例双对照）；不可核实项均已按 `(待补充：…)` 标注（U-01~U-07 及散布条目）。

