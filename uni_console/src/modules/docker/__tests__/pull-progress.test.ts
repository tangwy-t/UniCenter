/**
 * 拉取进度面（4b）纯逻辑测试：行解析校验、按层折叠、字节记账、终态捕获、
 * 输入引用校验与字节文案。
 *
 * 组件把哪条数据接到哪个控件（取消路径 / 双通道收尾 / 关闭清理）在
 * pull-dialog.test.ts（jsdom）；这里只钉纯函数的语义。
 *
 * P2 起推送与拉取共用这份折叠器（daemon 的同一个 JSON 进度流，LayerFeedFlavor
 * 换语义字面量）：推送组钉的是**换词后折叠纪律不变** —— 拉取组的每个用例都该
 * 有对应的推送形态。构建帧是另一种形态，纯逻辑在 build-progress.test.ts。
 */
import { describe, expect, it } from 'vitest'
import {
  createPullFeed,
  createPushFeed,
  formatPullBytes,
  isValidImageRef,
  isLayerDoneStatus,
  isPushLayerDoneStatus,
  layerPercent,
  layerShortId,
  parsePullStreamLine
} from '../utils/pull'

/** 一条进度行（t 给合法默认；行尾带 \n —— NDJSON 的行都以换行收尾，编码器如此）。 */
function frame(o: Record<string, unknown>): string {
  return `${JSON.stringify({ t: 1700000000000, ...o })}\n`
}

/** 三个层 id（64 位十六进制形态，与 daemon 的层 id 同形）。 */
const L1 = 'aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff66667777888899990000'
const L2 = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
const L3 = 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'

describe('行解析（parsePullStreamLine）', () => {
  it('层行：字段全解析', () => {
    const p = parsePullStreamLine(frame({ id: L1, status: 'Downloading', current: 5, total: 10 }))
    expect(p).toEqual({
      item: {
        t: 1700000000000,
        id: L1,
        status: 'Downloading',
        current: 5,
        total: 10,
        done: false,
        error: ''
      },
      eof: false
    })
  })

  it('成功终态行：done=true（eof 同行挂靠是 core 的活，解析侧只认字段）', () => {
    const p = parsePullStreamLine(frame({ done: true, eof: true }))
    expect(p?.item?.done).toBe(true)
    expect(p?.eof).toBe(true)
  })

  it('失败终态行：error 原文透传', () => {
    const p = parsePullStreamLine(
      frame({ error: 'denied: requested access to the resource is denied' })
    )
    expect(p?.item?.error).toBe('denied: requested access to the resource is denied')
    expect(p?.item?.done).toBe(false)
  })

  it('单独的 eof 行（t 是零值序列化）：只当 eof 信号，不当坏行', () => {
    const p = parsePullStreamLine(JSON.stringify({ seq: 9, t: 0, eof: true }))
    expect(p).toEqual({ item: null, eof: true })
  })

  it('非 JSON / 非对象 / eof 非布尔 → null（坏行跳过，不打断流）', () => {
    expect(parsePullStreamLine('not json')).toBeNull()
    expect(parsePullStreamLine('[1,2]')).toBeNull()
    expect(parsePullStreamLine('null')).toBeNull()
    expect(
      parsePullStreamLine(frame({ id: L1 }).replace('"t":1700000000000,', '"t":"x",'))
    ).toBeNull()
    expect(parsePullStreamLine(JSON.stringify({ t: 1, eof: 'yes' }))).toBeNull()
  })

  it('协议校验的镜像：t 非正 / 双终态 / 负字节 / current 越过 total → null', () => {
    expect(parsePullStreamLine(frame({ id: L1, t: 0, status: 'x' }))).toBeNull()
    expect(parsePullStreamLine(frame({ id: L1, done: true, error: 'x' }))).toBeNull()
    expect(parsePullStreamLine(frame({ id: L1, current: -1 }))).toBeNull()
    expect(parsePullStreamLine(frame({ id: L1, total: -5 }))).toBeNull()
    expect(parsePullStreamLine(frame({ id: L1, current: 11, total: 10 }))).toBeNull()
  })

  it('字段类型不符 → null（数字字段给了字符串这类形状意外）', () => {
    expect(parsePullStreamLine(frame({ id: L1, current: '5' }))).toBeNull()
    expect(parsePullStreamLine(frame({ id: 42, status: 'x' }))).toBeNull()
    expect(parsePullStreamLine(frame({ id: L1, done: 'yes' }))).toBeNull()
  })
})

