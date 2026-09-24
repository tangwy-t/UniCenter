#!/usr/bin/env node
/**
 * 离线图标清单的同步器与守卫。
 *
 * ## 要解决的问题
 *
 * @iconify/vue 在本机注册表里找不到图标时,会去 api.iconify.design 兜底拉取。
 * 内网/断网环境下这条兜底必然失败,图标就渲染成空白 —— 侧边栏、页签、页头
 * 一起白。故"随包发布"的图标必须显式注册(见 src/utils/ui/iconify-loader.ts)。
 *
 * ## 为什么是"用到的图标"而不是整包
 *
 * 全量内置现有 15 套图标集要 +38MB(gzip 约 +9MB,比整个前端包还大一倍多),
 * 而只打进源码/种子/预设里**真正用到**的图标只要十几 KB —— 差三个数量级。
 * 唯一例外是 `ri:`:它整套全量内置(选择器的手动输入允许随手敲任意 ri 名,
 * 这条自由度是刻意的),故本脚本不收录任何 `ri:` 图标,只校验其拼写存在。
 *
 * ## 图标名有三个来源,最容易漏的是后端种子
 *
 *   ① uni_console/src 里的图标字面量(`icon="a:b"` / `:icon="… 'a:b' …"` / `icon: 'a:b'`)
 *   ② uni_core 迁移里的菜单种子(`Icon: "a:b"`)—— **新菜单的图标写在后端,不在前端**
 *   ③ 选择器的精选预设清单(art-icon-picker/icons.ts 的裸字符串数组)
 *
 * 抽取必须按"图标上下文"抓,不能全文抓 `a:b` 形态:权限码(`device:query`)、
 * CSS 变体(`md:flex`)、Redis 键(`agent:device:1001:history`)都会撞进来
 * (实测全文抓法 587 个候选里一半是这类噪音)。
 *
 * ## 用法
 *
 *   pnpm icons:sync    # 重新生成 src/utils/ui/offline-icons.generated.ts(需已装 @iconify-json/<集合>)
 *   pnpm check:icons   # 守卫:未内置/不存在/陈旧的图标 → 非零退出(CI 与 pnpm test 用)
 *
 * --check 不需要联网:拼写校验读本机 @iconify-json/ri,清单校验比对生成物,
 * 两者都不碰网络(只有 --write 拉新图标时才读本机的 @iconify-json/<集合>)。
 *
 * ## 跨仓库合并时生成物会冲突,这是预期而非故障
 *
 * 生成物是按**本仓库自己的**源码/种子/预设算出来的,上游(WebManagerFramework
 * 的 web/,同一套框架文件)与本地两份必然不同(各自用到的图标集合不一样)。故从
 * 上游合并进来时 offline-icons.generated.ts 会报 add/add 冲突 —— 处理办法:
 * **先随便取一边,再跑一次 `pnpm icons:sync`**(它按当前仓库重算),不要去手工
 * 合并那两份清单。合并后 `pnpm check:icons` 绿了才算合完(守卫也会把"清单与
 * 源码不同步"直接报出来)。
 */
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

/** 脚本所在目录的上一级 = uni_console 根。按脚本位置解析,不依赖 cwd。 */
export const ROOT = resolve(fileURLToPath(new URL('..', import.meta.url)))
export const SRC_DIR = join(ROOT, 'src')
export const MIGRATIONS_DIR = resolve(ROOT, '../uni_core/internal/pkg/migration/migrations')
export const PRESET_FILE = join(SRC_DIR, 'components/core/forms/art-icon-picker/icons.ts')
export const OUT_FILE = join(SRC_DIR, 'utils/ui/offline-icons.generated.ts')

/**
 * 整套全量内置的集合:它们不进生成物,任意名字都离线可用。
 * 与 iconify-loader.ts 的 addCollection(riIcons) 一一对应 —— 改这里必须同时改那里。
 */
