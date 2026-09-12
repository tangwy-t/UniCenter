import { describe, it, expect, vi } from 'vitest'
import {
  useCacheValuePaging,
  truncateText,
  summarize,
  cumulativeStart,
  pageOffset,
  pageCountOf,
  nextStringWindow,
  formatBytes,
  STRING_WINDOW_INITIAL,
  STRING_WINDOW_MAX,
  type CacheValuePage,
  type FetchPageOptions
} from './use-cache-value-paging'

/** 构造一页响应 */
function makePage(over: Partial<CacheValuePage> = {}): CacheValuePage {
  return {
    key: 'k',
    type: 'list',
    ttl: -1,
    total: 0,
    start: 0,
    has_more: false,
    next_cursor: '0',
    truncated: false,
    value: [],
    ...over
  }
}

/** 按顺序回放脚本的假 fetcher，同时记录入参 */
function scriptFetcher(script: Array<(p: FetchPageOptions) => CacheValuePage>) {
  const calls: FetchPageOptions[] = []
  return {
    calls,
    fetch: async (p: FetchPageOptions) => {
      calls.push(p)
      const step = script[Math.min(calls.length - 1, script.length - 1)]
      return step(p)
    }
  }
}

describe('纯函数', () => {
  it('truncateText 超长截断', () => {
    expect(truncateText('abc', 300)).toBe('abc')
    expect(truncateText('x'.repeat(300), 5)).toBe('xxxxx…')
  })
  it('truncateText 默认上限 300 字符', () => {
    expect(truncateText('x'.repeat(300))).toBe('x'.repeat(300))
    expect(truncateText('x'.repeat(301))).toBe('x'.repeat(300) + '…')
  })
  it('summarize 摘要', () => {
    expect(summarize('ab', 500)).toBe('ab')
    const long = 'y'.repeat(600)
    expect(summarize(long)).toBe('y'.repeat(500) + '…')
  })
  it('cumulativeStart 累计条数', () => {
    const stack = [makePage({ value: ['a', 'b'] }), makePage({ value: ['c'] })]
    expect(cumulativeStart(stack, 0)).toBe(0)
    expect(cumulativeStart(stack, 1)).toBe(2)
    expect(cumulativeStart(stack, 2)).toBe(3)
  })
  it('pageOffset/pageCountOf', () => {
    expect(pageOffset(3, 100)).toBe(200)
    expect(pageCountOf(30000, 100)).toBe(300)
    expect(pageCountOf(0, 100)).toBe(1)
  })
  it('nextStringWindow 翻倍封顶', () => {
    expect(nextStringWindow(STRING_WINDOW_INITIAL)).toBe(524288)
    expect(nextStringWindow(STRING_WINDOW_MAX)).toBe(STRING_WINDOW_MAX)
    expect(nextStringWindow(1)).toBe(STRING_WINDOW_INITIAL * 2)
  })
  it('formatBytes', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(262144)).toBe('256 KB')
    expect(formatBytes(2 * 1024 * 1024)).toBe('2 MB')
  })
})