describe('按层折叠（createPullFeed）', () => {
  it('新层追加、同层更新：三个 id 各占一行，行内是最新一帧', () => {
    const feed = createPullFeed()
    feed.pushRaw(
      [
        frame({ id: L1, status: 'Pulling fs layer' }),
        frame({ id: L2, status: 'Pulling fs layer' }),
        frame({ id: L1, status: 'Downloading', current: 500, total: 1000 }),
        frame({ id: L3, status: 'Already exists' })
      ].join('\n')
    )
    expect(feed.layers.map((l) => l.id)).toEqual([L1, L2, L3])
    expect(feed.layers[0]).toMatchObject({ status: 'Downloading', current: 500, total: 1000 })
    expect(feed.layers[2]).toMatchObject({ status: 'Already exists', done: true })
  })

  it('跨 chunk 的半行能接上（NDJSON 拆包纪律在拉取流同样成立）', () => {
    const feed = createPullFeed()
    const line = frame({ id: L1, status: 'Downloading', current: 3, total: 9 })
    const cut = Math.floor(line.length / 2)
    feed.pushRaw(line.slice(0, cut))
    expect(feed.layers.length).toBe(0) // 半行不成帧
    feed.pushRaw(line.slice(cut)) // 后半自带行尾换行，补齐即成帧
    expect(feed.layers.length).toBe(1)
    expect(feed.layers[0]).toMatchObject({ id: L1, status: 'Downloading' })
  })

  it('完成类状态行不带字节：既有 current/total 不被抹回 0（不退零）', () => {
    const feed = createPullFeed()
    feed.pushRaw(frame({ id: L1, status: 'Downloading', current: 800, total: 1000 }))
    feed.pushRaw(frame({ id: L1, status: 'Verifying Checksum' }))
    feed.pushRaw(frame({ id: L1, status: 'Download complete' }))
    const layer = feed.layers[0]!
    expect(layer.current).toBe(800)
    expect(layer.total).toBe(1000)
  })

  it('汇总按下载段记账：Download complete 补满；解压段开始后汇总不倒退', () => {
    const feed = createPullFeed()
    feed.pushRaw(frame({ id: L1, status: 'Downloading', current: 800, total: 1000 }))
    expect(feed.downloadedBytes).toBe(800)
    expect(feed.totalBytes).toBe(1000)

    feed.pushRaw(frame({ id: L1, status: 'Download complete' }))
    expect(feed.downloadedBytes).toBe(1000) // 下载段补满

    // 条形字段跟着解压段换对（50/400），汇总仍是下载段的账
    feed.pushRaw(frame({ id: L1, status: 'Extracting', current: 50, total: 400 }))
    expect(feed.layers[0]).toMatchObject({ current: 50, total: 400 })
    expect(feed.downloadedBytes).toBe(1000)
    expect(feed.totalBytes).toBe(1000)
  })

  it('Pull complete 标记层终态；层数完成比随之上升', () => {
    const feed = createPullFeed()
    feed.pushRaw(frame({ id: L1, status: 'Downloading', current: 1, total: 2 }))
    feed.pushRaw(frame({ id: L2, status: 'Already exists' }))
    expect(feed.doneLayers).toBe(1)

    feed.pushRaw(frame({ id: L1, status: 'Pull complete' }))
    expect(feed.doneLayers).toBe(2)
    expect(feed.layers[0]!.done).toBe(true)
  })

  it('消息行（无 id）：最新一条进 note；终态行不清掉它', () => {
    const feed = createPullFeed()
    feed.pushRaw(frame({ status: 'Pulling from library/nginx:latest' }))
    expect(feed.note).toBe('Pulling from library/nginx:latest')
    feed.pushRaw(frame({ status: 'Digest: sha256:abc' }))
    expect(feed.note).toBe('Digest: sha256:abc')
    feed.pushRaw(frame({ id: L1, status: 'Downloading', current: 1, total: 2 }))
    expect(feed.note).toBe('Digest: sha256:abc') // 层行不动消息行
    feed.pushRaw(frame({ done: true, eof: true }))
    expect(feed.note).toBe('Digest: sha256:abc') // 终态行没有 status，不覆盖
  })

  it('终态项恰一条被捕获（成功 / 失败两种）', () => {
    const ok = createPullFeed()
    ok.pushRaw(
      [frame({ id: L1, status: 'Pull complete' }), frame({ done: true, eof: true })].join('\n')
    )
    expect(ok.terminal).toEqual({ ok: true, error: '' })
    expect(ok.eof).toBe(true)

    const fail = createPullFeed()
    fail.pushRaw(frame({ error: 'manifest unknown', eof: true }))
    expect(fail.terminal).toEqual({ ok: false, error: 'manifest unknown' })
  })

  it('eof 之后残余字节不再入账（与 stats 流同纪律）', () => {
    const feed = createPullFeed()
    feed.pushRaw(frame({ id: L1, status: 'Pull complete' }))
    feed.pushRaw(frame({ done: true, eof: true }))
    const before = feed.layers.length
    feed.pushRaw(frame({ id: L2, status: 'Pulling fs layer' }))
    expect(feed.layers.length).toBe(before)
  })

  it('坏行被跳过：折叠表不进脏数据，流不中断', () => {
    const feed = createPullFeed()
    feed.pushRaw(
      [
        '{bad json',
        frame({ id: L1, done: true, error: '双终态' }),
        frame({ id: L2, status: 'Waiting' })
      ].join('\n')
    )
    expect(feed.layers.map((l) => l.id)).toEqual([L2])
  })
})

