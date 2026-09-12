<template>
  <div class="art-icon-picker">
    <ElPopover
      v-model:visible="visible"
      :placement="placement"
      :width="pickerWidth"
      trigger="click"
      popper-class="art-icon-picker-popper"
      :show-arrow="false"
      :offset="6"
      @show="onOpen"
    >
      <template #reference>
        <div
          ref="triggerEl"
          class="picker-trigger"
          :class="{ 'is-empty': !modelValue }"
          @click="prepareLayout"
        >
          <ArtSvgIcon v-if="modelValue" :icon="modelValue" class="trigger-icon" />
          <span v-else class="placeholder">{{ placeholder }}</span>
          <ArtSvgIcon
            v-if="modelValue"
            icon="ri:close-circle-fill"
            class="clear-icon"
            @click.stop="clear"
          />
          <ArtSvgIcon v-else icon="ri:arrow-down-s-line" class="arrow-icon" />
        </div>
      </template>

      <div class="picker-body">
        <div class="picker-search">
          <ElInput
            v-model="keyword"
            placeholder="搜索图标，如 home / user / settings"
            clearable
            size="small"
          >
            <template #prefix>
              <ArtSvgIcon icon="ri:search-line" />
            </template>
          </ElInput>
        </div>

        <!-- 手动输入：可直接粘贴官网复制的 icon title（如 home-line、mdi:home）
             无需受限在预设图标清单内。 -->
        <div class="picker-manual">
          <div class="picker-manual-label">或手动输入 / 粘贴图标名</div>
          <div class="picker-manual-row">
            <ElInput
              v-model="manualIcon"
              placeholder="如 home-line / user-line / mdi:home"
              clearable
              size="small"
              @change="applyManual"
              @keyup.enter="applyManual"
            >
              <template #prefix>
                <ArtSvgIcon :icon="normalizedManual" class="manual-preview" />
              </template>
            </ElInput>
            <div class="manual-value">存为：{{ normalizedManual || '空' }}</div>
          </div>
        </div>

        <ElScrollbar :height="scrollHeight" class="picker-scroll">
          <div v-for="group in filteredGroups" :key="group.label" class="picker-group">
            <div class="picker-group-label">{{ group.label }}</div>
            <div class="picker-grid">
              <div
                v-for="name in group.icons"
                :key="name"
                class="picker-item"
                :class="{ 'is-active': modelValue === name }"
                :title="name"
                @click="select(name)"
              >
                <ArtSvgIcon :icon="name" />
              </div>
            </div>
          </div>
          <div v-if="!hasResult" class="picker-empty">未找到匹配图标</div>
        </ElScrollbar>
      </div>
    </ElPopover>
  </div>
</template>

<script setup lang="ts">
  import { computed, ref } from 'vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { MENU_ICON_GROUPS } from './icons'

  defineOptions({ name: 'ArtIconPicker', inheritAttrs: false })

  interface Props {
    modelValue?: string
    placeholder?: string
    /** 弹层宽度，默认 420px */
    width?: number
    /** 图标滚动区高度，默认 320px */
    height?: number
  }

  const props = withDefaults(defineProps<Props>(), {
    modelValue: '',
    placeholder: '点击选择图标',
    width: 420,
    height: 320
  })

  const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

  const visible = ref(false)
  const keyword = ref('')
  // 手动输入框内容（不参与 grid 过滤）。
  const manualIcon = ref('')
  const triggerEl = ref<HTMLElement>()
  // 弹层方向与图标滚动区高度，均在打开时按视口可用空间动态计算，
  // 确保弹层整体（含顶部输入区）始终完整显示在视口内。
  const placement = ref<'top-start' | 'bottom-start'>('bottom-start')
  const scrollHeight = ref(props.height)

  const pickerWidth = computed(() => props.width)
  // 弹层固定头部区高度近似值：搜索输入 + 手动输入区 + 内边距/间距。
  const HEADER_HEIGHT = 120
  const MIN_SCROLL = 120

  /**
   * 规范化手动输入的图标名：
   * - 含 ':' 视为完整 Iconify 名（如 mdi:home、ri:user-line），原样保留；
   * - 不含 ':' 视为 Remix 官网复制的裸标题（如 home-line、user-line），自动补 ri: 前缀。
   */
  const normalizedManual = computed(() => normalizeIcon(manualIcon.value))

  function normalizeIcon(value: string): string {
    const v = (value || '').trim()
    if (!v) return ''
    return v.includes(':') ? v : `ri:${v}`
  }

  function applyManual() {
    emit('update:modelValue', normalizedManual.value)
    visible.value = false
  }

  // 按关键词过滤；空关键词时保留全部分组。
  const filteredGroups = computed(() => {
    const kw = keyword.value.trim().toLowerCase()
    if (!kw) return MENU_ICON_GROUPS
    return MENU_ICON_GROUPS.map((g) => ({
      ...g,
      icons: g.icons.filter((i) => i.toLowerCase().includes(kw))
    })).filter((g) => g.icons.length > 0)
  })

  const hasResult = computed(() => filteredGroups.value.some((g) => g.icons.length > 0))

  function onOpen() {
    keyword.value = ''
    // 回显当前已保存值到手动输入框：ri: 前缀裁剪掉以便编辑裸标题，
    // 其它完整 Iconify 名（mdi:xxx）原样保留。
    const current = (props.modelValue || '').trim()
    manualIcon.value = current.startsWith('ri:') ? current.slice(3) : current
  }

  /**
   * 在触发点击时（弹层渲染前）同步计算展开方向与滚动区高度，并把响应式值设好。
   * 这样 ElPopover 首次渲染时就带上正确尺寸，避免先以默认高度(320)渲染、
   * 再由 onOpen 收缩导致的“高度闪一下”。
   */
  function prepareLayout() {
    const { place, scrollH } = computeLayout()
    placement.value = place
    scrollHeight.value = scrollH
  }

  function computeLayout(): { place: 'top-start' | 'bottom-start'; scrollH: number } {
    const el = triggerEl.value
    const viewH = window.innerHeight || document.documentElement.clientHeight
    const rect = el?.getBoundingClientRect()

    let below = viewH - 8
    let above = viewH - 8
    if (rect) {
      below = viewH - rect.bottom - 8
      above = rect.top - 8
    }

    // 上方空间更大或下方不足时向上展开，否则向下展开。
    const useTop = above >= below || below < HEADER_HEIGHT
    const available = (useTop ? above : below) - HEADER_HEIGHT
    const scrollH = Math.max(MIN_SCROLL, Math.min(props.height, available))

    return { place: useTop ? 'top-start' : 'bottom-start', scrollH }
  }

  function select(name: string) {
    emit('update:modelValue', name)
    manualIcon.value = name.replace(/^ri:/, '')
    visible.value = false
  }

  function clear() {
    emit('update:modelValue', '')
    manualIcon.value = ''
  }
