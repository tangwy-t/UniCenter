/**
 * 四期配置编辑的纯函数中枢（spec §8/§9）：YAML ↔ 文档模型、变更集生成、注释检测、
 * diff 摘要、服务模板。组件层（components/compose-editor/*）只做绑定与编排。
 *
 * ── 文档模型的边界（为什么长这样）────────────────────────────────────
 *
 * 模型只收「表单可表达」的键（services/networks/volumes 三段按 §9 的 patch 范围），
 * 其余一切原样进 `_raw`：
 *   - 顶层未建模键（version、x- 前缀扩展、configs、secrets…）→ doc._raw；
 *   - 服务里的未建模键（build/healthcheck/deploy…）→ service._raw；
 *   - 形状不是表单规范形的键（长语法 ports、条件 depends_on、缺 = 的环境变量列表）
 *     也进 _raw —— 表单不认识就**不碰它**，保存走 patch 时 agent 会逐字节保留。
 *
 * ── 两条保存路径的取舍都从这里长出来（§9 如实标注）────────────────────
 *
 * buildComposePatch 只输出「被改过的键」（删除 = null），表单保存永远走它，
 * 未触碰段落（含注释）逐字节保留；serializeComposeDoc 只服务 YML 模式的显示与
 * 「表单改动带出到 YML」——它会丢掉注释，编译器不承诺逐字节（这是双模式固有代价，
 * 页面在切换与卡片上有三处如实标注）。
 *
 * ── 注释 token 检测 ────────────────────────────────────────────────
 *
 * countYamlComments 用 `yaml` 的 AST 收集挂在节点上的注释（防「引号里的 #」与
 * 「块标量内容」误报）；文件末尾的独立注释行不会被挂到任何节点（实测），故再补
 * 一次尾部整行扫描。
 */
import { isMap, parseDocument, stringify, visit, type Node as YamlNode } from 'yaml'

// ── 动作名（四期三条 + 一期只读一条；白名单 29 条内的名字）────────────────

/** 配置编辑用到的动作名（写/校验/补丁是四期，read 是一期就有的只读动作）。 */
export const COMPOSE_ACTIONS = {
  read: 'compose.file:read',
  validate: 'compose.file:validate',
  write: 'compose.file:write',
  patch: 'compose.file:patch'
} as const

// ── 文档模型 ─────────────────────────────────────────────────────────

export type ComposeScalar = string | number | boolean | null

/** 服务条目：建模键 + 未建模字段原样透传（`_order` 是书写顺序，序列化按它排）。 */
export interface ComposeService {
  image?: string
  container_name?: string
  restart?: string
  cpus?: ComposeScalar
  mem_limit?: ComposeScalar
  ports?: string[]
  volumes?: string[]
  environment?: Record<string, ComposeScalar>
  depends_on?: string[]
  command?: string | string[]
  networks?: string[]
  _raw: Record<string, unknown>
  _order: string[]
  /** 索引签名：条目在「泛型段落工具」里按普通键值映射处理。 */
  [key: string]: unknown
}

/** 网络/数据卷条目（同形：driver/name/external 三个常用键）。 */
export interface ComposeSectionEntry {
  driver?: string
  name?: string
  external?: boolean
  _raw: Record<string, unknown>
  _order: string[]
  [key: string]: unknown
}

export interface ComposeSection<T> {
  /** 名字的书写顺序（序列化与表单卡片顺序都按它）。 */
  keys: string[]
  items: Record<string, T>
}

export interface ComposeDoc {
  services: ComposeSection<ComposeService>
  networks: ComposeSection<ComposeSectionEntry>
  volumes: ComposeSection<ComposeSectionEntry>
  /** 顶层未建模键（原样透传）。 */
  _raw: Record<string, unknown>
  /** 顶层键顺序（含 services/networks/volumes 与 _raw 键）。 */
  order: string[]
}

export type ComposeSectionName = 'services' | 'networks' | 'volumes'

export interface ComposeParseResult {
  ok: boolean
  doc?: ComposeDoc
  /** 失败时的结论句（语法错误的 message 已剥掉位置尾巴，位置另给 line/column）。 */
  error?: string
  /** 1-based；仅语法错误有。 */
  line?: number
  column?: number
}

/** 服务上可由表单编辑的键（顺序 = 表单卡片里的字段顺序）。 */
export const SERVICE_FIELD_KEYS = [
  'image',
  'container_name',
  'restart',
  'cpus',
  'mem_limit',
  'ports',
  'volumes',
  'environment',
  'depends_on',
  'command',
  'networks'
] as const
export type ServiceFieldKey = (typeof SERVICE_FIELD_KEYS)[number]

