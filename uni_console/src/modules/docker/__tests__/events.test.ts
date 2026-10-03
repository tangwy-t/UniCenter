/**
 * utils/events 的纯逻辑钉子：行解析（回放/实时/坏行/**sentinel**）、退出码三值
 * 语义、时间排序窗口与显示上限、重连回放的去重守卫、**去重键**（跨来源合并判据）、
 * **过滤谓词**（与 core 同一把尺）、**历史行解析与详版页时间轴**（历史 ∪ 实时、
 * t 降序、内容键去重）、action 中文化映射（协议词表逐词条覆盖 + health_status 后缀
 * 结论 + 表外词元透传）、秒级相对时间（含毫秒除千的浮点整段取整）、绝对时刻格式化、
 * 四类资源的图标/色槽/类型名、账目口径句。
 *
 * 与 log-viewer.test.ts 同一约定：组件不挂载（组件级行为在 events-feed.test.ts，
 * 那边是 jsdom + vi.mock 的流消费测试）。
 */
import { describe, expect, it } from 'vitest'
import {
  createEventsFeed,
  createEventsLog,
  eventActionText,
  eventClockTime,
  eventFeedHint,
  eventRelativeTime,
  eventTypeLabel,
  eventTypeMeta,
  eventsDedupKey,
  exitCodeText,
  exitCodeTone,
  matchesEventFilter,
  parseEventLine,
  parseHistoryItem,
  EVENT_ACTION_TEXT,
  MAX_EVENT_ENTRIES,
  MAX_EVENT_LOG_ENTRIES
} from '../utils/events'