</script>

<style lang="scss" scoped>
  .art-icon-picker {
    width: 100%;

    .picker-trigger {
      display: flex;
      align-items: center;
      gap: 8px;
      width: 100%;
      height: 32px;
      padding: 0 8px;
      border: 1px solid var(--el-border-color);
      border-radius: 4px;
      cursor: pointer;
      transition: border-color 0.2s ease;
      background: var(--el-input-bg-color, #fff);

      &:hover {
        border-color: var(--el-color-primary);
      }

      .trigger-icon,
      .clear-icon,
      .arrow-icon {
        font-size: 18px;
        color: var(--el-text-color-regular);
        flex-shrink: 0;
      }

      .placeholder {
        flex: 1;
        font-size: 14px;
        color: var(--el-text-color-placeholder);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }

      .clear-icon {
        display: none;
        color: var(--el-text-color-placeholder);
        cursor: pointer;

        &:hover {
          color: var(--el-color-danger);
        }
      }

      .arrow-icon {
        font-size: 14px;
        color: var(--el-text-color-secondary);
      }

      &.is-empty {
        .placeholder {
          display: block;
        }
      }

      &:not(.is-empty) {
        .clear-icon {
          display: inline-flex;
        }
        .arrow-icon {
          display: none;
        }
      }
    }
  }

  .picker-body {
    .picker-search {
      padding: 6px 6px 0;
    }

    .picker-manual {
      padding: 6px 6px 0;

      .picker-manual-label {
        font-size: 12px;
        color: var(--el-text-color-secondary);
        padding: 2px;
      }

      .picker-manual-row {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-top: 4px;

        .el-input {
          flex: 1;
        }

        .manual-preview {
          font-size: 16px;
          color: var(--el-text-color-regular);
        }

        .manual-value {
          flex-shrink: 0;
          font-size: 12px;
          color: var(--el-text-color-placeholder);
          white-space: nowrap;
          max-width: 160px;
          overflow: hidden;
          text-overflow: ellipsis;
        }
      }
    }

    .picker-scroll {
      padding: 0 6px 6px;
    }

    .picker-group {
      margin-top: 8px;

      .picker-group-label {
        font-size: 12px;
        color: var(--el-text-color-secondary);
        padding: 4px 2px;
      }

      .picker-grid {
        display: grid;
        grid-template-columns: repeat(6, 1fr);
        gap: 4px;
      }

      .picker-item {
        display: flex;
        align-items: center;
        justify-content: center;
        height: 44px;
        border-radius: 6px;
        cursor: pointer;
        transition:
          background-color 0.15s ease,
          color 0.15s ease;
        color: var(--el-text-color-regular);

        :deep(.art-svg-icon) {
          font-size: 22px;
        }

        &:hover {
          background: var(--el-fill-color-light);
          color: var(--el-color-primary);
        }

        &.is-active {
          background: var(--el-color-primary-light-9);
          color: var(--el-color-primary);
        }
      }
    }

    .picker-empty {
      padding: 32px 0;
      text-align: center;
      font-size: 13px;
      color: var(--el-text-color-secondary);
    }
  }
</style>

<style lang="scss">
  .art-icon-picker-popper {
    padding: 0 !important;
    border-radius: 8px !important;
  }
</style>
