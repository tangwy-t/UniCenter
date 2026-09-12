<template>
  <ElDialog
    v-model="visible"
    class="upload-dialog"
    width="min(620px, 92vw)"
    :close-on-click-modal="false"
    append-to-body
    @closed="handleClosed"
  >
    <template #header>
      <div class="head">
        <span class="head-icon bg-theme/12 text-theme">
          <ArtSvgIcon icon="ri:upload-cloud-2-line" class="text-lg" />
        </span>
        <div class="head-text">
          <p class="head-title">上传文件</p>
          <p class="head-sub">拖拽或选择文件,支持批量上传</p>
        </div>
      </div>
    </template>

    <!-- 拖拽区 -->
    <div
      class="dropzone"
      :class="{ 'is-dragging': isDragging }"
      role="button"
      tabindex="0"
      aria-label="拖拽文件到此处或点击选择文件"
      @click="pick"
      @keydown.enter="pick"
      @dragover.prevent="isDragging = true"
      @dragleave.prevent="isDragging = false"
      @drop.prevent="onDrop"
    >
      <span class="dz-icon">
        <ArtSvgIcon icon="ri:upload-cloud-2-line" class="text-[34px]" />
      </span>
      <p class="dz-title"> 拖拽文件到此处,或 <span class="text-theme">点击选择</span> </p>
      <p class="dz-sub">支持图片 / 文档 / 压缩包 / 代码等常见格式,单次最多 {{ MAX_QUEUE }} 个</p>
      <input ref="fileInputRef" type="file" class="hidden" multiple @change="onPick" />
    </div>

    <!-- 上传队列 -->
    <div v-if="queue.length" class="queue">
      <div class="queue-head">
        <span>已选择 {{ queue.length }} 个文件</span>
        <ArtButtonTable
          class="!mr-0"
          icon="ri:delete-bin-5-line"
          iconClass="bg-g-300/55 text-g-700"
          title="清空列表"
          :disabled="uploading"
          @click="clearQueue"
        />
      </div>
      <div class="queue-body">
        <div v-for="item in queue" :key="item.uid" class="queue-item">
          <span class="q-icon" :class="[item.meta.tile, item.meta.text]">
            <ArtSvgIcon :icon="item.meta.icon" class="text-lg" />
          </span>

          <div class="q-main">
            <ElTooltip
              :content="item.file.name"
              placement="top"
              :disabled="item.file.name.length < 28"
            >
              <p class="q-name">{{ item.file.name }}</p>
            </ElTooltip>
            <ElProgress
              v-if="item.status === 'uploading'"
              :percentage="item.progress"
              :stroke-width="4"
              :show-text="false"
              class="q-progress"
            />
            <p class="q-meta" :class="{ 'text-error': item.status === 'error' }">
              {{ itemStatusText(item) }}
            </p>
          </div>

          <div class="q-side">
            <ElIcon v-if="item.status === 'uploading'" class="is-loading q-spin"
              ><Loading
            /></ElIcon>
            <ArtSvgIcon v-else-if="item.status === 'done'" icon="ri:check-line" class="q-ok" />
            <ElTooltip
              v-else-if="item.status === 'error'"
              :content="item.error || '上传失败'"
              placement="top"
            >
              <ArtSvgIcon icon="ri:error-warning-line" class="text-error q-state" />
            </ElTooltip>
            <button
              v-if="item.status !== 'uploading'"
              type="button"
              class="q-remove"
              title="移除"
              @click="removeItem(item.uid)"
            >
              <ArtSvgIcon icon="ri:close-line" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="footer">
        <span class="footer-hint">{{ footerHint }}</span>
        <div class="art-dialog-footer">
          <ArtButtonTable
            icon="ri:close-line"
            iconClass="bg-g-300/55 text-g-700"
            :title="uploading ? '取消上传' : '关闭'"
            @click="onCloseClick"
          />
          <ArtButtonTable
            v-if="hasFailures && !uploading"
            icon="ri:refresh-line"
            iconClass="bg-warning/12 text-warning"
            title="重试失败项"
            :disabled="!canStart"
            @click="start"
          />
          <ArtButtonTable
            icon="ri:upload-2-line"
            iconClass="bg-theme/12 text-theme"
            :title="uploading ? '上传中…' : '开始上传'"
            :disabled="!canStart"
            @click="start"
          />
        </div>
      </div>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import { Loading } from '@element-plus/icons-vue'
  import { uploadFile } from '../api'
  import { extOf, fileMetaOf, formatBytes } from '../file-meta'

  defineOptions({ name: 'SystemFileUploadDialog' })

  type UploadStatus = 'pending' | 'uploading' | 'done' | 'error'

  interface UploadQueueItem {
    uid: string
    file: File
    status: UploadStatus
    progress: number
    error: string
    meta: ReturnType<typeof fileMetaOf>
    abort?: () => void
  }

  const emit = defineEmits<{
    (e: 'finished'): void
  }>()

  const MAX_QUEUE = 50

  const visible = ref(false)
  const isDragging = ref(false)
  const uploading = ref(false)
  const queue = ref<UploadQueueItem[]>([])
  const fileInputRef = ref<HTMLInputElement>()
  let cancelRequested = false
  let uidSeed = 0

  const hasFailures = computed(() => queue.value.some((i) => i.status === 'error'))
  const hasPending = computed(() =>
    queue.value.some((i) => i.status === 'pending' || i.status === 'error')
  )
  const canStart = computed(() => hasPending.value && !uploading.value)
  const doneCount = computed(() => queue.value.filter((i) => i.status === 'done').length)
  const footerHint = computed(() => {
    if (uploading.value) return `正在上传 ${uploadingCount.value} 个文件…`
    if (doneCount.value > 0) return `已完成 ${doneCount.value} 个`
    return `总大小 ${formatBytes(queue.value.reduce((sum, i) => sum + i.file.size, 0))}`
  })
  const uploadingCount = computed(() => queue.value.filter((i) => i.status === 'uploading').length)

  function open() {
    queue.value = []
    cancelRequested = false
    visible.value = true
    nextTick(() => pick())
  }

  function close() {
    visible.value = false
  }

  function handleClosed() {
    if (uploading.value) return
    queue.value = []
    cancelRequested = false
  }

  function pick() {
    fileInputRef.value?.click()
  }

  function onPick(event: Event) {
    const input = event.target as HTMLInputElement
    if (input.files?.length) addFiles(input.files)
    input.value = ''
  }

  function onDrop(event: DragEvent) {
    isDragging.value = false
    if (event.dataTransfer?.files?.length) addFiles(event.dataTransfer.files)
  }

  /** 剪贴板粘贴上传(在弹窗打开时监听) */
  function onPaste(event: ClipboardEvent) {
    if (!visible.value || uploading.value) return
    if (event.clipboardData?.files?.length) {
      addFiles(event.clipboardData.files)
      event.preventDefault()
    }
  }

  function addFiles(list: FileList | File[]) {
    const files = Array.from(list)
    let skipped = 0
    for (const file of files) {
      if (queue.value.length >= MAX_QUEUE) {
        skipped++
        continue
      }
      // 同名同大小视为重复,直接跳过
      if (queue.value.some((q) => q.file.name === file.name && q.file.size === file.size)) continue
      uidSeed++
      queue.value.push({
        uid: `${Date.now()}-${uidSeed}`,
        file,
        status: 'pending',
        progress: 0,
        error: '',
        meta: fileMetaOf({ ext: extOf(file.name) })
      })
    }
    if (skipped > 0) ElMessage.warning(`最多同时上传 ${MAX_QUEUE} 个文件,已忽略 ${skipped} 个`)
  }

  function removeItem(uid: string) {
    queue.value = queue.value.filter((i) => i.uid !== uid)
  }

  async function clearQueue() {
    if (uploading.value) return
    await ElMessageBox.confirm('确认清空已选择的文件吗？', '清空确认', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })
    queue.value = []
  }

  function itemStatusText(item: UploadQueueItem): string {
    switch (item.status) {
      case 'uploading':
        return `上传中 ${item.progress}%`
      case 'done':
        return '上传完成'
      case 'error':
        return item.error || '上传失败'
      default:
        return formatBytes(item.file.size)
    }
  }

  async function start() {
    if (uploading.value) return
    uploading.value = true
    cancelRequested = false

    const targets = queue.value.filter((i) => i.status === 'pending' || i.status === 'error')
    let successCount = 0

    for (const item of targets) {
      if (cancelRequested) break

      const controller = new AbortController()
      item.abort = () => controller.abort()
      item.status = 'uploading'
      item.progress = 0
      item.error = ''

      try {
        await uploadFile(item.file, (p) => (item.progress = p), controller.signal)
        item.status = 'done'
        item.progress = 100
        successCount++
      } catch (err) {
        if (controller.signal.aborted) {
          item.status = 'pending'
          item.progress = 0
        } else {
          item.status = 'error'
          item.error = extractError(err)
        }
      }
      item.abort = undefined
    }

    uploading.value = false

    if (successCount > 0) {
      ElMessage.success(`成功上传 ${successCount} 个文件`)
      emit('finished')
    } else if (!cancelRequested) {
      ElMessage.error('上传失败,请检查文件后重试')
    }

    // 全部成功且队列非空:短暂停留后自动关闭
    if (queue.value.length > 0 && queue.value.every((i) => i.status === 'done')) {
      setTimeout(() => (visible.value = false), 500)
    }
  }

  function onCloseClick() {
    if (uploading.value) cancelAll()
    else visible.value = false
  }

  function cancelAll() {
    cancelRequested = true
    for (const item of queue.value) {
      if (item.status === 'uploading') item.abort?.()
    }
  }

  function extractError(err: unknown): string {
    const message = (err as { message?: string })?.message
    return message && message !== 'Network Error' ? message : '上传失败,请重试'
  }

  watch(visible, (val) => {
    if (val) document.addEventListener('paste', onPaste)
    else document.removeEventListener('paste', onPaste)
  })

  onBeforeUnmount(() => {
    document.removeEventListener('paste', onPaste)
  })

  defineExpose({ open, close })
