/**
 * 六期（活动流）跨主机 docker 事件流的纯逻辑：NDJSON 行解析、按时间排序的显示
 * 窗口（上限丢最早）、重连回放的去重守卫、action 中文化映射、秒级相对时间、
 * 四类资源的图标与语义色槽位。
 *
 * 本波（事件增强）新增四件，全部落在这一份纯逻辑里：
 *   - **退出码**（die 事件的事实：0 = 自己人停的、≠0 = 异常退出；nil = 不可考，
 *     显示层不显示不猜）—— 流行与历史行两个来源都要归一化成同一个字段；
 *   - **历史查询的解析与合并**（/docker/events/history 的 DTO 行 → 同一个
 *     DockerEventFields；createEventsLog 把「先历史、后实时」拼成一条 t 降序的
 *     时间轴，跨两个来源按内容复合键去重）；
 *   - **去重键 eventsDedupKey**（事件没有天然唯一 id，见那里的论证）；
 *   - **过滤谓词 matchesEventFilter**（与 core 的 keyword 同一把尺，实时行在
 *     客户端按它过滤 —— 流本身是全量的，页面筛了就得自己把不合的行拦下）。
 *
 * 为什么单独一份而不是塞进 utils/stream.ts：那里是「文本行 / 帧解码」的通道层
 * （日志/终端共用），这里是「结构化事件条目」的消费层 —— 行形状、缓冲形态
 * （按时间排序的对象窗口 vs 文本行窗口）、去重守卫都不同，混在一起两头都不纯
 * （与 utils/stats.ts 单独成文的理由同一口径）。
 *
 * 行形状（core 的 docker_events.go 的 eventNDJSONLine，字段打平、归属由 core 注入）：
 *   {"host_id":"<雪花 id 字符串>","hostname":"s","t":unix毫秒,"type":"container",
 *    "action":"start","actor_name":"s","actor_id":"s","exit_code":n}
 * host_id 是**字符串**（全站雪花 id 编码纪律；数字形态在 JS 里会丢精度，见
 * parseEventLine 的归一化注释）。
 *
 * 流的**首帧是 sentinel** `{"kind":"opened"}`（流建立即发：让响应头立刻穿过
 * dev 代理，否则空窗口时页面会停在「连接中…」直到第一条事件）。解析层对它
 * 返回 null（形状不符 → 跳过），这是全 NDJSON 族既有的容忍纪律，见 parseEventLine。
 *
 * 「t 是毫秒」不是猜的：协议侧的 DockerEventItem.T 就是 UnixMilli（agent 在
 * copyEvents 里给接收时刻打戳，口径与 stats 样本的 T 一致），core 只做归属
 * 注入、原样透传。**下断言前先看这条** —— 曾经这里写的是「unix 秒」，于是活动流
 * 把毫秒当秒减，「多久之前」恒显示「刚刚」（QA 路 1 P2 实测）；消费端要相对时间
 * 时必须先除 1000（见 overview-events-feed 的行模型）。
 */
import { createNdjsonParser } from './stream'

/**
 * 显示上限（条）：活动流是「开着看」的面板，不是审计账本 —— 无限累积会让内存与
 * 重渲染成本随挂机时长线性放大；100 条在 24rem 的滚动区里约 3 屏，足够回看
 * 「刚才发生了什么」，更久的历史属于日志系统，不属于这条流。
 */
export const MAX_EVENT_ENTRIES = 100

/**
 * 详版页（/docker/events）的时间轴上限（条）：整页列表比面板耐读，翻页又由
 * 「加载更早」接力；上限取 **core 保留窗口的单主机容量**（RetainDepth = 500）——
 * 它只是**渲染上限**（多主机聚合的时间轴可以更长，翻页与窗口决定实际有多少），
 * 取这个数是因为：比它更小会让「翻到底」变成假动作（窗口里明明还有），比它更大
 * 只是把挂机时的渲染成本往上抬，换不到新的可读性。
 */
export const MAX_EVENT_LOG_ENTRIES = 500

