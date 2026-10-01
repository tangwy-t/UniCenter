/**
 * P2（构建/推送前端半边）的纯逻辑：构建进度流的行解析与「构建播报」折叠、
 * 构建表单的协议镜像校验（tag / context / dockerfile / build-args 键值）。
 *
 * 为什么不并进 utils/pull.ts：pull.ts 收的是「按层 id 折叠的层表 + 字节记账」
 * （同一层的后帧是更新而不是新事实），构建帧没有字节口径、主体是**按到达顺序
 * 追加的文本行** —— 折叠对象不同（播报行 vs 层表），硬凑一个泛型只会让两家都
 * 读不懂。推送则与拉取同形（daemon 的同一个 JSON 进度流），留在 pull.ts 里经
 * LayerFeedFlavor 复用（createPushFeed），本文件不重复那份折叠器。
 *
 * 行形状（core 的 docker_stream.go 的 buildNDJSONLine，字段与协议
 * DockerBuildProgressItem 同名同义）：
 *   {"seq":n,"t":unix毫秒,"id":"步骤id","status":"状态文案","stream":"文本行",
 *    "done":b,"error":"s","eof":b}
 * 三条形状纪律与 pull 同源（都是发送端钉的、前端只镜像不发明）：
 *   - 终态项（done 或 error 恰其一）**恰一条、恰在最后**，eof 挂在同一行上；
 *   - eof 帧不带记录行时单独发一行 `{"seq":n,"t":0,"eof":true}`（t 是零值序列化，
 *     不是合法记录时刻）；
 *   - 一行解码失败跳过并留痕（core 侧已挡一道，前端再挡一道只为坏行不进播报表）。
 *
 * 两种记录行（agent 的 consumeBuildStream 产出）：
 *   - **文本行**（stream 非空）：经典构建器的全部输出（"Step 1/4 : FROM …" /
 *     buildkit 的 "#1 [internal] …"），agent 已裁尾部换行并截长保尾 —— 按到达
 *     顺序全部入播报；
 *   - **步骤行**（id 非空）：带步骤句柄的状态行（如 id="Step 1/2"），同 id 后帧
 *     是**更新**不是新行 —— 折叠成一行、位置钉在首次到达处（与 pull 的层表同一
 *     纪律，agent 的折叠器 4b 同款）。
 */
import { createNdjsonParser } from './stream'

/** 一行构建进度记录（协议 DockerBuildProgressItem 的打平形态，seq 不参与消费）。 */
export interface BuildProgressItem {
  /** 记录时刻（unix 毫秒；终态项用它判「这行有内容」）。 */
  t: number
  /** 步骤 id（buildkit 步骤句柄 / 经典构建器的 Step n/m；文本行为空串）。 */
  id: string
  /** 状态文案（步骤行的正文；文本行为空串）。 */
  status: string
  /** 构建输出的文本行（经典构建器的输出主体）。 */
  stream: string
  /** 成功终态标记。 */
  done: boolean
  /** 失败终态标记（daemon 原文错误）。 */
  error: string
}

/** 解析结果：进度记录（空 eof 行给 null）与 eof 信号分开携带（与 pull 同构）。 */
export interface BuildStreamLine {
  item: BuildProgressItem | null
  eof: boolean
}

/**
 * 解析一行构建进度流；形状不符给 null（调用方跳过这一行，不打断整条流 ——
 * 与 parsePullStreamLine 同一纪律）。
 *
 * 内容校验逐条镜像协议 `DockerBuildProgressItem.Validate` 的取向：t 必须为正；
 * done 与 error 不得同时出现（放行会让页面弹一对相反的结论）；构建帧没有字节
 * 字段（current/total 不在形状里，出现也不认）。
 */
export function parseBuildStreamLine(line: string): BuildStreamLine | null {
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
    o.stream !== undefined ||
    o.done !== undefined ||
    o.error !== undefined
  if (!hasItem) return eof ? { item: null, eof: true } : null
  if (typeof o.t !== 'number' || !Number.isFinite(o.t) || o.t <= 0) return null
  if (o.id !== undefined && typeof o.id !== 'string') return null
  if (o.status !== undefined && typeof o.status !== 'string') return null
  if (o.stream !== undefined && typeof o.stream !== 'string') return null
  if (o.done !== undefined && typeof o.done !== 'boolean') return null
  if (o.error !== undefined && typeof o.error !== 'string') return null
  const item: BuildProgressItem = {
    t: o.t,
    id: typeof o.id === 'string' ? o.id : '',
    status: typeof o.status === 'string' ? o.status : '',
    stream: typeof o.stream === 'string' ? o.stream : '',
    done: o.done === true,
    error: typeof o.error === 'string' ? o.error : ''
  }
  if (item.done && item.error !== '') return null // 双终态标记：状态机算错了
  return { item, eof }
}

/** 播报行（组件逐行渲染的形态）。 */
export interface BuildFeedLine {
  /** 稳定 key：步骤行 = `s:步骤id`（折叠不变位）；文本行 = `l:全局行号`。 */
  key: string
  /** 步骤 id（文本行为空串 —— 展示用，折叠语义已由 feed 收口）。 */
  id: string
  /** 行文本（步骤行 = 最新 status 文案；文本行 = stream 原文）。 */
  text: string
  /** 是否步骤行（同 id 后帧更新此行而不是追加新行）。 */
  step: boolean
}