describe('list/zset 页码模式', () => {
  it('open 取第一页并建立分页状态', async () => {
    const fk = scriptFetcher([
      () =>
        makePage({
          type: 'list',
          total: 30000,
          has_more: true,
          value: Array.from({ length: 100 }, (_, i) => `item-${i}`)
        })
    ])
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('job:logs', 'list')
    expect(fk.calls[0]).toEqual({ key: 'job:logs', offset: 0, limit: 100 })
    expect(pager.page.value?.total).toBe(30000)
    expect(pager.pageCount.value).toBe(300)
  })

  it('gotoPage 按 offset 取页，越界页码被拦截', async () => {
    const fk = scriptFetcher([
      () => makePage({ type: 'list', total: 30000, value: [] }),
      (p) =>
        makePage({ type: 'list', total: 30000, start: p.offset ?? 0, value: [String(p.offset)] })
    ])
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'list')
    await pager.gotoPage(3)
    expect(fk.calls[1]).toEqual({ key: 'k', offset: 200, limit: 100 })
    expect(pager.currentPage.value).toBe(3)
    await pager.gotoPage(999) // 超过页数：不发起请求
    expect(fk.calls.length).toBe(2)
  })

  it('changePageSize 回到第 1 页，ElPagination 连带 current-change 不产生重复请求', async () => {
    const fk = scriptFetcher([() => makePage({ type: 'list', total: 30000, value: [] })])
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'list')
    await pager.gotoPage(2)
    await pager.changePageSize(50)
    await pager.gotoPage(1) // 模拟 size-change 后 Element Plus 自动触发的 current-change(1)
    expect(fk.calls).toEqual([
      { key: 'k', offset: 0, limit: 100 },
      { key: 'k', offset: 100, limit: 100 },
      { key: 'k', offset: 0, limit: 50 }
    ])
    expect(pager.currentPage.value).toBe(1)
  })

  it('并发竞态：后发请求的结果生效', async () => {
    const first = makePage({ type: 'list', total: 500, start: 0, value: ['a0'] })
    let resolveB!: (v: CacheValuePage) => void
    let resolveC!: (v: CacheValuePage) => void
    const pb = new Promise<CacheValuePage>((r) => (resolveB = r))
    const pc = new Promise<CacheValuePage>((r) => (resolveC = r))
    const fetch = vi.fn((p: FetchPageOptions) => {
      if (p.offset === 0) return Promise.resolve(first)
      if (p.offset === 100) return pb
      return pc
    })
    const pager = useCacheValuePaging(fetch)
    await pager.open('k', 'list') // 首页立即落地,pageCount=5
    const pB = pager.gotoPage(2) // offset 100,挂起
    const pC = pager.gotoPage(3) // offset 200,挂起(reqSeq 已是更新值)
    resolveC(makePage({ type: 'list', total: 500, start: 200, value: ['c'] }))
    resolveB(makePage({ type: 'list', total: 500, start: 100, value: ['b'] }))
    await Promise.all([pB, pC])
    expect(pager.page.value?.start).toBe(200) // 后发（第 3 页）结果生效,先发结果被丢弃
    expect((pager.page.value?.value as string[])[0]).toBe('c')
  })

  it('并发同参数请求合并为一次', async () => {
    let resolve!: (v: CacheValuePage) => void
    const pending = new Promise<CacheValuePage>((r) => (resolve = r))
    const fetch = vi.fn(() => pending)
    const pager = useCacheValuePaging(fetch)
    const a = pager.open('k', 'list') // offset 0 limit 100,在途
    const b = pager.gotoPage(1) // 同参数,应合并且不重复发起请求
    resolve(makePage({ type: 'list', total: 100, start: 0, value: ['x'] }))
    await a
    await b
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('翻页失败不抛出，停留当前页（http 层已统一提示）', async () => {
    const first = makePage({ type: 'list', total: 500, start: 0, value: ['a0'] })
    const fetch = vi.fn((p: FetchPageOptions) =>
      p.offset === 0 ? Promise.resolve(first) : Promise.reject(new Error('boom'))
    )
    const pager = useCacheValuePaging(fetch)
    await pager.open('k', 'list')
    await pager.gotoPage(2) // 失败:不抛出
    expect(pager.page.value?.start).toBe(0)
    expect(pager.currentPage.value).toBe(1)
    expect(pager.loading.value).toBe(false)
  })
})