/** 一行事件的载荷（不含到达序号 —— seq 是 feed 的事，见 createEventsFeed）。 */
export interface DockerEventFields {
  /** 归属主机 id（core 注入；显示兜底用）。 */
  hostId: string
  /** 归属主机名（core 注入，清单里叫什么就是什么）。 */
  hostname: string
  /** 事件时刻（unix 毫秒，见文件头「t 是毫秒」；agent 打戳、core 透传）。 */
  t: number
  /** 资源类型：container / image / volume / network（其它值按未知兜底）。 */
  type: string
  /** 动作名（docker events 的 Action，小写下划线形态）。 */
  action: string
  /** 动作者名（容器名 / 镜像名 / 网络名；可为空）。 */
  actorName: string
  /** 动作者 id（容器是 sha256:… 形态；可为空）。 */
  actorId: string
  /**
   * 退出码（die 事件的事实；**nil → null**，两个来源都归一化成这个形态）。
   *
   * 三值语义与协议 DockerEventItem.ExitCode 逐字一致：0 = 自己人停的、≠0 =
   * 异常退出、null = 不可考（非 die / 旧 agent / daemon 没给）—— null 一律
   * 不显示、不猜（把正常的 stop 显示成「exit 0」是编造，把不可考显示成红色是吓人）。
   */
  exitCode: number | null
}

/**
 * 事件条目：载荷 + 到达序号。序号是组件 `:key` 的唯一来源 —— 事件本身没有稳定
 * id（同名容器同一秒可以有多条事件），靠内容拼 key 会撞，靠内容+下标会因排序
 * 而漂移；单调递增的到达序号两者都躲开。
 */
export interface DockerEventItem extends DockerEventFields {
  seq: number
}

/**
 * 内容复合去重键：**跨「历史查询」与「实时流」两个来源**判定「这条已经在了」。
 *
 * 事件没有天然唯一 id（daemon 不给，core 的到达序号是进程寿命的计数、重启归零，
 * 见 dockerevents.retained 的论证），所以键只能由事实本身拼：归属主机 + 时刻
 * （毫秒）+ 类型 + 动作原文 + 主体 id（**id 优先、名字兜底** —— 同名容器可以
 * 先后存在，id 才是身份；只有名字的条目（daemon 没给 id）用名字兜底，宁重不漏
 * 反过来是不漏真事）。
 *
 * 为什么这五项够：历史端点与实时流读的是**同一个保留窗口里的同一条记录**
 * （同一次 JSON 解析出的 T/Type/Action/ActorID），逐位相同 —— 两处对同一条事件
 * 的键必然相等。而两条**真实不同**的事件要撞键，得同主机、同毫秒、同类型、同
 * 动作、同主体：daemon 的毫秒戳下这已近乎「同一条」的语义（同容器同毫秒同动作
 * 再来一条，用户看到一条也不丢信息）。
 */
export function eventsDedupKey(f: DockerEventFields): string {
  return `${f.hostId}:${f.t}:${f.type}:${f.action}:${f.actorId || f.actorName}`
}

/**
 * 解析一行事件流；形状不符给 null（调用方跳过这一行，不打断整条流 —— 与
 * parseLogStreamLine / parseStatsStreamLine 同一纪律：丢一行只让流缺一格，
 * 杀掉整条流则是杀了全部数据）。
 *
 * **首帧 sentinel 的容忍是刻意的**（`{"kind":"opened"}`）：它没有 host_id，形状
 * 判据第一关就把它判 null 跳过 —— 这里显式认一下，是为了让「跳过 sentinel」在
 * 代码里是一个**被想到的决定**而不是巧合（协议日后若给 sentinel 添字段，也不会
 * 意外撞进事件行）。
 */
