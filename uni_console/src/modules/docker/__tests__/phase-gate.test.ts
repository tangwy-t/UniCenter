/**
 * 守卫：docker 模块的 action 字面量白名单 + 各分类接线入口（**分类口径**）。
 *
 * 白名单 = **前端已接线**的动作全集，按语义分类收在六处（互为分类事实，不是发布期次）：
 *   - 只读轮询 4 条（utils/cmd.ts 的 PHASE1_ACTIONS —— 名字是历史遗留，内容是分类事实）
 *   - 写动作 25 条（utils/actions.ts 的注册表）
 *   - 终端 1 条（container:exec，字面量只在 pty-terminal 组件里）
 *   - 配置编辑 3 条（compose.file:write/validate/patch，清单本体在 utils/compose.ts）
 *   - 统计流 1 条（container:stats，字面量只在 container-stats 组件里）
 *   - 聚合日志 1 条（compose:logs，收在 utils/cmd.ts 的 COMPOSE_LOGS_ACTIONS —— 会话制
 *     只读，不进只读 runRead 清单，也不进写动作注册表）
 *   合计已接线 35 条；协议全集（`uni_protocol/docker.go` 的 dockerActionSpecs，
 *   六前缀口径、docker:events 不在扫描口径内）35 条 —— P2 收编 image:build /
 *   image:push、P3 收编 image:scan 后**两层相等**（PENDING_WIRING 已清空：协议
 *   再有新动作而前端没跟，差额会重新出现在红灯里）。
 *   本守卫**没有「后期动作」清单**：模块源码里出现的 action 字面量必须全部落在
 *   已接线 35 条内（新增动作而忘了同步，就会以「不在白名单」红灯）。
 *   - 各分类清单互补的条数断言保留（清单少一条、注册表多一条都不行）。
 *   - 配置编辑入口的存在断言：「入口存在且受 docker:config 权限门控」（不渲染 ≠
 *     禁用；没有权限的人连按钮都不该看到 —— spec §11.0 控件矩阵）。
 *
 * ── 扫描口径 ────────────────────────────────────────────────────────
 * 只扫 action 的六种前缀（container/image/volume/network/compose/compose.service/
 * compose.file）；不扫 `docker:` —— 那是权限码（docker:config 等），不是动作名。
 * 注释与字符串里的字面量同样计入（action 名不该以任何形态出现在别的语义里）。
 *
 * ── 为什么要跳过隐藏目录 ──────────────────────────────────────────
 * 模块目录里可能落下工具产物（例如安全扫描 hook 的 `.mimosa/`，它已在 .gitignore
 * 但确实存在于磁盘上）。遍历时连它们一起读毫无意义，还会让守卫的失败信息被噪音
 * 淹没。以 `.` 开头的目录一律跳过 —— 源码目录不存在合法的「点开头」子目录。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { COMPOSE_LOGS_ACTIONS, PHASE1_ACTIONS } from '../utils/cmd'
import { DOCKER_ACTION_REGISTRY } from '../utils/actions'

const ROOT = new URL('..', import.meta.url).pathname

/**
 * 配置编辑动作（协议全集减去各分类后的**恰 3 条** compose.file 写路径）。
 *
 * 跨语言没法互相 import，这份清单必须自己与协议对齐（差一条就等于少守一个动作）：
 * `uni_protocol/docker.go` 的 `AllDockerActions()`；清单本体在 utils/compose.ts 的
 * `COMPOSE_ACTIONS`。以后往 `dockerActionSpecs` 里加动作时，要同步往这里加一条。
 */
const CONFIG_EDIT_ACTIONS = ['compose.file:write', 'compose.file:validate', 'compose.file:patch']

/** 终端动作（会话制交互；单列是为了条数互补与「确实接线了」的正向断言）。 */
const INTERACTIVE_ACTIONS = ['container:exec']

/** 统计流动作（会话制只读监控面）。与终端同款存在方式：动作字面量只出现在
 *  组件里（container-stats.vue），不进 utils/cmd.ts 的只读清单 —— 那份清单服务
 *  runRead 轮询闭环，而 stats 是会话制流，不走那个闭环。 */
const STATS_STREAM_ACTIONS = ['container:stats']