/** 网络/卷上可由表单编辑的键。 */
export const SECTION_FIELD_KEYS = ['driver', 'name', 'external'] as const
export type SectionFieldKey = (typeof SECTION_FIELD_KEYS)[number]

/** 字段中文标签（表单标签 + diff 明细共用；页面只说结论，不出现原始键名）。 */
export const FIELD_LABELS: Record<string, string> = {
  image: '镜像',
  container_name: '容器名',
  restart: '重启策略',
  cpus: 'CPU 上限',
  mem_limit: '内存上限',
  ports: '端口',
  volumes: '卷',
  environment: '环境变量',
  depends_on: '依赖',
  command: '命令',
  networks: '网络',
  driver: '驱动',
  name: '名称',
  external: '外部'
}

/** 段落名词（diff 摘要用）：新增/移除 N 个「网元/网络/数据卷」。 */
const SECTION_NOUNS: Record<ComposeSectionName, string> = {
  services: '网元',
  networks: '网络',
  volumes: '数据卷'
}

export function emptyComposeDoc(): ComposeDoc {
  return {
    services: { keys: [], items: {} },
    networks: { keys: [], items: {} },
    volumes: { keys: [], items: {} },
    _raw: {},
    order: []
  }
}

// ── 解析：YAML → 文档模型 ────────────────────────────────────────────

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** 从 AST 的映射节点取键顺序（别名/非映射节点给 null，调用方回退 Object.keys）。 */
function mapKeyOrder(node: unknown): string[] | null {
  if (!isMap(node)) return null
  const keys: string[] = []
  for (const item of node.items) {
    const k = (item as { key?: { value?: unknown } }).key
    if (k && (typeof k.value === 'string' || typeof k.value === 'number')) {
      keys.push(String(k.value))
    }
  }
  return keys.length > 0 ? keys : null
}

/** 在根映射节点里找 `key` 的子节点（供下一层取键顺序）。 */
function childNode(root: unknown, key: string): unknown {
  if (!isMap(root)) return undefined
  for (const item of root.items) {
    const k = (item as { key?: { value?: unknown } }).key
    if (k && String(k.value) === key) return (item as { value?: unknown }).value
  }
  return undefined
}

/** order ∪ Object.keys(obj)，保持 order 优先（别名解析后可能少键，防御补齐）。 */
function withAllKeys(order: string[], obj: Record<string, unknown>): string[] {
  const out = [...order]
  for (const key of Object.keys(obj)) if (!out.includes(key)) out.push(key)
  return out
}

/** 全字符串的非空数组；其余形态返回 undefined（该键落 _raw，表单不碰）。 */
function stringList(v: unknown): string[] | undefined {
  if (!Array.isArray(v) || v.length === 0) return undefined
  return v.every((item) => typeof item === 'string') ? (v as string[]) : undefined
}

/** 环境变量：映射（标量值）直收；`K=V` 列表归一成映射；其余形态 undefined。 */
function envRecord(v: unknown): Record<string, ComposeScalar> | undefined {
  if (isPlainObject(v)) {
    const out: Record<string, ComposeScalar> = {}
    for (const [k, val] of Object.entries(v)) {
      if (
        val === null ||
        typeof val === 'string' ||
        typeof val === 'number' ||
        typeof val === 'boolean'
      ) {
        out[k] = val
      } else return undefined
    }
    return Object.keys(out).length > 0 ? out : undefined
  }
  if (Array.isArray(v)) {
    const out: Record<string, ComposeScalar> = {}
    for (const item of v) {
      if (typeof item !== 'string') return undefined
      const eq = item.indexOf('=')
      if (eq <= 0) return undefined // 缺 = 的条目无法用映射表达（依赖宿主环境），整键落 _raw
      out[item.slice(0, eq)] = item.slice(eq + 1)
    }
    return Object.keys(out).length > 0 ? out : undefined
  }
  return undefined
}

/** 命令：字符串或字符串数组直收（两种写法语义不同，保持原形态）。 */
function commandValue(v: unknown): string | string[] | undefined {
  if (typeof v === 'string') return v
  return stringList(v)
}

function parseServiceEntry(value: Record<string, unknown>, node: unknown): ComposeService {
  const order = withAllKeys(mapKeyOrder(node) ?? Object.keys(value), value)
  const svc: ComposeService = { _raw: {}, _order: order }
  for (const key of order) {
    if (key === '_raw' || key === '_order') continue
    const v = value[key]
    if (key === 'image' || key === 'container_name' || key === 'restart') {
      if (typeof v === 'string') {
        svc[key] = v
        continue
      }
    } else if (key === 'cpus' || key === 'mem_limit') {
      if (typeof v === 'string' || typeof v === 'number') {
        svc[key] = v
        continue
      }
    } else if (key === 'ports' || key === 'volumes' || key === 'depends_on' || key === 'networks') {
      const arr = stringList(v)
      if (arr) {
        svc[key] = arr
        continue
      }
    } else if (key === 'environment') {
      const env = envRecord(v)
      if (env) {
        svc.environment = env
        continue
      }
    } else if (key === 'command') {
      const cmd = commandValue(v)
      if (cmd !== undefined) {
        svc.command = cmd
        continue
      }
    }
    svc._raw[key] = v
  }
  return svc
}