describe('推送语义（createPushFeed —— flavor 换词、折叠纪律不变）', () => {
  it('层终态判定：Pushed / Layer already exists / Mounted from …；Pushing 不算', () => {
    expect(isPushLayerDoneStatus('Pushed')).toBe(true)
    expect(isPushLayerDoneStatus('Layer already exists')).toBe(true)
    expect(isPushLayerDoneStatus('Mounted from library/nginx')).toBe(true)
    expect(isPushLayerDoneStatus('Pushing')).toBe(false)
    expect(isPushLayerDoneStatus('Preparing')).toBe(false)
  })

  it('字节段换 Pushing：汇总按推送段记账；拉取的 Downloading 不再是字节段', () => {
    const feed = createPushFeed()
    feed.pushRaw(frame({ id: L1, status: 'Pushing', current: 300, total: 600 }))
    expect(feed.downloadedBytes).toBe(300)
    expect(feed.totalBytes).toBe(600)
    // 拉取语义词对推送折叠器只是普通状态行：不进字节账（也不报错）。
    feed.pushRaw(frame({ id: L2, status: 'Downloading', current: 100, total: 200 }))
    expect(feed.downloadedBytes).toBe(300)
    expect(feed.totalBytes).toBe(600)
  })

  it('终态即补满（push 没有中途补满态）：Pushed 后汇总补满、字节不退零', () => {
    const feed = createPushFeed()
    feed.pushRaw(frame({ id: L1, status: 'Pushing', current: 800, total: 1000 }))
    feed.pushRaw(frame({ id: L1, status: 'Pushed' }))
    expect(feed.layers[0]!.done).toBe(true)
    // Pushed 不带 progressDetail：条形字段保留 800/1000（不退零），记账补满到 1000。
    expect(feed.layers[0]).toMatchObject({ current: 800, total: 1000 })
    expect(feed.downloadedBytes).toBe(1000)
  })

  it('同层折叠 / 消息行 / 终态捕获 / eof 纪律与拉取逐条同源（只是词换了）', () => {
    const feed = createPushFeed()
    feed.pushRaw(
      [
        frame({ status: 'The push refers to repository [harbor.example.com/app]' }),
        frame({ id: L1, status: 'Preparing' }),
        frame({ id: L1, status: 'Pushing', current: 5, total: 10 }),
        frame({ id: L2, status: 'Layer already exists' })
      ].join('\n')
    )
    expect(feed.layers.map((l) => l.id)).toEqual([L1, L2])
    expect(feed.layers[0]).toMatchObject({ status: 'Pushing', current: 5, total: 10 })
    expect(feed.doneLayers).toBe(1)
    expect(feed.note).toBe('The push refers to repository [harbor.example.com/app]')
    feed.pushRaw(frame({ done: true, eof: true }))
    expect(feed.terminal).toEqual({ ok: true, error: '' })
    expect(feed.eof).toBe(true)
  })

  it('拉取语义未被推送改动（flavor 缺省值保持 4b 行为）：Downloading 仍是字节段', () => {
    // 一条对拍：同一段帧喂两个 feed，拉取侧认 Downloading、推送侧不认 ——
    // 缺省 flavor 没被 PUSH_FEED_FLAVOR 污染（createPullFeed() 无参调用走拉取语义）。
    const pull = createPullFeed()
    pull.pushRaw(frame({ id: L1, status: 'Downloading', current: 7, total: 10 }))
    expect(pull.downloadedBytes).toBe(7)
  })
})

