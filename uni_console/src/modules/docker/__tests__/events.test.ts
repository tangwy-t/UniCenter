/**
 * utils/events 的纯逻辑钉子：行解析（回放/实时/坏行）、时间排序窗口与显示上限、
 * 重连回放的去重守卫、action 中文化映射（含未映射透传）、秒级相对时间、
 * 四类资源的图标/色槽、账目口径句。
 *
 * 与 log-viewer.test.ts 同一约定：组件不挂载（组件级行为在 events-feed.test.ts，
 * 那边是 jsdom + vi.mock 的流消费测试）。
 */
import { describe, expect, it } from 'vitest'
import {
  createEventsFeed,
  eventActionText,
  eventFeedHint,
  eventRelativeTime,
  eventTypeMeta,
  EVENT_ACTION_TEXT,
  MAX_EVENT_ENTRIES,
  parseEventLine
} from '../utils/events'

/** 造一行事件的 JSON（字段形状对齐 core 的 eventNDJSONLine）。 */
function line(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    host_id: 1,
    hostname: 'bogon',
    t: 1790600000,
    type: 'container',
    action: 'start',
    actor_name: 'uni-center-core',
    actor_id: 'sha256:abcdef123456',
    ...overrides
  })
}

describe('事件行解析', () => {
  it('好行：snake_case → camelCase 全字段映射，actor 缺省给空串（omitempty）', () => {
    const e = parseEventLine(
      '{"host_id":1,"hostname":"bogon","t":1790600000,"type":"image","action":"pull"}'
    )
    expect(e).toEqual({
      hostId: '1',
      hostname: 'bogon',
      t: 1790600000,
      type: 'image',
      action: 'pull',
      actorName: '',
      actorId: ''
    })
  })

  it('完整行：字段齐给全值（host_id 数字转字符串）', () => {
    const e = parseEventLine(line())
    expect(e).toEqual({
      hostId: '1',
      hostname: 'bogon',
      t: 1790600000,
      type: 'container',
      action: 'start',
      actorName: 'uni-center-core',
      actorId: 'sha256:abcdef123456'
    })
  })

  it('坏 JSON / 非对象（数组、字面量）给 null，调用方跳行不打断流', () => {
    expect(parseEventLine('not json')).toBeNull()
    expect(parseEventLine('[1,2]')).toBeNull()
    expect(parseEventLine('42')).toBeNull()
    expect(parseEventLine('null')).toBeNull()
  })

  it('形状不符给 null：缺字段或类型不对各查一项', () => {
    expect(parseEventLine('{"hostname":"h","t":1,"type":"container","action":"start"}')).toBeNull() // 缺 host_id
    expect(parseEventLine(line({ host_id: '1' }))).toBeNull() // host_id 不是数字
    expect(parseEventLine(line({ t: 'x' }))).toBeNull() // t 不是数字
    expect(parseEventLine(line({ t: 0 }))).toBeNull() // t 非正（缺字段被 JSON 解成 0 的判据）
    expect(parseEventLine(line({ type: '' }))).toBeNull() // type 空串
    expect(parseEventLine(line({ action: '' }))).toBeNull() // action 空串
    expect(parseEventLine(line({ hostname: 3 }))).toBeNull() // hostname 不是串
    expect(parseEventLine(line({ actor_name: 7 }))).toBeNull() // actor_name 出现却不是串
    expect(parseEventLine(line({ actor_id: [] }))).toBeNull() // actor_id 出现却不是串
  })

  it('空行（拆包器已过滤）与空白行给 null', () => {
    expect(parseEventLine('')).toBeNull()
    expect(parseEventLine('   ')).toBeNull()
  })
})

