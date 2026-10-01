/**
 * 4b（拉取进度面）的纯逻辑：NDJSON 进度行解析、按层 id 折叠的层列表、
 * 下载字节记账、镜像引用校验与字节文案。
 *
 * 为什么单独一份而不是塞进 utils/stats.ts：stats 的缓冲是「滑动窗口的数值样本」
 * （上限按点数、喂给曲线），拉取进度的缓冲是「按层 id 折叠的终值表」—— 同一层的
 * 后帧是**更新**而不是新事实，列表里只该有一行 —— 两者的消费形态不同，与
 * utils/events.ts 单独成文的理由同一口径。
 *
 * P2 起本文件是**镜像分发进度流的公共折叠器**：push 与 pull 是 daemon 的同一个
 * JSON 进度流（协议 DockerPushProgressItem 与 DockerPullProgressItem 同形、
 * agent 侧同一消费器 consumePullStream），差异只有终态集合/字节段文案/补满时机
 * 三处字面量 —— 折成 LayerFeedFlavor 参数（缺省 = 拉取语义，4b 的两个既有调用
 * 零改动），推送经 createPushFeed 取同一份折叠纪律。构建帧是另一种形态（文本
 * 播报、无字节口径），单独收在 utils/build-push.ts，不硬凑进这份层表。
 *
 * 行形状（core 的 docker_stream.go 的 pullNDJSONLine，字段与协议
 * DockerPullProgressItem 同名同义）：
 *   {"seq":n,"t":unix毫秒,"id":"层id","status":"Downloading","current":n,
 *    "total":n,"done":b,"error":"s","eof":b}
 * 三条形状纪律（都来自发送端、前端只镜像不发明）：
 *   - 终态项（done 或 error 恰其一）**恰一条、恰在最后**，eof 挂在同一行上；
 *   - eof 帧不带进度行时单独发一行 `{"seq":n,"t":0,"eof":true}`（t 是零值序列化，
 *     不是合法记录时刻 —— 与 stats 流「空收尾行」的判法同理）；
 *   - id 为空的行是**消息行**（"Pulling from …" / "Digest: …" / "Status: …"），
 *     agent 按到达顺序全部保留 —— 页面只取最新一条作阶段提示（全量消息历史属于
 *     日志/活动流的职责，不属于进度面）。
 */
import { createNdjsonParser } from './stream'

/** 一行进度记录（协议 DockerPullProgressItem 的打平形态，seq 不参与消费）。 */
export interface PullProgressItem {
  /** 记录时刻（unix 毫秒；终态项用它判「这行有内容」）。 */
  t: number
  /** 层 id（无层消息行为空串）。 */
  id: string
  /** daemon 原文状态文案（页面直接显示）。 */
  status: string
  /** 该层已传输字节（0 = 未知，协议用 0 表示 progressDetail 缺席）。 */
  current: number
  /** 该层总字节（0 = 未知）。 */
  total: number
  /** 成功终态标记。 */
  done: boolean
  /** 失败终态标记（daemon 原文错误）。 */
  error: string
}

/** 解析结果：进度记录（空 eof 行给 null）与 eof 信号分开携带（与 stats 同构）。 */
export interface PullStreamLine {
  item: PullProgressItem | null
  eof: boolean
}

/**
 * 解析一行拉取进度流；形状不符给 null（调用方跳过这一行，不打断整条流 ——
 * 与 parseLogStreamLine / parseStatsStreamLine 同一纪律：丢一眼进度不致命，
 * 把整条流当错误杀掉才是）。
 *
 * 内容校验逐条镜像协议 `DockerPullProgressItem.Validate`（core 转发前已挡过一道，
 * 前端再挡一道只为坏行不进折叠表）：t 必须为正；done 与 error 不得同时出现
 * （放行会让页面弹一对相反的结论）；字节不得为负、current 不得越过 total。
 */
