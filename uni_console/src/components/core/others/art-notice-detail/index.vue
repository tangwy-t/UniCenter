<!-- 公告/通知详情弹窗:公告管理页(管理形态)与铃铛(收件箱形态)共用。
     设计概念「公文台卡」:纸面公文 + 衬线标题 + 优先级印章(签名元素)。 -->
<template>
  <el-dialog
    v-model="visible"
    :show-close="false"
    width="min(560px, calc(100vw - 32px))"
    append-to-body
    class="notice-detail-dialog"
    @closed="handleClose"
  >
    <div class="nd-doc">
      <template v-if="detail">
        <header class="nd-head">
          <div class="nd-eyebrow">
            <span :class="['nd-type', typeChipClass]">
              <ArtSvgIcon
                :icon="detail.noticeType === 2 ? 'ri:megaphone-line' : 'ri:notification-3-line'"
              />
              {{ typeLabel }}
            </span>
            <button type="button" class="nd-close" aria-label="关闭" @click="visible = false">
              <ArtSvgIcon icon="ri:close-line" />
            </button>
          </div>

          <h1 class="nd-title">{{ detail.title }}</h1>

          <div class="nd-meta">
            <span v-if="detail.createBy" class="nd-meta-item">
              <ArtSvgIcon icon="ri:user-3-line" />
              <span>{{ detail.createBy }}</span>
            </span>
            <span class="nd-meta-item">
              <ArtSvgIcon icon="ri:time-line" />
              <span>{{ detail.publishTime || detail.createdAt || '—' }}</span>
            </span>
            <span v-if="detail.createBy" class="nd-meta-item nd-meta-status">
              <i :class="['nd-status-dot', statusDotClass]"></i>
              <span>{{ statusLabel }}</span>
            </span>
          </div>
        </header>

        <div class="nd-bind" aria-hidden="true">
          <i></i>
          <span></span>
          <i></i>
        </div>

        <div class="nd-body">
          <div v-if="hasContent" class="nd-content" v-html="safeContent" />
          <div v-else class="nd-empty">
            <ArtSvgIcon icon="ri:file-list-3-line" />
            <span>{{ '暂无内容' }}</span>
          </div>

          <div v-if="sealText" class="nd-seal" aria-hidden="true">
            <span :class="sealClass">{{ sealText }}</span>
          </div>
        </div>
      </template>

      <div v-else class="nd-empty nd-empty-full">
        <ArtSvgIcon icon="ri:file-list-3-line" />
        <span>{{ '暂无数据' }}</span>
      </div>
    </div>
  </el-dialog>
</template>

<script setup lang="ts">
  import { computed, ref } from 'vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { useNoticeDict } from '@/modules/system-notice/composables/useNoticeDict'
  import { sanitizeHtml } from '@/utils/ui/sanitize'
  import type { ArtNoticeDetailData } from './types'

  defineOptions({ name: 'ArtNoticeDetail' })

  const noticeDict = useNoticeDict()

  const visible = ref(false)
  const detail = ref<ArtNoticeDetailData | null>(null)

  // 正文为后端下发的富文本 HTML,渲染前必须消毒(防存储型 XSS)。
  const safeContent = computed(() => sanitizeHtml(detail.value?.content ?? ''))

  const typeLabel = computed(() =>
    noticeDict.labelOf(
      'sys_notice_type',
      detail.value?.noticeType,
      detail.value?.noticeType === 2 ? '公告' : '通知'
    )
  )

  // 类型徽标色彩语义跟着字典 list_class 走(管理端同样接 sys_notice_type)
  const typeChipClass = computed(() => {
    const cls = noticeDict.clsOf('sys_notice_type', detail.value?.noticeType)
    const map: Record<string, string> = {
      primary: 'is-primary',
      warning: 'is-warning',
      danger: 'is-danger',
      success: 'is-success',
      info: 'is-info'
    }
    return map[cls ?? ''] ?? 'is-info'
  })

  const hasContent = computed(() => {
    const content = detail.value?.content
    return content != null && String(content).trim() !== ''
  })

  // 状态:0=草稿 1=已发布 2=已撤回(仅管理形态展示,label 对接 sys_notice_status 字典,中文兜底)
  const statusLabel = computed(() => {
    const fallback = (() => {
      switch (detail.value?.status) {
        case 1:
          return '已发布'
        case 2:
          return '已撤回'
        default:
          return '草稿'
      }
    })()
    return noticeDict.labelOf('sys_notice_status', detail.value?.status, fallback)
  })
  const statusDotClass = computed(() => {
    switch (detail.value?.status) {
      case 1:
        return 'is-ok'
      case 2:
        return 'is-off'
      default:
        return 'is-draft'
    }
  })

  // 优先级印章:对接 sys_notice_priority 字典(1=重要 2=紧急才落印,0 无印);
  // 印文取字典 label(中文兜底),印色跟字典 list_class(danger 朱砂/warning 琥珀)。
  const sealText = computed(() => {
    const p = detail.value?.priority
    if (p !== 1 && p !== 2) return ''
    const fallback = p === 1 ? '重要' : '紧急'
    return noticeDict.labelOf('sys_notice_priority', p, fallback)
  })
  const sealClass = computed(() =>
    noticeDict.clsOf('sys_notice_priority', detail.value?.priority) === 'danger'
      ? 'is-urgent'
      : 'is-important'
  )

  function open(row: ArtNoticeDetailData) {
    detail.value = row
    visible.value = true
    noticeDict.ensure() // 预热字典(store 缓存,管理页已加载时零请求)
  }

  function handleClose() {
    detail.value = null
  }

  defineExpose({ open })
