<template>
  <ElDialog
    v-model="visible"
    class="preview-dialog"
    width="min(960px, 94vw)"
    top="5vh"
    :show-close="false"
    append-to-body
    @closed="onClosed"
  >
    <template #header>
      <div v-if="current" class="pv-head">
        <span class="pv-icon" :class="[currentMeta.tile, currentMeta.text]">
          <ArtSvgIcon :icon="currentMeta.icon" class="text-lg" />
        </span>
        <div class="pv-title">
          <p class="pv-name">{{ current.name }}</p>
          <p class="pv-meta">
            {{ currentMeta.label }} · {{ formatBytes(current.size) }} · 上传于
            {{ formatTime(current.createdAt) }}
          </p>
        </div>
        <div class="pv-actions">
          <ElTooltip content="下载文件" placement="bottom">
            <button
              type="button"
              class="pv-btn"
              :disabled="!hasPermission('system:file:download')"
              @click="downloadCurrent"
            >
              <ArtSvgIcon icon="ri:download-2-line" />
            </button>
          </ElTooltip>
          <span class="pv-divider" />
          <ElTooltip content="上一个" placement="bottom">
            <button type="button" class="pv-btn" :disabled="!hasPrev" @click="step(-1)">
              <ArtSvgIcon icon="ri:arrow-left-s-line" />
            </button>
          </ElTooltip>
          <ElTooltip content="下一个" placement="bottom">
            <button type="button" class="pv-btn" :disabled="!hasNext" @click="step(1)">
              <ArtSvgIcon icon="ri:arrow-right-s-line" />
            </button>
          </ElTooltip>
          <span class="pv-divider" />
          <ElTooltip content="关闭" placement="bottom">
            <button type="button" class="pv-btn" @click="visible = false">
              <ArtSvgIcon icon="ri:close-line" />
            </button>
          </ElTooltip>
        </div>
      </div>
    </template>

    <div class="pv-body">
      <!-- 加载中 -->
      <div v-if="loading" class="pv-state">
        <ElIcon class="is-loading" :size="26"><Loading /></ElIcon>
        <p>加载预览中…</p>
      </div>

      <!-- 加载失败 -->
      <div v-else-if="loadError" class="pv-state">
        <span class="pv-state-icon bg-error/12 text-error"
          ><ArtSvgIcon icon="ri:error-warning-line" class="text-[30px]"
        /></span>
        <p class="pv-state-title">预览加载失败</p>
        <p class="pv-state-sub">{{ loadError }}</p>
        <div class="flex-c gap-2">
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-g-300/55 text-g-700"
            title="重试"
            @click="loadCurrent"
          />
          <ArtButtonTable
            icon="ri:download-2-line"
            iconClass="bg-theme/12 text-theme"
            title="下载文件"
            @click="downloadCurrent"
          />
        </div>
      </div>

      <!-- 超大媒体引导下载 -->
      <div v-else-if="tooLarge" class="pv-state">
        <span class="pv-state-icon bg-warning/12 text-warning"
          ><ArtSvgIcon icon="ri:hard-drive-2-line" class="text-[30px]"
        /></span>
        <p class="pv-state-title">文件较大,建议下载后查看</p>
        <p class="pv-state-sub"
          >该文件超过 {{ formatBytes(MAX_PREVIEW_SIZE) }},在线预览需要较长加载时间</p
        >
        <div class="flex-c gap-2">
          <ArtButtonTable
            icon="ri:eye-line"
            iconClass="bg-g-300/55 text-g-700"
            title="仍然预览"
            @click="forcePreview"
          />
          <ArtButtonTable
            icon="ri:download-2-line"
            iconClass="bg-theme/12 text-theme"
            title="下载文件"
            @click="downloadCurrent"
          />
        </div>
      </div>

      <!-- 图片 -->
      <div v-else-if="previewKind === 'image'" class="pv-media-stage">
        <img :src="blobUrl" class="pv-img" :alt="current?.name" />
      </div>

      <!-- 视频 -->
      <div v-else-if="previewKind === 'video'" class="pv-media-stage">
        <video :src="blobUrl" class="pv-media" controls autoplay />
      </div>

      <!-- 音频 -->
      <div v-else-if="previewKind === 'audio'" class="pv-audio">
        <span class="pv-audio-icon" :class="[currentMeta.tile, currentMeta.text]">
          <ArtSvgIcon :icon="currentMeta.icon" class="text-[44px]" />
        </span>
        <audio :src="blobUrl" controls class="pv-audio-player" />
      </div>

      <!-- PDF -->
      <div v-else-if="previewKind === 'pdf'" class="pv-media-stage">
        <iframe :src="blobUrl" class="pv-frame" title="PDF 预览" />
      </div>

      <!-- 文本/代码 -->
      <div v-else-if="previewKind === 'text'" class="pv-text-wrap">
        <pre class="pv-text">{{ textContent }}</pre>
      </div>

      <!-- 兜底 -->
      <div v-else class="pv-state">
        <span class="pv-state-icon" :class="[currentMeta.tile, currentMeta.text]">
          <ArtSvgIcon :icon="currentMeta.icon" class="text-[40px]" />
        </span>
        <p class="pv-state-title">{{ current?.name }}</p>
        <p class="pv-state-sub">该格式暂不支持在线预览,请下载后查看</p>
        <ArtButtonTable
          icon="ri:download-2-line"
          iconClass="bg-theme/12 text-theme"
          title="下载文件"
          @click="downloadCurrent"
        />
      </div>
    </div>
  </ElDialog>