export function parsePullStreamLine(line: string): PullStreamLine | null {
  let raw: unknown
  try {
    raw = JSON.parse(line)
  } catch {
    return null
  }
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return null
  const o = raw as Record<string, unknown>
  if (o.eof !== undefined && typeof o.eof !== 'boolean') return null
  const eof = o.eof === true
  // 任何内容字段在场 → 是一行记录；全部缺席 → 只当 eof 信号（t 是零值序列化）。
  const hasItem =
    o.id !== undefined ||
    o.status !== undefined ||
    o.done !== undefined ||
    o.error !== undefined ||
    o.current !== undefined ||
    o.total !== undefined
  if (!hasItem) return eof ? { item: null, eof: true } : null
  // 记录行的 t 是 agent 挂钟，协议要求为正；非正说明这行不是它声称的东西。
  if (typeof o.t !== 'number' || !Number.isFinite(o.t) || o.t <= 0) return null
  if (o.id !== undefined && typeof o.id !== 'string') return null
  if (o.status !== undefined && typeof o.status !== 'string') return null
  if (o.done !== undefined && typeof o.done !== 'boolean') return null
  if (o.error !== undefined && typeof o.error !== 'string') return null
  if (o.current !== undefined && (typeof o.current !== 'number' || !Number.isFinite(o.current)))
    return null
  if (o.total !== undefined && (typeof o.total !== 'number' || !Number.isFinite(o.total)))
    return null
  const item: PullProgressItem = {
    t: o.t,
    id: typeof o.id === 'string' ? o.id : '',
    status: typeof o.status === 'string' ? o.status : '',
    current: typeof o.current === 'number' ? o.current : 0,
    total: typeof o.total === 'number' ? o.total : 0,
    done: o.done === true,
    error: typeof o.error === 'string' ? o.error : ''
  }
  if (item.done && item.error !== '') return null // 双终态标记：状态机算错了
  if (item.current < 0 || item.total < 0 || (item.total > 0 && item.current > item.total)) {
    return null
  }
  return { item, eof }
}

/** 层完成态的 daemon 状态集合（「下载完」不算：解压还在后面）。 */
const LAYER_DONE_STATUS: ReadonlySet<string> = new Set(['Pull complete', 'Already exists'])

/** 该 status 是否层的终态（Pull complete：拉到位；Already exists：本机已有）。 */
export function isLayerDoneStatus(status: string): boolean {
  return LAYER_DONE_STATUS.has(status)
}

/**
 * 推送侧的层终态判定（与拉取同位不同词 —— daemon 对 push 用的是另一组状态）：
 * Pushed 是主终态；Layer already exists / Mounted from … 是「这层直接复用了仓库
 * 既有层」的等价终态（后者带仓库后缀，精确集合表不了，走前缀匹配）。
 */
export function isPushLayerDoneStatus(status: string): boolean {
  return (
    status === 'Pushed' || status === 'Layer already exists' || status.startsWith('Mounted from ')
  )
}

/**
 * 层折叠器的语义配置（拉取/推送两族的差异点，P2 起成为公共折叠器的参数）。
 *
 * 三处差异的字面量都来自 daemon 的状态文案，前端只镜像不发明：
 *   - 拉取：终态 Pull complete / Already exists；字节段 Downloading；中途补满态
 *     Download complete（下载确实完了，解压还在后面）；
 *   - 推送：终态 Pushed / Layer already exists / Mounted from …；字节段 Pushing；
 *     **没有中途补满态** —— Pushed 本身就是「传完了」，fillStatus 传空串表示
 *     「层到终态即补满」。
 */
export interface LayerFeedFlavor {
  /** 层终态判定。 */
  isDoneStatus: (status: string) => boolean
  /** 字节段的进行态文案（汇总行只认这段的字节，见 createPullFeed 的记账取舍）。 */
  byteStatus: string
  /** 字节记账的中途补满态文案；'' = 没有中途态，层到终态即补满（推送形态）。 */
  fillStatus: string
}

/** 拉取语义（缺省 flavor —— 既有调用 createPullFeed() 与 4b 行为逐字一致）。 */
const PULL_FEED_FLAVOR: LayerFeedFlavor = {
  isDoneStatus: isLayerDoneStatus,
  byteStatus: 'Downloading',
  fillStatus: 'Download complete'
}

/** 推送语义（createPushFeed 用）。 */
const PUSH_FEED_FLAVOR: LayerFeedFlavor = {
  isDoneStatus: isPushLayerDoneStatus,
  byteStatus: 'Pushing',
  fillStatus: ''
}

/**
 * 层短 id：daemon 的层 id 是 64 位十六进制（不带 sha256: 前缀），与 docker CLI
 * 同口径取 12 位；带前缀的形态先剥前缀（对 digest 形态的层 id 也成立）。
 */
export function layerShortId(id: string): string {
  const bare = id.startsWith('sha256:') ? id.slice('sha256:'.length) : id
  return bare.slice(0, 12)
}