describe('set/hash 游标模式', () => {
  function pileScript() {
    return scriptFetcher([
      () =>
        makePage({
          type: 'set',
          total: 250,
          has_more: true,
          next_cursor: '100',
          value: Array.from({ length: 100 }, (_, i) => `t-${i}`)
        }),
      () =>
        makePage({
          type: 'set',
          total: 250,
          has_more: true,
          next_cursor: '200',
          value: Array.from({ length: 100 }, (_, i) => `t-${100 + i}`)
        }),
      () =>
        makePage({
          type: 'set',
          total: 250,
          has_more: false,
          next_cursor: '0',
          value: Array.from({ length: 50 }, (_, i) => `t-${200 + i}`)
        })
    ])
  }

  it('首页从 cursor 0 扫起，下一页用游标续扫', async () => {
    const fk = pileScript()
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'set')
    expect(fk.calls[0]).toEqual({ key: 'k', cursor: 0, limit: 100 })
    expect(pager.hasNext.value).toBe(true)
    expect(pager.hasPrev.value).toBe(false)
    expect(pager.cursorStart.value).toBe(0)
    await pager.cursorNext()
    expect(fk.calls[1]).toEqual({ key: 'k', cursor: '100', limit: 100 })
    expect(pager.stackIndex.value).toBe(1)
    expect(pager.cursorStart.value).toBe(100)
  })

  it('上一页走缓存零请求', async () => {
    const fk = pileScript()
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'set')
    await pager.cursorNext()
    const n = fk.calls.length
    pager.cursorPrev()
    expect(fk.calls.length).toBe(n)
    expect(pager.page.value).toBe(pager.pageStack.value[0])
    expect(pager.hasPrev.value).toBe(false)
  })

  it('末页 has_more=false 时下一页被拦截', async () => {
    const fk = pileScript()
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'set')
    await pager.cursorNext()
    await pager.cursorNext()
    expect(fk.calls.length).toBe(3)
    expect(pager.hasNext.value).toBe(false)
    await pager.cursorNext()
    expect(fk.calls.length).toBe(3)
  })

  it('切换页大小重置游标栈从头扫（hash 同构）', async () => {
    const fk = pileScript()
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'hash')
    await pager.cursorNext()
    await pager.changePageSize(50)
    expect(fk.calls.at(-1)).toEqual({ key: 'k', cursor: 0, limit: 50 })
    expect(pager.pageStack.value.length).toBe(1)
    expect(pager.stackIndex.value).toBe(0)
  })
})

describe('string 字节窗模式', () => {
  it('open 用 256KB 窗口，显示更多按 512KB→1MB 翻倍', async () => {
    const fk = scriptFetcher([
      () =>
        makePage({
          type: 'string',
          total: 1024 * 1024,
          truncated: true,
          has_more: true,
          value: 'abc'
        }),
      () =>
        makePage({
          type: 'string',
          total: 1024 * 1024,
          truncated: true,
          has_more: true,
          value: 'abcd'
        }),
      () =>
        makePage({
          type: 'string',
          total: 1024 * 1024,
          truncated: false,
          has_more: false,
          value: 'abcde'
        })
    ])
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'string')
    expect(fk.calls[0]).toEqual({ key: 'k', offset: 0, limit: STRING_WINDOW_INITIAL })
    expect(pager.canShowStringMore.value).toBe(true)
    await pager.showStringMore()
    expect(fk.calls[1]).toEqual({ key: 'k', offset: 0, limit: STRING_WINDOW_INITIAL * 2 })
    await pager.showStringMore() // 1MB 中间档
    expect(fk.calls[2]).toEqual({ key: 'k', offset: 0, limit: STRING_WINDOW_INITIAL * 4 })
    expect(pager.canShowStringMore.value).toBe(false) // 已完整取回,truncated=false
  })

  it('窗口封顶后 canShowStringMore=false 且不再请求', async () => {
    const fk = scriptFetcher([
      () => makePage({ type: 'string', total: 10 * 1024 * 1024, truncated: true, value: 'x' })
    ])
    const pager = useCacheValuePaging(fk.fetch)
    await pager.open('k', 'string')
    pager.strWindow.value = STRING_WINDOW_MAX
    expect(pager.canShowStringMore.value).toBe(false)
    await pager.showStringMore()
    expect(fk.calls.length).toBe(1)
  })
})
