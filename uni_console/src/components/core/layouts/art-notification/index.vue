<!-- 通知组件:公报台 —— 单流真数据 + 时间脊 + 未读信号轨 + LIVE 状态 -->
<template>
  <div
    class="art-notification-panel art-card-sm !shadow-xl"
    :style="{
      transform: show ? 'scaleY(1)' : 'scaleY(0.9)',
      opacity: show ? 1 : 0
    }"
    v-show="visible"
    @click.stop
  >
    <div class="panel-header">
      <span class="panel-title">{{ '通知' }}</span>
      <span
        class="read-all-btn"
        :class="{ 'is-disabled': unreadCount === 0 || markingAll }"
        @click="handleReadAll"
        >{{ '全部已读' }}</span
      >
    </div>

    <div class="panel-sub">
      <span class="live-dot" :class="liveOnline ? 'ok' : 'offline'"></span>
      <span class="sub-status" :class="{ 'is-retry': error }" @click="handleStatusClick">{{
        subStatusText
      }}</span>
      <span v-if="unreadCount > 0" class="sub-count">{{ `${unreadCount} 条未读` }}</span>
    </div>

    <div class="panel-header-rule"></div>

    <div class="list-area scrollbar-thin">
      <template v-if="!loaded">
        <div v-for="i in 3" :key="i" class="skeleton-row">
          <span class="skeleton-chip"></span>
          <span class="skeleton-lines">
            <span class="skeleton-line w-3/4"></span>
            <span class="skeleton-line w-1/3"></span>
          </span>
        </div>
      </template>

      <template v-else-if="error && grouped.length === 0">
        <div class="empty-block c-p" @click="fetchInbox">
          <ArtSvgIcon icon="ri:wifi-off-line" class="text-4xl" />
          <p class="empty-title">{{ '同步失败，点击重试' }}</p>
        </div>
      </template>

      <template v-else-if="grouped.length === 0">
        <div class="empty-block">
          <ArtSvgIcon icon="system-uicons:inbox" class="text-4xl" />
          <p class="empty-title">{{ '暂无通知公告' }}</p>
          <p class="empty-desc">{{ '管理员发布的通知与公告会实时出现在这里' }}</p>
        </div>
      </template>

      <template v-else>
        <section v-for="g in grouped" :key="g.key" class="group">
          <h5 class="group-label">{{ g.label }}</h5>
          <ul>
            <li
              v-for="item in g.items"
              :key="item.id"
              class="notice-item"
              :class="{ 'is-read': item.isRead }"
              @click="openItem(item)"
            >
              <span class="rail-dot" :class="{ unread: !item.isRead }"></span>
              <span class="type-chip" :class="typeChipTint(item.noticeType)">
                <ArtSvgIcon
                  class="text-lg !bg-transparent"
                  :icon="item.noticeType === 2 ? 'ri:megaphone-line' : 'ri:notification-3-line'"
                />
              </span>
              <div class="item-body">
                <p class="item-title" :class="{ unread: !item.isRead }">{{ item.title }}</p>
                <p class="item-meta">
                  <span class="item-time">{{ fmtTime(item.publishTime) }}</span>
                  <span
                    v-if="priorityLabel(item.priority)"
                    class="priority-tag"
                    :class="priorityTint(item.priority)"
                    >{{ priorityLabel(item.priority) }}</span
                  >
                </p>
              </div>
            </li>
          </ul>
        </section>
      </template>
    </div>

    <div class="panel-footer">
      <ArtButtonTable
        class="!mr-0 !w-full"
        icon="ri:arrow-right-line"
        iconClass="bg-g-300/55 text-g-700"
        title="查看全部通知公告"
        @click="handleViewAll"
      />
    </div>

    <ArtNoticeDetail ref="detailRef" />
  </div>
</template>