describe('事件窗口：回放、实时与排序', () => {
  it('回放 + 实时连续喂入，条目按 t 升序（跨主机乱序到达也排好）', () => {
    const feed = createEventsFeed()
    // 回放按主机成段：h1 的三条先到（t 100..102），h2 的再到（t 90..92）
    feed.pushRaw(
      Array.from({ length: 3 }, (_, i) => line({ host_id: 1, t: 100 + i })).join('\n') + '\n'
    )
    feed.pushRaw(
      Array.from({ length: 3 }, (_, i) => line({ host_id: 2, t: 90 + i })).join('\n') + '\n'
    )
    // 实时：两台交错
    feed.pushRaw(line({ host_id: 1, t: 200, action: 'die' }) + '\n')
    feed.pushRaw(line({ host_id: 2, t: 201, action: 'pull', type: 'image' }) + '\n')

    const es = feed.entries
    expect(es).toHaveLength(8)
    expect(es[0].t).toBe(90) // 全场最早（h2 的回放开头），不是最先到达的 h1
    expect(es.at(-1)!.t).toBe(201)
    for (let i = 1; i < es.length; i++) {
      expect(es[i].t).toBeGreaterThanOrEqual(es[i - 1].t)
    }
    expect(feed.totalSeen).toBe(8)
  })

  it('同 t 的两条按到达序并列（稳定，不随合并乱跳）', () => {
    const feed = createEventsFeed()
    feed.pushRaw(
      line({ host_id: 1, t: 100, action: 'start' }) +
        '\n' +
        line({ host_id: 1, t: 100, action: 'die' }) +
        '\n'
    )
    feed.pushRaw(line({ host_id: 1, t: 100, action: 'stop' }) + '\n')
    expect(feed.entries.map((e) => e.action)).toEqual(['start', 'die', 'stop'])
  })

  it('坏行夹在好行中间：跳过它，前后照常入窗', () => {
    const feed = createEventsFeed()
    feed.pushRaw(line({ t: 100 }) + '\n{broken json\n' + line({ t: 101, action: 'die' }) + '\n')
    expect(feed.entries).toHaveLength(2)
    expect(feed.entries.map((e) => e.action)).toEqual(['start', 'die'])
  })

  it('半行跨 chunk 续接：一个 JSON 劈两半喂也能拼回来', () => {
    const feed = createEventsFeed()
    const full = line({ t: 100 })
    const cut = Math.floor(full.length / 2)
    feed.pushRaw(full.slice(0, cut))
    feed.pushRaw(full.slice(cut) + '\n')
    expect(feed.entries).toHaveLength(1)
    expect(feed.entries[0].action).toBe('start')
  })

  it('显示上限丢**最早**的（按 t 而非到达序）：三台主机回放 150 条只留最新 100', () => {
    const feed = createEventsFeed()
    // h1 回放 t=1..50，h2 回放 t=51..100，h3 回放 t=101..150 —— 到达序是 1..150
    for (let h = 1; h <= 3; h++) {
      feed.pushRaw(
        Array.from({ length: 50 }, (_, i) => line({ host_id: h, t: (h - 1) * 50 + i + 1 })).join(
          '\n'
        ) + '\n'
      )
    }
    const es = feed.entries
    expect(es).toHaveLength(MAX_EVENT_ENTRIES)
    // 按到达序截断会整段丢掉 h1（含它最新的 t=50）；按 t 截断丢的是最早的 t=1..50
    expect(es[0].t).toBe(51)
    expect(es.at(-1)!.t).toBe(150)
    expect(feed.totalSeen).toBe(150)
  })

  it('恰好等于上限不算截断（「仅显示最近 N 条」只给真被截断的内容看）', () => {
    const feed = createEventsFeed(3)
    feed.pushRaw([line({ t: 1 }), line({ t: 2 }), line({ t: 3 })].join('\n') + '\n')
    expect(feed.entries).toHaveLength(3)
  })

  it('上限 100 是默认值（提示文案里的数字与窗口同源）', () => {
    expect(MAX_EVENT_ENTRIES).toBe(100)
  })
})

describe('重连回放的去重守卫', () => {
  it('重连后重放已见事件（含同秒边界）：全部丢弃，窗口不重复', () => {
    const feed = createEventsFeed()
    const batch = [
      line({ host_id: 1, t: 100 }),
      line({ host_id: 1, t: 101 }),
      line({ host_id: 2, t: 100 })
    ].join('\n')
    feed.pushRaw(batch + '\n')
    feed.pushRaw(batch + '\n') // 断流重连：同一批重放（t ≤ 各自主机的已见最新）
    expect(feed.entries).toHaveLength(3)
    expect(feed.totalSeen).toBe(3) // 接受口径不含被守卫丢掉的
    expect(feed.deduped).toBe(3)
  })

  it('守卫按主机记账：h1 的旧事不影响 h2 的新事（各机时钟无对齐保证）', () => {
    const feed = createEventsFeed()
    feed.pushRaw(line({ host_id: 1, t: 500 }) + '\n')
    // h2 首次出现，t=100 < h1 的 500，但 h2 自己没见过 —— 接受
    feed.pushRaw(line({ host_id: 2, t: 100 }) + '\n')
    expect(feed.entries).toHaveLength(2)
    expect(feed.deduped).toBe(0)
  })

  it('同秒**不同**动作的新事件照常接受（宁重不漏的反面是不漏真事）', () => {
    const feed = createEventsFeed()
    feed.pushRaw(line({ host_id: 1, t: 100, action: 'start' }) + '\n')
    feed.pushRaw(line({ host_id: 1, t: 100, action: 'die' }) + '\n')
    expect(feed.entries.map((e) => e.action)).toEqual(['start', 'die'])
    expect(feed.deduped).toBe(0)
  })

  it('同秒同动作同对象（重放边界的精确形态）丢弃', () => {
    const feed = createEventsFeed()
    feed.pushRaw(line({ host_id: 1, t: 100, action: 'die' }) + '\n')
    feed.pushRaw(line({ host_id: 1, t: 100, action: 'die' }) + '\n')
    expect(feed.entries).toHaveLength(1)
    expect(feed.deduped).toBe(1)
  })

  it('重连后的新事件（t 更新）照常接受并排序', () => {
    const feed = createEventsFeed()
    feed.pushRaw(line({ host_id: 1, t: 100 }) + '\n')
    feed.pushRaw(line({ host_id: 1, t: 100 }) + '\n') // 重放（被守卫丢）
    feed.pushRaw(line({ host_id: 1, t: 300, action: 'pull', type: 'image' }) + '\n')
    expect(feed.entries.map((e) => e.t)).toEqual([100, 300])
    expect(feed.deduped).toBe(1)
  })
})

