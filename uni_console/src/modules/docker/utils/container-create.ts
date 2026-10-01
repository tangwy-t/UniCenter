/**
 * container:create（四支柱·创建面，4a）的表单模型、逐字段校验与载荷/命令生成（纯函数）。
 *
 * ── 单一事实源在哪 ──────────────────────────────────────────────────
 * 跨语言无法 import，故这里的每条规则**逐字镜像**协议 `uni_protocol/docker.go` 的
 * `validateDockerContainerCreate`（约 615-679 行）与它引用的正则/常量：
 *   - 镜像引用 `dockerImageRefRe`、名字 `dockerNameRe`（§「名字/引用校验」段）；
 *   - 端口 `dockerPortBindRe`（宿主位 `0` 刻意不进白名单）+ 数值范围判定；
 *   - env `dockerEnvRe` + 整条 ≤ `MaxDockerStringBytes`（512B）；
 *   - mounts 的 SplitN(":") 形态判定（源 = 命名卷或绝对路径，模式只认 ro）；
 *   - 条数上限 `maxDockerCreateListItems`（32）、CPU `maxDockerCPULimit`（32 核）、
 *     内存 `maxDockerMemLimitMB`（32768MB）。
 * 两份规则若漂移，`__tests__/container-create.test.ts` 的正反例（与协议测试同一条
 * 边界）会红灯；发现不一致时**以协议为准**并回来改这里。
 *
 * ── 为什么校验收在纯函数而不是 ElForm rules ─────────────────────────
 * 校验规则是协议的镜像（不是 UI 的自由裁量），把它写进组件会让「对齐协议」变成
 * 模板里的散落断言；收在这里，组件只做「何时显示哪条结论」的展示决策。
 */
import type { DockerActionOptions } from './actions'

/** 端口/env/mounts 各自的条目上限（协议 maxDockerCreateListItems：挡畸形巨数组，表单用不到几十条）。 */
export const MAX_CREATE_LIST_ITEMS = 32
/** cpu_limit 的核数上限（协议 maxDockerCPULimit：0 = 不限额，32 只是拦手滑）。 */
export const MAX_CPU_LIMIT = 32
/** mem_limit_mb 的上限（协议 maxDockerMemLimitMB：0 = 不限额，32GB 与 CPU 的 32 核同档）。 */
export const MAX_MEM_LIMIT_MB = 32 * 1024
/** env/mounts 单条的字节上限（协议 MaxDockerStringBytes：更长的值要么误输要么另有企图）。 */
export const MAX_ENTRY_BYTES = 512

/** 重启策略枚举（协议 restartPolicySet；'' = 缺席，由 docker 默认 no 接住）。 */
export const RESTART_POLICIES = ['no', 'on-failure', 'always', 'unless-stopped'] as const
export type RestartPolicy = (typeof RESTART_POLICIES)[number]

/** 端口行（结构化编辑；拼装成协议形态 `宿主:容器[/协议]` 在 buildCreateOptions 里做一次）。 */
export interface CreatePortRow {
  host: string
  container: string
  proto: 'tcp' | 'udp'
}

/** 环境变量行：`KEY=VALUE` 的两列（值可为空串 —— 协议 dockerEnvRe 允许 `KEY=`）。 */
export interface CreateEnvRow {
  key: string
  value: string
}

/** 挂载行：源（命名卷或绝对路径）+ 目的地 + 只读勾选（协议模式位只有 ro）。 */
export interface CreateMountRow {
  source: string
  dest: string
  ro: boolean
}

/** 创建抽屉的表单模型（草稿态；空行/空串 = 不发该字段）。 */
export interface CreateContainerForm {
  image: string
  name: string
  restartPolicy: '' | RestartPolicy
  /** 创建后是否立即启动（协议 start 缺省 = true；false = 只创建不启动）。 */
  start: boolean
  ports: CreatePortRow[]
  env: CreateEnvRow[]
  mounts: CreateMountRow[]
  /** CPU 核数；null = 留空（不发字段 = 不限额）。 */
  cpuLimit: number | null
  /** 内存 MB；null = 留空（不发字段 = 不限额）。 */
  memLimitMb: number | null
  network: string
}

/** 全空的表单（开抽屉/换主机重置用）。 */
export function emptyCreateForm(): CreateContainerForm {
  return {
    image: '',
    name: '',
    restartPolicy: '',
    start: true,
    ports: [],
    env: [],
    mounts: [],
    cpuLimit: null,
    memLimitMb: null,
    network: ''
  }
}

// ── 协议正则的前端镜像（逐字照抄，含「0 与前导零不合法」这类刻意收紧）──────────