<script setup lang="ts">
  import { computed, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElMessage } from 'element-plus'
  import { useSocketStore } from '@/store/modules/socket'
  import { fetchMyNotices, markNoticeRead, markAllNoticesRead } from '@/modules/system-notice/api'
  import { useNoticeDict } from '@/modules/system-notice/composables/useNoticeDict'
  import { useNoticeInbox } from './use-notice-inbox'
  import ArtNoticeDetail from '@/components/core/others/art-notice-detail/index.vue'

  defineOptions({ name: 'ArtNotification' })

  const router = useRouter()
  const socketStore = useSocketStore()
  const noticeDict = useNoticeDict()

  const props = defineProps<{
    value: boolean
  }>()

  const emit = defineEmits<{
    'update:value': [value: boolean]
  }>()

  const show = ref(false)
  const visible = ref(false)
  const detailRef = ref<InstanceType<typeof ArtNoticeDetail>>()

  const inbox = useNoticeInbox({
    api: { fetchMyNotices, markNoticeRead, markAllNoticesRead },
    socket: { setUnreadCount: (n) => socketStore.setUnreadCount(n) },
    onError: () => ElMessage.error('操作失败，请重试')
  })

  const {
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
  } = inbox

  /** 开合动画:开→先显后展开;关→先收起 350ms 再释放 DOM(沿用原机制) */
  const showNotice = (open: boolean): void => {
    if (open) {
      visible.value = true
      setTimeout(() => {
        show.value = true
      }, 5)
    } else {
      show.value = false
      setTimeout(() => {
        visible.value = false
      }, 350)
    }
  }

  watch(
    () => props.value,
    (nv) => {
      showNotice(nv)
      if (nv) {
        fetchInbox()
        noticeDict.ensure() // 类型/优先级标签接字典(缓存后零请求)
      }
    }
  )

  // WS 推送:面板存在期间 300ms 防抖重拉,列表与角标同源归真
  watch(
    () => socketStore.lastNotice,
    (n) => {
      if (n) debouncedRefetch()
    }
  )

  /** Esc 关闭:面板可见时按下送回 false(头部栏监听 v-model 变化收起) */
  const onKeydown = (e: KeyboardEvent): void => {
    if (e.key === 'Escape' && visible.value) {
      emit('update:value', false)
    }
  }

  /** 断连降级同步:WS 未认证时,窗口重新聚焦/回前台补拉一次(不含定时轮询) */
  const onRefocus = (): void => {
    if (!socketStore.authed && document.visibilityState === 'visible') {
      fetchInbox()
    }
  }

  onMounted(() => {
    fetchInbox()
    window.addEventListener('keydown', onKeydown)
    window.addEventListener('focus', onRefocus)
    document.addEventListener('visibilitychange', onRefocus)
  })

  onUnmounted(() => {
    reset()
    window.removeEventListener('keydown', onKeydown)
    window.removeEventListener('focus', onRefocus)
    document.removeEventListener('visibilitychange', onRefocus)
  })

  /** LIVE 连线状态:以 socket 认证态为准 */
  const liveOnline = computed(() => socketStore.authed)

  const subStatusText = computed(() => {
    if (error.value) return '同步失败，点击重试'
    if (loading.value && !loaded.value) return '…'
    return liveOnline.value ? '已连接 · 实时同步' : '未连接 · 打开时同步'
  })

  const handleStatusClick = (): void => {
    if (error.value) fetchInbox()
  }

  /** 时间脊分组标签(中文面量) */
  const GROUP_LABELS: Record<string, string> = {
    today: '今天',
    yesterday: '昨天',
    earlier: '更早'
  }

  /** 时间脊分组:今天 / 昨天 / 更早(本地时区) */
  const grouped = computed(() => {
    const today = new Date()
    today.setHours(0, 0, 0, 0)
    const yesterday = new Date(today)
    yesterday.setDate(today.getDate() - 1)

    const groups: { key: string; label: string; items: Api.Notice.MyItem[] }[] = []
    for (const item of items.value) {
      const d = parseLocalTime(item.publishTime)
      let key = 'earlier'
      if (d && d.getTime() >= today.getTime()) key = 'today'
      else if (d && d.getTime() >= yesterday.getTime()) key = 'yesterday'
      let group = groups.find((g) => g.key === key)
      if (!group) {
        group = { key, label: GROUP_LABELS[key] ?? key, items: [] }
        groups.push(group)
      }
      group.items.push(item)
    }
    return groups
  })

  /** 服务端 JSONTime 形如 "2026-09-05 14:35:09",非 ISO,补 T 保证浏览器可解析 */
  function parseLocalTime(v: string | null | undefined): Date | null {
    if (!v) return null
    const d = new Date(v.replace(' ', 'T'))
    return Number.isNaN(d.getTime()) ? null : d
  }

  const fmtTime = (v: string | null | undefined): string => {
    const d = parseLocalTime(v)
    if (!d) return '—'
    const hh = String(d.getHours()).padStart(2, '0')
    const mm = String(d.getMinutes()).padStart(2, '0')
    const now = new Date()
    const sameYear = d.getFullYear() === now.getFullYear()
    const sameDay =
      d.getFullYear() === now.getFullYear() &&
      d.getMonth() === now.getMonth() &&
      d.getDate() === now.getDate()
    if (sameDay) return `${hh}:${mm}`
    const mo = String(d.getMonth() + 1).padStart(2, '0')
    const dd = String(d.getDate()).padStart(2, '0')
    return sameYear ? `${mo}-${dd} ${hh}:${mm}` : `${d.getFullYear()}-${mo}-${dd}`
  }

  /* 类型徽标色:跟 sys_notice_type 字典 list_class 走(管理页同源),icon 仍按值语义 */
  const typeChipTint = (t: number | undefined): string => {
    const cls = noticeDict.clsOf('sys_notice_type', t)
    const map: Record<string, string> = {
      primary: 'bg-theme/12 text-theme',
      warning: 'bg-warning/12 text-warning',
      danger: 'bg-danger/12 text-danger',
      success: 'bg-success/12 text-success',
      info: 'bg-info/12 text-info'
    }
    return map[cls ?? ''] ?? map.info
  }

  /** 优先级标签:label 接 sys_notice_priority 字典(中文兜底),仅重要/紧急展示 */
  const priorityLabel = (p?: number): string => {
    if (p !== 1 && p !== 2) return ''
    const fallback = p === 1 ? '重要' : '紧急'
    return noticeDict.labelOf('sys_notice_priority', p, fallback)
  }

  const priorityTint = (p?: number): string => {
    if (p !== 1 && p !== 2) return 'is-warning'
    return noticeDict.clsOf('sys_notice_priority', p) === 'danger' ? 'is-danger' : 'is-warning'
  }

  const openItem = (item: Api.Notice.MyItem): void => {
    if (!item.isRead) markRead(item.id)
    detailRef.value?.open({
      id: item.id,
      title: item.title,
      content: item.content,
      noticeType: item.noticeType,
      priority: item.priority,
      publishTime: item.publishTime
    })
  }

  const handleReadAll = async (): Promise<void> => {
    if (unreadCount.value === 0 || markingAll.value) return
    await markAllRead()
  }

  const handleViewAll = (): void => {
    router.push('/system/notice')
    emit('update:value', false)
  }
