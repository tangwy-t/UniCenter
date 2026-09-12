import { ref } from 'vue'

export interface NoticeInboxApi {
  fetchMyNotices: () => Promise<Api.Notice.MyList>
  markNoticeRead: (id: string) => Promise<void>
  markAllNoticesRead: () => Promise<void>
}

export interface NoticeInboxSocket {
  setUnreadCount: (count: number) => void
}

export interface NoticeInboxOptions {
  api: NoticeInboxApi
  socket: NoticeInboxSocket
  /** 操作失败提示(由组件注入全局 ElMessage/文案) */
  onError?: () => void
}

/**
 * 铃铛收件箱状态机:与视图分离便于单测。
 * - 真值:/notices/my 响应(list + unreadCount),负责校准 socket 角标;
 * - markRead/markAllRead 乐观更新,失败回滚并权威重拉;
 * - debouncedRefetch:WS 推送后 300ms 合并重拉。
 */
export function useNoticeInbox(options: NoticeInboxOptions) {
  const items = ref<Api.Notice.MyItem[]>([])
  const unreadCount = ref(0)
  const loading = ref(false)
  const error = ref(false)
  const markingAll = ref(false)
  const loaded = ref(false)

  const syncCount = (count: number): void => {
    unreadCount.value = count
    options.socket.setUnreadCount(count)
  }

  const fetchInbox = async (): Promise<void> => {
    loading.value = true
    try {
      const res = await options.api.fetchMyNotices()
      items.value = res.list
      syncCount(res.unreadCount)
      error.value = false
    } catch {
      error.value = true
    } finally {
      loading.value = false
      loaded.value = true
    }
  }

  let timer: ReturnType<typeof setTimeout> | null = null
  const debouncedRefetch = (): void => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      fetchInbox()
    }, 300)
  }

  const markRead = async (id: string): Promise<void> => {
    const item = items.value.find((i) => i.id === id)
    if (!item || item.isRead) return
    const prevCount = unreadCount.value
    item.isRead = true
    syncCount(Math.max(0, prevCount - 1))
    try {
      await options.api.markNoticeRead(id)
    } catch {
      item.isRead = false
      syncCount(prevCount)
      options.onError?.()
      fetchInbox()
    }
  }

  const markAllRead = async (): Promise<void> => {
    if (markingAll.value || unreadCount.value === 0) return
    markingAll.value = true
    try {
      await options.api.markAllNoticesRead()
      items.value.forEach((i) => {
        i.isRead = true
      })
      syncCount(0)
    } catch {
      options.onError?.()
      fetchInbox()
    } finally {
      markingAll.value = false
    }
  }

  const reset = (): void => {
    if (timer) clearTimeout(timer)
    timer = null
  }

  return {
    items,
    unreadCount,
    loading,
    error,
    markingAll,
    loaded,
    fetchInbox,
    debouncedRefetch,
    markRead,
    markAllRead,
    reset
  }
}