</template>

<script setup lang="ts">
  import { computed, onBeforeUnmount, ref, watch } from 'vue'
  import { ElMessage } from 'element-plus'
  import { Loading } from '@element-plus/icons-vue'
  import { fetchFileContent } from '../api'
  import {
    fileMetaOf,
    formatBytes,
    formatTime,
    hasPermission,
    isTextFile,
    MAX_PREVIEW_SIZE,
    saveBlob
  } from '../file-meta'

  defineOptions({ name: 'SystemFilePreviewDialog' })

  type PreviewKind = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'other'

  const MAX_TEXT_PREVIEW = 4 * 1024 * 1024

  const visible = ref(false)
  const loading = ref(false)
  const loadError = ref('')
  const tooLarge = ref(false)
  const forceLoad = ref(false)
  const blobUrl = ref('')
  const textContent = ref('')
  const items = ref<Api.File.FileItem[]>([])
  const index = ref(0)

  const current = computed(() => items.value[index.value] ?? null)
  const currentMeta = computed(() => (current.value ? fileMetaOf(current.value) : fileMetaOf({})))
  const hasPrev = computed(() => index.value > 0)
  const hasNext = computed(() => index.value < items.value.length - 1)

  const previewKind = computed<PreviewKind>(() => {
    const item = current.value
    if (!item) return 'other'
    const ext = (item.ext || '').toLowerCase()
    switch (item.category) {
      case 'image':
        return 'image'
      case 'video':
        return 'video'
      case 'audio':
        return 'audio'
    }
    if (ext === '.pdf' || item.mimeType?.includes('pdf')) return 'pdf'
    if (isTextFile(ext)) return 'text'
    return 'other'
  })

  function open(item: Api.File.FileItem, list: Api.File.FileItem[] = [item]) {
    items.value = list.length ? list : [item]
    const idx = items.value.findIndex((i) => i.id === item.id)
    index.value = idx < 0 ? 0 : idx
    visible.value = true
    // 显式触发加载:同一文件重复打开时 current 引用不变,watch(current) 不会
    // 触发,而 close 已把 blobUrl revoke 清零,故必须在此主动重载。
    loadCurrent()
  }

  function step(delta: number) {
    const next = index.value + delta
    if (next < 0 || next >= items.value.length) return
    index.value = next
    loadCurrent()
  }

  function revokeBlob() {
    if (blobUrl.value) {
      URL.revokeObjectURL(blobUrl.value)
      blobUrl.value = ''
    }
  }

  // —— 预览内容内存缓存 ——
  // 按文件 id 缓存已拉取的 Blob,重复打开秒开、零网络(HTTP 层另有
  // Cache-Control: max-age 兜底覆盖未命中/超限场景)。按 LRU + 总字节上限
  // 淘汰,避免长时间预览大文件把内存撑爆。
  const previewCache = new Map<string, Blob>()
  const PREVIEW_CACHE_MAX_BLOB = 20 * 1024 * 1024 // 单文件 >20MB 不进缓存
  const PREVIEW_CACHE_MAX_BYTES = 64 * 1024 * 1024 // 缓存总字节上限
  let previewCacheBytes = 0

  function cacheGet(id: string): Blob | undefined {
    const blob = previewCache.get(id)
    if (blob) {
      previewCache.delete(id)
      previewCache.set(id, blob) // LRU 触达,移到队尾
    }
    return blob
  }

  function cachePut(id: string, blob: Blob) {
    if (!blob || blob.size > PREVIEW_CACHE_MAX_BLOB) return
    const prev = previewCache.get(id)
    if (prev) {
      previewCache.delete(id)
      previewCacheBytes -= prev.size
    }
    previewCache.set(id, blob)
    previewCacheBytes += blob.size
    while (previewCacheBytes > PREVIEW_CACHE_MAX_BYTES && previewCache.size > 1) {
      const oldestId = previewCache.keys().next().value
      if (oldestId === undefined) break
      const oldest = previewCache.get(oldestId)
      previewCache.delete(oldestId)
      if (oldest) previewCacheBytes -= oldest.size
    }
  }

  async function loadCurrent() {
    const item = current.value
    if (!item) return
    loading.value = true
    loadError.value = ''
    tooLarge.value = false
    textContent.value = ''
    revokeBlob()

    try {
      // 优先命中内存缓存;未命中才走网络并回填缓存
      let blob = cacheGet(item.id)
      if (!blob) {
        blob = await fetchFileContent(item.id)
        cachePut(item.id, blob)
      }

      // 视频/音频在线预览有体积上限,首次命中引导下载;用户强制仍然预览
      if (
        !forceLoad.value &&
        (previewKind.value === 'video' || previewKind.value === 'audio') &&
        blob.size > MAX_PREVIEW_SIZE
      ) {
        tooLarge.value = true
        return
      }
      forceLoad.value = false

      if (previewKind.value === 'text') {
        if (blob.size > MAX_TEXT_PREVIEW) {
          textContent.value =
            '// 文本过大,仅显示前 4MB 内容\n\n' + (await blob.slice(0, MAX_TEXT_PREVIEW).text())
        } else {
          textContent.value = await blob.text()
        }
      } else {
        blobUrl.value = URL.createObjectURL(blob)
      }
    } catch (err) {
      const message = (err as { message?: string })?.message
      loadError.value = message && message !== 'Network Error' ? message : '预览加载失败,请重试'
    } finally {
      loading.value = false
    }
  }

  function forcePreview() {
    forceLoad.value = true
    tooLarge.value = false
    loadCurrent()
  }

  async function downloadCurrent() {
    const item = current.value
    if (!item) return
    try {
      const blob = await fetchFileContent(item.id, true)
      saveBlob(blob, item.name)
    } catch (err) {
      const message = (err as { message?: string })?.message
      ElMessage.error(message && message !== 'Network Error' ? message : '下载失败,请重试')
    }
  }

  /** 键盘导航:左右方向键切换文件,esc 关闭由 ElDialog 处理 */
  function onKeydown(event: KeyboardEvent) {
    if (!visible.value) return
    const target = event.target as HTMLElement
    if (target && ['INPUT', 'TEXTAREA'].includes(target.tagName)) return
    if (event.key === 'ArrowLeft' && hasPrev.value) step(-1)
    if (event.key === 'ArrowRight' && hasNext.value) step(1)
  }

  watch(visible, (val) => {
    if (val) document.addEventListener('keydown', onKeydown, true)
    else document.removeEventListener('keydown', onKeydown, true)
  })

  function onClosed() {
    revokeBlob()
    textContent.value = ''
    loadError.value = ''
    tooLarge.value = false
    forceLoad.value = false
  }

  onBeforeUnmount(() => {
    document.removeEventListener('keydown', onKeydown, true)
    revokeBlob()
  })

  defineExpose({ open })