</script>

<style scoped>
  @reference '@styles/core/tailwind.css';

  .art-notification-panel {
    @apply absolute top-14.5 right-5 w-95 h-130 overflow-hidden transition-all duration-300 origin-top will-change-[top,left]
      max-[640px]:top-[65px] max-[640px]:right-0 max-[640px]:w-full max-[640px]:h-[80vh];
    display: flex;
    flex-direction: column;
  }

  .panel-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 16px 6px;
  }

  .panel-title {
    font-size: 15px;
    font-weight: 600;
    color: var(--g-900);
  }

  .read-all-btn {
    font-size: 12px;
    color: var(--g-800);
    padding: 2px 6px;
    border-radius: 4px;
    cursor: pointer;
    user-select: none;
  }

  .read-all-btn:hover {
    background: var(--g-200);
  }

  .read-all-btn.is-disabled {
    color: var(--g-400);
    cursor: not-allowed;
  }

  .read-all-btn.is-disabled:hover {
    background: transparent;
  }

  .panel-sub {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 0 16px 10px;
    font-size: 12px;
    color: var(--g-500);
    letter-spacing: 0.02em;
  }

  .live-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }

  .live-dot.ok {
    background: var(--el-color-success);
  }

  .live-dot.offline {
    background: var(--el-color-info);
  }

  .sub-status.is-retry {
    color: var(--el-color-warning);
    cursor: pointer;
    text-decoration: underline;
    text-underline-offset: 3px;
  }

  .sub-count {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
  }

  .panel-header-rule {
    border-top: 1px solid var(--art-card-border);
  }

  .list-area {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
  }

  .scrollbar-thin::-webkit-scrollbar {
    width: 5px !important;
  }

  .dark .scrollbar-thin::-webkit-scrollbar-track {
    background-color: var(--default-box-color);
  }

  .dark .scrollbar-thin::-webkit-scrollbar-thumb {
    background-color: #222 !important;
  }

  .group-label {
    margin: 10px 16px 4px;
    font-size: 11px;
    letter-spacing: 0.12em;
    color: var(--g-400);
    text-transform: uppercase;
  }

  .notice-item {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 16px;
    cursor: pointer;
  }

  .notice-item:hover {
    background: var(--g-100);
  }

  .rail-dot {
    align-self: stretch;
    width: 3px;
    border-radius: 2px;
    background: transparent;
    flex: none;
  }

  .rail-dot.unread {
    background: var(--theme-color);
  }

  .type-chip {
    width: 36px;
    height: 36px;
    border-radius: 10px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex: none;
  }

  .item-body {
    min-width: 0;
    flex: 1;
  }

  .item-title {
    margin: 0;
    font-size: 13px;
    line-height: 1.5;
    color: var(--g-500);
    overflow: hidden;
    text-overflow: ellipsis;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
  }

  .item-title.unread {
    color: var(--g-900);
    font-weight: 600;
  }

  .item-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 4px 0 0;
    font-size: 12px;
    color: var(--g-500);
  }

  .item-time {
    font-variant-numeric: tabular-nums;
  }

  .priority-tag {
    font-size: 11px;
    line-height: 1.6;
    padding: 0 6px;
    border-radius: 4px;
  }

  .priority-tag.is-warning {
    background: var(--el-color-warning-light-9);
    color: var(--el-color-warning);
  }

  .priority-tag.is-danger {
    background: var(--el-color-danger-light-9);
    color: var(--el-color-danger);
  }

  .empty-block {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 6px;
    height: 100%;
    min-height: 220px;
    text-align: center;
    color: var(--g-400);
    padding: 0 32px;
  }

  .empty-title {
    margin: 4px 0 0;
    font-size: 13px;
    color: var(--g-600);
  }

  .empty-desc {
    margin: 0;
    font-size: 12px;
    line-height: 1.6;
  }

  .panel-footer {
    padding: 10px 16px 14px;
  }

  .skeleton-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 16px;
  }

  .skeleton-chip {
    width: 36px;
    height: 36px;
    border-radius: 10px;
    flex: none;
    background: var(--g-200);
  }

  .skeleton-lines {
    display: flex;
    flex-direction: column;
    gap: 8px;
    flex: 1;
  }

  .skeleton-line {
    height: 10px;
    border-radius: 5px;
    background: var(--g-200);
    animation: shimmer 1.2s ease-in-out infinite;
  }

  @keyframes shimmer {
    0%,
    100% {
      opacity: 0.45;
    }
    50% {
      opacity: 1;
    }
  }
</style>