/** 造一行事件的 JSON（字段形状对齐 core 的 eventNDJSONLine；t 用 unix 毫秒）。 */
function line(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    host_id: 1,
    hostname: 'bogon',
    t: 1790600000000,
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
      '{"host_id":1,"hostname":"bogon","t":1790600000000,"type":"image","action":"pull"}'
    )
    expect(e).toEqual({
      hostId: '1',
      hostname: 'bogon',
      t: 1790600000000,
      type: 'image',
      action: 'pull',
      actorName: '',
      actorId: '',
      exitCode: null // omitempty 缺省：不可考（非 die / 旧 agent），不是 0
    })
  })

  it('完整行：字段齐给全值（host_id 数字转字符串、t 原样透传毫秒）', () => {
    const e = parseEventLine(line())
    expect(e).toEqual({
      hostId: '1',
      hostname: 'bogon',
      t: 1790600000000,
      type: 'container',
      action: 'start',
      actorName: 'uni-center-core',
      actorId: 'sha256:abcdef123456',
      exitCode: null
    })
  })

  it('exit_code：die 事件原样透传（0 与 ≠0 都是事实）；出现却不是数给 null 整行拒绝', () => {
    expect(parseEventLine(line({ action: 'die', exit_code: 0 }))?.exitCode).toBe(0)
    expect(parseEventLine(line({ action: 'die', exit_code: 137 }))?.exitCode).toBe(137)
    // 显式 null（不携带判据的另一种编码）与缺省同义：不可考。
    expect(parseEventLine(line({ exit_code: null }))?.exitCode).toBeNull()
    expect(parseEventLine(line({ exit_code: '137' }))).toBeNull() // 形状漂移：整行拒掉
    expect(parseEventLine(line({ exit_code: true }))).toBeNull()
  })

  it('host_id 两种形态归一成字符串：字符串（core 现格式）与数字（老格式/夹具）同解', () => {
    expect(parseEventLine(line({ host_id: '42' }))?.hostId).toBe('42')
    expect(parseEventLine(line({ host_id: 42 }))?.hostId).toBe('42')
    // 雪花 id 只能走字符串形态：数字形态在 JS 里已经丢精度（2105604795992641536 →
    // …641500），而该量级的 id 正是「历史与实时合并出双份」的根因 —— 见去重键用例。
    expect(parseEventLine(line({ host_id: '2105604795992641536' }))?.hostId).toBe(
      '2105604795992641536'
    )
  })

  it('流首帧 sentinel（{"kind":"opened"}）：**显式跳过**，不是事件行', () => {
    // 流建立即发的那一帧（core 的 writeNDJSONOpened）：它让响应头立刻穿过 dev 代理，
    // 消费端必须容忍它 —— 判 null 即跳过，不计数、不打断流。
    expect(parseEventLine('{"kind":"opened"}')).toBeNull()
    // 将来 sentinel 添字段也仍然不是事件（形状判据的第一关就挡下）。
    expect(parseEventLine('{"kind":"opened","t":1790600000000}')).toBeNull()
  })

  it('坏 JSON / 非对象（数组、字面量）给 null，调用方跳行不打断流', () => {
    expect(parseEventLine('not json')).toBeNull()
    expect(parseEventLine('[1,2]')).toBeNull()
    expect(parseEventLine('42')).toBeNull()
    expect(parseEventLine('null')).toBeNull()
  })

  it('形状不符给 null：缺字段或类型不对各查一项', () => {
    expect(parseEventLine('{"hostname":"h","t":1,"type":"container","action":"start"}')).toBeNull() // 缺 host_id
    expect(parseEventLine(line({ host_id: [] }))).toBeNull() // host_id 既不是数也不是串
    expect(parseEventLine(line({ host_id: '' }))).toBeNull() // 空串不算归属
    expect(parseEventLine(line({ host_id: true }))).toBeNull()
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

describe('action 中文化映射（协议词表全覆盖）', () => {
  // 契约镜像：协议侧 dockerEventActions 的逐词条快照（uni_protocol/docker.go）。
  // 前端测试无法 import Go 源码，两处的同步靠这里 + events.ts 的双向注释；
  // 「集合相等」断言是机器守卫 —— 少一条 = 直显英文的漏网，多一条 = 表与契约漂移。
  const PROTOCOL_ACTIONS = [
    'attach',
    'commit',
    'copy',
    'create',
    'destroy',
    'detach',
    'die',
    'exec_create',
    'exec_detach',
    'exec_die',
    'exec_start',
    'enable',
    'disable',
    'export',
    'health_status',
    'import',
    'kill',
    'load',
    'mount',
    'oom',
    'pause',
    'pull',
    'push',
    'reload',
    'remove',
    'rename',
    'resize',
    'restart',
    'save',
    'start',
    'stop',
    'tag',
    'top',
    'unmount',
    'unpause',
    'update'
  ] as const

  it('映射表与协议词表集合相等：无缺失（不留直显英文）且无表外词条（不暗示可达）', () => {
    expect([...Object.keys(EVENT_ACTION_TEXT)].sort()).toEqual([...PROTOCOL_ACTIONS].sort())
  })

  it.each(PROTOCOL_ACTIONS)('词条 %s 有中文映射（非原样透传、无 ASCII 残留）', (token) => {
    const text = eventActionText(token)
    expect(text).not.toBe('')
    expect(text).not.toBe(token) // 原样透传就是漏网
    expect(text).not.toMatch(/[a-zA-Z]/) // 上屏文案不许带英文
  })

  it('高频词条逐条对值（防止映射值错位或误译）', () => {
    expect(eventActionText('start')).toBe('启动')
    expect(eventActionText('stop')).toBe('停止')
    expect(eventActionText('die')).toBe('退出')
    expect(eventActionText('create')).toBe('创建')
    expect(eventActionText('destroy')).toBe('销毁')
    expect(eventActionText('restart')).toBe('重启')
    expect(eventActionText('pause')).toBe('暂停')
    expect(eventActionText('unpause')).toBe('恢复')
    expect(eventActionText('rename')).toBe('改名')
    expect(eventActionText('oom')).toBe('内存不足')
    expect(eventActionText('pull')).toBe('拉取')
    expect(eventActionText('push')).toBe('推送')
    expect(eventActionText('remove')).toBe('移除')
    expect(eventActionText('top')).toBe('查看进程')
    expect(eventActionText('export')).toBe('导出')
    expect(eventActionText('enable')).toBe('启用')
    expect(eventActionText('disable')).toBe('停用')
    expect(eventActionText('reload')).toBe('重载')
    expect(eventActionText('exec_start')).toBe('开始执行')
  })

  it('health_status 带后缀：检查结果进文案（异常与正常各有其句，不再只给基调）', () => {
    expect(eventActionText('health_status')).toBe('健康检查') // 老 daemon 无后缀
    expect(eventActionText('health_status: unhealthy')).toBe('健康检查异常')
    expect(eventActionText('health_status: healthy')).toBe('健康检查正常')
    // 未知结果词退回基调（不误译成异常/正常任一侧），原始短语由 title 兜底。
    expect(eventActionText('health_status: starting')).toBe('健康检查')
  })

  it('带 ": " 后缀的动作按词元切分（协议 Validate 同一口径；exec 族上游已过滤，函数仍须正确）', () => {
    expect(eventActionText('exec_create: /bin/sh -c ls')).toBe('准备执行')
    expect(eventActionText('exec_die: exit 0')).toBe('执行退出')
    expect(eventActionText('kill: signal 9')).toBe('强杀')
  })

  it('表外词元原样透传（协议扩词的兜底 —— 正常帧不可达，宁可原始词不误译）', () => {
    expect(eventActionText('frobnicate')).toBe('frobnicate')
    expect(eventActionText('')).toBe('')
  })
})

describe('详版页时间轴（历史 ∪ 实时，t 降序，内容键去重）', () => {
  /** 一条 DTO 行（走 parseHistoryItem 的真实入口形态）。 */
  const dto = (over: Record<string, unknown> = {}) => ({
    hostId: '1',
    hostname: 'bogon',
    t: 1790600000000,
    type: 'container',
    action: 'start',
    actorName: 'web',
    actorId: 'sha256:abc',
    ...over
  })

  it('历史种子按 t 降序（最新在前），与 feed 的升序窗口刻意分家', () => {
    const log = createEventsLog()
    log.seedHistory([
      parseHistoryItem(dto({ t: 1790600001000, action: 'start' }))!,
      parseHistoryItem(dto({ t: 1790600003000, action: 'die' }))!,
      parseHistoryItem(dto({ t: 1790600002000, action: 'stop' }))!
    ])
    expect(log.entries.map((e) => e.action)).toEqual(['die', 'stop', 'start'])
    expect(log.entries.map((e) => e.seq)).toEqual([2, 3, 1]) // seq 是到达序：die 先 seed
  })

  it('跨来源去重：流回放重发已 seed 的条目被丢弃（不出现第二份）', () => {
    const log = createEventsLog()
    log.seedHistory([parseHistoryItem(dto())!])
    log.pushRaw(
      JSON.stringify({
        host_id: 1,
        hostname: 'bogon',
        t: 1790600000000,
        type: 'container',
        action: 'start',
        actor_name: 'web',
        actor_id: 'sha256:abc'
      }) + '\n'
    )
    expect(log.entries).toHaveLength(1)
    expect(log.deduped).toBe(1)
    expect(log.totalSeen).toBe(1)
  })

  it('实时新事件并入同一时间轴（t 降序），同 t 时后到的在前', () => {
    const log = createEventsLog()
    log.seedHistory([parseHistoryItem(dto({ action: 'start' }))!])
    log.pushRaw(
      JSON.stringify({
        host_id: 1,
        hostname: 'bogon',
        t: 1790600000000, // 同 t 的新事件
        type: 'container',
        action: 'stop',
        actor_name: 'web',
        actor_id: 'sha256:abc'
      }) + '\n'
    )
    log.pushRaw(
      JSON.stringify({
        host_id: 2,
        hostname: 'nas',
        t: 1790600005000,
        type: 'image',
        action: 'pull',
        actor_name: 'redis:7'
      }) + '\n'
    )
    expect(log.entries.map((e) => e.action)).toEqual(['pull', 'stop', 'start'])
  })

  it('sentinel 与坏行都不进时间轴（容忍跳过：流活着，行不脏）', () => {
    const log = createEventsLog()
    log.pushRaw('{"kind":"opened"}\n')
    log.pushRaw('{broken json' + '\n')
    log.pushRaw('\n')
    expect(log.entries).toHaveLength(0)
    expect(log.totalSeen).toBe(0)
    expect(log.deduped).toBe(0)
  })

  it('accept 谓词对历史与实时**同一把尺**：两条来源都不放行不合的行', () => {
    const log = createEventsLog({ accept: (f) => matchesEventFilter(f, { type: 'image' }) })
    log.seedHistory([
      parseHistoryItem(dto({ type: 'container' }))!, // 不合：被谓词拦下
      parseHistoryItem(dto({ type: 'image', action: 'pull', t: 1790600001000 }))!
    ])
    log.pushRaw(
      JSON.stringify({
        host_id: 9,
        hostname: 'beta',
        t: 1790600002000,
        type: 'container',
        action: 'die',
        actor_name: 'x'
      }) + '\n'
    )
    log.pushRaw(
      JSON.stringify({
        host_id: 9,
        hostname: 'beta',
        t: 1790600003000,
        type: 'image',
        action: 'push',
        actor_name: 'redis:7'
      }) + '\n'
    )
    expect(log.entries.map((e) => e.action)).toEqual(['push', 'pull'])
    expect(log.deduped).toBe(0) // 被谓词拦下的不算「重复」，账目分开
  })

  it('上限丢最旧的（t 降序尾部的那些），键集合随条目一起收缩', () => {
    const log = createEventsLog({ maxEntries: 3 })
    for (let i = 0; i < 5; i++) {
      log.seedHistory([parseHistoryItem(dto({ t: 1790600000000 + i * 1000 }))!])
    }
    expect(log.entries).toHaveLength(3)
    // 留的是最新三条（t 降序尾部被丢 = 最旧三条被丢）
    expect(log.entries.map((e) => e.t)).toEqual([1790600004000, 1790600003000, 1790600002000])
    // 被挤出的条目其键一并撤掉：同一条再次到达仍会被接受（重新进榜）而不是被误判成重复。
    log.seedHistory([parseHistoryItem(dto({ t: 1790600000000 }))!])
    expect(log.entries.map((e) => e.t)).toEqual([1790600004000, 1790600003000, 1790600002000]) // 仍在榜外（最旧），但账目上它是「被接受后再挤出」
    expect(log.totalSeen).toBe(6)
  })

  it('默认上限是 MAX_EVENT_LOG_ENTRIES（与保留窗口的单主机容量同档）', () => {
    const log = createEventsLog()
    for (let i = 0; i <= MAX_EVENT_LOG_ENTRIES; i++) {
      log.seedHistory([parseHistoryItem(dto({ t: 1790600000000 + i }))!])
    }
    expect(log.entries).toHaveLength(MAX_EVENT_LOG_ENTRIES)
  })

  it('seedHistory([]) 是空操作（翻页到尽头/端点给空页不扰动现有时间轴）', () => {
    const log = createEventsLog()
    log.seedHistory([parseHistoryItem(dto())!])
    log.seedHistory([])
    expect(log.entries).toHaveLength(1)
    expect(log.totalSeen).toBe(1)
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

  it('浮点秒（毫秒戳除千的形态）整段取整：不出现 4.41100001335144 秒前（QA 路 1 B1）', () => {
    // 任务中心/活动流的真实调用形态：毫秒时间戳除 1000 得到浮点秒。
    expect(eventRelativeTime((NOW * 1000 - 4411) / 1000, NOW)).toBe('4 秒前')
    expect(eventRelativeTime(NOW - 4.411, NOW)).toBe('4 秒前')
    // floor 整段差值而不是各自入参：4.4 秒前是「4 秒前」，不是四舍五入或进位到 5。
    expect(eventRelativeTime(NOW - 3.999, NOW)).toBe('3 秒前')
    // 未来浮点（时钟偏差）仍然夹到「刚刚」，不出现负数档位。
    expect(eventRelativeTime(NOW + 0.5, NOW)).toBe('刚刚')
  })
})

describe('绝对时刻（悬停 title）', () => {
  it('本地时区格式化为 YYYY-MM-DD HH:mm:ss（个位补零）', () => {
    // 用本地时区构造再格式化：断言与环境 TZ 无关。
    const t = new Date(2026, 0, 5, 3, 4, 5).getTime()
    expect(eventClockTime(t)).toBe('2026-01-05 03:04:05')
    const t2 = new Date(2026, 9, 2, 11, 41, 23).getTime()
    expect(eventClockTime(t2)).toBe('2026-10-02 11:41:23')
  })

  it('毫秒部分舍去（秒级粒度，与相对时间同一精度）', () => {
    const t = new Date(2026, 9, 2, 11, 41, 23, 987).getTime()
    expect(eventClockTime(t)).toBe('2026-10-02 11:41:23')
  })
})

describe('资源类型的图标、色槽与类型名', () => {
  it('四类资源各有图标、色槽与中文名（徽标的文字半边）', () => {
    expect(eventTypeMeta('container')).toEqual({
      icon: 'ri:box-3-line',
      tone: 'container',
      label: '容器'
    })
    expect(eventTypeMeta('image')).toEqual({
      icon: 'ri:image-2-line',
      tone: 'image',
      label: '镜像'
    })
    expect(eventTypeMeta('volume')).toEqual({
      icon: 'ri:database-2-line',
      tone: 'volume',
      label: '卷'
    })
    expect(eventTypeMeta('network')).toEqual({
      icon: 'ri:node-tree',
      tone: 'network',
      label: '网络'
    })
  })

  it('未知类型走问号 + 中性兜底（「不认识」本身要被看出来）；展示名原样透传', () => {
    expect(eventTypeMeta('plugin')).toEqual({ icon: 'ri:question-line', tone: 'other', label: '' })
    expect(eventTypeMeta('')).toEqual({ icon: 'ri:question-line', tone: 'other', label: '' })
    // 表内给中文、表外直显原始词（不显示空白 —— 未知本身要被看见）。
    expect(eventTypeLabel('container')).toBe('容器')
    expect(eventTypeLabel('plugin')).toBe('plugin')
    expect(eventTypeLabel('')).toBe('')
  })
})

describe('退出码的展示与染色', () => {
  it('exit 0 中性、≠0 红染（0 是正常收工，不该被染成异常）', () => {
    expect(exitCodeTone(0)).toBe('neutral')
    expect(exitCodeTone(137)).toBe('danger')
    expect(exitCodeTone(-1)).toBe('danger')
  })

  it('文案是原始事实（exit N），不做解释性措辞', () => {
    expect(exitCodeText(0)).toBe('exit 0')
    expect(exitCodeText(137)).toBe('exit 137')
  })
})

describe('去重键（跨历史与实时的合并判据）', () => {
  const base = {
    hostId: '1',
    hostname: 'bogon',
    t: 1790600000000,
    type: 'container',
    action: 'die',
    actorName: 'web',
    actorId: 'sha256:abc',
    exitCode: 0
  }

  it('五项事实拼键：同一事件两条通道给出同一个键（逐位相同）', () => {
    const fromStream = parseEventLine(line({ action: 'die', actor_id: 'sha256:abc' }))
    const fromHistory = parseHistoryItem({
      hostId: '1',
      hostname: 'bogon',
      t: 1790600000000,
      type: 'container',
      action: 'die',
      actorName: 'uni-center-core',
      actorId: 'sha256:abc',
      exitCode: null
    })
    expect(fromStream && fromHistory).toBeTruthy()
    expect(eventsDedupKey(fromStream!)).toBe(eventsDedupKey(fromHistory!))
  })

  it('五项任一变则键变（主机/时刻/类型/动作/主体）', () => {
    const k = eventsDedupKey(base)
    expect(eventsDedupKey({ ...base, hostId: '2' })).not.toBe(k)
    expect(eventsDedupKey({ ...base, t: base.t + 1 })).not.toBe(k)
    expect(eventsDedupKey({ ...base, type: 'image' })).not.toBe(k)
    expect(eventsDedupKey({ ...base, action: 'stop' })).not.toBe(k)
    expect(eventsDedupKey({ ...base, actorId: 'sha256:def' })).not.toBe(k)
  })

  it('雪花 id（> 2^53）：两条通道的 hostId 逐位一致时键才相等（精度丢失的回归钉）', () => {
    const big = '2105604795992641536'
    const fromStream = parseEventLine(line({ host_id: big, action: 'die' }))
    const fromHistory = parseHistoryItem({
      hostId: big,
      hostname: 'bogon',
      t: 1790600000000,
      type: 'container',
      action: 'die',
      actorName: 'uni-center-core',
      actorId: 'sha256:abcdef123456'
    })
    expect(fromStream && fromHistory).toBeTruthy()
    // 若把流行当数字解析（Number(big) → …641500），这里就会不等 —— 合并去重失效、
    // 列表出现双份（真栈实测过的形态）。
    expect(eventsDedupKey(fromStream!)).toBe(eventsDedupKey(fromHistory!))
  })

  it('主体 id 优先、名字兜底（daemon 没给 id 时用名字）', () => {
    expect(eventsDedupKey({ ...base, actorName: 'x' })).toBe(
      eventsDedupKey({ ...base, actorName: 'y' })
    )
    expect(eventsDedupKey({ ...base, actorId: '' })).toBe(
      eventsDedupKey({ ...base, actorId: '', actorName: 'web' })
    )
  })
})

describe('过滤谓词（服务端与实时行共用一把尺）', () => {
  const f = {
    hostId: '7',
    hostname: 'alpha',
    t: 1790600000000,
    type: 'container',
    action: 'health_status: unhealthy',
    actorName: 'web',
    actorId: 'sha256:beef',
    exitCode: null
  }

  it('空过滤放行一切', () => {
    expect(matchesEventFilter(f, {})).toBe(true)
    expect(matchesEventFilter(f, { hostId: '', type: '', keyword: '  ' })).toBe(true)
  })

  it('主机与类型是精确等值（不匹配即拦下）', () => {
    expect(matchesEventFilter(f, { hostId: '7' })).toBe(true)
    expect(matchesEventFilter(f, { hostId: '9' })).toBe(false)
    expect(matchesEventFilter(f, { type: 'container' })).toBe(true)
    expect(matchesEventFilter(f, { type: 'image' })).toBe(false)
  })

  it('关键字命中主体名 / 主体 id / 动作原文三处任一，大小写不敏感', () => {
    expect(matchesEventFilter(f, { keyword: 'WEB' })).toBe(true)
    expect(matchesEventFilter(f, { keyword: 'BEEF' })).toBe(true)
    expect(matchesEventFilter(f, { keyword: 'UNHEALTHY' })).toBe(true)
    expect(matchesEventFilter(f, { keyword: 'nope' })).toBe(false)
  })

  it('三项组合是「与」（逐层收窄）', () => {
    expect(matchesEventFilter(f, { hostId: '7', type: 'container', keyword: 'web' })).toBe(true)
    expect(matchesEventFilter(f, { hostId: '7', type: 'image', keyword: 'web' })).toBe(false)
  })
})

describe('历史行的解析与归一化', () => {
  it('DTO（camelCase + hostId string）→ 同一个 DockerEventFields', () => {
    expect(
      parseHistoryItem({
        hostId: '42',
        hostname: 'alpha',
        t: 1790600000000,
        type: 'container',
        action: 'die',
        actorName: 'web',
        actorId: 'sha256:abc',
        exitCode: 137
      })
    ).toEqual({
      hostId: '42',
      hostname: 'alpha',
      t: 1790600000000,
      type: 'container',
      action: 'die',
      actorName: 'web',
      actorId: 'sha256:abc',
      exitCode: 137
    })
  })

  it('omitempty 缺省：actor 两字段给空串、exitCode 给 null', () => {
    expect(
      parseHistoryItem({
        hostId: '1',
        hostname: 'h',
        t: 1790600000000,
        type: 'image',
        action: 'pull'
      })
    ).toEqual({
      hostId: '1',
      hostname: 'h',
      t: 1790600000000,
      type: 'image',
      action: 'pull',
      actorName: '',
      actorId: '',
      exitCode: null
    })
  })

  it('形状不符给 null（跳过该行，不打断整页）', () => {
    expect(parseHistoryItem(null)).toBeNull()
    expect(parseHistoryItem([1])).toBeNull()
    expect(parseHistoryItem({ hostId: 1 })).toBeNull() // hostId 必须是字符串形态
    expect(parseHistoryItem({ hostId: '1', t: 0 })).toBeNull()
    expect(parseHistoryItem({ hostId: '1', t: 1, type: '', action: 'start' })).toBeNull()
    expect(
      parseHistoryItem({ hostId: '1', t: 1, type: 'container', action: 'start', exitCode: 'x' })
    ).toBeNull()
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