</script>

<style lang="scss" scoped>
  /* 纸面:始终贴合系统明暗,拒绝奶油稿纸风 */
  .nd-doc {
    max-height: 72vh;
    overflow-y: auto;
    border-radius: inherit; /* padding 清零后背景贴角,继承 .el-dialog 圆角避免方角溢出 */
    background: linear-gradient(180deg, var(--el-bg-color) 0%, var(--el-bg-color-page) 100%);
    overscroll-behavior: contain;

    /* 隐藏内部滚动条(滚轮/触摸滚动不受影响),与仓库 el-ui.scss 弹窗内滚规则同风格 */
    scrollbar-width: none; /* Firefox */
    -ms-overflow-style: none; /* IE / 旧版 Edge */

    &::-webkit-scrollbar {
      display: none; /* Chrome / Safari / 新版 Edge */
    }
  }

  .nd-head {
    padding: 22px 28px 16px;
  }

  .nd-eyebrow {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 14px;
  }

  /* 类型徽标:细排小章,非块状色带 */
  .nd-type {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 10px;
    border: 1px solid currentColor;
    border-radius: 999px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.14em;
  }

  /* 类型徽标色彩语义与字典 list_class 一一对应(管理列表 tag 同源) */
  .nd-type.is-primary {
    color: var(--el-color-primary);
    background: color-mix(in srgb, var(--el-color-primary) 7%, transparent);
  }

  .nd-type.is-warning {
    color: var(--el-color-warning);
    background: color-mix(in srgb, var(--el-color-warning) 9%, transparent);
  }

  .nd-type.is-danger {
    color: var(--el-color-danger);
    background: color-mix(in srgb, var(--el-color-danger) 8%, transparent);
  }

  .nd-type.is-success {
    color: var(--el-color-success);
    background: color-mix(in srgb, var(--el-color-success) 9%, transparent);
  }

  .nd-type.is-info {
    color: var(--el-color-info);
    background: color-mix(in srgb, var(--el-color-info) 8%, transparent);
  }

  /* 自绘关闭(EP 默认头部隐藏,消除双标题) */
  .nd-close {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border: 0;
    border-radius: 50%;
    background: transparent;
    color: var(--el-text-color-secondary);
    font-size: 16px;
    cursor: pointer;
    transition:
      background-color 0.2s ease,
      color 0.2s ease;
  }

  .nd-close:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }

  .nd-close:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }

  /* 衬线标题:唯一的字体冒险,公文视觉身份 */
  .nd-title {
    margin: 0 0 14px;
    font-family:
      'Noto Serif SC', 'Source Han Serif SC', 'Source Han Serif CN', 'Songti SC', 'STSong', Georgia,
      'Times New Roman', serif;
    font-size: 22px;
    font-weight: 600;
    line-height: 1.55;
    letter-spacing: 0.01em;
    color: var(--el-text-color-primary);
    word-break: break-word;
    text-wrap: balance;
  }

  /* 十二点式元信息:hairline 上边 + 中点分隔 */
  .nd-meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px 0;
    padding-top: 12px;
    border-top: 1px solid var(--el-border-color-lighter);
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .nd-meta-item {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-variant-numeric: tabular-nums;
  }

  .nd-meta-item + .nd-meta-item::before {
    content: '·';
    margin: 0 9px;
    color: var(--el-border-color-dark);
  }

  .nd-status-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    display: inline-block;
  }

  .nd-status-dot.is-ok {
    background: var(--el-color-success);
  }

  .nd-status-dot.is-off {
    background: var(--el-color-error);
  }

  .nd-status-dot.is-draft {
    background: var(--el-color-info);
  }

  /* 装订压线:虚线 + 两端装订孔(结构装置,呼应布告栏) */
  .nd-bind {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 28px;
    color: var(--el-border-color);
  }

  .nd-bind span {
    flex: 1;
    border-top: 1px dashed var(--el-border-color);
  }

  .nd-bind i {
    width: 7px;
    height: 7px;
    border: 1px solid var(--el-border-color);
    border-radius: 50%;
    position: relative;
  }

  .nd-bind i::after {
    content: '';
    position: absolute;
    inset: 2px;
    border-radius: 50%;
    background: var(--el-border-color-dark);
  }

  .nd-body {
    padding: 6px 28px 26px;
  }

  /* 阅读版心与正文排版 */
  .nd-content {
    font-size: 14px;
    line-height: 1.85;
    color: var(--el-text-color-primary);
    word-break: break-word;
  }

  .nd-content :deep(p) {
    margin: 0 0 1em;
  }

  .nd-content :deep(h1),
  .nd-content :deep(h2),
  .nd-content :deep(h3) {
    font-family:
      'Noto Serif SC', 'Source Han Serif SC', 'Source Han Serif CN', 'Songti SC', 'STSong', Georgia,
      serif;
    font-weight: 600;
    color: var(--el-text-color-primary);
    margin: 1.5em 0 0.6em;
  }

  .nd-content :deep(h4),
  .nd-content :deep(h5) {
    font-weight: 600;
    color: var(--el-text-color-primary);
    margin: 1.4em 0 0.6em;
  }

  .nd-content :deep(h1) {
    font-size: 17px;
  }

  .nd-content :deep(h2) {
    font-size: 16px;
  }

  .nd-content :deep(h3) {
    font-size: 14.5px;
  }

  .nd-content :deep(a) {
    color: var(--el-color-primary);
    text-decoration: underline;
    text-underline-offset: 3px;
  }

  .nd-content :deep(img) {
    max-width: 100%;
    border-radius: 6px;
    margin: 8px 0;
    border: 1px solid var(--el-border-color-lighter);
  }

  /* tailwind preflight 全局重置 ol/ul 的 list-style,富文本需恢复 marker */
  .nd-content :deep(ul),
  .nd-content :deep(ol) {
    padding-left: 22px;
    margin: 0 0 1em;
  }

  .nd-content :deep(ul) {
    list-style: disc;
  }

  .nd-content :deep(ol) {
    list-style: decimal;
  }

  .nd-content :deep(li) {
    margin-bottom: 4px;
  }

  .nd-content :deep(blockquote) {
    border-left: 3px solid var(--el-color-primary-light-5);
    margin: 1.1em 0;
    padding: 8px 16px;
    color: var(--el-text-color-secondary);
    background: var(--el-fill-color-light);
    border-radius: 0 6px 6px 0;
  }

  .nd-content :deep(table) {
    border-collapse: collapse;
    width: 100%;
    margin: 1em 0;
    font-size: 13px;
  }

  .nd-content :deep(table th),
  .nd-content :deep(table td) {
    border: 1px solid var(--el-border-color-lighter);
    padding: 7px 12px;
  }

  .nd-content :deep(table th) {
    background: var(--el-fill-color-light);
    font-weight: 600;
  }

  /* 优先级印章:签名元素,盖在文末右缘 */
  .nd-seal {
    display: flex;
    justify-content: flex-end;
    margin-top: 20px;
  }

  .nd-seal span {
    display: inline-block;
    padding: 4px 10px;
    border: 2px solid currentColor;
    border-radius: 4px;
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 3px;
    line-height: 1.4;
    transform: rotate(-3deg);
    animation: nd-seal-stamp 240ms cubic-bezier(0.2, 0.7, 0.3, 1.15) 120ms both;
  }

  .nd-seal .is-urgent {
    color: var(--el-color-error);
    background: color-mix(in srgb, var(--el-color-error) 7%, transparent);
  }

  .nd-seal .is-important {
    color: var(--el-color-warning);
    background: color-mix(in srgb, var(--el-color-warning) 9%, transparent);
  }

  @keyframes nd-seal-stamp {
    0% {
      transform: rotate(7deg) scale(1.45);
      opacity: 0.15;
    }
    60% {
      transform: rotate(-2deg) scale(0.94);
    }
    100% {
      transform: rotate(-3deg) scale(1);
      opacity: 1;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .nd-seal span {
      animation: none;
    }
  }

  .nd-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    padding: 36px 0;
    text-align: center;
    color: var(--el-text-color-placeholder);
    font-size: 13px;
  }

  .nd-empty :deep(svg) {
    width: 30px;
    height: 30px;
    opacity: 0.7;
  }

  .nd-empty-full {
    min-height: 200px;
  }
</style>

<style lang="scss">
  /* EP 对话框骨架:纸面满铺对话框,仅本弹窗生效。
     - .el-dialog 根自带 16px 内边距(class 经 $attrs 合在该节点自身)→ 根节点零化;
     - 仓库全局 el-ui.scss:139 给 .el-dialog__body 写了 padding:25px 0 !important
       (弹窗内滚系统)→ 低于其特异性必输,故此处以更高特异性(0,2,0)带 !important 反制;
     - 其他弹窗(无本 class)继续走全局 25px 内滚系统,不受影响。 */
  .notice-detail-dialog {
    &.el-dialog {
      padding: 0;
    }

    .el-dialog__header {
      display: none;
    }

    .el-dialog__body {
      padding: 0 !important; /* 压过 el-ui.scss 全局 !important(0,2,0 > 0,1,0) */
      background: var(--el-bg-color);
    }
  }
</style>
