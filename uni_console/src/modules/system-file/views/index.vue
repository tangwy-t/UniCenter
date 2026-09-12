<template>
  <div class="file-page">
    <!-- ── 概览统计卡 ─────────────────────────────────────────────── -->
    <section class="stats-grid">
      <article
        v-for="card in statCards"
        :key="card.key"
        class="stat-card"
        :class="{ 'is-loading': statsLoading }"
        role="button"
        tabindex="0"
        @click="card.onClick"
        @keydown.enter="card.onClick"
      >
        <span class="stat-icon" :class="card.tile">
          <ArtSvgIcon :icon="card.icon" class="text-[22px]" />
        </span>
        <span class="stat-body">
          <span class="stat-label">{{ card.label }}</span>
          <span class="stat-value">
            <ArtCountTo v-if="card.count != null" :target="card.count" :duration="900" />
            <template v-else>{{ card.text }}</template>
          </span>
          <span class="stat-sub">{{ statsLoading ? '加载中…' : card.sub }}</span>
        </span>
      </article>
    </section>

    <!-- ── 分类分布条 ─────────────────────────────────────────────── -->
    <section class="dist-panel">
      <div class="dist-bar" aria-hidden="true">
        <ElTooltip
          v-for="d in categoryDistribution"
          :key="d.key"
          :content="`${d.label} ${d.count} 个 · ${d.pct.toFixed(1)}%`"
          placement="top"
        >
          <span class="dist-seg" :class="d.solid" :style="{ width: `${d.pct}%` }" />
        </ElTooltip>
      </div>
      <div class="dist-legend">
        <button
          v-for="d in categoryDistribution"
          :key="d.key"
          type="button"
          class="legend-item"
          @click="switchCategory(d.key)"
        >
          <span class="legend-dot" :class="d.solid" />
          <span class="legend-label">{{ d.label }}</span>
          <span class="legend-count">{{ d.count }}</span>
        </button>
      </div>
    </section>

    <!-- ── 主面板 ─────────────────────────────────────────────────── -->
    <section class="main-panel">
      <!-- 工具栏 -->
      <header class="panel-toolbar">
        <nav class="chips-scroll" aria-label="分类筛选">
          <button
            type="button"
            class="chip"
            :class="{ 'is-active': !activeCategory }"
            @click="switchCategory('')"
          >
            <span>全部</span>
            <span class="chip-count">{{ stats?.total ?? 0 }}</span>
          </button>
          <button
            v-for="cat in FILE_CATEGORY_LIST"
            :key="cat.key"
            type="button"
            class="chip"
            :class="{ 'is-active': activeCategory === cat.key }"
            @click="switchCategory(cat.key)"
          >
            <ArtSvgIcon :icon="cat.icon" class="text-[14px]" />
            <span>{{ cat.label }}</span>
            <span class="chip-count">{{ stats?.categoryCounts?.[cat.key] ?? 0 }}</span>
          </button>
        </nav>

        <div class="tool-actions">
          <ElInput
            v-model="keyword"
            class="search-input"
            placeholder="搜索文件名"
            clearable
            @input="onKeywordInput"
            @clear="onKeywordInput"
          >
            <template #prefix>
              <ArtSvgIcon icon="ri:search-line" class="text-g-400" />
            </template>
          </ElInput>

          <ElDropdown trigger="click" @command="handleSortChange">
            <button type="button" class="tool-btn" title="排序方式">
              <ArtSvgIcon icon="ri:arrow-up-down-line" />
              <span class="hidden md:inline">{{ sort.label }}</span>
            </button>
            <template #dropdown>
              <ElDropdownMenu>
                <ElDropdownItem
                  v-for="(opt, index) in sortOptions"
                  :key="opt.label"
                  :command="index"
                  :class="{ 'sort-active': sort.index === index }"
                >
                  <span class="flex-c gap-2">
                    <ArtSvgIcon
                      v-if="sort.index === index"
                      icon="ri:check-line"
                      class="text-theme"
                    />
                    <span v-else class="inline-block w-3.5" />
                    {{ opt.label }}
                  </span>
                </ElDropdownItem>
              </ElDropdownMenu>
            </template>
          </ElDropdown>

          <div class="view-toggle" role="group" aria-label="视图切换">
            <button
              type="button"
              class="view-btn"
              :class="{ 'is-active': viewMode === 'grid' }"
              title="网格视图"
              aria-label="网格视图"
              @click="viewMode = 'grid'"
            >
              <ArtSvgIcon icon="ri:layout-grid-line" />
            </button>
            <button
              type="button"
              class="view-btn"
              :class="{ 'is-active': viewMode === 'list' }"
              title="列表视图"
              aria-label="列表视图"
              @click="viewMode = 'list'"
            >
              <ArtSvgIcon icon="ri:list-check-2" />
            </button>
          </div>

          <button
            type="button"
            class="tool-btn"
            title="刷新"
            :class="{ 'is-loading': loading }"
            @click="refreshAll"
          >
            <ArtSvgIcon icon="ri:refresh-line" />
          </button>

          <ArtButtonTable
            v-perm="PermFileUpload"
            class="!mr-0"
            icon="ri:upload-2-line"
            iconClass="bg-theme/12 text-theme"
            title="上传文件"
            @click="openUpload"
          />
        </div>
      </header>

      <!-- 内容区 -->
      <div class="panel-body">
        <!-- 加载状态 -->
        <div v-if="bodyLoading" class="panel-loading">
          <ArtSvgIcon icon="ri:loader-4-line" class="loading-spin" />
          <span class="loading-text">加载中…</span>
        </div>

        <!-- 空状态 -->
        <div v-else-if="!loading && !files.length" class="empty-state">
          <span
            class="empty-icon"
            :class="isFiltering ? 'bg-g-300/60 text-g-500' : 'bg-theme/12 text-theme'"
          >
            <ArtSvgIcon
              :icon="isFiltering ? 'ri:file-search-line' : 'ri:inbox-2-line'"
              class="text-[42px]"
            />
          </span>
          <p class="empty-title">{{ isFiltering ? '没有找到匹配的文件' : '暂无文件' }}</p>
          <p class="empty-sub">
            {{ isFiltering ? '换个关键字或清除筛选条件试试' : '上传第一个文件,开始管理你的资源' }}
          </p>
          <div class="flex-c gap-2">
            <ArtButtonTable
              v-if="isFiltering"
              icon="ri:filter-off-line"
              iconClass="bg-g-300/55 text-g-700"
              title="清除筛选"
              @click="resetFilters"
            />
            <ArtButtonTable
              v-else
              v-perm="PermFileUpload"
              icon="ri:upload-2-line"
              iconClass="bg-theme/12 text-theme"
              title="立即上传"
              @click="openUpload"
            />
          </div>
        </div>

        <!-- 网格视图 -->
        <div v-else-if="viewMode === 'grid'" class="file-grid">
          <article
            v-for="(entry, index) in displayFiles"
            :key="entry.file.id"
            class="file-card"
            :class="{ 'is-selected': isSelected(entry.file) }"
            :style="{ animationDelay: `${Math.min(index, 16) * 24}ms` }"
          >
            <ElCheckbox
              class="card-check"
              :model-value="isSelected(entry.file)"
              :aria-label="`选择 ${entry.file.name}`"
              @click.stop
              @change="(v: string | number | boolean) => toggleSelect(entry.file, !!v)"
            />

            <div class="card-actions">
              <button
                type="button"
                class="card-btn"
                title="预览"
                @click.stop="openPreview(entry.file)"
              >
                <ArtSvgIcon icon="ri:eye-line" />
              </button>
              <button
                v-if="hasPermission('system:file:download')"
                type="button"
                class="card-btn"
                title="下载"
                @click.stop="handleDownload(entry.file)"
              >
                <ArtSvgIcon icon="ri:download-2-line" />
              </button>
              <button
                v-if="hasPermission('system:file:edit')"
                type="button"
                class="card-btn"
                title="重命名"
                @click.stop="openRename(entry.file)"
              >
                <ArtSvgIcon icon="ri:pencil-line" />
              </button>
              <button
                v-if="hasPermission('system:file:delete')"
                type="button"
                class="card-btn is-danger"
                title="删除"
                @click.stop="handleDelete([entry.file])"
              >
                <ArtSvgIcon icon="ri:delete-bin-5-line" />
              </button>
            </div>

            <button type="button" class="card-main" @click="openPreview(entry.file)">
              <span class="card-tile" :class="entry.meta.tile">
                <ArtSvgIcon :icon="entry.meta.icon" class="tile-icon" :class="entry.meta.text" />
                <img
                  v-if="entry.meta.category === 'image' && thumbOf(entry.file).status === 'ok'"
                  :src="thumbOf(entry.file).url"
                  class="card-thumb"
                  loading="lazy"
                  decoding="async"
                  alt=""
                />
              </span>
              <span class="card-name" :title="entry.file.name">{{ entry.file.name }}</span>
              <span class="card-meta">
                <span class="card-size"
                  >{{ entry.meta.label }} · {{ formatBytes(entry.file.size) }}</span
                >
                <span class="card-time">{{ formatRelativeTime(entry.file.createdAt) }}</span>
              </span>
            </button>
          </article>
        </div>

        <!-- 列表视图 -->
        <div v-else class="list-view">
          <ArtTable
            ref="tableRef"
            :data="files"
            :columns="columns"
            row-key="id"
            v-bind="{ onSelectionChange: onSelectionChange }"
          />
        </div>
      </div>

      <!-- 分页 -->
      <footer v-if="pagination.total > 0" class="panel-footer">
        <span class="footer-hint">
          <template v-if="selectedIds.length"
            >已选 <b class="text-theme">{{ selectedIds.length }}</b> 项</template
          >
          <template v-else>共 {{ pagination.total }} 个文件</template>
        </span>
        <ElPagination
          background
          small
          layout="sizes, prev, pager, next"
          :page-sizes="[12, 24, 48, 96]"
          :total="pagination.total"
          :current-page="pagination.current"
          :page-size="pagination.size"
          @size-change="onSizeChange"
          @current-change="onCurrentChange"
        />
      </footer>
    </section>

    <!-- ── 批量操作浮动条 ─────────────────────────────────────────── -->
    <transition name="batch-bar">
      <div v-if="selectedIds.length" class="batch-bar">
        <span class="batch-icon"><ArtSvgIcon icon="ri:checkbox-multiple-line" /></span>
        <span class="batch-text"
          >已选 <b class="text-theme">{{ selectedIds.length }}</b> 项</span
        >
        <span class="batch-divider" />
        <ArtButtonTable
          v-perm="PermFileDownload"
          class="!mr-0"
          icon="ri:download-2-line"
          iconClass="bg-theme/12 text-theme"
          :title="batchDownloading ? '下载中…' : `下载（${selectedIds.length}）`"
          :disabled="batchDownloading"
          @click="handleDownload(selectedRows)"
        />
        <ArtButtonTable
          v-perm="PermFileDelete"
          class="!mr-0"
          icon="ri:delete-bin-5-line"
          iconClass="bg-danger/12 text-danger"
          :title="`删除（${selectedIds.length}）`"
          :disabled="batchDownloading"
          @click="handleDelete(selectedRows)"
        />
        <ArtButtonTable
          class="!mr-0"
          icon="ri:close-line"
          iconClass="bg-g-300/55 text-g-700"
          title="清空选择"
          :disabled="batchDownloading"
          @click="clearSelection"
        />
      </div>
    </transition>

    <!-- ── 弹窗 ───────────────────────────────────────────────────── -->
    <UploadDialog ref="uploadDialogRef" @finished="onUploaded" />
    <PreviewDialog ref="previewDialogRef" />
  </div>