export const FULL_PREFIXES = ['ri']

/** 图标名形态:`<集合>:<名字>`,集合与名字都是 Iconify 的字符集(小写/数字/连字符)。 */
export const ICON_NAME_RE = /^[a-z][a-z0-9-]*:[a-z0-9]+(?:-[a-z0-9]+)*$/

/** ① 属性式:`icon="a:b"`(排除 `:icon="a:b"` —— 那个由下面的绑定式处理)。 */
const ATTR_RE = /(?<![:\w-])icon=["']([a-z][a-z0-9-]*:[a-z0-9-]+)["']/g
/** ② 绑定式:`:icon="…"`,其表达式内的**带引号**字面量(含三元? 两侧)。 */
const BIND_RE = /:icon="([\s\S]*?)"/g
const QUOTED_RE = /['"]([a-z][a-z0-9-]*:[a-z0-9-]+)['"]/g
/** ③ 对象属性式:`icon: 'a:b'`(TS 里的 computed/配置对象)。 */
const PROP_RE = /\bicon:\s*['"]([a-z][a-z0-9-]*:[a-z0-9-]+)['"]/g
/** ④ 后端菜单种子:`Icon: "a:b"`。 */
const GO_RE = /\bIcon:\s*"([a-z][a-z0-9-]*:[a-z0-9-]+)"/g
/** ⑤ 选择器预设:整行只有一个带引号的图标名。 */
const PRESET_RE = /^\s*['"]([a-z][a-z0-9-]*:[a-z0-9-]+)['"],?\s*$/gm

/** 行号(1 起),供报错定位。 */
export function lineOf(text, index) {
  let line = 1
  for (let i = 0; i < index && i < text.length; i++) if (text[i] === '\n') line++
  return line
}

/**
 * 纯函数:按"图标上下文"从一段文本里抽图标名。
 * `kind`: 'source'(vue/ts) | 'go'(迁移种子) | 'presets'(选择器预设清单)。
 */
export function extractIconNames(text, kind) {
  const out = []
  const push = (m, group = 1) => {
    if (ICON_NAME_RE.test(m[group])) out.push({ name: m[group], line: lineOf(text, m.index) })
  }
  if (kind === 'go') {
    for (const m of text.matchAll(GO_RE)) push(m)
    return out
  }
  if (kind === 'presets') {
    for (const m of text.matchAll(PRESET_RE)) push(m)
    return out
  }
  for (const m of text.matchAll(ATTR_RE)) push(m)
  for (const m of text.matchAll(PROP_RE)) push(m)
  for (const bind of text.matchAll(BIND_RE)) {
    for (const m of bind[1].matchAll(QUOTED_RE)) {
      if (ICON_NAME_RE.test(m[1])) {
        out.push({ name: m[1], line: lineOf(text, bind.index + m.index) })
      }
    }
  }
  return out
}

/** 递归收集待扫描的源码文件(与 check-permissions 同款排除:测试/生成物/自身)。 */
export function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (!['node_modules', 'dist', 'assets', '__tests__'].includes(name)) walk(p, out)
    } else if (/\.(ts|vue)$/.test(name) && !name.endsWith('.test.ts') && !name.endsWith('.d.ts')) {
      if (resolve(p) !== resolve(OUT_FILE)) out.push(p)
    }
  }
  return out
}

/**
 * 纯函数:汇总三个来源里用到的图标名。
 * `sources` 形如 `{ path, text, kind }`;返回按"名字 → 出现位置"归并的 Map。
 */
export function collectUsedIconNames(sources) {
  const used = new Map()
  for (const { path, text, kind } of sources) {
    for (const { name, line } of extractIconNames(text, kind)) {
      const where = `${relative(ROOT, path)}:${line}`
      if (!used.has(name)) used.set(name, [])
      const hits = used.get(name)
      if (!hits.includes(where)) hits.push(where)
    }
  }
  return new Map([...used.entries()].sort(([a], [b]) => (a < b ? -1 : 1)))
}

/** 读取仓库里所有图标来源(源码 + 后端迁移种子 + 选择器预设)。 */
export function readSources({ srcDir = SRC_DIR, migrationsDir = MIGRATIONS_DIR } = {}) {
  const sources = walk(srcDir).map((path) => ({
    path,
    text: readFileSync(path, 'utf8'),
    kind: resolve(path) === resolve(PRESET_FILE) ? 'presets' : 'source'
  }))
  // 后端种子是"最容易漏"的那一处来源(新菜单的图标写在 uni_core 里,不在前端),
  // 故这里宁可硬失败也不静默跳过:否则在"只拷了 uni_console 的目录"里跑守卫,
  // 会因为少扫一个来源而变绿 —— 那比不跑更危险。
  // (console 镜像的构建阶段就只拷了 uni_console,所以那个环境不要跑本脚本;
  //  生成物是入库的,构建本身不需要它。)
  if (!existsSync(migrationsDir)) {
    throw new Error(
      `找不到后端迁移目录 ${migrationsDir}:本脚本需要完整仓库(图标名有三个来源,` +
        `其中后端菜单种子在 uni_core 内)。请在仓库根内运行,或确认 uni_core 未被裁掉。`
    )
  }
  for (const name of readdirSync(migrationsDir)) {
    if (!name.endsWith('.go') || name.endsWith('_test.go')) continue
    const path = join(migrationsDir, name)
    sources.push({ path, text: readFileSync(path, 'utf8'), kind: 'go' })
  }
  return sources
}

/** 读取本机安装的某个图标集(devDependency),用于取子集与拼写校验。 */
export function loadSet(prefix, { root = ROOT } = {}) {
  const file = join(root, 'node_modules', '@iconify-json', prefix, 'icons.json')
  try {
    return JSON.parse(readFileSync(file, 'utf8'))
  } catch {
    throw new Error(
      `图标集 ${prefix} 未安装(${relative(ROOT, file)} 读不到):请 \`pnpm add -D @iconify-json/${prefix}\` 后重试`
    )
  }
}

/** 集合里是否存在该短名(图标或别名)。 */
export function setHasIcon(set, short) {
  return Boolean(set?.icons?.[short] || set?.aliases?.[short])
}

/**
 * 纯函数:从集合里取出用到的图标子集(含别名链)。
 *
 * 别名(aliases)在 Iconify 里是"换个名字的同一个图标",只带 parent 不带宽高与
 * 图形数据;若只收别名不收 parent,addCollection 会告警且渲染不出。故这里沿
 * parent 递归补齐(带 visited 防环)。
 */
export function subsetCollection(prefix, shorts, set) {
  const icons = {}
  const aliases = {}
  const take = (short, seen = new Set()) => {
    if (seen.has(short)) return
    seen.add(short)
    if (set?.icons?.[short]) {
      icons[short] = set.icons[short]
      return
    }
    const alias = set?.aliases?.[short]
    if (!alias) return
    aliases[short] = alias
    if (alias.parent) take(alias.parent, seen)
  }
  for (const short of shorts) take(short)
  const out = { prefix, width: set.width, height: set.height, icons }
  if (set.lastModified) out.lastModified = set.lastModified
  if (Object.keys(aliases).length) out.aliases = aliases
  return out
}

/** 生成物的稳定文本(键序固定 → 字节可复现,便于 --check 比对)。 */
export function renderModule(collections, names) {
  const header = `/**
 * 离线图标子集 —— 本文件由 \`pnpm icons:sync\` 生成,请勿手改。
 *
 * 全量内置现有 15 套图标集要 +38MB,这里只收录源码、后端菜单种子与选择器预设里
 * **真正用到**的图标(十几 KB)。改图标之后跑一次 \`pnpm icons:sync\`;忘了跑会被
 * \`pnpm check:icons\` 与 scripts/offline-icons.test.mjs 拦住。
 *
 * \`ri:\` 不在这里:它整套全量内置(见 ./iconify-loader.ts),任意 ri 名都离线可用。
 */
import type { IconifyJSON } from '@iconify/vue'

/** 非全量集合的图标子集(结构即 IconifyJSON,可直接喂 addCollection)。 */
export const OFFLINE_ICON_COLLECTIONS: IconifyJSON[] = `
  const namesBlock = `
/** 以上集合里所有可离线渲染的完整图标名(含别名),判定"是否离线可用"用。 */
export const OFFLINE_ICON_NAMES: string[] = `
  const body = JSON.stringify(collections, null, 2)
  const namesBody = JSON.stringify(names, null, 2)
  return `${header}${body}\n${namesBlock}${namesBody}\n`
}

/** 解析生成物里的图标名清单(--- check 用;生成物是受控文本,按块提取即可)。 */
export function readGeneratedNames(text) {
  const m = text.match(/export const OFFLINE_ICON_NAMES: string\[\] = (\[[\s\S]*?\])\n/)
  return m ? JSON.parse(m[1]) : null
}

/**
 * 纯函数:算出所有问题(--check 的全部判定,不读盘、不退出,便于单测)。
 *
 * `used`:名字 → 出现位置;`generatedNames`:生成物里的清单;`loadSetData`:取集合数据。
 * 四类问题:
 *   - unknown-set  引用了没装过/没见过的集合(多半是新集合,也可能是误抽)
 *   - not-exist    名字在该集合里不存在(拼写错 —— 会渲染成空白,最隐蔽的一类)
 *   - missing      非全量集合的图标没进生成物(需要 icons:sync)
 *   - stale        生成物里有、但源码已不再引用(需要 icons:sync)
 */
export function collectProblems(used, generatedNames, loadSetData = loadSet) {
  const problems = []
  const generated = new Set(generatedNames ?? [])
  const sets = new Map()
  const setOf = (prefix) => {
    if (!sets.has(prefix)) sets.set(prefix, loadSetData(prefix))
    return sets.get(prefix)
  }

  for (const [name, at] of used) {
    const prefix = name.slice(0, name.indexOf(':'))
    const short = name.slice(prefix.length + 1)
    let set
    try {
      set = setOf(prefix)
    } catch (err) {
      problems.push({ kind: 'unknown-set', name, at, detail: err.message })
      continue
    }
    if (!setHasIcon(set, short)) {
      problems.push({ kind: 'not-exist', name, at })
      continue
    }
    if (!FULL_PREFIXES.includes(prefix) && !generated.has(name)) {
      problems.push({ kind: 'missing', name, at })
    }
  }

  for (const name of generated) {
    if (!used.has(name)) problems.push({ kind: 'stale', name, at: [] })
  }
  return problems
}

/** 建生成物(--write 的唯一副作用入口)。 */
export function buildOutput(used, { loadSetData = loadSet } = {}) {
  const byPrefix = new Map()
  for (const name of used.keys()) {
    const prefix = name.slice(0, name.indexOf(':'))
    if (FULL_PREFIXES.includes(prefix)) continue
    if (!byPrefix.has(prefix)) byPrefix.set(prefix, [])
    byPrefix.get(prefix).push(name.slice(prefix.length + 1))
  }
  const collections = []
  for (const prefix of [...byPrefix.keys()].sort()) {
    const set = loadSetData(prefix)
    const shorts = [...new Set(byPrefix.get(prefix))].sort()
    collections.push(subsetCollection(prefix, shorts, set))
  }
  collections.sort((a, b) => (a.prefix < b.prefix ? -1 : 1))

  const names = []
  for (const c of collections) {
    for (const short of Object.keys(c.icons ?? {})) names.push(`${c.prefix}:${short}`)
    for (const short of Object.keys(c.aliases ?? {})) names.push(`${c.prefix}:${short}`)
  }
  names.sort()
  return { collections, names, text: renderModule(collections, names) }
}

/**
 * 主流程的另一半:把仓库实况算成一份结论(不打印、不退出)。
 *
 * `run()` 与单测共用这一条路径 —— 守卫的"判定"与"报告"分开,测试里才能
 * 直接断言 `problems` 为空,而不是靠解析退出码。
 */
export function checkRepo(opts = {}) {
  const sources = readSources(opts)
  const used = collectUsedIconNames(sources)
  const existing = (() => {
    try {
      return readFileSync(OUT_FILE, 'utf8')
    } catch {
      return ''
    }
  })()
  const generatedNames = readGeneratedNames(existing) ?? []
  const problems = collectProblems(used, generatedNames, opts.loadSetData)

  // 名字层面已经错了(未知集合/拼写错)就不必再算内容漂移:那两份必然不同,
  // 报出来只会盖住真正的原因。
  const fatal = problems.some((p) => p.kind === 'unknown-set' || p.kind === 'not-exist')
  const text = fatal ? null : buildOutput(used, opts).text
  if (text !== null && text !== existing) {
    problems.push({ kind: 'drift', name: relative(ROOT, OUT_FILE), at: [] })
  }
  return { used, generatedNames, problems, text, existing }
}

/** 主流程:`--write` 生成,缺省/`--check` 守卫。返回进程退出码。 */
export function run({ write = false, ...opts } = {}) {
  const { used, generatedNames, problems, text } = checkRepo(opts)

  if (write) {
    if (problems.some((p) => p.kind === 'unknown-set' || p.kind === 'not-exist')) {
      report(problems)
      console.error('存在无法内置的图标(见上),未改写生成物。')
      return 1
    }
    const { collections, names } = buildOutput(used, opts)
    writeFileSync(OUT_FILE, text)
    console.log(
      `offline icons: 已生成 ${relative(ROOT, OUT_FILE)}` +
        `(${collections.length} 个集合 / ${names.length} 个图标 / ${Buffer.byteLength(text)}B;` +
        `${FULL_PREFIXES.join('+')} 全量内置,不计入)`
    )
    return 0
  }

  if (problems.length) {
    report(problems)
    return 1
  }
  console.log(
    `offline icons guard: OK (${used.size} 个图标名; ${generatedNames.length} 个离线内置;` +
      ` ${FULL_PREFIXES.join('+')} 全量)`
  )
  return 0
}

/** 报告问题(先按类型分组,再逐条给位置与修法)。 */
function report(problems) {
  const titles = {
    'unknown-set': '引用了本机没装的图标集',
    'not-exist': '图标名在该集合里不存在(会渲染成空白 —— 多半是拼写错)',
    missing: '图标未随前端离线包发布(断网环境不显示)',
    stale: '离线包里已不再被引用的陈留图标',
    drift: '生成物与源码不同步(内容漂移)'
  }
  console.error('offline icons guard: 发现 %d 个问题:', problems.length)
  for (const kind of ['unknown-set', 'not-exist', 'missing', 'stale', 'drift']) {
    const items = problems.filter((p) => p.kind === kind)
    if (!items.length) continue
    console.error(`  [${titles[kind]}]`)
    for (const p of items) {
      console.error(`    - ${p.name}${p.detail ? ` —— ${p.detail}` : ''}`)
      for (const where of p.at) console.error(`        ${where}`)
    }
  }
  console.error(
    '修法:改掉名字,或(新增图标时)`pnpm add -D @iconify-json/<集合>` 后 `pnpm icons:sync`。'
  )
}

// 仅在被当作脚本执行时跑主流程 —— 被单测 import 时不得有副作用
// (与 check-permissions.mjs 同款:历史上顶层 process.exit 会终止宿主进程)。
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(run({ write: process.argv.includes('--write') }))
}