export function parseEventLine(line: string): DockerEventFields | null {
  let raw: unknown
  try {
    raw = JSON.parse(line)
  } catch {
    return null
  }
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return null
  const o = raw as Record<string, unknown>
  if (o.kind === 'opened') return null // 流首帧 sentinel：不是事件，跳过
  // 归属与时刻是渲染的最小充分集：缺一就画不出「哪台机、什么时候、发生了什么」。
  // host_id 收**字符串与数字两种形态**：core 现在发字符串（雪花 id > 2^53，JSON
  // 数字在 JS 里会丢精度 —— 实测 2105604795992641536 会变成 …641500，于是与历史
  // DTO 的 string hostId 拼出的去重键永不相等，列表当场出现双份），数字形态是
  // 老格式/测试夹具的兼容输入，一并归一化成字符串。
  const hostId =
    typeof o.host_id === 'number' && Number.isFinite(o.host_id)
      ? String(o.host_id)
      : typeof o.host_id === 'string' && o.host_id !== ''
        ? o.host_id
        : null
  if (hostId === null) return null
  if (typeof o.t !== 'number' || !Number.isFinite(o.t) || o.t <= 0) return null
  if (typeof o.type !== 'string' || o.type === '') return null
  if (typeof o.action !== 'string' || o.action === '') return null
  if (typeof o.hostname !== 'string') return null
  // actor 两字段是 omitempty：缺省按空串，出现却不是串则形状不对。
  if (o.actor_name !== undefined && typeof o.actor_name !== 'string') return null
  if (o.actor_id !== undefined && typeof o.actor_id !== 'string') return null
  // exit_code 同理：缺省（非 die / 旧 agent）按 null，出现却不是数则形状不对。
  if (o.exit_code !== undefined && o.exit_code !== null && typeof o.exit_code !== 'number') {
    return null
  }
  return {
    hostId,
    hostname: o.hostname,
    t: o.t,
    type: o.type,
    action: o.action,
    actorName: typeof o.actor_name === 'string' ? o.actor_name : '',
    actorId: typeof o.actor_id === 'string' ? o.actor_id : '',
    exitCode: typeof o.exit_code === 'number' ? o.exit_code : null
  }
}

/**
 * 历史查询的一行（core 的 DockerEventHistoryItem）→ 同一个 DockerEventFields。
 *
 * 与流行**同一份事实、两种编码**（DTO 走 camelCase + hostId 的 string 编码，
 * 流行是打平的 snake_case），归一化只在这一个函数里做一次 —— 列表与合并逻辑
 * 因此不必知道「这一条是从哪条通道来的」。
 *
 * 形状不符给 null（跳过该行）：历史是服务端已经校验过的数据，坏行的唯一可能
 * 是编解码漂移，跳过一行好过整页报错（与流行同纪律）。
 */
export function parseHistoryItem(raw: unknown): DockerEventFields | null {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return null
  const o = raw as Record<string, unknown>
  // hostId 是 `json:"hostId,string"`：core 给字符串形态的雪花 id（全站纪律）。
  if (typeof o.hostId !== 'string' || o.hostId === '') return null
  const t = typeof o.t === 'number' ? o.t : NaN
  if (!Number.isFinite(t) || t <= 0) return null
  if (typeof o.type !== 'string' || o.type === '') return null
  if (typeof o.action !== 'string' || o.action === '') return null
  if (typeof o.hostname !== 'string') return null
  if (o.actorName !== undefined && o.actorName !== null && typeof o.actorName !== 'string') {
    return null
  }
  if (o.actorId !== undefined && o.actorId !== null && typeof o.actorId !== 'string') return null
  if (o.exitCode !== undefined && o.exitCode !== null && typeof o.exitCode !== 'number') {
    return null
  }
  return {
    hostId: o.hostId,
    hostname: o.hostname,
    t,
    type: o.type,
    action: o.action,
    actorName: typeof o.actorName === 'string' ? o.actorName : '',
    actorId: typeof o.actorId === 'string' ? o.actorId : '',
    exitCode: typeof o.exitCode === 'number' ? o.exitCode : null
  }
}

/**
 * 事件过滤谓词：主机 / 类型 / 关键字（空值 = 不过滤），三项独立、逐层收窄。
 *
 * 历史行由服务端过滤（端点参数），实时行由这个谓词在客户端过滤 —— 流本身是
 * 全量的（core 不知道页面在筛什么），不拦的话筛出来的列表会被不匹配的新事件
 * 污染。关键词的命中面与 core 的 matchesKeyword **逐字同款**（主体名 / 主体 id /
 * 动作原文，大小写不敏感子串）：两边同一把尺，同一个筛选在历史段与实时段不会
 * 捞出两套结果。
 */