</script>

<style scoped>
  .pv-head {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
  }

  .pv-icon {
    display: flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 38px;
    height: 38px;
    border-radius: 10px;
  }

  .pv-title {
    flex: 1;
    min-width: 0;
  }

  .pv-name {
    margin: 0;
    font-size: 15px;
    font-weight: 500;
    color: var(--art-gray-900);
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }

  .pv-meta {
    margin: 1px 0 0;
    font-size: 12px;
    color: var(--art-gray-500);
  }

  .pv-actions {
    display: flex;
    flex: none;
    align-items: center;
    gap: 4px;
  }

  .pv-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border: none;
    border-radius: 8px;
    background: none;
    font-size: 16px;
    color: var(--art-gray-600);
    cursor: pointer;
    transition:
      background-color 0.15s ease,
      color 0.15s ease;
  }

  .pv-btn:hover:not(:disabled) {
    background: var(--art-hover-color);
    color: var(--theme-color);
  }

  .pv-btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }

  .pv-divider {
    width: 1px;
    height: 16px;
    margin: 0 2px;
    background: var(--default-border);
  }

  .pv-body {
    min-height: 380px;
  }

  /* 深色媒体舞台:图片/视频/PDF 的扁平深底 */
  .pv-media-stage {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 420px;
    max-height: calc(100vh - 190px);
    border-radius: 12px;
    background: #0b0f19;
    overflow: hidden;
  }

  .pv-img {
    display: block;
    max-width: 100%;
    max-height: calc(100vh - 200px);
    object-fit: contain;
    user-select: none;
  }

  .pv-media {
    display: block;
    width: 100%;
    max-height: calc(100vh - 200px);
    outline: none;
  }

  .pv-frame {
    display: block;
    width: 100%;
    height: calc(100vh - 190px);
    min-height: 420px;
    border: none;
  }

  .pv-audio {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 22px;
    min-height: 380px;
  }

  .pv-audio-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 108px;
    height: 108px;
    border-radius: 26px;
  }

  .pv-audio-player {
    width: min(440px, 100%);
    outline: none;
  }

  .pv-text-wrap {
    max-height: calc(100vh - 190px);
    min-height: 380px;
    border-radius: 12px;
    background: #0b0f19;
    overflow: auto;
  }

  .pv-text {
    margin: 0;
    padding: 16px 18px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 13px;
    line-height: 1.6;
    color: #c7c7d1;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .pv-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    min-height: 380px;
    text-align: center;
  }

  .pv-state-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 76px;
    height: 76px;
    border-radius: 20px;
  }

  .pv-state-title {
    margin: 6px 0 0;
    font-size: 15px;
    font-weight: 500;
    color: var(--art-gray-800);
  }

  .pv-state-sub {
    margin: 0 0 8px;
    max-width: 420px;
    font-size: 13px;
    color: var(--art-gray-500);
  }
</style>