</script>

<style scoped>
  .head {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .head-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 9px;
  }

  .head-title {
    font-size: 15px;
    font-weight: 500;
    line-height: 20px;
    color: var(--art-gray-900);
  }

  .head-sub {
    margin-top: 1px;
    font-size: 12px;
    color: var(--art-gray-500);
  }

  .dropzone {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 34px 20px;
    border: 1.5px dashed var(--default-border-dashed);
    border-radius: 12px;
    background: var(--art-gray-100);
    text-align: center;
    cursor: pointer;
    outline: none;
    transition:
      border-color 0.15s ease,
      background-color 0.15s ease;
  }

  .dropzone:hover,
  .dropzone:focus-visible,
  .dropzone.is-dragging {
    border-color: var(--theme-color);
    background: color-mix(in oklab, var(--theme-color) 4%, var(--art-gray-100));
  }

  .dz-icon {
    font-size: 34px;
    color: var(--art-gray-400);
    transition: color 0.15s ease;
  }

  .dropzone:hover .dz-icon,
  .dropzone.is-dragging .dz-icon {
    color: var(--theme-color);
  }

  .dz-title {
    margin: 8px 0 0;
    font-size: 14px;
    color: var(--art-gray-800);
  }

  .dz-sub {
    margin: 4px 0 0;
    font-size: 12px;
    color: var(--art-gray-500);
  }

  .queue {
    margin-top: 14px;
    border: 1px solid var(--default-border);
    border-radius: 12px;
    overflow: hidden;
  }

  .queue-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 12px;
    border-bottom: 1px solid var(--default-border);
    background: var(--art-gray-100);
    font-size: 12px;
    color: var(--art-gray-600);
  }

  .queue-body {
    max-height: 236px;
    overflow-y: auto;
  }

  .queue-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
  }

  .queue-item + .queue-item {
    border-top: 1px solid var(--default-border);
  }

  .q-icon {
    display: flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    border-radius: 9px;
  }

  .q-main {
    flex: 1;
    min-width: 0;
  }

  .q-name {
    margin: 0;
    font-size: 13px;
    color: var(--art-gray-800);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .q-progress {
    margin-top: 4px;
  }

  .q-meta {
    margin: 2px 0 0;
    font-size: 12px;
    color: var(--art-gray-500);
  }

  .q-side {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: none;
  }

  .q-spin {
    font-size: 16px;
  }

  .q-ok {
    font-size: 16px;
  }

  .q-state {
    font-size: 16px;
  }

  .q-remove {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border: none;
    border-radius: 6px;
    background: none;
    font-size: 14px;
    color: var(--art-gray-500);
    cursor: pointer;
    transition: background-color 0.15s ease;
  }

  .q-remove:hover {
    background: var(--art-hover-color);
  }

  .footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    width: 100%;
  }

  .footer-hint {
    font-size: 12px;
    color: var(--art-gray-500);
  }

  @media (prefers-reduced-motion: reduce) {
    .dropzone {
      transition: none;
    }
  }
</style>