export interface DockerEventFilter {
  /** 限定单主机（空串 = 全部主机）。 */
  hostId?: string
  /** 限定资源类型（空串 = 全部）。 */
  type?: string
  /** 关键字（主体名 / 主体 id / 动作原文子串，大小写不敏感）。 */
  keyword?: string
}

export function matchesEventFilter(f: DockerEventFields, filter: DockerEventFilter): boolean {
  if (filter.hostId && f.hostId !== filter.hostId) return false
  if (filter.type && f.type !== filter.type) return false
  const kw = (filter.keyword ?? '').trim().toLowerCase()
  if (kw === '') return true
  return (
    f.actorName.toLowerCase().includes(kw) ||
    f.actorId.toLowerCase().includes(kw) ||
    f.action.toLowerCase().includes(kw)
  )
}

/**
 * action 词元 → 中文文案的**完备**映射表：逐词条对齐协议的动作白名单
 * （uni_protocol/docker.go 的 dockerEventActions，36 词条）。「类型白名单
 * （container|image|volume|network）+ 动作白名单」合起来划定了可到达动作的
 * 上界，而这里按上界收全（不分类型）—— 表内不设缺口，活动流就不存在「直显
 * 英文」的漏网。测试逐词条钉住（含集合相等断言），协议扩词而这里忘跟会被
 * events.test.ts 直接拦下。
 *
 * 「词表 ≠ 投递清单」的两种形态各有一处对应：
 *   - exec 族（exec_create/exec_detach/exec_die/exec_start）在 agent 侧整族
 *     过滤（信噪比 + 命令原文泄漏面，见 copyEvents 注释），正常帧不会到达；
 *     这里仍给全映射 —— 词表是校验契约，前端跟着契约走，不跟着投递现状走。
 *   - 表外的 daemon 原生动作（image 的 untag/delete、network 的 connect/
 *     disconnect）不在协议白名单内，两道校验都会拦（agent Validate 丢弃并留痕、
 *     core 整行丢弃），永远到不了这里 —— 它们不是「漏网」，是上游就不放行，
 *     故不收录（收录反而暗示可达）。未映射动作的兜底仍是原样透传（见
 *     eventActionText），为的是协议日后扩词时读者看到原始词而不是空白或误译。
 */
export const EVENT_ACTION_TEXT: Readonly<Record<string, string>> = {
  // 容器（词表原文顺序不打乱，方便与 docker.go 对照）
  attach: '挂接',
  commit: '提交',
  copy: '复制',
  create: '创建',
  destroy: '销毁',
  detach: '分离',
  die: '退出',
  exec_create: '准备执行',
  exec_detach: '执行分离',
  exec_die: '执行退出',
  exec_start: '开始执行',
  enable: '启用',
  disable: '停用',
  export: '导出',
  health_status: '健康检查',
  import: '导入',
  kill: '强杀',
  load: '载入',
  mount: '挂载',
  oom: '内存不足',
  pause: '暂停',
  pull: '拉取',
  push: '推送',
  reload: '重载',
  remove: '移除',
  rename: '改名',
  resize: '调整终端',
  restart: '重启',
  save: '保存',
  start: '启动',
  stop: '停止',
  tag: '打标签',
  top: '查看进程',
  unmount: '卸载',
  unpause: '恢复',
  update: '更新'
}

/**
 * health_status 的后缀 → 具体结论。daemon 把检查结果放动作后缀里
 * （"health_status: unhealthy"），协议校验取「首个 ": " 之前的词元」，后缀是
 * 数据不是动作 —— 展示时若只给「健康检查」，异常与正常两条就没了差别，这条
 * 正是 dockernotify 告警联动的事实源（unhealthy），读者不该自己去认英文。
 * 非 healthy/unhealthy 的后缀（daemon 未来加结果词）退回基调「健康检查」并
 * 由原始短语（title）兜底，不误译。
 */
const HEALTH_STATUS_TEXT: Readonly<Record<string, string>> = {
  healthy: '健康检查正常',
  unhealthy: '健康检查异常'
}

