/**
 * 六期（活动流）跨主机 docker 事件流的纯逻辑：NDJSON 行解析、按时间排序的显示
 * 窗口（上限丢最早）、重连回放的去重守卫、action 中文化映射、秒级相对时间、
 * 四类资源的图标与语义色槽位。
 *
 * 为什么单独一份而不是塞进 utils/stream.ts：那里是「文本行 / 帧解码」的通道层
 * （日志/终端共用），这里是「结构化事件条目」的消费层 —— 行形状、缓冲形态
 * （按时间排序的对象窗口 vs 文本行窗口）、去重守卫都不同，混在一起两头都不纯
 * （与 utils/stats.ts 单独成文的理由同一口径）。
 *
 * 行形状（core 的 docker_events.go 的 eventNDJSONLine，字段打平、归属由 core 注入）：
 *   {"host_id":n,"hostname":"s","t":unix毫秒,"type":"container","action":"start",
 *    "actor_name":"s","actor_id":"s"}
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
 * 解析一行事件流；形状不符给 null（调用方跳过这一行，不打断整条流 —— 与
 * parseLogStreamLine / parseStatsStreamLine 同一纪律：丢一行只让流缺一格，
 * 杀掉整条流则是杀了全部数据）。
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
  // 归属与时刻是渲染的最小充分集：缺一就画不出「哪台机、什么时候、发生了什么」。
  if (typeof o.host_id !== 'number' || !Number.isFinite(o.host_id)) return null
  if (typeof o.t !== 'number' || !Number.isFinite(o.t) || o.t <= 0) return null
  if (typeof o.type !== 'string' || o.type === '') return null
  if (typeof o.action !== 'string' || o.action === '') return null
  if (typeof o.hostname !== 'string') return null
  // actor 两字段是 omitempty：缺省按空串，出现却不是串则形状不对。
  if (o.actor_name !== undefined && typeof o.actor_name !== 'string') return null
  if (o.actor_id !== undefined && typeof o.actor_id !== 'string') return null
  return {
    hostId: String(o.host_id),
    hostname: o.hostname,
    t: o.t,
    type: o.type,
    action: o.action,
    actorName: typeof o.actor_name === 'string' ? o.actor_name : '',
    actorId: typeof o.actor_id === 'string' ? o.actor_id : ''
  }
}

/**
 * action → 中文文案的映射表。收录 docker events 的高频动作；**未映射的动作原样
 * 显示** —— 新版本 docker 引入未知动作时，读者看到的是原始词而不是空白或误译，
 * 排障时原始词反而更有用。
 */
export const EVENT_ACTION_TEXT: Readonly<Record<string, string>> = {
  // 容器（含 exec_* / health_status 这类带下划线的动作名）
  create: '创建',
  destroy: '销毁',
  start: '启动',
  stop: '停止',
  die: '退出',
  kill: '强杀',
  restart: '重启',
  pause: '暂停',
  unpause: '恢复',
  rename: '改名',
  update: '更新',
  attach: '挂接',
  detach: '分离',
  commit: '提交',
  copy: '复制',
  exec_create: '准备执行',
  exec_start: '开始执行',
  exec_die: '执行退出',
  exec_detach: '执行分离',
  health_status: '健康检查',
  oom: '内存不足',
  resize: '调整终端',
  // 镜像
  pull: '拉取',
  tag: '打标签',
  untag: '去标签',
  delete: '删除',
  import: '导入',
  load: '载入',
  save: '保存',
  // 卷 / 网络
  mount: '挂载',
  unmount: '卸载',
  connect: '接入',
  disconnect: '断开'
}

/** action 的展示文案（映射表命中给中文，未映射原样透传）。 */
export function eventActionText(action: string): string {
  return EVENT_ACTION_TEXT[action] ?? action
}

/**
 * 四类资源（+未知兜底）的图标与色槽。颜色不在纯函数里给 —— 「EP 语义色零写死」
 * 的口径是映射落在组件 CSS 的 var(--el-color-*) 上，这里只给槽位名。
 */
export type DockerEventTone = 'container' | 'image' | 'volume' | 'network' | 'other'

export interface DockerEventTypeMeta {
  /** iconify 图标名（ri 集，与模块其余图标同源）。 */
  icon: string
  /** 色槽名（组件 CSS 按 is-<tone> 挂 EP 语义色）。 */
  tone: DockerEventTone
}

const TYPE_META: Readonly<Record<string, DockerEventTypeMeta>> = {
  container: { icon: 'ri:box-3-line', tone: 'container' },
  image: { icon: 'ri:image-2-line', tone: 'image' },
  volume: { icon: 'ri:database-2-line', tone: 'volume' },
  network: { icon: 'ri:node-tree', tone: 'network' }
}

/** 未知类型的兜底：问号 + 中性色 —— 「我们不认识这条」本身就该被看出来。 */
const UNKNOWN_TYPE_META: DockerEventTypeMeta = { icon: 'ri:question-line', tone: 'other' }

/** 资源类型的图标与色槽（四类之外的 type 走未知兜底）。 */
export function eventTypeMeta(type: string): DockerEventTypeMeta {
  return TYPE_META[type] ?? UNKNOWN_TYPE_META
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
 *   ② t 恰好等于已见最新 t 的（回放边界），按 (action, actorId) 精确去重 ——
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

  const keyOf = (f: DockerEventFields): string => `${f.action} ${f.actorId}`

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
 * 头部的账目口径句：累计数与截断提示。「累计」数不受窗口丢弃影响（暂停期间的新
 * 事件计数与它同一记账），被截断时说清楚只显示最近一段 —— 读者关心的只是
 * 「我看到的全不全」，与 log-viewer 的提示同一理由。
 */
export function eventFeedHint(totalSeen: number, shown: number, cap = MAX_EVENT_ENTRIES): string {
  if (totalSeen <= 0) return ''
  if (shown < totalSeen) return `累计 ${totalSeen} 条 · 仅显示最近 ${cap} 条`
  return `累计 ${totalSeen} 条`
}