function parseSectionEntry(value: Record<string, unknown>, node: unknown): ComposeSectionEntry {
  const order = withAllKeys(mapKeyOrder(node) ?? Object.keys(value), value)
  const entry: ComposeSectionEntry = { _raw: {}, _order: order }
  for (const key of order) {
    if (key === '_raw' || key === '_order') continue
    const v = value[key]
    if (key === 'driver' || key === 'name') {
      if (typeof v === 'string') {
        entry[key] = v
        continue
      }
    } else if (key === 'external') {
      if (typeof v === 'boolean') {
        entry.external = v
        continue
      }
    }
    entry._raw[key] = v
  }
  return entry
}

/** 把错误消息的第一行与位置折成结论句（去掉 yaml 自带的多行源码回显）。 */
function parseErrorInfo(err: { message?: string; linePos?: { line: number; col: number }[] }): {
  error: string
  line?: number
  column?: number
} {
  const raw = String(err.message ?? '配置内容不是合法的 YAML')
  const first = raw.split('\n')[0] ?? raw
  const message = first.replace(/\s+at line \d+, column \d+:?\s*$/, '')
  const pos = err.linePos?.[0]
  return { error: message, line: pos?.line, column: pos?.col }
}

/**
 * 解析配置文本。失败分两类：
 *   - 语法错误：给错误位置（YML 模式据此提示）；
 *   - 最外层不是映射：可编辑但表单表达不了，提示切 YML。
 */
export function parseComposeDoc(text: string): ComposeParseResult {
  let doc: ReturnType<typeof parseDocument>
  try {
    doc = parseDocument(text)
  } catch (e) {
    return { ok: false, ...parseErrorInfo(e as { message?: string }) }
  }
  if (doc.errors.length > 0) {
    return { ok: false, ...parseErrorInfo(doc.errors[0]) }
  }
  let raw: unknown
  try {
    raw = doc.toJS() as unknown
  } catch (e) {
    return { ok: false, error: `配置内容无法解析：${(e as Error).message || '嵌套引用过深'}` }
  }
  if (text.trim() !== '' && !isPlainObject(raw)) {
    return { ok: false, error: '配置的最外层不是键值结构，无法按表单编辑；请在 YML 模式编辑' }
  }
  const root = isPlainObject(raw) ? raw : {}
  const rootNode = doc.contents
  const out = emptyComposeDoc()
  out.order = withAllKeys(mapKeyOrder(rootNode) ?? Object.keys(root), root)
  for (const key of out.order) {
    const value = root[key]
    if (key !== 'services' && key !== 'networks' && key !== 'volumes') {
      if (value !== undefined) out._raw[key] = value
      continue
    }
    const sectionNode = childNode(rootNode, key)
    // 段落本身或任一子条目不是映射（`web: null` 这类非法形态）：整段原样进 _raw，
    // 表单不展示也不碰它 —— 把它拆成半个模型才是真正会丢东西的做法。
    const entries = value
    if (!isPlainObject(entries) || Object.values(entries).some((entry) => !isPlainObject(entry))) {
      out._raw[key] = value
      continue
    }
    const entryOrder = withAllKeys(mapKeyOrder(sectionNode) ?? Object.keys(entries), entries)
    const section = out[key] as ComposeSection<ComposeService | ComposeSectionEntry>
    for (const name of entryOrder) {
      const entryValue = entries[name]
      if (!isPlainObject(entryValue)) continue
      const entryNode = childNode(sectionNode, name)
      if (key === 'services') {
        section.items[name] = parseServiceEntry(entryValue, entryNode)
      } else {
        section.items[name] = parseSectionEntry(entryValue, entryNode)
      }
      section.keys.push(name)
    }
    // order 里保留段落的书写位置；条目为空也让序列化知道这个段落原来在哪。
  }
  return { ok: true, doc: out }
}

// ── 序列化：文档模型 → YAML（只服务 YML 模式显示）────────────────────

function readEntryField(
  entry: Record<string, unknown>,
  key: string,
  modeled: readonly string[]
): unknown {
  if (modeled.includes(key)) return entry[key]
  return (entry._raw as Record<string, unknown> | undefined)?.[key]
}