/**
 * action 的展示文案：先切词元（首个 ": " 之前，与协议 Validate 同一口径 ——
 * 谁在说话是动作，说话内容是数据），health_status 的结果后缀单独成句，其余
 * 词元查表；表外词元原样透传（协议扩词的兜底，正常帧不可达，见映射表注释）。
 */
export function eventActionText(action: string): string {
  const i = action.indexOf(': ')
  const token = i >= 0 ? action.slice(0, i) : action
  if (token === 'health_status' && i >= 0) {
    const status = action.slice(i + 2).trim()
    return HEALTH_STATUS_TEXT[status] ?? EVENT_ACTION_TEXT.health_status
  }
  return EVENT_ACTION_TEXT[token] ?? action
}

/**
 * 四类资源（+未知兜底）的图标、色槽与**类型名**。颜色不在纯函数里给 ——
 * 「EP 语义色零写死」的口径是映射落在组件 CSS 的 var(--el-color-*) 上，
 * 这里只给槽位名。
 *
 * label 是「四类订阅维度直显」的那一半：icon 说类别、label 把类别写成字
 * （容器/镜像/卷/网络）。四个词与协议的类型白名单逐字对应（IsDockerEventType），
 * 刻意不加「未知」的兜底词 —— 表外类型直显原文（见 eventTypeLabel），
 * 「我们不认识这条」本身就该被看出来。
 */
export type DockerEventTone = 'container' | 'image' | 'volume' | 'network' | 'other'

export interface DockerEventTypeMeta {
  /** iconify 图标名（ri 集，与模块其余图标同源）。 */
  icon: string
  /** 色槽名（组件 CSS 按 is-<tone> 挂 EP 语义色）。 */
  tone: DockerEventTone
  /** 类型的中文名（容器/镜像/卷/网络）。 */
  label: string
}

const TYPE_META: Readonly<Record<string, DockerEventTypeMeta>> = {
  container: { icon: 'ri:box-3-line', tone: 'container', label: '容器' },
  image: { icon: 'ri:image-2-line', tone: 'image', label: '镜像' },
  volume: { icon: 'ri:database-2-line', tone: 'volume', label: '卷' },
  network: { icon: 'ri:node-tree', tone: 'network', label: '网络' }
}

/** 未知类型的兜底：问号 + 中性色 —— 「我们不认识这条」本身就该被看出来。 */
const UNKNOWN_TYPE_META: DockerEventTypeMeta = {
  icon: 'ri:question-line',
  tone: 'other',
  label: ''
}

/** 资源类型的图标与色槽（四类之外的 type 走未知兜底）。 */
export function eventTypeMeta(type: string): DockerEventTypeMeta {
  return TYPE_META[type] ?? UNKNOWN_TYPE_META
}

/** 类型的展示名：表内给中文，表外**原样透传**（未知类型显示原始词而非空白）。 */
export function eventTypeLabel(type: string): string {
  return TYPE_META[type]?.label ?? type
}

/**
 * 退出码的展示与染色判据（仅 die 系事件有值；null 一律不显示 —— 调用方先用
 * `exitCode !== null` 拦一道，本函数不接 null）。
 *
 * 三值语义与协议同源：exit 0 = 自己人停的（中性 —— 正常收工不该被染成异常），
 * ≠0 = 异常退出（红染 —— 这是排障要找的那一条）。
 */
export function exitCodeTone(code: number): 'neutral' | 'danger' {
  return code === 0 ? 'neutral' : 'danger'
}

/** 退出码的展示文案（`exit 137`）：原始事实直接可读，不做任何解释性措辞。 */
export function exitCodeText(code: number): string {
  return `exit ${code}`
}

