/**
 * 守卫：docker 模块的 action 字面量白名单 + 四期编辑入口（**收尾形态**）+ 五期监控面。
 *
 * 四期（配置编辑）已交付，期次边界守完最后一班岗；五期（监控面）补进白名单；
 * 5a（项目聚合日志 compose:logs）随之并入 —— 它收在 utils/cmd.ts 的
 * COMPOSE_LOGS_ACTIONS（会话制只读，不进一期 runRead 清单，也不进二期写注册表）：
 *   - 一期只读 4 条 + 二期写 22 条 + 三期 exec 1 条 + 四期 compose.file 3 条 +
 *     五期 stats 实时流 1 条 + 5a 聚合日志 1 条 = 协议白名单（六前缀口径）32 条
 *     （`uni_protocol/docker.go` 的 `dockerActionSpecs` 全集 33 条含 docker:events，
 *     走流订阅不带六前缀、不在本扫描口径内）。
 *     本守卫**不再有「后期动作」清单**：模块源码里出现的 action 字面量必须全部落在
 *     这 32 条内（新增动作而忘了同步，就会以「不在白名单」红灯）。
 *   - 六份清单互补的条数断言保留（清单少一条、注册表多一条都不行）。
 *   - 「四期控件零 DOM」已随本期交付失效：编辑入口现在**存在**，改为断言
 *     「入口存在且受 docker:config 权限门控」（不渲染 ≠ 禁用；没有权限的人连按钮
 *     都不该看到 —— spec §11.0 分期控件矩阵）。
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
 * 四期动作（协议白名单 30 条去掉一期只读 4 条、二期写 21 条、三期 1 条与五期 1 条
 * 后的**恰 3 条**）。
 *
 * 跨语言没法互相 import，这份清单必须自己与协议对齐（差一条就等于少守一个动作）：
 * `uni_protocol/docker.go` 的 `AllDockerActions()`；`uni_core/internal/pkg/dockerpolicy`
 * 的 `policies` 里 Phase=4 的行逐条数也是这 3 条。以后往 `dockerActionSpecs`
 * 里加动作时，要同步往这里加一条。
 */
const PHASE4_ACTIONS = ['compose.file:write', 'compose.file:validate', 'compose.file:patch']

/** 三期动作（终端；单列是为了条数互补与「确实接线了」的正向断言）。 */
const PHASE3_ACTIONS = ['container:exec']

/** 五期动作（监控面 · stats 实时流）。与三期同款存在方式：动作字面量只出现在
 * 组件里（container-stats.vue），不进 utils/cmd.ts 的只读清单 —— 那份清单服务
 * 一期 runRead 轮询闭环，而 stats 是会话制流，不走那个闭环。 */
const PHASE5_ACTIONS = ['container:stats']

/**
 * 5a 动作（项目聚合日志 compose:logs）：**不从本文件再抄一份** —— 它收在
 * utils/cmd.ts 的 COMPOSE_LOGS_ACTIONS（与五期同样不进 runRead 清单，但清单本体
 * 在源码侧，phase-gate 与 actions 的互补断言都从那里 import，漂移只剩协议一处）。
 */

/** 协议白名单的总条数（六前缀口径；docker:events 不在扫描口径内，见文件头）。 */
const PROTOCOL_ACTION_COUNT = 32