/** 把一个条目折成保序 Map（`_order` 里的键先出，_raw 里漏掉的补齐）。 */
function entryToValue(
  entry: Record<string, unknown>,
  modeled: readonly string[]
): Map<string, unknown> {
  const m = new Map<string, unknown>()
  const order = (entry._order as string[] | undefined) ?? []
  for (const key of order) {
    const v = readEntryField(entry, key, modeled)
    if (v !== undefined) m.set(key, v)
  }
  const raw = (entry._raw as Record<string, unknown> | undefined) ?? {}
  for (const [key, v] of Object.entries(raw)) {
    if (!m.has(key) && v !== undefined) m.set(key, v)
  }
  return m
}

/** 文档模型 → YAML 文本（lineWidth=0：长字符串不折行，源码视图里一行就是一行）。 */
export function serializeComposeDoc(doc: ComposeDoc): string {
  const root = new Map<string, unknown>()
  const emitSection = (name: ComposeSectionName) => {
    const section = doc[name] as ComposeSection<Record<string, unknown>>
    if (section.keys.length === 0) return
    const m = new Map<string, unknown>()
    const modeled = name === 'services' ? SERVICE_FIELD_KEYS : SECTION_FIELD_KEYS
    for (const key of section.keys) {
      const entry = section.items[key]
      if (entry === undefined) continue
      m.set(key, entryToValue(entry, modeled))
    }
    root.set(name, m)
  }
  for (const key of doc.order) {
    if (key === 'services' || key === 'networks' || key === 'volumes') {
      if (!root.has(key)) emitSection(key)
    } else if (key in doc._raw && !root.has(key)) {
      root.set(key, doc._raw[key])
    }
  }
  for (const name of ['services', 'networks', 'volumes'] as const) {
    if (!root.has(name)) emitSection(name)
  }
  for (const [key, value] of Object.entries(doc._raw)) {
    if (!root.has(key)) root.set(key, value)
  }
  if (root.size === 0) return ''
  return stringify(root, { lineWidth: 0 })
}

// ── 变更集：原模型 vs 新模型 → 最小 patch（§9 表单路径）──────────────────

/** patch 的形状：段落 → 名字 → 键值映射（null = 删除该键/整个条目）。 */
export type ComposePatch = Partial<
  Record<ComposeSectionName, Record<string, Record<string, unknown> | null>>
>

function isBlankValue(v: unknown): boolean {
  if (v === undefined || v === null || v === '') return true
  if (Array.isArray(v)) return v.length === 0
  if (isPlainObject(v)) return Object.keys(v).length === 0
  return false
}

/** 条目 → 完整键值映射（建模键 + _raw；空值剔除，patch 里空值没有意义）。 */
export function flattenEntry(
  entry: Record<string, unknown>,
  modeled: readonly string[]
): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const key of modeled) {
    const v = cleanValue(entry[key])
    if (v !== undefined) out[key] = v
  }
  for (const [key, v] of Object.entries((entry._raw as Record<string, unknown>) ?? {})) {
    const cleaned = cleanValue(v)
    if (cleaned !== undefined) out[key] = cleaned
  }
  return out
}

/**
 * 去掉「列表里的空行」与「键为空的环境变量」——表单允许留白行（正在编辑），
 * 但落到 patch/正文里的空项是无效配置；空标量则由 isBlankValue 单独判。
 */
function cleanValue(v: unknown): unknown {
  if (isBlankValue(v)) return undefined
  if (Array.isArray(v)) {
    const arr = v.filter((item) => !(typeof item === 'string' && item.trim() === ''))
    return arr.length > 0 ? arr : undefined
  }
  if (isPlainObject(v)) {
    const out: Record<string, unknown> = {}
    for (const [key, value] of Object.entries(v)) {
      if (key.trim() !== '' && value !== undefined) out[key] = value
    }
    return Object.keys(out).length > 0 ? out : undefined
  }
  return v
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((v, i) => deepEqual(v, b[i]))
  }
  if (isPlainObject(a) && isPlainObject(b)) {
    const ka = Object.keys(a).sort()
    const kb = Object.keys(b).sort()
    return ka.length === kb.length && ka.every((k, i) => k === kb[i] && deepEqual(a[k], b[k]))
  }
  return false
}

/** 两个条目之间的键级差异（删除 = null；无差异 = null）。 */
function diffEntry(
  before: Record<string, unknown>,
  after: Record<string, unknown>
): Record<string, unknown> | null {
  const out: Record<string, unknown> = {}
  const keys = new Set([...Object.keys(before), ...Object.keys(after)])
  for (const key of keys) {
    if (deepEqual(before[key], after[key])) continue
    out[key] = after[key] === undefined ? null : after[key]
  }
  return Object.keys(out).length > 0 ? out : null
}