/**
 * 相对时间（秒级粒度）：活动流的判读问题是「多久之前」，1 秒采样的事件用
 * 「3 秒前」回答比「09:41:23」直接。分档比 utils/display 的 formatRelativeTime
 * 细（那里服务静态创建时间，四档够用；这里要区分「3 秒前 / 40 秒前」）。
 * 未来时刻（主机时钟偏差）夹到「刚刚」，不出现负数。
 *
 * 取整在**差值整段**上做（`floor(nowSec - t)`），不在各自入参上做：t 允许是
 * 浮点秒 —— 调用方常拿毫秒时间戳直接除 1000（任务中心的 createdAt/1000、活动流
 * 的 t/1000），那种 t 是 1759470000.441 形态的浮点。整段 floor 回答的是「实际
 * 过去了几个整秒」，文案不会漏出「4.41100001335144 秒前」这种浮点（QA 路 1 B1
 * 实测）；而先各自 floor 再相减会在 t 的秒内小数处多算一秒（4.4 秒前会被显示成
 * 「5 秒前」）。nowSec 的小数（Date.now()/1000 直传）同样在这一步吃掉。
 */
export function eventRelativeTime(
  t: number,
  nowSec: number = Math.floor(Date.now() / 1000)
): string {
  const diff = Math.floor(nowSec - t)
  if (diff < 1) return '刚刚'
  if (diff < 60) return `${diff} 秒前`
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  if (diff < 86_400) return `${Math.floor(diff / 3600)} 小时前`
  if (diff < 2_592_000) return `${Math.floor(diff / 86_400)} 天前`
  return `${Math.floor(diff / 2_592_000)} 个月前`
}

/**
 * 绝对时刻（本地时区，YYYY-MM-DD HH:mm:ss）：活动流时间列的 title 用。
 *
 * 为什么要有它：相对时间回答「多久之前」，但排障对账要问「哪一刻」——
 * 「3 分钟前」换算不回事件序号，悬停给出精确挂钟才是可引用的账。载体选 title
 * 而不是加列：最小侵入（不占版面、不动行结构），列本身不动。
 *
 * 入参 t 是 unix **毫秒**（与 feed 条目同源，见文件头）——正好是 Date 的原生单位。
 * 不用 toLocaleString：其输出随运行环境的 locale 漂移，测试与截图都不稳定。
 */
export function eventClockTime(t: number): string {
  const d = new Date(t)
  const p = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  )
}

export interface EventsFeed {
  /** 喂入原始 NDJSON chunk（可跨行、跨 chunk 半行，复用日志流同一拆包器）。 */
  pushRaw(chunk: string): void
  /** 窗口内条目（按 t 升序，同 t 按到达序；超出上限丢最早的）。 */
  readonly entries: readonly DockerEventItem[]
  /** 至今接受的条目数（含被窗口丢掉的；「暂停期间 N 条」与累计口径靠它）。 */
  readonly totalSeen: number
  /** 被重连回放守卫丢弃的条数（诊断「流活着吗」用，正常重连后非零不为故障）。 */
  readonly deduped: number
}

/**
 * 事件流消费端：NDJSON 原始 chunk → 排序显示窗口的一条流水线
 * （拆包 → 逐行解析 → 重连去重 → 合并排序 → 上限截断）。
 *
 * 两个关键取舍：
 *
 * **排序而非按到达序展示**。回放是「按主机升序、每台各 50 条」成段到达的，跨主机
 * 不是全局时间序；按到达序直接展示（或截断）会让 h1 的新事件排在 h2 的旧事件下面，
 * 甚至把整台主机的回放从窗口里挤出去。合并后按 t 稳定排序（同 t 按到达序），
 * 实时事件 t 恒新、总落在末尾 —— 排序只在回放期真正介入，不打断阅读。
 *
 * **重连回放的去重守卫**。断流重连后服务端会重放各主机最近 50 条，其中大部分
 * 已在窗口里 —— 不设守卫，一次网络抖动就是 50N 条重复。守卫按主机记账两级：
 *   ① t 严格早于该主机已见最新 t 的，一定是重放旧事，丢弃；
 *   ② t 恰好等于已见最新 t 的（回放边界），按 eventsDedupKey 的内容复合键精确去重 ——
 *     回放重发的同一批条目 t 逐位相同（帧里的值进环形缓冲后原样重放），边界
 *     相等必然成立；同一 t 上真实存在多条不同事件（时刻戳细到毫秒也只缩小
 *     碰撞概率，不构成排除）：同 t **不同**动作照常接受（宁重不漏的反面是不漏
 *     真事），同 t **同动作同对象**的重放才丢。
 * 守卫只按 hostId 记账，不跨主机比较 —— 各主机时钟无对齐保证。
 */
