import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useNoticeInbox, type NoticeInboxApi } from './use-notice-inbox'

const makeItem = (over: Partial<Api.Notice.MyItem> = {}): Api.Notice.MyItem => ({
  id: '1',
  title: 't',
  content: '',
  noticeType: 1,
  priority: 0,
  isRead: false,
  publishTime: null,
  ...over
})

function make(
  opts: {
    list?: Api.Notice.MyItem[]
    unread?: number
    readFail?: boolean
    allFail?: boolean
  } = {}
) {
  const api = {
    fetchMyNotices: vi.fn(async (): Promise<Api.Notice.MyList> => ({
      list: opts.list ?? [makeItem()],
      unreadCount: opts.unread ?? 1
    })),
    markNoticeRead: vi.fn(async (): Promise<void> => {
      if (opts.readFail) throw new Error('x')
    }),
    markAllNoticesRead: vi.fn(async (): Promise<void> => {
      if (opts.allFail) throw new Error('x')
    })
  } satisfies NoticeInboxApi
  const socket = { setUnreadCount: vi.fn() }
  const onError = vi.fn()
  const inbox = useNoticeInbox({ api, socket, onError })
  return { inbox, api, socket, onError }
}

describe('useNoticeInbox', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => {
    vi.runOnlyPendingTimers()
    vi.useRealTimers()
  })

  it('fetchInbox 成功:装载列表并把未读数校准到 socket', async () => {
    const { inbox, socket } = make({
      list: [makeItem({ id: '1' }), makeItem({ id: '2', isRead: true })],
      unread: 1
    })
    await inbox.fetchInbox()
    expect(inbox.items.value).toHaveLength(2)
    expect(inbox.unreadCount.value).toBe(1)
    expect(inbox.error.value).toBe(false)
    expect(inbox.loaded.value).toBe(true)
    expect(socket.setUnreadCount).toHaveBeenCalledWith(1)
  })

  it('fetchInbox 失败:置 error、保留旧列表', async () => {
    const { inbox, api } = make({ list: [makeItem()] })
    await inbox.fetchInbox()
    api.fetchMyNotices.mockRejectedValueOnce(new Error('net'))
    await inbox.fetchInbox()
    expect(inbox.error.value).toBe(true)
    expect(inbox.items.value).toHaveLength(1)
  })

  it('markRead 成功:乐观已读、未读-1、socket 同步', async () => {
    const { inbox, api, socket } = make({ list: [makeItem({ id: '9' })], unread: 1 })
    await inbox.fetchInbox()
    await inbox.markRead('9')
    expect(api.markNoticeRead).toHaveBeenCalledWith('9')
    expect(inbox.items.value[0]!.isRead).toBe(true)
    expect(inbox.unreadCount.value).toBe(0)
    expect(socket.setUnreadCount).toHaveBeenLastCalledWith(0)
  })

  it('markRead 失败:回滚、提示、权威重拉', async () => {
    const { inbox, api, onError } = make({
      list: [makeItem({ id: '9' })],
      unread: 1,
      readFail: true
    })
    await inbox.fetchInbox()
    await inbox.markRead('9')
    expect(onError).toHaveBeenCalledTimes(1)
    expect(inbox.items.value[0]!.isRead).toBe(false)
    expect(inbox.unreadCount.value).toBe(1)
    expect(api.fetchMyNotices).toHaveBeenCalledTimes(2)
  })

  it('markRead 已读条目:idempotent 不调接口', async () => {
    const { inbox, api } = make({ list: [makeItem({ id: '9', isRead: true })], unread: 0 })
    await inbox.fetchInbox()
    await inbox.markRead('9')
    expect(api.markNoticeRead).not.toHaveBeenCalled()
  })

  it('markAllRead 成功:全部已读、计数归零、socket 校准', async () => {
    const { inbox, api, socket } = make({
      list: [makeItem({ id: '1' }), makeItem({ id: '2' })],
      unread: 2
    })
    await inbox.fetchInbox()
    await inbox.markAllRead()
    expect(api.markAllNoticesRead).toHaveBeenCalledTimes(1)
    expect(inbox.items.value.every((i) => i.isRead)).toBe(true)
    expect(inbox.unreadCount.value).toBe(0)
    expect(inbox.markingAll.value).toBe(false)
    expect(socket.setUnreadCount).toHaveBeenLastCalledWith(0)
  })

  it('markAllRead 失败:提示、重拉保持原状', async () => {
    const { inbox, onError, api } = make({
      list: [makeItem({ id: '1' })],
      unread: 1,
      allFail: true
    })
    await inbox.fetchInbox()
    await inbox.markAllRead()
    expect(inbox.markingAll.value).toBe(false)
    expect(onError).toHaveBeenCalledTimes(1)
    expect(inbox.unreadCount.value).toBe(1)
    expect(api.fetchMyNotices).toHaveBeenCalledTimes(2)
  })

  it('debouncedRefetch:300ms 内多次推送只重拉一次', async () => {
    const { inbox, api } = make({ list: [makeItem()] })
    inbox.debouncedRefetch()
    inbox.debouncedRefetch()
    inbox.debouncedRefetch()
    expect(api.fetchMyNotices).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(350)
    expect(api.fetchMyNotices).toHaveBeenCalledTimes(1)
  })
})