/** 镜像引用：仓库路径 + 可选 :tag + 可选 @sha256:digest（协议 dockerImageRefRe）。 */
const IMAGE_REF_RE = /^[a-zA-Z0-9][a-zA-Z0-9/._:@-]*$/
/** 容器名（协议 dockerNameRe：首字符字母数字，其余 [A-Za-z0-9_.-]）。 */
const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/
/** 端口位：`[1-9][0-9]{0,4}`（协议 dockerPortBindRe 的捕获组同款 —— 0 与前导零都不合法）。 */
const PORT_RE = /^[1-9][0-9]{0,4}$/
/** env 键：POSIX 风格标识符（协议 dockerEnvRe 的键段）。 */
const ENV_KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/

/**
 * NUL/换行的存在性判定（协议对 env 值与挂载路径的硬约束：NUL 炸协议字符串、
 * 换行污染结果展示）。用 includes 而不是正则 —— eslint 的 no-control-regex 会把
 * 控制字符写进正则当错误（规则本意是防「看不见的匹配」，includes 没有这层歧义）。
 */
function hasNulOrNewline(s: string): boolean {
  return s.includes('\x00') || s.includes('\n')
}

/**
 * 字符串的 UTF-8 字节数（协议的 len() 是字节数：中文值每字 3 字节，按字符数判会漏）。
 * 不引入 TextEncoder —— 测试环境有没有它不该成为校验的前置条件（本模块既有取向）。
 */
function byteLength(s: string): number {
  let n = 0
  for (const ch of s) {
    const c = ch.codePointAt(0) ?? 0
    n += c <= 0x7f ? 1 : c <= 0x7ff ? 2 : c <= 0xffff ? 3 : 4
  }
  return n
}

/** 端口行是否已填（一侧有值就算「填了」—— 半行是错误而不是静默丢弃）。 */
function portRowFilled(row: CreatePortRow): boolean {
  return row.host !== '' || row.container !== ''
}

/** env 行是否已填：键有值就算（值可为空串，协议允许 `KEY=`）。 */
function envRowFilled(row: CreateEnvRow): boolean {
  return row.key !== ''
}

/** 挂载行是否已填（源或目的地有值；半行是错误）。 */
function mountRowFilled(row: CreateMountRow): boolean {
  return row.source !== '' || row.dest !== ''
}

/** 校验一条端口映射（镜像协议 validateDockerPortBind：正则 + 数值范围双关）。 */
function portIssue(row: CreatePortRow): string {
  if (!portRowFilled(row)) return ''
  if (row.host === '' || row.container === '') return '宿主与容器端口都要填'
  if (!PORT_RE.test(row.host) || !PORT_RE.test(row.container)) {
    return '端口须为 1-65535 的数字（不支持 0 与前导零）'
  }
  if (Number(row.host) > 65535 || Number(row.container) > 65535) {
    return '端口须为 1-65535（当前超出范围）'
  }
  return ''
}

/** 校验一条环境变量（镜像协议 validateDockerEnv：键形态 + 值无换行 + 整条 ≤512B）。 */
function envIssue(row: CreateEnvRow): string {
  if (!envRowFilled(row)) return row.value !== '' ? '变量名没填' : ''
  if (!ENV_KEY_RE.test(row.key)) return '变量名须以字母或下划线开头（仅字母/数字/下划线）'
  if (hasNulOrNewline(row.value)) return '变量值不能包含换行'
  if (byteLength(`${row.key}=${row.value}`) > MAX_ENTRY_BYTES) {
    return `整条（键=值）不能超过 ${MAX_ENTRY_BYTES} 字节`
  }
  return ''
}

/**
 * 校验一条挂载（镜像协议 validateDockerMount：SplitN 三段、目的地绝对路径、
 * 源 = 命名卷（dockerNameRe）或绝对路径、模式只认 ro —— ro 在表单里是勾选框，
 * 结构上不可能给出别的模式位，故模式错误在前端不可达）。
 */
function mountIssue(row: CreateMountRow): string {
  if (!mountRowFilled(row)) return ''
  if (row.source === '') return '挂载源没填'
  if (row.dest === '') return '目的地没填'
  // 源的两种合法形态：绝对路径（bind）或命名卷（isDockerName）—— 以是否 '/' 开头分辨。
  if (row.source.startsWith('/')) {
    if (hasNulOrNewline(row.source)) return '宿主路径不能包含换行'
  } else if (!NAME_RE.test(row.source)) {
    return '源须为命名卷（字母/数字/._-）或以 / 开头的宿主路径'
  }
  if (!row.dest.startsWith('/') || hasNulOrNewline(row.dest)) {
    return '目的地必须是 / 开头的绝对路径'
  }
  if (byteLength(`${row.source}:${row.dest}${row.ro ? ':ro' : ''}`) > MAX_ENTRY_BYTES) {
    return `整条挂载不能超过 ${MAX_ENTRY_BYTES} 字节`
  }
  return ''
}

