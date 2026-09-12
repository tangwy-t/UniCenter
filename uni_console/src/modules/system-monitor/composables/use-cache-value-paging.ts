import { computed, ref } from 'vue'
import type { CacheValuePage, FetchPage, FetchPageOptions } from '../api'

/** 每页条数选项与默认值 */
export const PAGE_SIZE_OPTIONS = [50, 100, 200]
export const DEFAULT_PAGE_SIZE = 100

/** string 展示窗口：默认 256KB、封顶 2MB；语法高亮仅在 ≤64KB 时启用 */
export const STRING_WINDOW_INITIAL = 256 * 1024
export const STRING_WINDOW_MAX = 2 * 1024 * 1024
export const HIGHLIGHT_MAX_BYTES = 64 * 1024

/** 契约类型归属 api.ts（仓库惯例），这里重新导出便于视图/测试单一入口导入 */
export type { CacheValuePage, ZSetEntry, HashEntry, FetchPageOptions, FetchPage } from '../api'

/** set/hash 走游标分页 */
const CURSOR_TYPES = new Set(['set', 'hash'])

// ── 纯函数（Vitest 直测） ────────────────────────────────

/** 行内文本截断：超长取前 max 字符加 …（UI 层近似即可） */
export function truncateText(text: string, max = 300): string {
  if (text.length <= max) return text
  return `${text.slice(0, max)}…`
}

/** tooltip 摘要：前 max 字符，超长加 …（避免 title 挂全文本） */
export function summarize(text: string, max = 500): string {
  return text.length > max ? `${text.slice(0, max)}…` : text
}

/** 集合页条目数 */
export function itemCount(page: Pick<CacheValuePage, 'value'>): number {
  return Array.isArray(page.value) ? page.value.length : 0
}

/** 游标模式下第 index 页之前已加载的累计条数（= 本页 display start，0 起） */
export function cumulativeStart(
  stack: Array<Pick<CacheValuePage, 'value'>>,
  index: number
): number {
  let n = 0
  for (let i = 0; i < index; i++) n += itemCount(stack[i])
  return n
}

/** 页码 ↔ offset 换算 */
export function pageOffset(page: number, pageSize: number): number {
  return (page - 1) * pageSize
}

export function pageCountOf(total: number, pageSize: number): number {
  return Math.max(1, Math.ceil(total / pageSize))
}

/** string 窗口翻倍直至封顶 */
export function nextStringWindow(current: number): number {
  return Math.min(Math.max(current, STRING_WINDOW_INITIAL) * 2, STRING_WINDOW_MAX)
}

/** 字节数格式化（B/KB/MB；MB 整数不带小数） */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const kb = n / 1024
  if (kb < 1024) return `${kb.toFixed(kb < 10 ? 1 : 0)} KB`
  const mb = kb / 1024
  if (Number.isInteger(mb)) return `${mb} MB`
  return `${mb.toFixed(mb < 10 ? 1 : 0)} MB`
}

// ── 分页状态机 ──────────────────────────────────────────