/**
 * 生成表单路径的最小补丁：只含被改过的键；删除键/条目用 null；
 * 新增条目给出整条字段。无改动返回 null（调用方据此禁用保存）。
 */
export function buildComposePatch(original: ComposeDoc, next: ComposeDoc): ComposePatch | null {
  const patch: ComposePatch = {}
  for (const name of ['services', 'networks', 'volumes'] as const) {
    const before = original[name] as ComposeSection<Record<string, unknown>>
    const after = next[name] as ComposeSection<Record<string, unknown>>
    const modeled = name === 'services' ? SERVICE_FIELD_KEYS : SECTION_FIELD_KEYS
    const entries: Record<string, Record<string, unknown> | null> = {}
    for (const key of after.keys) {
      const nextEntry = after.items[key]
      if (nextEntry === undefined) continue
      const prevEntry = before.items[key]
      if (prevEntry === undefined) {
        entries[key] = flattenEntry(nextEntry, modeled)
      } else {
        const d = diffEntry(flattenEntry(prevEntry, modeled), flattenEntry(nextEntry, modeled))
        if (d) entries[key] = d
      }
    }
    for (const key of before.keys) {
      if (after.items[key] === undefined) entries[key] = null
    }
    if (Object.keys(entries).length > 0) patch[name] = entries
  }
  return Object.keys(patch).length > 0 ? patch : null
}

export function hasComposeChanges(original: ComposeDoc, next: ComposeDoc): boolean {
  return buildComposePatch(original, next) !== null
}

/** 某个服务相对基线是否被改过（表单卡片的两种标注靠它）。 */
export function isServiceModified(original: ComposeDoc, next: ComposeDoc, name: string): boolean {
  const before = original.services.items[name]
  const after = next.services.items[name]
  if (!after) return false
  if (!before) return true
  return (
    diffEntry(flattenEntry(before, SERVICE_FIELD_KEYS), flattenEntry(after, SERVICE_FIELD_KEYS)) !==
    null
  )
}

// ── diff 摘要与明细（保存预览弹窗的文案来源）────────────────────────────

export interface ComposeSectionDiff {
  added: string[]
  removed: string[]
  /** 被改过的条目与它的字段标签（键级）。 */
  modified: { name: string; fields: string[] }[]
}

export interface ComposeDiff {
  services: ComposeSectionDiff
  networks: ComposeSectionDiff
  volumes: ComposeSectionDiff
}

export function diffComposeDocs(original: ComposeDoc, next: ComposeDoc): ComposeDiff {
  const out = {
    services: { added: [], removed: [], modified: [] },
    networks: { added: [], removed: [], modified: [] },
    volumes: { added: [], removed: [], modified: [] }
  } as ComposeDiff
  for (const name of ['services', 'networks', 'volumes'] as const) {
    const before = original[name] as ComposeSection<Record<string, unknown>>
    const after = next[name] as ComposeSection<Record<string, unknown>>
    const modeled = name === 'services' ? SERVICE_FIELD_KEYS : SECTION_FIELD_KEYS
    for (const key of after.keys) {
      const a = after.items[key]
      if (a === undefined) continue
      const b = before.items[key]
      if (b === undefined) {
        out[name].added.push(key)
        continue
      }
      const fa = flattenEntry(b, modeled)
      const fb = flattenEntry(a, modeled)
      const fields = [...new Set([...Object.keys(fa), ...Object.keys(fb)])].filter(
        (k) => !deepEqual(fa[k], fb[k])
      )
      if (fields.length > 0) out[name].modified.push({ name: key, fields })
    }
    for (const key of before.keys) {
      if (after.items[key] === undefined) out[name].removed.push(key)
    }
  }
  return out
}

/** 摘要结论句（spec §8 步骤 b）：如「将新增 1 个网元、移除 1 个网元、修改 2 处」。 */
export function diffSummary(original: ComposeDoc, next: ComposeDoc): string {
  const diff = diffComposeDocs(original, next)
  const parts: string[] = []
  for (const name of ['services', 'networks', 'volumes'] as const) {
    const noun = SECTION_NOUNS[name]
    if (diff[name].added.length > 0) parts.push(`将新增 ${diff[name].added.length} 个${noun}`)
    if (diff[name].removed.length > 0) parts.push(`移除 ${diff[name].removed.length} 个${noun}`)
  }
  const modifiedKeys = (['services', 'networks', 'volumes'] as const).reduce(
    (sum, name) => sum + diff[name].modified.reduce((n, m) => n + m.fields.length, 0),
    0
  )
  if (modifiedKeys > 0) parts.push(`修改 ${modifiedKeys} 处`)
  return parts.length > 0 ? parts.join('、') : '没有改动'
}