/**
 * 聚合日志动作（compose:logs）：**不从本文件再抄一份** —— 它收在 utils/cmd.ts 的
 * COMPOSE_LOGS_ACTIONS（与统计流同样不进 runRead 清单，但清单本体在源码侧，
 * phase-gate 与 actions 的互补断言都从那里 import，漂移只剩协议一处）。
 */

/** 协议全集的总条数（六前缀口径；docker:events 不在扫描口径内，见文件头）。 */
const PROTOCOL_ACTION_COUNT = 35

/** 前端已接线的总条数（各分类清单合计；见文件头的分解）。 */
const WIRED_ACTION_COUNT = 35

/**
 * 协议有、前端尚未接线（P3 收编 image:scan 后为空）。
 * 常量保留而不是删掉：两层口径的「全集 = 已接线 + 待接线」等式仍然成立（为空
 * 即「收编完成」），协议下次加动作而前端没跟时差额在这里重新出现。
 */
const PENDING_WIRING: string[] = []

/** 白名单全集（模块里允许出现的 action 字面量只能来自它 = 前端已接线全集）。 */
const ACTION_WHITELIST: readonly string[] = [
  ...PHASE1_ACTIONS,
  ...DOCKER_ACTION_REGISTRY.map((e) => e.action),
  ...INTERACTIVE_ACTIONS,
  ...CONFIG_EDIT_ACTIONS,
  ...STATS_STREAM_ACTIONS,
  ...COMPOSE_LOGS_ACTIONS
]

/**
 * action 字面量形态：六种前缀之一 + `:` + 小写连字符名。
 * `compose.service:`/`compose.file:` 必须在 `compose:` 之前尝试（正则交替是有序的）。
 */
const ACTION_LITERAL_RE =
  /\b(?:container|image|volume|network|compose\.service|compose\.file|compose):[a-z][a-z-]*\b/g

function actionLiteralsIn(src: string): string[] {
  return src.match(ACTION_LITERAL_RE) ?? []
}

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === '__tests__') continue // 守卫文件自身会引用这些字符串
      if (name.startsWith('.')) continue // 工具产物目录（如 .mimosa）：不是源码
      walk(p, out)
    } else if (/\.(vue|ts)$/.test(name)) {
      out.push(p)
    }
  }
  return out
}