/**
 * 层进度条百分比（0–100）。total 未知（0）给 0 —— 「没有分母」由调用方转成
 * 不确定态动画，不是 0%；越界值夹回区间（协议校验过后理论到不了这里，夹一道
 * 防坏数据把条画出界）。
 */
export function layerPercent(current: number, total: number): number {
  if (!Number.isFinite(current) || !Number.isFinite(total) || total <= 0 || current <= 0) {
    return 0
  }
  return Math.min(100, Math.round((current / total) * 100))
}

/** 层的折叠视图（组件逐行渲染的形态；id 是稳定 key）。 */
export interface PullLayer {
  id: string
  /** 12 位短 id（等宽展示）。 */
  shortId: string
  /** 最新 status 文案。 */
  status: string
  /** 最新一对进度字节（下载段或解压段，谁最后到谁说了算 —— 条形随之换段，与 CLI 同观感）。 */
  current: number
  total: number
  /** 层是否已到终态（Pull complete / Already exists）。 */
  done: boolean
}

/** 流终态项（done 或 error 恰其一；取消路径上不会有）。 */
export interface PullTerminal {
  ok: boolean
  error: string
}

export interface PullFeed {
  /** 喂入原始 NDJSON chunk（可跨行、跨 chunk 半行，复用日志流同一拆包器）。 */
  pushRaw(chunk: string): void
  /** 层列表（到达序；同层只占一行，后帧更新既有行）。 */
  readonly layers: readonly PullLayer[]
  /** 最新一条消息行文案（空串 = 还没有）。 */
  readonly note: string
  /** 下载段已传输字节合计（汇总行「已下载」；推送形态下 = Pushing 段合计，页面文案换「已上传」）。 */
  readonly downloadedBytes: number
  /** 下载段总字节合计（汇总行「总字节」；0 = 还没有任何字节口径）。 */
  readonly totalBytes: number
  /** 已完成层数 / 层数。 */
  readonly doneLayers: number
  /** 是否已收到 eof（流自然收尾）。 */
  readonly eof: boolean
  /** 终态项（done/error 那一行；无则 null —— 取消路径上不发货）。 */
  readonly terminal: PullTerminal | null
}

/** 层的内部折叠态：视图之外再记一份「下载段」的字节对（见 createPullFeed 的说明）。 */
interface LayerFold {
  view: PullLayer
  /** 下载段已传输字节（「Download complete」时补满到 dlTotal）。 */
  dlCurrent: number
  /** 下载段总字节（0 = 该层还没有字节口径，如 Already exists）。 */
  dlTotal: number
}

/**
 * 拉取进度流消费端：NDJSON 原始 chunk → 按层 id 折叠的层表的一条流水线
 * （拆包 → 逐行解析 → 按层合并 → 终态捕获）。
 *
 * flavor 参数（P2 起有值）只换语义字面量，不动折叠纪律 —— 推送经 createPushFeed
 * 走同一条流水线，缺省值保持 4b 拉取的既有行为。
 *
 * 两个合并取舍：
 *
 * **字节对「只进不退零」**。完成类状态行（"Download complete"/"Pull complete"/推送的
 * "Pushed"）不带 progressDetail（协议 0 = 未知）—— 直接覆盖会把已知的
 * current/total 抹回 0，条形从 90% 跳回不确定态。故非正值不改既有值；拉取的
 * 「Download complete」与推送的终态各自把字节记账补满（传输确实完了）。
 *
 * **汇总按「传输段」记账，条形按「最新一对」**。daemon 的 progressDetail 在
 * Downloading 与 Extracting 两段（推送的 Pushing）都会出现，两段的字节不是同一笔
 * 账 —— 条形跟着最新一对走（换段重走，与 docker CLI 同观感），而顶部汇总行只认
 * 传输段（拉取的 Downloading / 推送的 Pushing）的字节，否则解压一开始汇总行就会
 * 「倒退」。
 */