describe('action 中文化映射', () => {
  it('高频动作映射为中文', () => {
    expect(eventActionText('start')).toBe('启动')
    expect(eventActionText('stop')).toBe('停止')
    expect(eventActionText('die')).toBe('退出')
    expect(eventActionText('create')).toBe('创建')
    expect(eventActionText('destroy')).toBe('销毁')
    expect(eventActionText('pull')).toBe('拉取')
    expect(eventActionText('health_status')).toBe('健康检查')
    expect(eventActionText('exec_start')).toBe('开始执行')
    expect(eventActionText('oom')).toBe('内存不足')
  })

  it('未映射的动作原样透传（新版本 docker 引入的动作不至于变成空白）', () => {
    expect(eventActionText('frobnicate')).toBe('frobnicate')
    expect(eventActionText('')).toBe('')
  })

  it('映射表本身可被整表断言（加词条忘了映射函数是另一类故障，留给渲染测试）', () => {
    expect(Object.keys(EVENT_ACTION_TEXT).length).toBeGreaterThanOrEqual(20)
  })
})

describe('相对时间（秒级粒度）', () => {
  const NOW = 1_790_600_000

  it('1 秒内（含未来/时钟偏差）夹到「刚刚」，不出负数', () => {
    expect(eventRelativeTime(NOW, NOW)).toBe('刚刚')
    expect(eventRelativeTime(NOW + 5, NOW)).toBe('刚刚')
  })

  it('秒档：3 秒前 / 59 秒前', () => {
    expect(eventRelativeTime(NOW - 3, NOW)).toBe('3 秒前')
    expect(eventRelativeTime(NOW - 59, NOW)).toBe('59 秒前')
  })

  it('分 / 时 / 天 / 月各档边界', () => {
    expect(eventRelativeTime(NOW - 60, NOW)).toBe('1 分钟前')
    expect(eventRelativeTime(NOW - 3599, NOW)).toBe('59 分钟前')
    expect(eventRelativeTime(NOW - 3600, NOW)).toBe('1 小时前')
    expect(eventRelativeTime(NOW - 86_400, NOW)).toBe('1 天前')
    expect(eventRelativeTime(NOW - 2_592_000, NOW)).toBe('1 个月前')
  })

  it('nowSec 带小数也按整秒截断（调用方给的是 Date.now()/1000）', () => {
    expect(eventRelativeTime(NOW - 2, NOW + 0.9)).toBe('2 秒前')
  })
})

describe('资源类型的图标与色槽', () => {
  it('四类资源各有图标与自己的色槽', () => {
    expect(eventTypeMeta('container')).toEqual({ icon: 'ri:box-3-line', tone: 'container' })
    expect(eventTypeMeta('image')).toEqual({ icon: 'ri:image-2-line', tone: 'image' })
    expect(eventTypeMeta('volume')).toEqual({ icon: 'ri:database-2-line', tone: 'volume' })
    expect(eventTypeMeta('network')).toEqual({ icon: 'ri:node-tree', tone: 'network' })
  })

  it('未知类型走问号 + 中性兜底（「不认识」本身要被看出来）', () => {
    expect(eventTypeMeta('plugin')).toEqual({ icon: 'ri:question-line', tone: 'other' })
    expect(eventTypeMeta('')).toEqual({ icon: 'ri:question-line', tone: 'other' })
  })
})

describe('账目口径句', () => {
  it('没有累计时不给句（空态另说，不给「累计 0 条」）', () => {
    expect(eventFeedHint(0, 0)).toBe('')
  })

  it('未被上限截断时只报累计', () => {
    expect(eventFeedHint(12, 12)).toBe('累计 12 条')
  })

  it('被截断时说清楚只显示最近一段（上限数与句同源）', () => {
    expect(eventFeedHint(250, 100)).toBe('累计 250 条 · 仅显示最近 100 条')
    expect(eventFeedHint(7, 3, 3)).toBe('累计 7 条 · 仅显示最近 3 条')
  })
})