export function createEventsFeed(maxEntries = MAX_EVENT_ENTRIES): EventsFeed {
  const parser = createNdjsonParser()
  const cap =
    Number.isFinite(maxEntries) && maxEntries > 0 ? Math.floor(maxEntries) : MAX_EVENT_ENTRIES
  let items: DockerEventItem[] = []
  let seen = 0
  let deduped = 0
  /** 每台主机已接受的最新 t（重连回放守卫的第一级）。 */
  const lastT = new Map<string, number>()
  /** 每台主机在最新 t 上的 (action, actorId) 集合（第二级：同秒精确去重）。 */
  const keysAtLastT = new Map<string, Set<string>>()

  // 第二级去重键与「历史 ↔ 实时」合并（createEventsLog）用的是同一个内容复合键
  // （见 eventsDedupKey）：「什么算同一条」在全局只有一处定义，重连守卫与详版页
  // 不会各判各的。
  const keyOf = (f: DockerEventFields): string => eventsDedupKey(f)

  return {
    pushRaw(chunk: string): void {
      if (!chunk) return
      // 先攒进 inbox 再合并：合并生成**新数组**（而不是原地 sort）—— 消费端把
      // entries 赋进响应式 ref，引用不变就不重渲染；原地排序会让新事件静默失踪。
      const inbox: DockerEventItem[] = []
      for (const line of parser.push(chunk)) {
        const fields = parseEventLine(line)
        if (!fields) continue // 坏行跳过，不打断流
        const last = lastT.get(fields.hostId)
        if (last !== undefined) {
          if (fields.t < last) {
            deduped++
            continue
          }
          if (fields.t === last && keysAtLastT.get(fields.hostId)?.has(keyOf(fields))) {
            deduped++
            continue
          }
        }
        if (last === undefined || fields.t > last) {
          lastT.set(fields.hostId, fields.t)
          keysAtLastT.set(fields.hostId, new Set([keyOf(fields)]))
        } else {
          keysAtLastT.get(fields.hostId)!.add(keyOf(fields))
        }
        inbox.push({ ...fields, seq: ++seen })
      }
      if (inbox.length === 0) return
      // 合并新到达后整体排序：稳定排序 + 到达序做并列决胜，窗口内次序确定可复现。
      items = [...items, ...inbox].sort((a, b) => a.t - b.t || a.seq - b.seq)
      if (items.length > cap) items.splice(0, items.length - cap)
    },
    get entries(): readonly DockerEventItem[] {
      return items
    },
    get totalSeen(): number {
      return seen
    },
    get deduped(): number {
      return deduped
    }
  }
}

/**
 * 详版页（/docker/events）的时间轴：**先历史、后实时**两个来源合成一条 t 降序的
 * 列表（活动流面板是 t 升序的 live tail，这里是「最新在前」的历史读面 —— 详版页
 * 问的第一句是「刚才都发生了什么」，最新一条必须落在第一屏）。
 *
 * 三个口径：
 *   - **排序 t 降序、同 t 按到达序降序**：t 是页面画出来的时间列，排序键与展示键
 *     必须同一个（同 feed 的理由：主键取到达序会让时间列忽上忽下）；同 t 时后到
 *     的在前（实时新事件落在历史段同刻条目的上面，新到的就该更靠前）。
 *   - **去重是内容复合键**（eventsDedupKey）：历史端点与随后接入的实时流会重叠
 *     （流的回放重发各主机最近 50 条，其中大半就在刚拉的历史页里），跨来源按
 *     同一把尺判「已经在了」。
 *   - **accept 谓词对两个来源都生效**：历史行虽已由服务端过滤，仍过一次同一个
 *     谓词 —— 同一把尺用一个实现，历史段与实时段不会捞出两套结果。
 *
 * 与 createEventsFeed 分家的理由：feed 是「一条流 + 重连守卫」（上限 100、升序、
 * 按主机两级记账），这里是「两个来源 + 全量去重键」（上限 500、降序）——
 * 后者带着一个 feed 不需要的键集合（内容键无界增长，故只在详版页按窗口上限
 * 兜住：超出上限丢最旧的条目时**连带丢它的键**不可能——键集合是 Map 形态，
 * 用「条目数上限 + 重建」维持有界，见 push 实现）。
 */