describe('分类控件矩阵：action 字面量全在白名单内', () => {
  it('扫描不是空转：确实扫到了模块源码与全部配置编辑组件', () => {
    const files = walk(ROOT)
    expect(files.length).toBeGreaterThan(0)
    expect(files.some((f) => f.endsWith('utils/cmd.ts'))).toBe(true)
    expect(files.some((f) => f.endsWith('utils/actions.ts'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/pty-terminal.vue'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/compose-editor/compose-editor.vue'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/compose-editor/form-mode.vue'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/compose-editor/yaml-mode.vue'))).toBe(true)
  })

  it('模块源码里出现的每个 action 字面量都在已接线 35 条白名单内', () => {
    const whitelist = new Set(ACTION_WHITELIST)
    const seen = new Set<string>()
    for (const file of walk(ROOT)) {
      const src = readFileSync(file, 'utf8')
      for (const literal of actionLiteralsIn(src)) {
        seen.add(literal)
        expect(
          whitelist.has(literal),
          `${file} 出现了白名单之外的 action 字面量「${literal}」（已接线 35 条的名单见本文件头）`
        ).toBe(true)
      }
    }
    // 反向自检：扫描确实命中了配置编辑、终端、统计流、创建面、聚合日志、P2
    // 分发闭环与 P3 安全面的关键动作（不然这个守卫可能空转）。
    for (const must of [
      'compose.file:read',
      'compose.file:validate',
      'compose.file:write',
      'compose.file:patch',
      'container:exec',
      'container:stats',
      'container:create',
      'compose:up',
      'compose:logs',
      'image:build',
      'image:push',
      'image:scan'
    ]) {
      expect(seen.has(must), `扫描没有命中 ${must}（守空转）`).toBe(true)
    }
  })

  it('只读动作清单恰好四个（runRead 轮询闭环的全部入口）', () => {
    expect([...PHASE1_ACTIONS]).toHaveLength(4)
  })

  it('六份清单互补：已接线 35 = 4 只读 + 25 写 + 1 终端 + 3 配置编辑 + 1 统计流 + 1 聚合日志，无重复、无交集', () => {
    // 防的是「从清单或注册表里删掉一条」这种静默失守：条数不对就红灯。
    const writes = DOCKER_ACTION_REGISTRY.map((e) => e.action)
    expect(DOCKER_ACTION_REGISTRY).toHaveLength(25)
    expect(new Set(CONFIG_EDIT_ACTIONS).size).toBe(CONFIG_EDIT_ACTIONS.length)
    expect(new Set(INTERACTIVE_ACTIONS).size).toBe(INTERACTIVE_ACTIONS.length)
    expect(new Set(STATS_STREAM_ACTIONS).size).toBe(STATS_STREAM_ACTIONS.length)
    expect(new Set(PHASE1_ACTIONS).size).toBe(PHASE1_ACTIONS.length)
    expect(new Set(COMPOSE_LOGS_ACTIONS).size).toBe(COMPOSE_LOGS_ACTIONS.length)
    expect(new Set(writes).size).toBe(writes.length)
    expect(
      COMPOSE_LOGS_ACTIONS.length +
        STATS_STREAM_ACTIONS.length +
        CONFIG_EDIT_ACTIONS.length +
        INTERACTIVE_ACTIONS.length +
        PHASE1_ACTIONS.length +
        writes.length
    ).toBe(WIRED_ACTION_COUNT)
    const all = [
      ...PHASE1_ACTIONS,
      ...writes,
      ...INTERACTIVE_ACTIONS,
      ...CONFIG_EDIT_ACTIONS,
      ...STATS_STREAM_ACTIONS,
      ...COMPOSE_LOGS_ACTIONS
    ]
    expect(new Set(all).size).toBe(WIRED_ACTION_COUNT)
    for (const a of [...INTERACTIVE_ACTIONS, ...CONFIG_EDIT_ACTIONS, ...STATS_STREAM_ACTIONS]) {
      expect(writes, `非写动作 ${a} 不该进写动作注册表`).not.toContain(a)
    }
    for (const a of [...PHASE1_ACTIONS, ...STATS_STREAM_ACTIONS, ...COMPOSE_LOGS_ACTIONS]) {
      expect(CONFIG_EDIT_ACTIONS, `${a} 不该出现在配置编辑清单里`).not.toContain(a)
      expect(writes, `${a} 不该进写动作注册表`).not.toContain(a)
    }
  })

  it('两层口径：协议全集 35 = 已接线 35 + 待接线 0（image:build / image:push / image:scan 已收编）', () => {
    // 待接线动作不进白名单（模块源码出现它们 = 提前接线而忘了收编清单，红灯）；
    // PENDING_WIRING 为空的现在，这条等式断的是「收编完成」—— 协议再加动作而
    // 前端没跟，差额会在这里重新变红。
    const whitelist = new Set(ACTION_WHITELIST)
    for (const a of PENDING_WIRING) {
      expect(whitelist.has(a), `${a} 尚未接线，不该进白名单`).toBe(false)
    }
    expect(ACTION_WHITELIST).toHaveLength(WIRED_ACTION_COUNT)
    expect(WIRED_ACTION_COUNT + PENDING_WIRING.length).toBe(PROTOCOL_ACTION_COUNT)
  })

  it('终端控件已接线：终端引入 xterm、日志查看器有跟随开关', () => {
    const files = walk(ROOT)
    const terminal = files.find((f) => f.endsWith('components/pty-terminal.vue'))
    expect(terminal).toBeTruthy()
    const terminalSrc = readFileSync(terminal as string, 'utf8')
    expect(terminalSrc.includes('@xterm/xterm')).toBe(true)
    expect(terminalSrc.includes('container:exec')).toBe(true)
    const viewerSrc = readFileSync(join(ROOT, 'components/log-viewer.vue'), 'utf8')
    expect(viewerSrc.includes('followable')).toBe(true)
    expect(viewerSrc.includes('following')).toBe(true)
  })

  it('配置编辑入口存在且受 docker:config 权限门控（渲染 + 组件两层）', () => {
    // 7b 起编辑入口的**唯一**宿主是工作台配置区（projects 列表页薄化为索引，不再
    // 挂编辑器）：页面层改为断言 project-config，权限门与组件挂载两层不变。
    const configSrc = readFileSync(
      join(ROOT, 'components/project-workspace/project-config.vue'),
      'utf8'
    )
    expect(configSrc).toContain('PermDockerConfig')
    expect(
      /canConfig\s*=\s*computed\(\s*\(\)\s*=>\s*hasAuth\(PermDockerConfig\)\s*\)/.test(configSrc)
    ).toBe(true)
    expect(configSrc).toContain('v-if="canConfig"')
    expect(configSrc).toContain('ComposeEditor')
    expect(configSrc).toContain('＋添加服务')
    // 组件层：保存/回滚按钮同样只在有配置编辑权限时渲染。
    const editorSrc = readFileSync(
      join(ROOT, 'components/compose-editor/compose-editor.vue'),
      'utf8'
    )
    expect(editorSrc).toContain('PermDockerConfig')
    expect(editorSrc).toContain('v-if="canConfig"')
  })

  it('监控面已接线：详情页概览 Tab 挂 stats 组件，组件发 container:stats 并接流', () => {
    // 页面层：概览 Tab 挂 ContainerStats（8a 页面化 —— 宿主从被删的 workload-drawer
    // 换成 views/container-detail/index.vue；v-if 懒挂载纪律与终端 Tab 同款）。
    const detailSrc = readFileSync(join(ROOT, 'views/container-detail/index.vue'), 'utf8')
    expect(detailSrc).toContain('ContainerStats')
    expect(detailSrc).toContain("activeTab === 'overview'")
    // 组件层：走指令通道发 container:stats，并接 NDJSON 统计流。
    const statsSrc = readFileSync(join(ROOT, 'components/container-stats.vue'), 'utf8')
    expect(statsSrc).toContain("action: 'container:stats'")
    expect(statsSrc).toContain('openDockerStatsStream')
    expect(statsSrc).toContain('createStatsFeed')
  })

  it('创建面（4a/8b）已接线：两个入口受 docker:manage 门控，创建页发 container:create', () => {
    // 容器页入口：hero 的创建钮 v-if canManage（hasAuth(PermDockerManage)），
    // 8b 起是 router.push 到创建页（不是挂抽屉）；布局族统一后收进 ArtButtonTable
    // 家族（type=add 图标钮，title 即入口语义 —— 异族混排的文本钮已删）。
    const containersSrc = readFileSync(join(ROOT, 'views/containers.vue'), 'utf8')
    expect(containersSrc).toContain('/docker/containers/create')
    expect(containersSrc).toContain('PermDockerManage')
    expect(containersSrc).toMatch(
      /canManage\s*=\s*computed\(\s*\(\)\s*=>\s*hasAuth\(PermDockerManage\)\s*\)/
    )
    expect(containersSrc).toContain('v-if="canManage"')
    expect(containersSrc).toContain('title="创建容器"')
    expect(containersSrc).toContain('type="add"')
    // 镜像详情入口：同一权限档，预填走 query（主机 + 本镜像引用），8b 起也是整页路由。
    const imageDetailSrc = readFileSync(join(ROOT, 'views/image-detail.vue'), 'utf8')
    expect(imageDetailSrc).toContain('用此镜像创建…')
    expect(imageDetailSrc).toContain('/docker/containers/create')
    expect(imageDetailSrc).toContain('image: actionTarget.value')
    // 页面层：创建页走指令通道发 container:create（注册表标准档确认在 action-confirm
    // 之间）、读 query 预填、运行预览由 buildRunPreview 生成（与被删抽屉同一套纯函数）。
    const createSrc = readFileSync(join(ROOT, 'views/container-create.vue'), 'utf8')
    expect(createSrc).toContain("action: 'container:create'")
    expect(createSrc).toContain('DockerActionConfirm')
    expect(createSrc).toContain('buildRunPreview')
    expect(createSrc).toContain('route.query.image')
  })

  it('任务中心（8c）已接线：容器页 hero 与总览 hero 两个入口都指向 /docker/tasks，页面自带 5 秒轮询', () => {
    // 入口层：容器页 hero（原 docker-page 主机条上的入口随该组件删除后移栽）与
    // 总览 hero 都 push 这个 path —— 8c 前它是就地抽屉（没有 URL），页面化后入口
    // 是两个 hero 的图标钮（不挂侧边栏菜单）。
    const containersSrc = readFileSync(join(ROOT, 'views/containers.vue'), 'utf8')
    expect(containersSrc).toContain("'/docker/tasks'")
    expect(containersSrc).toContain('PermDockerList')
    const overviewSrc = readFileSync(join(ROOT, 'views/overview.vue'), 'utf8')
    expect(overviewSrc).toContain("'/docker/tasks'")
    // 页面层：列表读 GET /docker/tasks、可见期间 5 秒轮询、进度三族的内联观看复用
    // 同一个进度组件（被删 task-center-drawer 的三块内容原样平移；观看与执行解耦后
    // 展开区还承载「取消」这个独立动作 —— 组件名随之从 pull 专用改为族通用）。
    const tasksSrc = readFileSync(join(ROOT, 'views/tasks.vue'), 'utf8')
    expect(tasksSrc).toContain('fetchDockerTasks')
    expect(tasksSrc).toContain('POLL_MS = 5000')
    expect(tasksSrc).toContain('TaskProgress')
  })

  it('P2 分发面已接线：构建入口在镜像 tab 底栏（manage 门控），推送入口在镜像详情页头（manage 门控、预填镜像）', () => {
    // 镜像 tab 层：底栏「构建镜像…」只对 docker:manage 渲染，挂构建进度对话框；
    // 主机切换把它与拉取对话框一起关掉（一场构建属于受理它的那台主机）。
    const tabSrc = readFileSync(join(ROOT, 'components/resources/images-tab.vue'), 'utf8')
    expect(tabSrc).toContain('BuildProgressDialog')
    expect(tabSrc).toContain('构建镜像…')
    expect(tabSrc).toMatch(/v-if="canManage"/)
    // 对话框的主机是**受理时锁定的那台**（targetHostId）：跨主机表没有「当前主机」，
    // 一场构建属于发起它的机器，不随列表筛选漂移。
    expect(tabSrc).toContain(':host-id="targetHostId"')
    // 详情页层：头部「推送到仓库…」同一权限档，预填 actionTarget（仓库标签优先）、
    // 镜像清单来自本页快照（选择项数据源已在手，不另拉）；**不传 refresh** ——
    // 推送不改本地任何事实（推的是副本），重拉无物可读（与拉取/构建的口径差）。
    const detailSrc = readFileSync(join(ROOT, 'views/image-detail.vue'), 'utf8')
    expect(detailSrc).toContain('PushProgressDialog')
    expect(detailSrc).toContain('推送到仓库…')
    expect(detailSrc).toContain(':initial-target="actionTarget"')
    const pushBlock = detailSrc.match(/<PushProgressDialog[\s\S]*?\/>/)?.[0] ?? ''
    expect(pushBlock, '详情页的推送对话框接线应存在（自闭合标签块）').not.toBe('')
    expect(pushBlock, '推送对话框不该接 refresh（推送不改本地清单）').not.toContain('refresh')
    // 组件层：构建对话框发 image:build（无 target，主参数在 options）、接构建流
    //（build 端点）并按播报表折叠；推送对话框发 image:push（target + 可选 registry）、
    // 接推送流（push 端点）复用层表折叠。
    const buildSrc = readFileSync(join(ROOT, 'components/build-progress-dialog.vue'), 'utf8')
    expect(buildSrc).toContain("action: 'image:build'")
    expect(buildSrc).toContain('openDockerBuildStream')
    expect(buildSrc).toContain('createBuildFeed')
    const pushSrc = readFileSync(join(ROOT, 'components/push-progress-dialog.vue'), 'utf8')
    expect(pushSrc).toContain("action: 'image:push'")
    expect(pushSrc).toContain('openDockerPushStream')
    expect(pushSrc).toContain('createPushFeed')
  })

  it('P3 安全面已接线：详情页「安全」Tab 挂扫描面板（面板发 image:scan 并解析报告），镜像 tab 行内可触发', () => {
    // 详情页层：第四个 Tab（安全）挂 ImageScanPanel，出入两份事实（主机 + 目标引用，
    // target 与页面写操作的 actionTarget 同源 —— 仓库标签优先、无标签退回镜像 id）。
    const detailSrc = readFileSync(join(ROOT, 'views/image-detail.vue'), 'utf8')
    expect(detailSrc).toContain('ImageScanPanel')
    expect(detailSrc).toContain('label="安全"')
    expect(detailSrc).toContain(':target="actionTarget"')
    // 组件层：走指令通道发 image:scan（manage 档、无确认），报告经 utils/scan 的
    // 本地类型镜像解析（payload 通道在生成类型里是 unknown —— 同 image:inspect 先例）。
    const panelSrc = readFileSync(join(ROOT, 'components/image-scan-panel.vue'), 'utf8')
    expect(panelSrc).toContain("action: 'image:scan'")
    expect(panelSrc).toContain('parseDockerScanReport')
    // 列表层：行 ⋯ 菜单的 actions 含扫描条目（注册表驱动）—— 行内就近触发，
    // 分钟级的进行态由任务中心呈现（只受理不轮询，见组件内 onScanImage 注释）。
    const tabSrc = readFileSync(join(ROOT, 'components/resources/images-tab.vue'), 'utf8')
    expect(tabSrc).toContain("'image:scan'")
    expect(tabSrc).toContain('onScanImage')
  })

  it('P3 构建上下文上传已接线：构建对话框以上传形态消费 ② 端点（进度经 http 层的 XHR 通道）', () => {
    // 对话框层：上下文字段有「上传文件 / 主机已有文件」两种形态，选择后走
    // uploadDockerBuildContext（api.ts 的封装：octet-stream 流式 + onUploadProgress）。
    const buildSrc = readFileSync(join(ROOT, 'components/build-progress-dialog.vue'), 'utf8')
    expect(buildSrc).toContain('uploadDockerBuildContext')
    expect(buildSrc).toContain('isGzipFile')
    expect(buildSrc).toContain('MAX_BUILD_CONTEXT_BYTES')
    // api 层：上传端点带进度回调与显式超时/类型头（为什么不用 fetch：无上传进度事件）。
    const apiSrc = readFileSync(join(ROOT, 'api.ts'), 'utf8')
    expect(apiSrc).toContain('uploadDockerBuildContext')
    expect(apiSrc).toContain('onUploadProgress')
  })

  it('5b 工作台已接线：四个分区组件挂进页面，聚合日志发 compose:logs 并复用日志流端点', () => {
    // 页面层：hero/网元卡/聚合日志/配置四个分区组件都在编排页上。
    const workspaceSrc = readFileSync(join(ROOT, 'views/project-workspace.vue'), 'utf8')
    for (const part of ['ProjectHero', 'ProjectServices', 'ProjectLogs', 'ProjectConfig']) {
      expect(workspaceSrc, `工作台编排页缺分区组件 ${part}`).toContain(part)
    }
    // 组件层：聚合日志走 compose:logs（动作名取自 utils/cmd 的清单，不在组件里再写字面量）
    // 并接入与 container:logs 同一条 NDJSON 流端点。
    const logsSrc = readFileSync(
      join(ROOT, 'components/project-workspace/project-logs.vue'),
      'utf8'
    )
    expect(logsSrc).toContain('COMPOSE_LOGS_ACTIONS')
    expect(logsSrc).toContain('openDockerLogStream')
    expect(logsSrc).toContain('createLogFeed')
    expect(logsSrc).toContain('filterComposeLogLines')
    // 索引页入口：薄索引的行动作「打开工作台」（host 随行 —— 7b 起列表页只有这一条路；
    // 跨主机索引后 host 取**行主机**，页面级主机上下文已随 9b 收敛掉）。
    const projectsSrc = readFileSync(join(ROOT, 'views/projects.vue'), 'utf8')
    expect(projectsSrc).toContain('DockerProjectWorkspace')
    expect(projectsSrc).toContain('打开工作台')
    expect(projectsSrc).toContain('query: { host: project.hostId }')
  })
})