export function createPullFeed(flavor: LayerFeedFlavor = PULL_FEED_FLAVOR): PullFeed {
  const parser = createNdjsonParser()
  const folds: LayerFold[] = []
  let noteText = ''
  let ended = false
  let terminal: PullTerminal | null = null

  function applyLayerFrame(fold: LayerFold, item: PullProgressItem): void {
    const v = fold.view
    if (item.status !== '') v.status = item.status
    if (item.current > 0) v.current = item.current
    if (item.total > 0) v.total = item.total
    if (flavor.isDoneStatus(item.status)) v.done = true
    if (item.status === flavor.byteStatus && item.total > 0) {
      fold.dlCurrent = item.current
      fold.dlTotal = item.total
    } else if (
      fold.dlTotal > 0 &&
      (item.status === flavor.fillStatus || (flavor.fillStatus === '' && v.done))
    ) {
      // 中途补满（拉取的 Download complete）或终态补满（推送的 Pushed）。
      fold.dlCurrent = fold.dlTotal
    }
  }

  function handleLine(line: string): void {
    const parsed = parsePullStreamLine(line)
    if (!parsed) return // 坏行跳过，不打断流
    const item = parsed.item
    if (item) {
      if (item.done || item.error !== '') {
        terminal = { ok: item.done, error: item.error }
      } else if (item.id !== '') {
        const fold = folds.find((f) => f.view.id === item.id)
        if (fold) {
          applyLayerFrame(fold, item)
        } else {
          const created: LayerFold = {
            view: {
              id: item.id,
              shortId: layerShortId(item.id),
              status: item.status,
              current: item.current,
              total: item.total,
              done: flavor.isDoneStatus(item.status)
            },
            dlCurrent: item.status === flavor.byteStatus ? item.current : 0,
            dlTotal: item.status === flavor.byteStatus ? item.total : 0
          }
          folds.push(created)
        }
      } else if (item.status !== '') {
        // 消息行：最新一条作阶段提示（见文件头对消息行的口径说明）。
        noteText = item.status
      }
    }
    if (parsed.eof) ended = true
  }

  return {
    pushRaw(chunk: string): void {
      if (ended) return // eof 后的残余字节不再入账（与 stats 同纪律）
      for (const line of parser.push(chunk)) handleLine(line)
      // 半行残留不影响 eof 判定：eof 只会挂在完整行上，flush 由调用方收尾时补。
    },
    get layers(): readonly PullLayer[] {
      return folds.map((f) => f.view)
    },
    get note(): string {
      return noteText
    },
    get downloadedBytes(): number {
      let sum = 0
      for (const f of folds) sum += f.dlCurrent
      return sum
    },
    get totalBytes(): number {
      let sum = 0
      for (const f of folds) sum += f.dlTotal
      return sum
    },
    get doneLayers(): number {
      let sum = 0
      for (const f of folds) if (f.view.done) sum++
      return sum
    },
    get eof(): boolean {
      return ended
    },
    get terminal(): PullTerminal | null {
      return terminal
    }
  }
}

/**
 * 推送进度流消费端（P2）：同一份折叠流水线换推送语义 —— 终态集合
 * （Pushed / Layer already exists / Mounted from …）、字节段（Pushing）、
 * 终态即补满（push 没有「Download complete」式的中途补满态）。
 *
 * 行解析直接复用 parsePullStreamLine（core 的 pushNDJSONLine 与 pullNDJSONLine
 * 同字段集 —— daemon 的 push 与 pull 是同一个 JSON 进度流，发送端已同形，消费端
 * 没理由分家）。
 */
export function createPushFeed(): PullFeed {
  return createPullFeed(PUSH_FEED_FLAVOR)
}

/**
 * 字节数 → 可读文案（B / KB / MB / GB）。
 *
 * 为什么不走 formatByUnit：它没有「静态字节」档 —— 'B/s' 把 /s 焊死在文案里，
 * 'MB' 入参又把亚 MB 精度抹掉（32 KB 的层会显示成「0.0 MB」）。层的体量横跨
 * 三个数量级（配置层数 KB、基础层数百 MB），KB 档必须保留；device/utils/upgrade.ts
 * 因同一缺口自过一份同形阶梯，是既成先例。
 */
export function formatPullBytes(v: number): string {
  if (typeof v !== 'number' || !Number.isFinite(v) || v < 0) return '—'
  if (v < 1024) return `${Math.round(v)} B`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`
  if (v < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB`
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB`
}

/**
 * 镜像引用是否合法（输入校验）。
 *
 * **逐字镜像**协议 `dockerImageRefRe`（IsDockerImageRef 的唯一事实源）：首字符
 * 字母数字，其后仅 [A-Za-z0-9/._:@-]。跨语言无法 import，漂移由测试钉正反例。
 */
const IMAGE_REF_RE = /^[a-zA-Z0-9][a-zA-Z0-9/._:@-]*$/

export function isValidImageRef(s: string): boolean {
  return s !== '' && IMAGE_REF_RE.test(s)
}