/** 变更明细（每条一行）：`redis：新增网元（镜像 redis:7-alpine）`。 */
export function diffDetails(original: ComposeDoc, next: ComposeDoc): string[] {
  const diff = diffComposeDocs(original, next)
  const lines: string[] = []
  for (const name of ['services', 'networks', 'volumes'] as const) {
    const noun = SECTION_NOUNS[name]
    for (const key of diff[name].added) {
      const entry = next[name].items[key] as Record<string, unknown> | undefined
      const image = name === 'services' ? entry?.image : undefined
      lines.push(`${key}：新增${noun}${typeof image === 'string' && image ? `（${image}）` : ''}`)
    }
    for (const key of diff[name].removed) lines.push(`${key}：移除${noun}`)
    for (const m of diff[name].modified) {
      const labels = m.fields.map((f) => FIELD_LABELS[f] ?? f)
      lines.push(`${m.name}：修改 ${labels.join('、')}`)
    }
  }
  return lines
}

// ── 逐行 diff（YML 模式的差异预览）────────────────────────────────────

export interface DiffLine {
  kind: 'same' | 'add' | 'del'
  text: string
}

/** LCS 单测上限：超出后退化成「整段删除 + 整段新增」（预览不卡死比好看重要）。 */
const DIFF_CELL_LIMIT = 1_000_000

function splitLines(text: string): string[] {
  const lines = text.split('\n')
  if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop()
  return lines
}

/** 逐行 diff：先剪公共前后缀，中间做 LCS（超限退化为删除+新增）。 */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = splitLines(before)
  const b = splitLines(after)
  let head = 0
  while (head < a.length && head < b.length && a[head] === b[head]) head++
  let tail = 0
  while (
    tail < a.length - head &&
    tail < b.length - head &&
    a[a.length - 1 - tail] === b[b.length - 1 - tail]
  ) {
    tail++
  }
  const midA = a.slice(head, a.length - tail)
  const midB = b.slice(head, b.length - tail)
  const out: DiffLine[] = []
  for (let i = 0; i < head; i++) out.push({ kind: 'same', text: a[i] })
  if (midA.length * midB.length > DIFF_CELL_LIMIT) {
    for (const text of midA) out.push({ kind: 'del', text })
    for (const text of midB) out.push({ kind: 'add', text })
  } else {
    const n = midA.length
    const m = midB.length
    const dp = new Int32Array((n + 1) * (m + 1))
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        dp[i * (m + 1) + j] =
          midA[i] === midB[j]
            ? dp[(i + 1) * (m + 1) + j + 1] + 1
            : Math.max(dp[(i + 1) * (m + 1) + j], dp[i * (m + 1) + j + 1])
      }
    }
    let i = 0
    let j = 0
    while (i < n && j < m) {
      if (midA[i] === midB[j]) {
        out.push({ kind: 'same', text: midA[i] })
        i++
        j++
      } else if (dp[(i + 1) * (m + 1) + j] >= dp[i * (m + 1) + j + 1]) {
        out.push({ kind: 'del', text: midA[i++] })
      } else {
        out.push({ kind: 'add', text: midB[j++] })
      }
    }
    while (i < n) out.push({ kind: 'del', text: midA[i++] })
    while (j < m) out.push({ kind: 'add', text: midB[j++] })
  }
  for (let i = a.length - tail; i < a.length; i++) out.push({ kind: 'same', text: a[i] })
  return out
}

// ── 注释 token 检测（切换模式前的防丢失确认，spec §8 防线①）─────────────

function commentLineCount(comment: string | undefined | null): number {
  if (!comment) return 0
  return comment.split('\n').filter((line) => line.trim() !== '').length
}

/**
 * 数出文本里的注释行数（切换 YML→表单前弹确认用）。
 *
 * 口径：AST 挂在节点上的注释（commentBefore/comment）逐行计数 —— 引号里的 `#` 与
 * 块标量内容不会被 AST 当成注释；文件末尾的独立注释行不会被挂到任何节点（实测），
 * 故补扫最后一个节点之后的整行注释。语法无效时返回 0（切回表单本来就会被拦）。
 */