</template>

<script setup lang="ts">
  import { PermFileDelete, PermFileDownload, PermFileUpload } from '@/enums/permission'
  import { computed, h, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import { useDebounceFn, useStorage } from '@vueuse/core'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import { operationColumn } from '@/components/core/tables/operation-column'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import type { ColumnOption } from '@/types/component'
  import {
    deleteFiles,
    fetchFileContent,
    fetchFiles,
    fetchFileStats,
    fetchFileThumb,
    renameFile
  } from '../api'
  import {
    FILE_CATEGORY_LIST,
    formatBytes,
    formatRelativeTime,
    formatTime,
    fileMetaOf,
    hasPermission,
    saveBlob
  } from '../file-meta'
  import UploadDialog from './upload-dialog.vue'
  import PreviewDialog from './preview-dialog.vue'

  defineOptions({ name: 'SystemFile' })

  // ── 基础状态 ─────────────────────────────────────────────────────────
  const loading = ref(false)
  const statsLoading = ref(false)
  const files = ref<Api.File.FileItem[]>([])
  const stats = ref<Api.File.FileStats>()
  const keyword = ref('')
  const activeCategory = ref<Api.File.FileCategory | ''>('')
  const selectedIds = ref<string[]>([])
  const batchDownloading = ref(false)
  const viewMode = useStorage<'grid' | 'list'>('system-file:viewMode', 'grid')
  const pagination = reactive({ current: 1, size: 24, total: 0 })

  const uploadDialogRef = ref<InstanceType<typeof UploadDialog>>()
  const previewDialogRef = ref<InstanceType<typeof PreviewDialog>>()
  const tableRef = ref<InstanceType<typeof ArtTable>>()

  // ── 排序 ────────────────────────────────────────────────────────────
  const sortOptions = [
    { label: '最近上传', by: 'createdAt', order: 'desc' },
    { label: '最早上传', by: 'createdAt', order: 'asc' },
    { label: '名称 A → Z', by: 'name', order: 'asc' },
    { label: '名称 Z → A', by: 'name', order: 'desc' },
    { label: '大小从大到小', by: 'size', order: 'desc' },
    { label: '大小从小到大', by: 'size', order: 'asc' }
  ] as const

  interface SortState {
    index: number
    label: string
    by: Exclude<Api.File.FileQuery['sortBy'], undefined>
    order: Exclude<Api.File.FileQuery['sortOrder'], undefined>
  }

  const sort = reactive<SortState>({
    index: 0,
    label: sortOptions[0].label,
    by: 'createdAt',
    order: 'desc'
  })

  function handleSortChange(index: number) {
    if (index < 0 || index >= sortOptions.length) return
    sort.index = index
    sort.label = sortOptions[index].label
    sort.by = sortOptions[index].by
    sort.order = sortOptions[index].order
    pagination.current = 1
    loadFiles()
  }

  function setSortBySize() {
    const idx = sortOptions.findIndex((o) => o.by === 'size' && o.order === 'desc')
    if (idx >= 0) handleSortChange(idx)
  }

  // ── 数据加载 ────────────────────────────────────────────────────────
  const fileQuery = computed<Api.File.FileQuery>(() => ({
    page: pagination.current,
    pageSize: pagination.size,
    ...(keyword.value.trim() ? { keyword: keyword.value.trim() } : {}),
    ...(activeCategory.value ? { category: activeCategory.value } : {}),
    sortBy: sort.by,
    sortOrder: sort.order
  }))

  async function loadFiles() {
    loading.value = true
    try {
      const res = await fetchFiles(fileQuery.value)
      files.value = res.list
      pagination.total = res.total
      syncTableSelection()
    } finally {
      loading.value = false
    }
  }

  async function loadStats() {
    statsLoading.value = true
    try {
      stats.value = await fetchFileStats()
    } catch {
      // 统计失败不阻塞列表:错误已由 http 层提示
    } finally {
      statsLoading.value = false
    }
  }

  function refreshAll() {
    loadFiles()
    loadStats()
  }

  const onKeywordInput = useDebounceFn(() => {
    pagination.current = 1
    loadFiles()
  }, 350)

  function switchCategory(category: Api.File.FileCategory | '') {
    if (activeCategory.value === category) return
    activeCategory.value = category
    pagination.current = 1
    // 切换分类是「结果集整体切换」:先清空列表让下方加载状态接管,否则上一个
    // 分类的旧卡片会在请求期间残留一闪、再被新结果(或空态)替换。
    files.value = []
    loadFiles()
  }

  function resetFilters() {
    keyword.value = ''
    activeCategory.value = ''
    pagination.current = 1
    files.value = []
    loadFiles()
  }

  const isFiltering = computed(() => !!keyword.value.trim() || !!activeCategory.value)

  // 下方内容区加载状态:仅在「加载中且尚无内容」时展示(首次加载 / 切换分类清空后)。
  // 刷新(已有内容)时保留旧内容原地替换,由顶部刷新按钮旋转提示,避免整区闪烁。
  const bodyLoading = computed(() => loading.value && files.value.length === 0)

  // ── 展示数据(预计算元数据,避免模板重复推导) ────────────────────────
  const displayFiles = computed(() => files.value.map((file) => ({ file, meta: fileMetaOf(file) })))

  // ── 图片缩略图(服务端 256px JPEG,前端 Blob 缓存 + 惰性加载) ───────
  // 仅对 image 分类按需请求;失败静默回退分类图标。容量裁剪会释放
  // 已不可见文件的 Blob URL,页面卸载时全部释放。
  type ThumbStatus = 'loading' | 'ok' | 'error'
  interface ThumbState {
    status: ThumbStatus
    url: string
  }
  const THUMB_CAP = 160
  const THUMB_FALLBACK: ThumbState = { status: 'loading', url: '' }
  const thumbStates = reactive(new Map<string, ThumbState>())
  const thumbOrder: string[] = []

  function thumbOf(file: Api.File.FileItem): ThumbState {
    return thumbStates.get(file.id) ?? THUMB_FALLBACK
  }

  function trimThumbs() {
    if (thumbStates.size <= THUMB_CAP) return
    const visible = new Set(files.value.map((f) => f.id))
    const target = Math.floor((THUMB_CAP * 3) / 4)
    let guard = thumbOrder.length
    while (thumbStates.size > target && guard-- > 0) {
      const id = thumbOrder.shift()
      if (id == null) break
      const state = thumbStates.get(id)
      if (state && !visible.has(id)) {
        if (state.url) URL.revokeObjectURL(state.url)
        thumbStates.delete(id)
      } else {
        thumbOrder.push(id) // 仍可见:轮转到队尾,不淘汰
      }
    }
  }

  function loadThumbs(
    entries: { file: Api.File.FileItem; meta: { category: Api.File.FileCategory } }[]
  ) {
    for (const entry of entries) {
      if (entry.meta.category !== 'image') continue
      const id = entry.file.id
      if (thumbStates.has(id)) continue
      thumbStates.set(id, { status: 'loading', url: '' })
      thumbOrder.push(id)
      fetchFileThumb(id)
        .then((blob: Blob) => {
          const url = URL.createObjectURL(blob)
          const cur = thumbStates.get(id)
          if (cur && cur.status === 'loading') {
            cur.url = url
            cur.status = 'ok'
          } else {
            URL.revokeObjectURL(url)
          }
        })
        .catch(() => {
          const cur = thumbStates.get(id)
          if (cur && cur.status === 'loading') cur.status = 'error'
        })
    }
    trimThumbs()
  }

  watch(displayFiles, (entries) => loadThumbs(entries), { immediate: true })

  onBeforeUnmount(() => {
    for (const state of thumbStates.values()) {
      if (state.url) URL.revokeObjectURL(state.url)
    }
    thumbStates.clear()
  })

  // ── 选择逻辑 ────────────────────────────────────────────────────────
  function isSelected(item: Api.File.FileItem): boolean {
    return selectedIds.value.includes(item.id)
  }

  function toggleSelect(item: Api.File.FileItem, checked: boolean) {
    const set = new Set(selectedIds.value)
    if (checked) set.add(item.id)
    else set.delete(item.id)
    selectedIds.value = [...set]
  }

  function onSelectionChange(rows: Api.File.FileItem[]) {
    selectedIds.value = rows.map((r) => r.id)
  }

  function clearSelection() {
    selectedIds.value = []
    tableRef.value?.elTableRef?.clearSelection()
  }

  /** 列表视图挂载/数据刷新后,把跨视图保持的选择回灌到 ElTable */
  async function syncTableSelection() {
    if (viewMode.value !== 'list') return
    await nextTick()
    const el = tableRef.value?.elTableRef
    if (!el) return
    el.clearSelection()
    const idSet = new Set(selectedIds.value)
    for (const row of files.value) {
      if (idSet.has(row.id)) el.toggleRowSelection(row, true)
    }
  }

  watch(viewMode, (mode) => {
    if (mode === 'list') nextTick(syncTableSelection)
  })

  const selectedRows = computed(() => files.value.filter((f) => selectedIds.value.includes(f.id)))

  // ── 统计卡 ──────────────────────────────────────────────────────────
  interface StatCard {
    key: string
    label: string
    icon: string
    tile: string
    count?: number
    text?: string
    sub: string
    onClick: () => void
  }

  const statCards = computed<StatCard[]>(() => {
    const s = stats.value
    const total = s?.total ?? 0
    const imageCount = s?.categoryCounts?.image ?? 0
    return [
      {
        key: 'total',
        label: '文件总数',
        icon: 'ri:file-copy-2-line',
        tile: 'bg-theme/12 text-theme',
        count: total,
        sub: '已登记的全部文件资源',
        onClick: () => switchCategory('')
      },
      {
        key: 'size',
        label: '占用空间',
        icon: 'ri:hard-drive-3-line',
        tile: 'bg-secondary/12 text-secondary',
        text: formatBytes(s?.totalSize),
        sub: '本地存储占用',
        onClick: () => setSortBySize()
      },
      {
        key: 'image',
        label: '图片资源',
        icon: 'ri:image-2-line',
        tile: 'bg-success/12 text-success',
        count: imageCount,
        sub: total > 0 ? `占全部文件的 ${Math.round((imageCount / total) * 100)}%` : '暂无图片',
        onClick: () => switchCategory('image')
      },
      {
        key: 'week',
        label: '近 7 天新增',
        icon: 'ri:calendar-2-line',
        tile: 'bg-warning/12 text-warning',
        count: s?.weekUploads ?? 0,
        sub: '最近一周上传记录',
        onClick: () => {
          switchCategory('')
          handleSortChange(0)
        }
      }
    ]
  })

  // ── 分类分布 ────────────────────────────────────────────────────────
  const categoryDistribution = computed(() => {
    const counts = stats.value?.categoryCounts ?? ({} as Record<Api.File.FileCategory, number>)
    const total = Math.max(stats.value?.total ?? 0, 1)
    return FILE_CATEGORY_LIST.map((meta) => {
      const count = counts[meta.key] ?? 0
      return { ...meta, count, pct: (count / total) * 100 }
    }).filter((d) => d.count > 0)
  })

  // ── 列表列配置 ──────────────────────────────────────────────────────
  const maxRowSize = computed(() => Math.max(...files.value.map((f) => f.size), 1))

  const columns = computed<ColumnOption<Api.File.FileItem>[]>(() => {
    const actions = (row: Api.File.FileItem) =>
      h('div', { class: 'flex items-center' }, [
        ...(hasPermission('system:file:download')
          ? [h(ArtButtonTable, { type: 'view', title: '预览', onClick: () => openPreview(row) })]
          : []),
        ...(hasPermission('system:file:download')
          ? [
              h(ArtButtonTable, {
                icon: 'ri:download-2-line',
                iconClass: 'bg-success/12 text-success',
                title: '下载',
                onClick: () => handleDownload(row)
              })
            ]
          : []),
        ...(hasPermission('system:file:edit')
          ? [h(ArtButtonTable, { type: 'edit', title: '重命名', onClick: () => openRename(row) })]
          : []),
        ...(hasPermission('system:file:delete')
          ? [
              h(ArtButtonTable, {
                type: 'delete',
                title: '删除',
                onClick: () => handleDelete([row])
              })
            ]
          : [])
      ])

    // 操作列：按可见按钮数量自动计算宽度；无可用操作时整列隐藏
    const operationColumnConfig = operationColumn<Api.File.FileItem>({
      count:
        (hasPermission('system:file:download') ? 2 : 0) +
        (hasPermission('system:file:edit') ? 1 : 0) +
        (hasPermission('system:file:delete') ? 1 : 0),
      formatter: actions
    })

    return [
      { type: 'selection', width: 48 },
      {
        prop: 'name',
        label: '名称',
        minWidth: 240,
        formatter: (row: Api.File.FileItem) => {
          const meta = fileMetaOf(row)
          const thumb = meta.category === 'image' ? thumbStates.get(row.id) : undefined
          const url = thumb?.status === 'ok' ? thumb.url : ''
          return h('div', { class: 'flex items-center gap-2.5 min-w-0' }, [
            url
              ? h('img', {
                  src: url,
                  class: 'h-8 w-8 shrink-0 rounded-lg object-cover',
                  alt: '',
                  loading: 'lazy'
                })
              : h(
                  'span',
                  {
                    class: `flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ${meta.tile}`
                  },
                  [h(ArtSvgIcon, { icon: meta.icon, class: `text-base ${meta.text}` })]
                ),
            h('span', { class: 'truncate', title: row.name }, row.name)
          ])
        }
      },
      {
        prop: 'size',
        label: '大小',
        width: 150,
        formatter: (row: Api.File.FileItem) =>
          h('div', { class: 'flex flex-col gap-1 py-1' }, [
            h('span', { class: 'text-[13px]' }, formatBytes(row.size)),
            h('span', { class: 'block h-1 w-16 overflow-hidden rounded-full bg-g-300/60' }, [
              h('span', {
                class: 'block h-full rounded-full bg-theme/70',
                style: { width: `${Math.max(4, Math.round((row.size / maxRowSize.value) * 100))}%` }
              })
            ])
          ])
      },
      {
        prop: 'category',
        label: '类型',
        width: 110,
        formatter: (row: Api.File.FileItem) => {
          const meta = fileMetaOf(row)
          return h(
            'span',
            {
              class: `inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs ${meta.tile} ${meta.text}`
            },
            [h(ArtSvgIcon, { icon: meta.icon, class: 'text-[13px]' }), meta.label]
          )
        }
      },
      {
        prop: 'mimeType',
        label: '存储',
        width: 110,
        formatter: (row: Api.File.FileItem) =>
          h(
            'span',
            { class: 'text-[13px] text-g-600' },
            row.storageType === 'local' ? '本地' : row.storageType
          )
      },
      {
        prop: 'createdAt',
        label: '上传时间',
        width: 150,
        formatter: (row: Api.File.FileItem) => formatTime(row.createdAt)
      },
      ...(operationColumnConfig ? [operationColumnConfig] : [])
    ]
  })

  // ── 操作 ────────────────────────────────────────────────────────────
  function openUpload() {
    uploadDialogRef.value?.open()
  }

  function onUploaded() {
    loadFiles()
    loadStats()
  }

  function openPreview(item: Api.File.FileItem) {
    previewDialogRef.value?.open(item, files.value)
  }

  async function openRename(item: Api.File.FileItem) {
    try {
      const { value } = await ElMessageBox.prompt('请输入新的文件名(不含路径分隔符)', '重命名', {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        inputValue: item.name,
        inputPattern: /\S/,
        inputErrorMessage: '文件名不能为空'
      })
      const name = value.trim()
      if (!name || name === item.name || name.includes('/') || name.includes('\\')) {
        ElMessage.warning('文件名不合法或未发生变化')
        return
      }
      await renameFile(item.id, name)
      ElMessage.success('重命名成功')
      loadFiles()
    } catch {
      // 用户取消
    }
  }

  async function handleDownload(target: Api.File.FileItem | Api.File.FileItem[]) {
    const items = Array.isArray(target) ? target : [target]
    if (!items.length) return
    const isBatch = items.length > 1
    if (isBatch) batchDownloading.value = true
    let succeeded = 0
    try {
      for (let i = 0; i < items.length; i++) {
        const item = items[i]
        try {
          const blob = await fetchFileContent(item.id, true)
          saveBlob(blob, item.name)
          succeeded++
        } catch {
          ElMessage.error(`「${item.name}」下载失败`)
        }
        if (isBatch) {
          ElMessage.info(`正在下载 ${i + 1}/${items.length}`)
        }
      }
    } finally {
      batchDownloading.value = false
    }
    clearSelection()
    if (isBatch) ElMessage.success(`已下载 ${succeeded} 个文件`)
  }

  async function handleDelete(target: Api.File.FileItem | Api.File.FileItem[]) {
    const items = Array.isArray(target) ? target : [target]
    if (!items.length) return
    const ids = items.map((i) => i.id)
    const label = items.length === 1 ? `「${items[0].name}」` : `选中的 ${items.length} 个文件`
    try {
      await ElMessageBox.confirm(`确认删除 ${label} 吗?删除后不可恢复。`, '删除确认', {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      })
    } catch {
      return
    }
    await deleteFiles(ids)
    ElMessage.success('已删除')
    selectedIds.value = selectedIds.value.filter((id) => !ids.includes(id))
    // 当前页删空后回退一页
    if (files.value.length === ids.length && pagination.current > 1) pagination.current -= 1
    refreshAll()
  }

  function onSizeChange(size: number) {
    pagination.size = size
    pagination.current = 1
    loadFiles()
  }

  function onCurrentChange(page: number) {
    pagination.current = page
    loadFiles()
  }

  // ── 初始化 ──────────────────────────────────────────────────────────
  refreshAll()
</script>

<style scoped>
  .file-page {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  /* ── 统计卡 ── */
  .stats-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 14px;
  }

  .stat-card {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 16px 18px;
    border: 1px solid var(--default-border);
    border-radius: 12px;
    background: var(--default-box-color);
    cursor: pointer;
    outline: none;
    transition:
      border-color 0.2s ease,
      transform 0.2s ease,
      box-shadow 0.2s ease;
  }

  .stat-card:hover,
  .stat-card:focus-visible {
    border-color: color-mix(in oklab, var(--theme-color) 45%, transparent);
    transform: translateY(-2px);
  }

  .stat-icon {
    display: flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 46px;
    height: 46px;
    border-radius: 11px;
  }

  .stat-body {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .stat-label {
    font-size: 13px;
    color: var(--art-gray-500);
  }

  .stat-value {
    margin-top: 1px;
    font-size: 21px;
    font-weight: 600;
    line-height: 1.35;
    color: var(--art-gray-900);
  }

  .stat-sub {
    margin-top: 1px;
    font-size: 12px;
    color: var(--art-gray-500);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .stat-card.is-loading .stat-value,
  .stat-card.is-loading .stat-sub {
    opacity: 0.45;
  }

  /* ── 分类分布条 ── */
  .dist-panel {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 10px 16px;
    border: 1px solid var(--default-border);
    border-radius: 12px;
    background: var(--default-box-color);
  }

  .dist-bar {
    display: flex;
    flex: 1;
    gap: 2px;
    height: 8px;
    border-radius: 999px;
    overflow: hidden;
    background: var(--art-gray-200);
  }

  .dist-seg {
    display: block;
    height: 100%;
    min-width: 4px;
    transition: width 0.3s ease;
  }

  .dist-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
  }

  .legend-item {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 6px;
    border: none;
    border-radius: 6px;
    background: none;
    cursor: pointer;
    transition: background-color 0.15s ease;
  }

  .legend-item:hover {
    background: var(--art-hover-color);
  }

  .legend-dot {
    width: 8px;
    height: 8px;
    border-radius: 2px;
  }

  .legend-label {
    font-size: 12px;
    color: var(--art-gray-600);
  }

  .legend-count {
    font-size: 12px;
    font-weight: 500;
    color: var(--art-gray-800);
  }

  /* ── 主面板 ── */
  .main-panel {
    border: 1px solid var(--default-border);
    border-radius: 12px;
    background: var(--default-box-color);
    overflow: hidden;
  }

  .panel-toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--default-border);
  }

  .chips-scroll {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }

  .chips-scroll::-webkit-scrollbar {
    display: none;
  }

  .chip {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 11px;
    border: 1px solid transparent;
    border-radius: 999px;
    background: transparent;
    font-size: 13px;
    color: var(--art-gray-700);
    cursor: pointer;
    transition:
      background-color 0.15s ease,
      color 0.15s ease,
      border-color 0.15s ease;
  }

  .chip:hover {
    background: var(--art-hover-color);
  }

  .chip.is-active {
    background: var(--theme-color);
    border-color: var(--theme-color);
    color: #fff;
  }

  .chip-count {
    padding: 0 6px;
    min-width: 18px;
    height: 16px;
    line-height: 16px;
    border-radius: 999px;
    background: var(--art-gray-300);
    font-size: 11px;
    text-align: center;
    color: var(--art-gray-600);
  }

  .chip.is-active .chip-count {
    background: rgba(255, 255, 255, 0.24);
    color: #fff;
  }

  .tool-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .tool-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 32px;
    padding: 0 9px;
    border: 1px solid var(--default-border);
    border-radius: 8px;
    background: var(--default-box-color);
    font-size: 13px;
    color: var(--art-gray-700);
    cursor: pointer;
    transition:
      background-color 0.15s ease,
      color 0.15s ease;
  }

  .tool-btn:hover {
    background: var(--art-hover-color);
  }

  .tool-actions :deep(.search-input) {
    width: 208px;
  }

  .tool-actions :deep(.search-input .el-input__wrapper) {
    border-radius: 8px;
  }

  .tool-btn.is-loading :deep(.art-svg-icon) {
    animation: spin 0.8s linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  .view-toggle {
    display: inline-flex;
    padding: 2px;
    border: 1px solid var(--default-border);
    border-radius: 8px;
    background: var(--art-gray-100);
  }

  .view-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 26px;
    border: none;
    border-radius: 6px;
    background: transparent;
    font-size: 15px;
    color: var(--art-gray-500);
    cursor: pointer;
    transition:
      background-color 0.15s ease,
      color 0.15s ease;
  }

  .view-btn.is-active {
    background: var(--default-box-color);
    color: var(--theme-color);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.06);
  }

  .panel-body {
    padding: 14px;
    min-height: 320px;
  }

  /* ── 网格视图 ── */
  .file-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(186px, 1fr));
    gap: 12px;
  }

  .file-card {
    position: relative;
    display: flex;
    flex-direction: column;
    border: 1px solid var(--default-border);
    border-radius: 12px;
    background: var(--default-box-color);
    overflow: hidden;
    animation: card-in 0.35s ease both;
    transition:
      border-color 0.18s ease,
      transform 0.18s ease,
      box-shadow 0.18s ease,
      background-color 0.18s ease;
  }

  .file-card:hover {
    border-color: color-mix(in oklab, var(--theme-color) 40%, var(--default-border));
    transform: translateY(-2px);
    box-shadow: 0 6px 18px rgba(15, 23, 42, 0.07);
  }

  .file-card.is-selected {
    border-color: var(--theme-color);
    background: color-mix(in oklab, var(--theme-color) 4%, var(--default-box-color));
  }

  @keyframes card-in {
    from {
      opacity: 0;
      transform: translateY(10px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }

  .card-check {
    position: absolute;
    top: 8px;
    left: 8px;
    z-index: 2;
    margin: 0;
    opacity: 0;
    transition: opacity 0.15s ease;
  }

  .file-card:hover .card-check,
  .file-card.is-selected .card-check {
    opacity: 1;
  }

  .card-actions {
    position: absolute;
    top: 8px;
    right: 8px;
    z-index: 2;
    display: flex;
    gap: 4px;
    opacity: 0;
    transform: translateY(-2px);
    transition:
      opacity 0.15s ease,
      transform 0.15s ease;
  }

  .file-card:hover .card-actions {
    opacity: 1;
    transform: translateY(0);
  }

  .card-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border: none;
    border-radius: 7px;
    background: var(--default-box-color);
    box-shadow: 0 1px 5px rgba(15, 23, 42, 0.12);
    font-size: 14px;
    color: var(--art-gray-700);
    cursor: pointer;
    transition:
      background-color 0.15s ease,
      color 0.15s ease;
  }

  .card-btn:hover {
    background: var(--art-hover-color);
    color: var(--theme-color);
  }

  .card-btn.is-danger:hover {
    background: var(--el-color-error-light-9);
    color: var(--el-color-error);
  }

  .card-main {
    display: flex;
    flex: 1;
    flex-direction: column;
    padding: 0;
    border: none;
    background: none;
    text-align: left;
    cursor: pointer;
  }

  .card-tile {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    height: 92px;
    border-bottom: 1px solid var(--default-border);
  }

  .tile-icon {
    font-size: 38px;
  }

  .card-thumb {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: cover;
    border-bottom: 1px solid var(--default-border);
    animation: thumb-in 0.3s ease both;
  }

  @keyframes thumb-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  .card-name {
    margin: 10px 12px 0;
    font-size: 13px;
    font-weight: 500;
    line-height: 20px;
    color: var(--art-gray-900);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .card-meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin: 6px 12px 12px;
    min-width: 0;
    font-size: 12px;
    color: var(--art-gray-500);
  }

  .card-size {
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .card-time {
    flex: none;
    white-space: nowrap;
  }

  /* ── 加载状态 ── */
  .panel-loading {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    min-height: 320px;
    gap: 12px;
  }

  .loading-spin {
    font-size: 30px;
    color: var(--theme-color);
    animation: spin 0.8s linear infinite;
  }

  .loading-text {
    font-size: 13px;
    color: var(--art-gray-500);
  }

  /* ── 空状态 ── */
  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 52px 16px;
    text-align: center;
  }

  .empty-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 84px;
    height: 84px;
    border-radius: 22px;
  }

  .empty-title {
    margin: 16px 0 0;
    font-size: 15px;
    font-weight: 500;
    color: var(--art-gray-800);
  }

  .empty-sub {
    margin: 6px 0 16px;
    font-size: 13px;
    color: var(--art-gray-500);
  }

  /* ── 分页区 ── */
  .panel-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 16px;
    border-top: 1px solid var(--default-border);
  }

  .footer-hint {
    font-size: 12px;
    color: var(--art-gray-500);
  }

  /* ── 批量操作浮动条 ── */
  .batch-bar {
    position: fixed;
    bottom: 24px;
    left: 50%;
    z-index: 1200;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px 8px 14px;
    border: 1px solid var(--default-border);
    border-radius: 999px;
    background: var(--default-box-color);
    box-shadow: 0 10px 32px rgba(15, 23, 42, 0.16);
    transform: translateX(-50%);
  }

  .batch-icon {
    display: flex;
    align-items: center;
    font-size: 17px;
    color: var(--theme-color);
  }

  .batch-text {
    font-size: 13px;
    color: var(--art-gray-700);
  }

  .batch-divider {
    width: 1px;
    height: 16px;
    background: var(--default-border);
  }

  .batch-bar-enter-active,
  .batch-bar-leave-active {
    transition:
      opacity 0.2s ease,
      transform 0.2s ease;
  }

  .batch-bar-enter-from,
  .batch-bar-leave-to {
    opacity: 0;
    transform: translate(-50%, 12px);
  }

  /* ── 响应式 ── */
  @media (max-width: 1280px) {
    .stats-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }

  @media (max-width: 768px) {
    .stats-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 10px;
    }

    .stat-card {
      padding: 12px;
      gap: 10px;
    }

    .stat-icon {
      width: 38px;
      height: 38px;
    }

    .dist-panel {
      flex-direction: column;
      align-items: stretch;
      gap: 10px;
    }

    .dist-legend {
      justify-content: center;
    }

    .panel-toolbar {
      padding: 10px 12px;
    }

    .tool-actions {
      width: 100%;
      justify-content: space-between;
    }

    .tool-actions :deep(.el-input) {
      flex: 1;
      min-width: 0;
      max-width: none;
    }
  }

  @media (max-width: 560px) {
    .stats-grid {
      grid-template-columns: 1fr;
    }

    .file-grid {
      grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
      gap: 8px;
    }

    .panel-body {
      padding: 10px;
    }

    .panel-footer {
      flex-direction: column;
      align-items: flex-start;
    }
  }

  @media (pointer: coarse) {
    .card-check,
    .card-actions {
      opacity: 1;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .file-card,
    .card-thumb {
      animation: none;
    }

    .stat-card,
    .file-card {
      transition: none;
    }
  }
</style>