/** 流终态项（done 或 error 恰其一；取消路径上不会有）。与 pull 的 PullTerminal 同形。 */
export interface BuildTerminal {
  ok: boolean
  error: string
}

export interface BuildFeed {
  /** 喂入原始 NDJSON chunk（可跨行、跨 chunk 半行，复用日志流同一拆包器）。 */
  pushRaw(chunk: string): void
  /** 播报行（文本行按到达序追加；步骤行按 id 折叠在后帧更新、位置钉在首次到达处）。 */
  readonly lines: readonly BuildFeedLine[]
  /** 因上限被丢弃的行数（构建输出可以几千行 —— 播报只保尾部，丢弃量说在明处）。 */
  readonly droppedLines: number
  /** 是否已收到 eof（流自然收尾）。 */
  readonly eof: boolean
  /** 终态项（done/error 那一行；无则 null —— 取消路径上不发货）。 */
  readonly terminal: BuildTerminal | null
}

/**
 * 播报表的行数上限。为什么比层表多一道闸：RUN 步骤的输出是逐行文本（npm install
 * 轻松几千行），层表按 id 折叠天然有界、播报表是纯追加 —— 上限保的是内存与
 * 渲染行数；保留**尾部**（排障价值在「最后发生了什么」），丢弃量记进
 * droppedLines 让页面说在明处，不静默吞。
 */
const MAX_BUILD_LINES = 400

/**
 * 构建进度流消费端：NDJSON 原始 chunk → 播报表的一条流水线
 * （拆包 → 逐行解析 → 文本行追加 / 步骤行按 id 折叠 → 终态捕获）。
 */
export function createBuildFeed(): BuildFeed {
  const parser = createNdjsonParser()
  const lines: BuildFeedLine[] = []
  /** 文本行的行号发生器（key 用；闭包内一份 —— 两场构建的行号互不串号）。 */
  let textLineSeq = 0
  let dropped = 0
  let ended = false
  let terminal: BuildTerminal | null = null

  function handleLine(line: string): void {
    const parsed = parseBuildStreamLine(line)
    if (!parsed) return // 坏行跳过，不打断流
    const item = parsed.item
    if (item) {
      if (item.done || item.error !== '') {
        terminal = { ok: item.done, error: item.error }
      } else if (item.id !== '') {
        const fold = lines.find((l) => l.id === item.id)
        if (fold) {
          // 同步骤后帧是更新（agent 折叠器跨窗去重后仍可能同 id 多帧 —— 状态文案
          // 以最新一帧为准，与 pull 层表的纪律同款）。
          if (item.status !== '') fold.text = item.status
        } else {
          lines.push({ key: `s:${item.id}`, id: item.id, text: item.status, step: true })
        }
      } else {
        // 文本行（stream）与无步骤 id 的状态行（消息行）都按到达序进播报：构建
        // 的主体就是这份输出，「按顺序读」是它的全部语义 —— 消息行不单设 note
        // （pull 的 note 是「层表之上的阶段提示」，这里播报本身就是阶段）。
        lines.push({
          key: `l:${textLineSeq++}`,
          id: '',
          text: item.stream !== '' ? item.stream : item.status,
          step: false
        })
      }
      if (lines.length > MAX_BUILD_LINES) {
        dropped += lines.length - MAX_BUILD_LINES
        lines.splice(0, lines.length - MAX_BUILD_LINES)
      }
    }
    if (parsed.eof) ended = true
  }

  return {
    pushRaw(chunk: string): void {
      if (ended) return // eof 后的残余字节不再入账（与 stats/pull 同纪律）
      for (const line of parser.push(chunk)) handleLine(line)
    },
    get lines(): readonly BuildFeedLine[] {
      return lines.slice()
    },
    get droppedLines(): number {
      return dropped
    },
    get eof(): boolean {
      return ended
    },
    get terminal(): BuildTerminal | null {
      return terminal
    }
  }
}

// ── 构建表单的协议镜像校验（跨语言无法 import，正则逐字照抄、漂移由测试钉）──
// 口径：tag 与 image:tag/pull 同一把镜像引用尺（pull.ts 的 isValidImageRef，
// 本文件不重复）；其余三把是 build 专属。

/**
 * 构建上下文文件名白名单 —— **逐字镜像**协议 `IsDockerBuildContextFilename`：
 * transferDir 内的文件名（无路径成分）、tar / tar.gz / tgz 三形态。与 image:load
 * 的口径分工：load 只拦空值（格式由服务端兜）；build 的这把尺前端先挡 ——
 * 构建失败的等待是分钟级，一眼就地改对比一轮「受理→失败」便宜得多。
 */
const BUILD_CONTEXT_FILENAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*\.(tar|tar\.gz|tgz)$/

export function isValidBuildContextFilename(s: string): boolean {
  return s !== '' && BUILD_CONTEXT_FILENAME_RE.test(s)
}