/** 白名单全集（模块里允许出现的 action 字面量只能来自它）。 */
const ACTION_WHITELIST: readonly string[] = [
  ...PHASE1_ACTIONS,
  ...DOCKER_ACTION_REGISTRY.map((e) => e.action),
  ...PHASE3_ACTIONS,
  ...PHASE4_ACTIONS,
  ...PHASE5_ACTIONS,
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

describe('分期控件矩阵收尾：action 字面量全在白名单内', () => {
  it('扫描不是空转：确实扫到了模块源码与全部四期组件', () => {
    const files = walk(ROOT)
    expect(files.length).toBeGreaterThan(0)
    expect(files.some((f) => f.endsWith('utils/cmd.ts'))).toBe(true)
    expect(files.some((f) => f.endsWith('utils/actions.ts'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/pty-terminal.vue'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/compose-editor/compose-editor.vue'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/compose-editor/form-mode.vue'))).toBe(true)
    expect(files.some((f) => f.endsWith('components/compose-editor/yaml-mode.vue'))).toBe(true)
  })

  it('模块源码里出现的每个 action 字面量都在 32 条白名单内', () => {
    const whitelist = new Set(ACTION_WHITELIST)
    const seen = new Set<string>()
    for (const file of walk(ROOT)) {
      const src = readFileSync(file, 'utf8')
      for (const literal of actionLiteralsIn(src)) {
        seen.add(literal)
        expect(
          whitelist.has(literal),
          `${file} 出现了白名单之外的 action 字面量「${literal}」（协议 32 条的名单见本文件头）`
        ).toBe(true)
      }
    }
    // 反向自检：扫描确实命中了四期、三期、五期、创建面与 5a 聚合日志的关键动作
    // （不然这个守卫可能空转）。
    for (const must of [
      'compose.file:read',
      'compose.file:validate',
      'compose.file:write',
      'compose.file:patch',
      'container:exec',
      'container:stats',
      'container:create',
      'compose:up',
      'compose:logs'
    ]) {
      expect(seen.has(must), `扫描没有命中 ${must}（守空转）`).toBe(true)
    }
  })

  it('一期动作清单恰好四个（与 spec §11.0 的一期矩阵一致）', () => {
    expect([...PHASE1_ACTIONS]).toHaveLength(4)
  })

  it('六份清单互补：32 条 = 4 只读 + 22 二期写 + 1 三期 + 3 四期 + 1 五期 + 1 条 5a 聚合日志，无重复、无交集', () => {
    // 防的是「从清单或注册表里删掉一条」这种静默失守：条数不对就红灯。
    const phase2 = DOCKER_ACTION_REGISTRY.map((e) => e.action)
    expect(DOCKER_ACTION_REGISTRY).toHaveLength(22)
    expect(new Set(PHASE4_ACTIONS).size).toBe(PHASE4_ACTIONS.length)
    expect(new Set(PHASE3_ACTIONS).size).toBe(PHASE3_ACTIONS.length)
    expect(new Set(PHASE5_ACTIONS).size).toBe(PHASE5_ACTIONS.length)
    expect(new Set(PHASE1_ACTIONS).size).toBe(PHASE1_ACTIONS.length)
    expect(new Set(COMPOSE_LOGS_ACTIONS).size).toBe(COMPOSE_LOGS_ACTIONS.length)
    expect(new Set(phase2).size).toBe(phase2.length)
    expect(
      COMPOSE_LOGS_ACTIONS.length +
        PHASE5_ACTIONS.length +
        PHASE4_ACTIONS.length +
        PHASE3_ACTIONS.length +
        PHASE1_ACTIONS.length +
        phase2.length
    ).toBe(PROTOCOL_ACTION_COUNT)
    const all = [
      ...PHASE1_ACTIONS,
      ...phase2,
      ...PHASE3_ACTIONS,
      ...PHASE4_ACTIONS,
      ...PHASE5_ACTIONS,
      ...COMPOSE_LOGS_ACTIONS
    ]
    expect(new Set(all).size).toBe(PROTOCOL_ACTION_COUNT)
    for (const a of [...PHASE3_ACTIONS, ...PHASE4_ACTIONS, ...PHASE5_ACTIONS]) {
      expect(phase2, `后期动作 ${a} 不该进二期写动作注册表`).not.toContain(a)
    }
    for (const a of [...PHASE1_ACTIONS, ...PHASE5_ACTIONS, ...COMPOSE_LOGS_ACTIONS]) {
      expect(PHASE4_ACTIONS, `${a} 不该出现在四期清单里`).not.toContain(a)
      expect(phase2, `${a} 不该进二期写动作注册表`).not.toContain(a)
    }
  })

  it('三期控件已接线：终端引入 xterm、日志查看器有跟随开关', () => {
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

  it('四期编辑入口存在且受 docker:config 权限门控（渲染 + 组件两层）', () => {
    // 页面层：入口受 canConfig（= hasAuth(PermDockerConfig)）门控，并挂上了编辑器组件。
    const projectsSrc = readFileSync(join(ROOT, 'views/projects.vue'), 'utf8')
    expect(projectsSrc).toContain('PermDockerConfig')
    expect(
      /canConfig\s*=\s*computed\(\s*\(\)\s*=>\s*hasAuth\(PermDockerConfig\)\s*\)/.test(projectsSrc)
    ).toBe(true)
    expect(projectsSrc).toContain('v-if="canConfig"')
    expect(projectsSrc).toContain('ComposeEditor')
    expect(projectsSrc).toContain('＋添加服务')
    // 组件层：保存/回滚按钮同样只在有配置编辑权限时渲染。
    const editorSrc = readFileSync(
      join(ROOT, 'components/compose-editor/compose-editor.vue'),
      'utf8'
    )
    expect(editorSrc).toContain('PermDockerConfig')
    expect(editorSrc).toContain('v-if="canConfig"')
  })

  it('五期监控面已接线：抽屉概览挂 stats 组件，组件发 container:stats 并接流', () => {
    // 抽屉层：概览 Tab 挂 ContainerStats（v-if 懒挂载纪律与终端 Tab 同款）。
    const drawerSrc = readFileSync(join(ROOT, 'components/workload-drawer.vue'), 'utf8')
    expect(drawerSrc).toContain('ContainerStats')
    expect(drawerSrc).toContain("activeTab === 'overview'")
    // 组件层：走指令通道发 container:stats，并接 NDJSON 统计流。
    const statsSrc = readFileSync(join(ROOT, 'components/container-stats.vue'), 'utf8')
    expect(statsSrc).toContain("action: 'container:stats'")
    expect(statsSrc).toContain('openDockerStatsStream')
    expect(statsSrc).toContain('createStatsFeed')
  })

  it('创建面（4a）已接线：两个入口受 docker:manage 门控，抽屉发 container:create', () => {
    // 容器页入口：工具栏按钮 v-if canManage（hasAuth(PermDockerManage)），挂创建抽屉。
    const containersSrc = readFileSync(join(ROOT, 'views/containers.vue'), 'utf8')
    expect(containersSrc).toContain('CreateContainerDrawer')
    expect(containersSrc).toContain('PermDockerManage')
    expect(containersSrc).toMatch(
      /canManage\s*=\s*computed\(\s*\(\)\s*=>\s*hasAuth\(PermDockerManage\)\s*\)/
    )
    expect(containersSrc).toContain('v-if="canManage"')
    expect(containersSrc).toContain('创建容器…')
    // 镜像详情入口：同一权限档，预填镜像与主机（initialImage/initialHostId）。
    const imageDetailSrc = readFileSync(join(ROOT, 'views/image-detail.vue'), 'utf8')
    expect(imageDetailSrc).toContain('CreateContainerDrawer')
    expect(imageDetailSrc).toContain('用此镜像创建…')
    expect(imageDetailSrc).toContain(':initial-image="actionTarget"')
    // 组件层：走指令通道发 container:create（注册表标准档确认在 action-confirm 之间）。
    const createSrc = readFileSync(join(ROOT, 'components/create-container-drawer.vue'), 'utf8')
    expect(createSrc).toContain("action: 'container:create'")
    expect(createSrc).toContain('DockerActionConfirm')
    expect(createSrc).toContain('buildRunPreview')
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
    // 列表页入口：项目头有「打开工作台」（host 随行）。
    const projectsSrc = readFileSync(join(ROOT, 'views/projects.vue'), 'utf8')
    expect(projectsSrc).toContain('DockerProjectWorkspace')
    expect(projectsSrc).toContain('打开工作台')
    expect(projectsSrc).toContain('query: { host: ctx.hostId }')
  })
})