/** 表单逐字段校验的结论集合（每条是给用户看的结论句；空串 = 没问题）。 */
export interface CreateFormIssues {
  image: string
  name: string
  /** 逐行结论（与表单行一一对应；空串 = 该行没问题或为空行）。 */
  ports: string[]
  env: string[]
  mounts: string[]
  /** 重复键：值非空串表示存在重复（docker 会用后者覆盖前者 —— 几乎必然是手误）。 */
  envDuplicate: string
  /** 条数上限的区块级结论（超 32 条时给在区块头，而不是塞进某一行）。 */
  portsCount: string
  envCount: string
  mountsCount: string
  cpuLimit: string
  memLimitMb: string
}

/**
 * 逐字段校验（**不发指令**的本地闸：协议层还有第二道，这里挡的是「发出去必被拒」
 * 的形态错误，让用户在输入框旁即时看到结论）。
 *
 * `imageExists` 是「该镜像是否已在所选主机本地」的检查器（快照里算好的事实）：
 * 镜像缺失是 create 的头号失败原因（agent 不自动拉取，结论句就是「先拉取」），
 * 前端提前给同一句引导。快照未到/拉取失败时调用方应返回 true —— 宁可放行让
 * agent 给结论句，也不把「还没读到清单」误报成「主机上没有」。
 */
export function validateCreateForm(
  form: CreateContainerForm,
  imageExists?: (ref: string) => boolean
): CreateFormIssues {
  const image = form.image.trim()
  let imageIssue = ''
  if (image === '') imageIssue = '镜像必填'
  else if (!IMAGE_REF_RE.test(image)) imageIssue = '不是合法的镜像引用（仓库[:标签] 或 带 @摘要）'
  else if (imageExists && !imageExists(image)) {
    imageIssue = '该主机本地没有这个镜像，请先在该主机拉取后再创建'
  }

  const name = form.name.trim()
  const nameIssue =
    name === '' ? '' : NAME_RE.test(name) ? '' : '容器名须以字母数字开头（仅字母/数字/._-）'

  const filledPorts = form.ports.filter(portRowFilled)
  const filledEnv = form.env.filter(envRowFilled)
  const filledMounts = form.mounts.filter(mountRowFilled)

  // 重复键：协议不禁止（daemon 语义 = 后者覆盖前者），但 UI 上两个同名变量
  // 几乎必然是手误 —— 这里拦下来并说出覆盖语义，比静默丢一个值诚实。
  const seen = new Set<string>()
  const dup = new Set<string>()
  for (const row of filledEnv) {
    if (seen.has(row.key)) dup.add(row.key)
    seen.add(row.key)
  }
  const envDuplicate = dup.size ? `变量 ${[...dup].join('、')} 重复定义（后值会覆盖前值）` : ''

  const overLimit = (n: number) => `最多 ${MAX_CREATE_LIST_ITEMS} 条，当前 ${n} 条`

  const cpu = form.cpuLimit
  const cpuIssue =
    cpu == null || cpu === 0
      ? ''
      : Number.isNaN(cpu) || cpu < 0 || cpu > MAX_CPU_LIMIT
        ? `CPU 上限须在 0-${MAX_CPU_LIMIT} 核之间（0 = 不限额）`
        : ''
  const mem = form.memLimitMb
  const memIssue =
    mem == null || mem === 0
      ? ''
      : Number.isNaN(mem) || mem < 0 || mem > MAX_MEM_LIMIT_MB
        ? `内存上限须在 0-${MAX_MEM_LIMIT_MB} MB 之间（0 = 不限额）`
        : ''

  return {
    image: imageIssue,
    name: nameIssue,
    ports: form.ports.map(portIssue),
    env: form.env.map(envIssue),
    mounts: form.mounts.map(mountIssue),
    envDuplicate,
    portsCount: filledPorts.length > MAX_CREATE_LIST_ITEMS ? overLimit(filledPorts.length) : '',
    envCount: filledEnv.length > MAX_CREATE_LIST_ITEMS ? overLimit(filledEnv.length) : '',
    mountsCount: filledMounts.length > MAX_CREATE_LIST_ITEMS ? overLimit(filledMounts.length) : '',
    cpuLimit: cpuIssue,
    memLimitMb: memIssue
  }
}

/** 是否存在任何待解问题（提交闸：有问题的表单不进确认弹窗）。 */
export function hasCreateIssue(issues: CreateFormIssues): boolean {
  return (
    issues.image !== '' ||
    issues.name !== '' ||
    issues.envDuplicate !== '' ||
    issues.portsCount !== '' ||
    issues.envCount !== '' ||
    issues.mountsCount !== '' ||
    issues.cpuLimit !== '' ||
    issues.memLimitMb !== '' ||
    issues.ports.some((s) => s !== '') ||
    issues.env.some((s) => s !== '') ||
    issues.mounts.some((s) => s !== '')
  )
}