/**
 * dockerfile 的上下文内相对路径 —— **逐字镜像**协议 `IsDockerBuildSubpath`：
 * POSIX 分隔符、段非空且匹配 `[A-Za-z0-9][A-Za-z0-9._-]*`、任何一段不得是
 * `..`/`.`（路径逃逸形态出不去）、反斜杠与 NUL 一律拒绝。协议把形态收敛到
 * 「普通文件的相对路径」子集，daemon 的解析就只剩一种答案 —— 前端同一把尺
 * 先挡，别把 `../Dockerfile` 送去吃一个 400。
 */
export function isValidBuildSubpath(s: string): boolean {
  if (s === '' || s.length > 512) return false
  if (s.includes('\\') || s.includes('\0')) return false
  const segRe = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
  return s.split('/').every((seg) => seg !== '' && seg !== '.' && seg !== '..' && segRe.test(seg))
}

/**
 * build-arg 键 —— **逐字镜像**协议 `IsDockerBuildArgKey`（POSIX 标识符：字母或
 * 下划线开头，其后字母数字下划线；与 create 的 env 键同一把尺）。键会变成
 * Dockerfile 里被引用的符号，形态必须收敛到标识符子集。
 */
const BUILD_ARG_KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/

export function isValidBuildArgKey(s: string): boolean {
  return s !== '' && BUILD_ARG_KEY_RE.test(s)
}

/** build-args 的条目上限（协议 maxDockerBuildArgs = 32，与 create 的 env 同档）。 */
export const MAX_BUILD_ARGS = 32

/**
 * build-arg 单值的字节上限（协议 maxDockerBuildArgValueBytes = 512B）。
 * **刻意不传秘密**：build-arg 会永驻镜像历史（docker history 可见）—— 这是协议
 * 注释钉的披露义务，表单侧的 hint 必须把这一点讲清楚（见 build-progress-dialog）。
 */
export const MAX_BUILD_ARG_VALUE_BYTES = 512

// ── P3·构建上下文上传（② 端点）的客户端预检 ─────────────────────────
// 上传端点（POST /docker/hosts/:id/build-context）对内容的执法在服务端：512MB
// 尺寸闸「即拒不等传完」、gzip 魔数「发出任何一帧之前拒」。客户端把同一把尺前移
// 到选择文件的瞬间 —— 不是替服务端执法（服务端照拒），是**别让用户白传 512MB
// 才收到 400**：上传的在途时间以分钟计，预检的反馈以毫秒计。

/**
 * 构建上下文的字节上限（**镜像协议常量** MaxDockerBuildContextBytes = 512<<20：
 * 协议定义尺、core/agent 两端执法，前端预检与提示共用同一个数）。
 */
export const MAX_BUILD_CONTEXT_BYTES = 512 * 1024 * 1024

/**
 * 文件选择器的 accept 与就地校验共用的后缀白名单（tar / tar.gz / tgz / gz）。
 * 刻意比「合法 context 文件名」宽一档：accept 是**引导**不是闸（.tar 也可能只是
 * 改错了后缀的 gzip），真正的闸是下面的 gzip 魔数 —— 它读的是内容不是名字。
 */
const BUILD_CONTEXT_SUFFIX_RE = /\.(tar|tar\.gz|tgz|gz)$/i

/** 文件选择器的 accept 值（与后缀白名单同一份事实，写死一处只会漂移）。 */
export const BUILD_CONTEXT_ACCEPT = '.tar,.tar.gz,.tgz,.gz'

/** 后缀是否在构建上下文的选择白名单内（就地校验用，与 accept 同一份事实）。 */
export function hasBuildContextSuffix(name: string): boolean {
  return BUILD_CONTEXT_SUFFIX_RE.test(name.trim())
}

/**
 * 读文件头两字节判 gzip 魔数（1f 8b）。
 *
 * **逐字镜像**服务端的上传段判定（core 的 buildCtxGzipMagic）：端点只收 gzip
 * 压缩的 tar 归档（agent 解压后喂 daemon），裸 tar 会被服务端以「内容不是 gzip
 * 压缩的 tar 归档」拒 —— 前端先读同样的两个字节，把同一句话提前到选文件的瞬间。
 * 用 FileReader（而非 blob.arrayBuffer()）读**切片**（file.slice(0, 2)）：
 * 只碰头部两字节，512MB 的文件也不会被整个读进内存。
 */
export function isGzipFile(file: Blob): Promise<boolean> {
  return new Promise((resolve) => {
    const reader = new FileReader()
    reader.onload = () => {
      const buf = reader.result
      const ok =
        buf instanceof ArrayBuffer &&
        buf.byteLength >= 2 &&
        new Uint8Array(buf)[0] === 0x1f &&
        new Uint8Array(buf)[1] === 0x8b
      resolve(ok)
    }
    // 读两个字节都失败 = 文件不可读：当「不是 gzip」处理（服务端同样会拒），
    // 不让一次 IO 意外变成白屏。
    reader.onerror = () => resolve(false)
    reader.readAsArrayBuffer(file.slice(0, 2))
  })
}