export function useCacheValuePaging(fetchPage: FetchPage) {
  const curKey = ref('')
  const page = ref<CacheValuePage | null>(null)
  const loading = ref(false)
  const pageSize = ref(DEFAULT_PAGE_SIZE)
  const currentPage = ref(1)
  /** set/hash 已加载页栈（含游标），供上一页零请求回退 */
  const pageStack = ref<CacheValuePage[]>([])
  const stackIndex = ref(0)
  const strWindow = ref(STRING_WINDOW_INITIAL)

  /** 并发保护：仅最新一次请求的结果可落地 */
  let reqSeq = 0
  /** 同参数在途请求合并表（ElPagination 切换页大小会连带触发 current-change） */
  const inflight = new Map<string, Promise<CacheValuePage | null>>()
  /** 已完成的同参数请求去重 */
  let lastReqSig = ''

  const isCursorType = computed(() => {
    const t = (page.value?.type ?? '').toLowerCase()
    return CURSOR_TYPES.has(t)
  })
  const pageCount = computed(() => pageCountOf(page.value?.total ?? 0, pageSize.value))

  const atStringCap = computed(() => strWindow.value >= STRING_WINDOW_MAX)
  const canShowStringMore = computed(() => !!page.value?.truncated && !atStringCap.value)
  /** set/hash 游标模式下当前页之前已加载的累计条数（0 起，视图徽章/位置文案直接使用，
   * 避免视图直读 pageStack/stackIndex 内部状态） */
  const cursorStart = computed(() => cumulativeStart(pageStack.value, stackIndex.value))

  const hasPrev = computed(() => isCursorType.value && stackIndex.value > 0)
  const hasNext = computed(() => {
    if (!isCursorType.value) return false
    const stack = pageStack.value
    const last = stack[stack.length - 1]
    if (!last) return false
    return stackIndex.value < stack.length - 1 || last.has_more
  })

  async function request(params: FetchPageOptions): Promise<CacheValuePage | null> {
    const reqSig = `${params.key}|${params.offset ?? ''}|${params.limit ?? ''}|${params.cursor ?? ''}`
    // 在途的同参数请求直接合并，避免在途期间去重失效导致重复网络请求
    const pending = inflight.get(reqSig)
    if (pending) return pending
    // 已完成的同参数请求直接回放
    if (page.value && reqSig === lastReqSig && !loading.value) return page.value
    const run = (async () => {
      const myReq = ++reqSeq
      lastReqSig = reqSig
      loading.value = true
      try {
        const res = await fetchPage(params)
        if (myReq === reqSeq) page.value = res
        return myReq === reqSeq ? res : null
      } finally {
        if (myReq === reqSeq) loading.value = false
        inflight.delete(reqSig)
      }
    })()
    inflight.set(reqSig, run)
    return run
  }

  /** 游标类型重置后从 cursor 0 起扫首页（open 与切换页大小共用） */
  async function loadCursorFirstPage(limit: number) {
    pageStack.value = []
    stackIndex.value = 0
    const res = await request({ key: curKey.value, cursor: 0, limit })
    if (res) pageStack.value = [res]
  }

  /** 打开 key：按类型选择分页方式取第一页（保留用户已选的每页条数） */
  async function open(key: string, type: string) {
    curKey.value = key
    page.value = null
    pageStack.value = []
    stackIndex.value = 0
    currentPage.value = 1
    strWindow.value = STRING_WINDOW_INITIAL
    const t = (type || '').toLowerCase()
    if (t === 'string') {
      await request({ key, offset: 0, limit: strWindow.value })
      return
    }
    if (CURSOR_TYPES.has(t)) {
      await loadCursorFirstPage(pageSize.value)
      return
    }
    await request({ key, offset: 0, limit: pageSize.value })
  }

  /** list/zset 跳页（游标类型由页脚 UI 保证不触发；此处守卫兜底）。
   *  失败不抛出：http 层已统一提示，状态机停留当前页。 */
  async function gotoPage(n: number) {
    if (!curKey.value || isCursorType.value || n < 1 || n > pageCount.value) return
    const res = await request({
      key: curKey.value,
      offset: pageOffset(n, pageSize.value),
      limit: pageSize.value
    }).catch(() => null)
    if (res) currentPage.value = n
  }

  /** 切换每页条数：回到第 1 页 / 重置游标栈从头扫。失败不抛出，停留当前状态。 */
  async function changePageSize(size: number) {
    if (!PAGE_SIZE_OPTIONS.includes(size) || size === pageSize.value || !curKey.value) return
    pageSize.value = size
    currentPage.value = 1
    if (isCursorType.value) {
      await loadCursorFirstPage(size).catch(() => null)
      return
    }
    await request({ key: curKey.value, offset: 0, limit: size }).catch(() => null)
  }

  /** set/hash 下一页：未加载则续扫压栈。失败不抛出，停留当前页。 */
  async function cursorNext() {
    if (loading.value || !isCursorType.value) return
    const stack = pageStack.value
    const last = stack[stack.length - 1]
    if (!last) return
    if (stackIndex.value < stack.length - 1) {
      stackIndex.value++
      page.value = stack[stackIndex.value]
      return
    }
    if (!last.has_more) return
    const res = await request({
      key: curKey.value,
      cursor: last.next_cursor,
      limit: pageSize.value
    }).catch(() => null)
    if (res) {
      pageStack.value = [...stack, res]
      stackIndex.value = stack.length
      page.value = res
    }
  }

  /** set/hash 上一页：出栈取缓存，零请求 */
  function cursorPrev() {
    if (!isCursorType.value || stackIndex.value <= 0) return
    stackIndex.value--
    page.value = pageStack.value[stackIndex.value]
  }

  /** string 显示更多：窗口翻倍重取（封顶 2MB）。失败不抛出，stay 当前状态。 */
  async function showStringMore() {
    const t = (page.value?.type ?? '').toLowerCase()
    if (!curKey.value || t !== 'string' || atStringCap.value) return
    strWindow.value = nextStringWindow(strWindow.value)
    await request({ key: curKey.value, offset: 0, limit: strWindow.value }).catch(() => null)
  }

  return {
    // 状态
    page,
    loading,
    pageSize,
    currentPage,
    pageStack,
    stackIndex,
    strWindow,
    // 派生
    pageCount,
    isCursorType,
    cursorStart,
    hasPrev,
    hasNext,
    atStringCap,
    canShowStringMore,
    // 动作
    open,
    gotoPage,
    changePageSize,
    cursorNext,
    cursorPrev,
    showStringMore
  }
}