/**
 * 表单 → 指令 options（**平铺、只发已填字段**）。
 *
 * 形状对齐协议 DockerCmdOptions 的 create 专属字段（json tag 逐字：restart_policy/
 * mem_limit_mb 是 snake_case；0/空 = 不发 —— Go 侧 omitempty 与「0 = 缺席语义」
 * 是同一件事，显式发 0 不会改变行为，但会破坏「载荷里只有用户真实给出的决定」
 * 这一可读性约定）。`start` 只在**显式 false** 时发：缺席 = 创建并启动（协议
 * Start 是指针，缺席与 false 不同义 —— true 不需要发）。
 */
export function buildCreateOptions(form: CreateContainerForm): DockerActionOptions {
  const options: DockerActionOptions = {}
  options.image = form.image.trim()
  const name = form.name.trim()
  if (name !== '') options.name = name
  const ports = form.ports
    .filter(portRowFilled)
    .map((r) => `${r.host}:${r.container}${r.proto === 'udp' ? '/udp' : ''}`)
  if (ports.length) options.ports = ports
  const env = form.env.filter(envRowFilled).map((r) => `${r.key}=${r.value}`)
  if (env.length) options.env = env
  const mounts = form.mounts
    .filter(mountRowFilled)
    .map((r) => `${r.source}:${r.dest}${r.ro ? ':ro' : ''}`)
  if (mounts.length) options.mounts = mounts
  // '' = 缺席（docker 默认 no）；'no' 与缺席同语义，表单里 '' 就是那个选项。
  if (form.restartPolicy !== '') options.restart_policy = form.restartPolicy
  if (form.cpuLimit && form.cpuLimit > 0) options.cpu_limit = form.cpuLimit
  if (form.memLimitMb && form.memLimitMb > 0) options.mem_limit_mb = form.memLimitMb
  if (form.network !== '') options.network = form.network
  if (form.start === false) options.start = false
  return options
}

/** 参数含空白时给 shell 引号（预览只是给高级用户核对的等价命令，不是可执行产物）。 */
function shellQuote(arg: string): string {
  return /\s/.test(arg) ? `'${arg}'` : arg
}

/**
 * 表单 → 等效 `docker run` 命令（只读预览）。
 *
 * 为什么给预览：高级用户对「表单里的十几个字段会变成什么」的既有心智模型就是
 * docker run —— 一行命令比一张表单更容易核对漏项。**未填的项不出现在命令里**
 * （与载荷同一取舍：预览与协议指令必须一一对应，多一个少一个都是误导）。
 * start=false 的等价物是 `docker create`（docker CLI 里「只创建不启动」的写法）。
 */
export function buildRunPreview(form: CreateContainerForm): string {
  const parts: string[] = [form.start ? 'docker run' : 'docker create']
  const name = form.name.trim()
  if (name !== '') parts.push(`--name ${shellQuote(name)}`)
  for (const r of form.ports) {
    if (!portRowFilled(r)) continue
    parts.push(`-p ${r.host}:${r.container}${r.proto === 'udp' ? '/udp' : ''}`)
  }
  for (const r of form.env) {
    if (!envRowFilled(r)) continue
    parts.push(`-e ${shellQuote(`${r.key}=${r.value}`)}`)
  }
  for (const r of form.mounts) {
    if (!mountRowFilled(r)) continue
    parts.push(`-v ${shellQuote(`${r.source}:${r.dest}${r.ro ? ':ro' : ''}`)}`)
  }
  // '' = 不写（docker 默认 no）；其余照抄枚举值（协议与 CLI 的取值同集）。
  if (form.restartPolicy !== '') parts.push(`--restart ${form.restartPolicy}`)
  if (form.cpuLimit && form.cpuLimit > 0) parts.push(`--cpus ${form.cpuLimit}`)
  if (form.memLimitMb && form.memLimitMb > 0) parts.push(`--memory ${form.memLimitMb}m`)
  if (form.network !== '') parts.push(`--network ${shellQuote(form.network)}`)
  const image = form.image.trim()
  if (image !== '') parts.push(shellQuote(image))
  return parts.join(' ')
}

/**
 * create 成功载荷的解析（协议 createPayload：id/short_id/started，snake_case）。
 * 形状不符时给空结论而不是抛错 —— 载荷来自 agent，页面不该因为一次形状意外白屏。
 */
export interface CreateSuccessInfo {
  id: string
  shortId: string
  started: boolean
}

export function parseCreatePayload(payload: unknown): CreateSuccessInfo {
  const p = (payload ?? {}) as Partial<{ id: unknown; short_id: unknown; started: unknown }>
  return {
    id: typeof p.id === 'string' ? p.id : '',
    shortId: typeof p.short_id === 'string' ? p.short_id : '',
    started: p.started === true
  }
}
