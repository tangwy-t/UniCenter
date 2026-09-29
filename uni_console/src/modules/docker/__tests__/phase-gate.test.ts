/**
 * 守卫：docker 模块的 action 字面量白名单 + 四期编辑入口（**收尾形态**）。
 *
 * 四期（配置编辑）已交付，期次边界守完最后一班岗：
 *   - 一期只读 4 条 + 二期写 21 条 + 三期 exec 1 条 + 四期 compose.file 3 条 = 协议
 *     白名单 29 条（`uni_protocol/docker.go` 的 `AllDockerActions()` / `dockerActionSpecs`）。
 *     本守卫**不再有「后期动作」清单**：模块源码里出现的 action 字面量必须全部落在
 *     这 29 条内（新增动作而忘了同步，就会以「不在白名单」红灯）。
 *   - 四份清单互补的条数断言保留（清单少一条、注册表多一条都不行）。
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
import { PHASE1_ACTIONS } from '../utils/cmd'
import { DOCKER_ACTION_REGISTRY } from '../utils/actions'

const ROOT = new URL('..', import.meta.url).pathname

/**
 * 四期动作（协议白名单 29 条去掉一期只读 4 条、二期写 21 条与三期 1 条后的**恰 3 条**）。
 *
 * 跨语言没法互相 import，这份清单必须自己与协议对齐（差一条就等于少守一个动作）：
 * `uni_protocol/docker.go` 的 `AllDockerActions()`；`uni_core/internal/pkg/dockerpolicy`
 * 的 `policies` 里 Phase=4 的行逐条数也是这 3 条。以后往 `dockerActionSpecs`
 * 里加动作时，要同步往这里加一条。
 */
const PHASE4_ACTIONS = ['compose.file:write', 'compose.file:validate', 'compose.file:patch']

/** 三期动作（终端；单列是为了条数互补与「确实接线了」的正向断言）。 */
const PHASE3_ACTIONS = ['container:exec']

/** 协议白名单的总条数（`AllDockerActions()` 的返回长度）。 */
const PROTOCOL_ACTION_COUNT = 29

/** 白名单全集（模块里允许出现的 action 字面量只能来自它）。 */
const ACTION_WHITELIST: readonly string[] = [
  ...PHASE1_ACTIONS,
  ...DOCKER_ACTION_REGISTRY.map((e) => e.action),
  ...PHASE3_ACTIONS,
  ...PHASE4_ACTIONS
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

  it('模块源码里出现的每个 action 字面量都在 29 条白名单内', () => {
    const whitelist = new Set(ACTION_WHITELIST)
    const seen = new Set<string>()
    for (const file of walk(ROOT)) {
      const src = readFileSync(file, 'utf8')
      for (const literal of actionLiteralsIn(src)) {
        seen.add(literal)
        expect(
          whitelist.has(literal),
          `${file} 出现了白名单之外的 action 字面量「${literal}」（协议 29 条的名单见本文件头）`
        ).toBe(true)
      }
    }
    // 反向自检：扫描确实命中了四期与三期的关键动作（不然这个守卫可能空转）。
    for (const must of [
      'compose.file:read',
      'compose.file:validate',
      'compose.file:write',
      'compose.file:patch',
      'container:exec',
      'compose:up'
    ]) {
      expect(seen.has(must), `扫描没有命中 ${must}（守空转）`).toBe(true)
    }
  })

  it('一期动作清单恰好四个（与 spec §11.0 的一期矩阵一致）', () => {
    expect([...PHASE1_ACTIONS]).toHaveLength(4)
  })

  it('四份清单互补：29 条 = 4 只读 + 21 二期写 + 1 三期 + 3 四期，无重复、无交集', () => {
    // 防的是「从清单或注册表里删掉一条」这种静默失守：条数不对就红灯。
    const phase2 = DOCKER_ACTION_REGISTRY.map((e) => e.action)
    expect(DOCKER_ACTION_REGISTRY).toHaveLength(21)
    expect(new Set(PHASE4_ACTIONS).size).toBe(PHASE4_ACTIONS.length)
    expect(new Set(PHASE3_ACTIONS).size).toBe(PHASE3_ACTIONS.length)
    expect(new Set(PHASE1_ACTIONS).size).toBe(PHASE1_ACTIONS.length)
    expect(new Set(phase2).size).toBe(phase2.length)
    expect(
      PHASE4_ACTIONS.length + PHASE3_ACTIONS.length + PHASE1_ACTIONS.length + phase2.length
    ).toBe(PROTOCOL_ACTION_COUNT)
    const all = [...PHASE1_ACTIONS, ...phase2, ...PHASE3_ACTIONS, ...PHASE4_ACTIONS]
    expect(new Set(all).size).toBe(PROTOCOL_ACTION_COUNT)
    for (const a of [...PHASE3_ACTIONS, ...PHASE4_ACTIONS]) {
      expect(phase2, `后期动作 ${a} 不该进二期写动作注册表`).not.toContain(a)
    }
    for (const a of PHASE1_ACTIONS) {
      expect(PHASE4_ACTIONS, `一期动作 ${a} 不该出现在四期清单里`).not.toContain(a)
      expect(phase2, `一期动作 ${a} 不该进二期写动作注册表`).not.toContain(a)
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
})