export interface EventsLog {
  /** 灌入历史段（端点一页；内部按 t 降序合并、跨来源去重）。 */
  seedHistory(items: DockerEventFields[]): void
  /** 喂入实时流的原始 NDJSON chunk（解析、过滤、去重、合并）。 */
  pushRaw(chunk: string): void
  /** 时间轴（t 降序，同 t 按到达序降序；超出上限丢最旧）。 */
  readonly entries: readonly DockerEventItem[]
  /** 至今接受的条目数（含被上限丢掉的）。 */
  readonly totalSeen: number
  /** 被去重（与已有条目同键）丢弃的条数。 */
  readonly deduped: number
}

export interface EventsLogOptions {
  /** 时间轴上限（缺省 MAX_EVENT_LOG_ENTRIES）。 */
  maxEntries?: number
  /** 过滤谓词（历史与实时都过它；缺省不过滤）。 */
  accept?: (f: DockerEventFields) => boolean
}

export function createEventsLog(opts: EventsLogOptions = {}): EventsLog {
  const parser = createNdjsonParser()
  const cap =
    Number.isFinite(opts.maxEntries) && (opts.maxEntries as number) > 0
      ? Math.floor(opts.maxEntries as number)
      : MAX_EVENT_LOG_ENTRIES
  const accept = opts.accept ?? (() => true)
  let items: DockerEventItem[] = []
  let seen = 0
  let deduped = 0
  /** 已见的内容键（跨来源去重）。 */
  const keys = new Set<string>()

  function absorb(batch: DockerEventFields[]): void {
    if (batch.length === 0) return
    const inbox: DockerEventItem[] = []
    for (const f of batch) {
      if (!accept(f)) continue
      const key = eventsDedupKey(f)
      if (keys.has(key)) {
        deduped++
        continue
      }
      keys.add(key)
      inbox.push({ ...f, seq: ++seen })
    }
    if (inbox.length === 0) return
    // 合并生成**新数组**（引用变化驱动重渲染；原地 sort 会让消费端看到同一引用
    // 而漏掉更新 —— 与 feed 同一条纪律）。
    items = [...items, ...inbox].sort((a, b) => b.t - a.t || b.seq - a.seq)
    if (items.length > cap) {
      const dropped = items.splice(cap)
      // 被上限挤出时间轴的条目，其内容键一并撤掉：键集合与可见条目同界
      //（否则挂机越久键集合越涨，而它的唯一用途就是「在不在列表里」）。
      for (const d of dropped) keys.delete(eventsDedupKey(d))
    }
  }

  return {
    seedHistory(list: DockerEventFields[]): void {
      absorb(list)
    },
    pushRaw(chunk: string): void {
      if (!chunk) return
      const batch: DockerEventFields[] = []
      for (const line of parser.push(chunk)) {
        const fields = parseEventLine(line) // sentinel 与坏行在这里被跳过
        if (fields) batch.push(fields)
      }
      absorb(batch)
    },
    get entries(): readonly DockerEventItem[] {
      return items
    },
    get totalSeen(): number {
      return seen
    },
    get deduped(): number {
      return deduped
    }
  }
}

/**
 * 头部的账目口径句：累计数与截断提示。「累计」数不受窗口丢弃影响（暂停期间的新
 * 事件计数与它同一记账），被截断时说清楚只显示最近一段 —— 读者关心的只是
 * 「我看到的全不全」，与 log-viewer 的提示同一理由。
 */
export function eventFeedHint(totalSeen: number, shown: number, cap = MAX_EVENT_ENTRIES): string {
  if (totalSeen <= 0) return ''
  if (shown < totalSeen) return `累计 ${totalSeen} 条 · 仅显示最近 ${cap} 条`
  return `累计 ${totalSeen} 条`
}