describe('进度条计算（layerPercent / 层终态判定 / 短 id）', () => {
  it('有 total：按比例取整', () => {
    expect(layerPercent(500, 1000)).toBe(50)
    expect(layerPercent(999, 1000)).toBe(100)
    expect(layerPercent(1, 3)).toBe(33)
  })

  it('无 total（0/负/非有限）：0 —— 「没有分母」由视图转不确定态，不是 0%', () => {
    expect(layerPercent(100, 0)).toBe(0)
    expect(layerPercent(100, -1)).toBe(0)
    expect(layerPercent(100, Number.NaN)).toBe(0)
    expect(layerPercent(Number.NaN, 100)).toBe(0)
  })

  it('越界夹回区间（坏数据不把条画出界）', () => {
    expect(layerPercent(1100, 1000)).toBe(100)
    expect(layerPercent(-5, 100)).toBe(0)
  })

  it('层终态判定：Pull complete / Already exists；下载完成不算终态', () => {
    expect(isLayerDoneStatus('Pull complete')).toBe(true)
    expect(isLayerDoneStatus('Already exists')).toBe(true)
    expect(isLayerDoneStatus('Download complete')).toBe(false)
    expect(isLayerDoneStatus('Extracting')).toBe(false)
  })

  it('短 id：12 位；sha256: 前缀先剥', () => {
    expect(layerShortId(L1)).toBe('aaaa1111bbbb')
    expect(layerShortId(`sha256:${L1}`)).toBe('aaaa1111bbbb')
    expect(layerShortId('short')).toBe('short')
  })
})

describe('输入校验（isValidImageRef —— 协议 dockerImageRefRe 的镜像）', () => {
  it('合法：短名 / 仓库标签 / 端口仓库 / digest', () => {
    expect(isValidImageRef('nginx')).toBe(true)
    expect(isValidImageRef('nginx:latest')).toBe(true)
    expect(isValidImageRef('library/nginx:1.25-alpine')).toBe(true)
    expect(isValidImageRef('registry.example.com:5000/team/app:v1')).toBe(true)
    expect(isValidImageRef('nginx@sha256:0123456789abcdef')).toBe(true)
  })

  it('非法：空串 / 非法首字符 / 空白 / 中文 / 非法符号', () => {
    expect(isValidImageRef('')).toBe(false)
    expect(isValidImageRef('-bad')).toBe(false)
    expect(isValidImageRef('/lead')).toBe(false)
    expect(isValidImageRef('nginx latest')).toBe(false)
    expect(isValidImageRef('镜像:latest')).toBe(false)
    expect(isValidImageRef('nginx?tag')).toBe(false)
    expect(isValidImageRef('nginx tag')).toBe(false)
  })
})

describe('字节文案（formatPullBytes）', () => {
  it('三段阶梯：B / KB / MB / GB，各留合理小数', () => {
    expect(formatPullBytes(0)).toBe('0 B')
    expect(formatPullBytes(512)).toBe('512 B')
    expect(formatPullBytes(2048)).toBe('2.0 KB')
    expect(formatPullBytes(32 * 1024)).toBe('32.0 KB')
    expect(formatPullBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatPullBytes(3 * 1024 * 1024 * 1024)).toBe('3.00 GB')
  })

  it('非法输入给 —（与 formatByUnit 的缺值口径一致，0 是合法值）', () => {
    expect(formatPullBytes(Number.NaN)).toBe('—')
    expect(formatPullBytes(-1)).toBe('—')
  })
})
