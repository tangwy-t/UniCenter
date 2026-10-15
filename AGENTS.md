# AGENTS.md — UniCenter 项目口径

本文件是仓库级工作基线（2026-10-15 与用户逐条确认后落笔）。改动本文件需用户明示；
若与 `docs/superpowers/specs/`（设计规格）或 `docs/superpowers/plans/`（阶段计划）更新的内容冲突，以更新的规格为准并回报。

## 0. 速览

- 模块：`uni_core`(Go 后端) · `uni_console`(Vue3+TS, pnpm) · `uni_agent`(Go 宿主端) · `uni_protocol`(零第三方依赖)
- 规范实体：`docs/superpowers/specs/`、`docs/superpowers/plans/`、`DOCKER_UI_REDESIGN.md`、`RELEASING.md`
- 门禁：`make test` / `make lint` / `make contract` / `make release-precheck`；前端 `build` 含 `vue-tsc`；另有 `check:icons`、`check:api-types`
- 沟通语言：中文

## 1. 设计与 UI 口径

1. **锚定先例**：提视觉/架构方案前先在仓库找同款实现，把数值、文件、行号作为出处写进方案，不给"建议值"。视觉主线是 `system-monitor` → `device`(token 源)；发现复制漂移要如实记录并给收敛路径，不静默采纳漂移值。
2. **页面优于抽屉**：展示面一律整页路由，不用 `ElDrawer`（多分辨率适配差）；瞬时交互（确认弹窗、进度对话框）仍可弹窗。不得新增抽屉做展示面。
3. **不做全站默认限高**：表格默认随内容增高 + 页面滚动；限高只按页显式收紧；**抽屉/弹窗/嵌入式表必须限高**（否则内容被 flex 裁切且无滚可达，属真 bug）。
   - 元规范：全站默认型 UI 参数（高度阀等）风险大 → 默认贴近内容、逐页显式定夺。
4. **页面零解释文案**：页面只展示硬性数据/指标/状态。
   - 必删：页面定位说明、功能去向说明（"X 在 Y 里"）、常驻口径解释句。
   - 报裁（删前问用户）：防误读口径（如"合计≠镜像大小"）、空态引导句。
   - 保留：数据标签/数值/状态、表单 placeholder、字段内即时校验错误。

## 2. 工程口径

5. **开发阶段零兼容**：重构不做兼容层——旧路由/旧菜单/旧组件/旧接口直接删；种子按"初始化即最终态"构造，不叠增删链；站内链接直接改指新地址。
   - 边界：这条针对重构对象；**≠ 可以重置生产库**（见 §4.14）。
6. **改动强度**：忽略工作量、彻底解决而非最小修改（能根除的保守边界要根除，兜底分支 fail-fast 删除而非对齐默认值）。
   - 边界：彻底 = 根除同类问题，**不是无限制扩大改动面**；确实不可区分的极端边角，须先给"不可区分性证明"才允许保留。

## 3. 协作与审核口径

7. **分工**：重活（全轨重建/协议扩展/裁决密集）并行派发子 agent；小活（单点修/清扫/文案）自己做，不为分工而派发。
8. **审核三步（不信报告只信 diff）**：范围核对（`git status` 对声明）→ 载荷论断源码抽查（每份报告挑 1-3 条最重的亲自验证）→ 亲自复跑测试（`-count=1` 防"缓存绿"）。
9. **终审全量电池，永不免跑**：`make test`（uni_core + uni_protocol + uni_agent + uni_console 全量）+ 前端 `build`(vue-tsc) + `check:icons` + `check:api-types` + `make contract`。模块级电池不算终审（曾漏全局断点守卫潜伏 4 天）。
10. **教训 → 守卫闭环**：终审坐实的每个 P0/静默类缺陷，当波内必须落一条守卫测试，禁止"修完即散"。
11. **并发纪律（WSL 硬约束）**：跨边界（`/mnt/c`、敲命令）子进程并发 ≤2-3，重活期写明"串行、勿并发堆进程"，看 load average 定并发。否则 `/init` 卡 vsock 成 D 态 → netlink ENOBUFS → 全部跨边界操作阻塞。

## 4. 部署与 git 口径

12. **改完直接部署**：代码改动完成后即走部署链路，不必询问——开发机 `make docker-build`（注入 VERSION）→ `docker save | gzip` → `scp` 到 `/data/UniCenter/images/` → 远端 `bash scripts/deploy.sh`（load + compose up + 健康自检）。
13. **部署 ≠ commit**：git 提交只在用户明示时打。
14. **破坏性操作先报**：DB 重置/删数据/改全站布局根口径（如 `hooks/core/useLayoutHeight.ts` 的 `--art-full-height`）/动共享机上其他项目 → 先问用户。
15. **部署自证**：部署后必须核验——容器镜像 ID == 新加载的 `:latest`；运行镜像里含本次新增文案/标识（对比回滚镜像里没有）；受改动影响的接口实测。
16. **契约同包**：改动涉及后端响应字段等契约时，console 与 core **必须同包一起部署**，否则前端按新契约取字段会拿到 undefined。
17. **回滚余量**：部署前给现役镜像打 `rollback-<日期>-<事由>` tag；把 `images/` 下旧包移入 `images/old/`（否则 `deploy.sh` 的 `images/*.tar.gz` 通配会按字母序后加载旧包、覆盖新包）。
18. **凭据**：生产凭据不入库、不写入本文件，只记位置——服务器 `/data/UniCenter/.credentials`、`/data/UniCenter/.env`、本地仓库根 `.env`（均已 gitignore，600）。
    - 拓扑：`192.168.12.105`(bogon, 共享机) = console `http://192.168.12.105:20080` + agent TLS `wss://…:20443` + core(仅容器网内)；`192.168.12.106`(wmind) = 仅 agent `/home/UniCenter`。

## 5. 不得删的守卫（防回归资产）

- `uni_console/src/config/breakpoints.test.ts` — 全局断点守卫
- `uni_console/src/test/agent-channel-proxy-guard.test.ts` — 20443 通道 XFF 转发
- `uni_console/src/test/event-clock-guard.test.ts` — 事件时钟
- `make contract`（协议形状漂移/单源/golden/零依赖）与 `uni_protocol-payload-guard`（离线载荷新鲜度）
- 测试基线：前端 ~102 文件 / 1800+ 用例全绿；后端 go 全包绿。

## 6. 环境事实

- WSL2 以 root 运行，用户不切用户；工具链在 `/root`；前端包管理器固定 `pnpm`。
- 本项目历史口径（含 2026-09/10 三轮 Docker 模块审计与修复的详细背景）另存于 Claude 的 memory 目录，本文件只保留仍然适用且用户确认的条目；快照类状态（commit/部署进度）一律以仓库与服务器实测为准，不照抄旧记忆。