export function countYamlComments(text: string): number {
  let doc: ReturnType<typeof parseDocument>
  try {
    doc = parseDocument(text)
  } catch {
    return 0
  }
  if (doc.errors.length > 0) return 0
  let count = 0
  let maxEnd = 0
  const seen = new Set<unknown>()
  try {
    visit(doc, {
      Node(_key, node: YamlNode) {
        if (seen.has(node)) return
        seen.add(node)
        count += commentLineCount(node.commentBefore)
        count += commentLineCount(node.comment)
        const range = node.range
        if (range && range[1] > maxEnd) maxEnd = range[1]
      }
    })
  } catch {
    return 0
  }
  // 尾部注释：文件末尾的独立注释行不会被挂到任何节点（实测），故补扫最后一个节点之后。
  // 残段（maxEnd 落在行中间，如行尾注释所在行的剩余部分）不数 —— 它可能已经作为
  // node.comment 计过一遍；只有「该行 maxEnd 之前只有空白且残段以 # 开头」才是整行注释。
  const tail = text.slice(maxEnd)
  const fragments = tail.split('\n')
  const lineStart = text.lastIndexOf('\n', Math.max(0, maxEnd - 1)) + 1
  const prefix = text.slice(lineStart, maxEnd)
  if (!(prefix.trim() === '' && (fragments[0] ?? '').trimStart().startsWith('#'))) {
    fragments.shift()
  }
  for (const line of fragments) {
    if (/^\s*#/.test(line)) count += 1
  }
  return count
}

// ── 结果载荷解析与展示格式 ───────────────────────────────────────────

export interface ComposeBackup {
  token: string
  hash: string
  sizeBytes: number
  at: number
}

export interface ComposeFileView {
  content: string
  hash: string
  path: string
  backups: ComposeBackup[]
}

/** 解析 compose.file:read/write 的结果载荷（线上键名是 snake_case）。 */
export function parseComposeFilePayload(payload: unknown): ComposeFileView {
  const p = (payload ?? {}) as {
    content?: unknown
    hash?: unknown
    path?: unknown
    backups?: unknown
  }
  const backups: ComposeBackup[] = Array.isArray(p.backups)
    ? p.backups
        .filter((b): b is Record<string, unknown> => isPlainObject(b))
        .map((b) => ({
          token: typeof b.token === 'string' ? b.token : '',
          hash: typeof b.hash === 'string' ? b.hash : '',
          sizeBytes: typeof b.size_bytes === 'number' ? b.size_bytes : 0,
          at: typeof b.at === 'number' ? b.at : 0
        }))
        .filter((b) => b.token !== '')
    : []
  return {
    content: typeof p.content === 'string' ? p.content : '',
    hash: typeof p.hash === 'string' ? p.hash : '',
    path: typeof p.path === 'string' ? p.path : '',
    backups
  }
}

/** 体积格式化（备份列表展示；页面不出现字节级的原始数字）。 */
export function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes < 1024) return `${Math.round(bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
}

/** 备份时刻（unix 秒）→ 本地 `YYYY-MM-DD HH:mm:ss`。 */
export function formatBackupTime(at: number): string {
  const d = new Date(at * 1000)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  )
}

// ── 服务模板（＋添加服务的预填数据，spec §8 模板化）────────────────────

export interface ServiceTemplate {
  key: string
  label: string
  description: string
  service: ComposeService
}

function makeService(fields: Partial<Record<ServiceFieldKey, unknown>>): ComposeService {
  const svc: ComposeService = { _raw: {}, _order: [] }
  for (const key of SERVICE_FIELD_KEYS) {
    const v = fields[key]
    if (v === undefined) continue
    ;(svc as Record<string, unknown>)[key] = v
    svc._order.push(key)
  }
  return svc
}

/** 常用服务模板（redis/mysql/nginx/postgres + 空白）；值只是推荐起点，卡里仍可改。 */
export const SERVICE_TEMPLATES: readonly ServiceTemplate[] = [
  {
    key: 'redis',
    label: 'Redis',
    description: '单实例键值缓存，数据落命名卷',
    service: makeService({
      image: 'redis:7-alpine',
      restart: 'unless-stopped',
      ports: ['6379:6379'],
      volumes: ['redis-data:/data'],
      command: 'redis-server --appendonly yes'
    })
  },
  {
    key: 'mysql',
    label: 'MySQL',
    description: '数据库服务，数据落命名卷；密码请改成自己的',
    service: makeService({
      image: 'mysql:8.0',
      restart: 'unless-stopped',
      ports: ['3306:3306'],
      environment: { MYSQL_ROOT_PASSWORD: 'change-me', MYSQL_DATABASE: 'app' },
      volumes: ['mysql-data:/var/lib/mysql']
    })
  },
  {
    key: 'nginx',
    label: 'Nginx',
    description: '静态站点/反向代理入口',
    service: makeService({
      image: 'nginx:1.27-alpine',
      restart: 'unless-stopped',
      ports: ['80:80'],
      volumes: ['nginx-html:/usr/share/nginx/html']
    })
  },
  {
    key: 'postgres',
    label: 'PostgreSQL',
    description: '数据库服务，数据落命名卷；密码请改成自己的',
    service: makeService({
      image: 'postgres:16-alpine',
      restart: 'unless-stopped',
      ports: ['5432:5432'],
      environment: { POSTGRES_PASSWORD: 'change-me', POSTGRES_DB: 'app' },
      volumes: ['pg-data:/var/lib/postgresql/data']
    })
  },
  {
    key: 'blank',
    label: '空白服务',
    description: '只建一张空卡，字段自己填',
    service: makeService({})
  }
]

function deepClone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** 深拷贝文档模型（基线与编辑副本必须各自独立：改动不能改到基线上）。 */
export function cloneComposeDoc(doc: ComposeDoc): ComposeDoc {
  return deepClone(doc)
}

/** 取模板（深拷贝：卡里改动不污染模板数据）。 */
export function templateService(key: string): ComposeService | null {
  const tpl = SERVICE_TEMPLATES.find((t) => t.key === key)
  return tpl ? deepClone(tpl.service) : null
}

// ── 模型维护助手（保持 keys/_order 与 items 一致）────────────────────────

function normalizeEntry(
  section: ComposeSectionName,
  entry: Record<string, unknown>
): ComposeService | ComposeSectionEntry {
  // 已是解析器产出的模型（模板 service 就带 _raw/_order）：深拷贝即可，不能当输入再解析。
  if (isPlainObject(entry._raw) && Array.isArray(entry._order)) {
    return deepClone(entry) as unknown as ComposeService | ComposeSectionEntry
  }
  const value = isPlainObject(entry) ? entry : {}
  // 复用解析器的规范形判定：不是规范形的键落 _raw（表单不碰）。
  const parsed =
    section === 'services'
      ? parseServiceEntry(value, undefined)
      : parseSectionEntry(value, undefined)
  return parsed
}

/** 新增段落条目（服务/网络/卷同一管线）；同名已存在时覆盖。 */
export function addSectionEntry(
  doc: ComposeDoc,
  section: ComposeSectionName,
  name: string,
  entry: Record<string, unknown>
): void {
  const sec = doc[section] as ComposeSection<ComposeService | ComposeSectionEntry>
  sec.items[name] = normalizeEntry(section, entry)
  if (!sec.keys.includes(name)) sec.keys.push(name)
}

/** 删除段落条目（保存走 patch 的 null 语义）。 */
export function removeSectionEntry(
  doc: ComposeDoc,
  section: ComposeSectionName,
  name: string
): void {
  const sec = doc[section] as ComposeSection<ComposeService | ComposeSectionEntry>
  delete sec.items[name]
  sec.keys = sec.keys.filter((k) => k !== name)
}

/** 写服务字段（空值 = 删除该键）；同步 `_order`。 */
export function setServiceField(svc: ComposeService, key: ServiceFieldKey, value: unknown): void {
  const target = svc as unknown as Record<string, unknown>
  if (isBlankValue(value)) {
    delete target[key]
    svc._order = svc._order.filter((k) => k !== key)
    return
  }
  target[key] = value
  if (!svc._order.includes(key)) svc._order.push(key)
}

/** 写网络/卷字段（空值 = 删除该键）；同步 `_order`。 */
export function setSectionField(
  entry: ComposeSectionEntry,
  key: SectionFieldKey,
  value: unknown
): void {
  const target = entry as unknown as Record<string, unknown>
  if (isBlankValue(value)) {
    delete target[key]
    entry._order = entry._order.filter((k) => k !== key)
    return
  }
  target[key] = value
  if (!entry._order.includes(key)) entry._order.push(key)
}

/** 名字去重：`redis` → `redis-2` → `redis-3`…（新增服务的默认名）。 */
export function uniqueServiceName(doc: ComposeDoc, base: string): string {
  if (!doc.services.items[base]) return base
  let n = 2
  while (doc.services.items[`${base}-${n}`]) n++
  return `${base}-${n}`
}

const SERVICE_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/

/**
 * 保存前的模型体检（结论句列表，空 = 通过）。
 *
 * 只查表单能发现的两类：服务名形态、以及「既没有镜像也没有构建配置」的空服务
 * （模板空白卡未填时会被它拦住，而不是把无效内容送去让 compose 拒绝）。
 */
export function composeModelIssues(doc: ComposeDoc): string[] {
  const issues: string[] = []
  for (const name of doc.services.keys) {
    if (!SERVICE_NAME_RE.test(name)) {
      issues.push(`网元名「${name}」不符合命名规则（字母或数字开头，可含 . _ -）`)
    }
    const svc = doc.services.items[name]
    if (!svc) continue
    const hasImage = typeof svc.image === 'string' && svc.image !== ''
    const build = svc._raw?.build
    const hasBuild = build !== undefined && build !== null && build !== ''
    if (!hasImage && !hasBuild) {
      issues.push(`网元「${name}」还没有填镜像（或构建配置），保存前请先填写`)
    }
  }
  return issues
}
