<!-- 表格头部，包含表格大小、刷新、全屏、列设置、其他设置 -->
<template>
  <!-- 短视口（手机横屏/矮窗）不做 max-md 的竖排换行：740 宽横屏下换行会把这条
       头部从 32px 撑到 68px（左右两组各占一行），而矮视口最缺的就是高度 ——
       「计数行 + 工具组」本来就能在一行里放下（表宽让位后更宽裕）。
       换行只保留给真正的窄屏（宽度维度），由高度档位闸门控制。 -->
  <div class="flex-cb" :class="{ 'max-md:!block': !isShortViewport }" id="art-table-header">
    <div class="flex-wrap">
      <slot name="left"></slot>
    </div>

    <div class="flex-c md:justify-end max-sm:!hidden" :class="{ 'max-md:mt-3': !isShortViewport }">
      <!-- 搜索切换钮：面板展开或有筛选生效时都进激活态 —— 面板收起但筛选仍生效
           （如 ?host= 深链进入、或收起面板后筛选未清）时必须给得出线索，否则用户
           看到被过滤的表却找不到原因。小点（::after）是「有筛选生效」的纯视觉记号。 -->
      <div
        v-if="showSearchBar != null"
        role="button"
        tabindex="0"
        :aria-label="hasActiveFilter ? '搜索（有筛选生效）' : '搜索'"
        class="button search-button"
        :class="[
          showSearchBar || hasActiveFilter ? 'active !bg-theme hover:!bg-theme/80' : '',
          hasActiveFilter ? 'has-active-filter' : ''
        ]"
        @click="search"
        @keydown.enter.prevent="search"
        @keydown.space.prevent="search"
      >
        <ArtSvgIcon
          icon="ri:search-line"
          :class="showSearchBar || hasActiveFilter ? 'text-white' : 'text-g-700'"
        />
      </div>
      <div
        v-if="shouldShow('refresh')"
        role="button"
        tabindex="0"
        aria-label="刷新"
        class="button"
        @click="refresh"
        @keydown.enter.prevent="refresh"
        @keydown.space.prevent="refresh"
        :class="{ loading: loading && isManualRefresh }"
      >
        <ArtSvgIcon
          icon="ri:refresh-line"
          :class="loading && isManualRefresh ? 'animate-spin text-g-600' : ''"
        />
      </div>

      <ElDropdown v-if="shouldShow('size')" trigger="click" @command="handleTableSizeChange">
        <div role="button" tabindex="0" aria-label="表格密度" class="button">
          <ArtSvgIcon icon="ri:arrow-up-down-fill" />
        </div>
        <template #dropdown>
          <ElDropdownMenu>
            <div
              v-for="item in tableSizeOptions"
              :key="item.value"
              class="table-size-btn-item [&_.el-dropdown-menu__item]:!mb-[3px] last:[&_.el-dropdown-menu__item]:!mb-0"
            >
              <ElDropdownItem
                :key="item.value"
                :command="item.value"
                :class="tableSize === item.value ? '!bg-g-300/55' : ''"
              >
                {{ item.label }}
              </ElDropdownItem>
            </div>
          </ElDropdownMenu>
        </template>
      </ElDropdown>

      <div
        v-if="shouldShow('fullscreen')"
        role="button"
        tabindex="0"
        :aria-label="isFullScreen ? '退出全屏' : '全屏'"
        class="button"
        @click="toggleFullScreen"
        @keydown.enter.prevent="toggleFullScreen"
        @keydown.space.prevent="toggleFullScreen"
      >
        <ArtSvgIcon :icon="isFullScreen ? 'ri:fullscreen-exit-line' : 'ri:fullscreen-line'" />
      </div>

      <!-- 列设置 -->
      <ElPopover v-if="shouldShow('columns')" placement="bottom" trigger="click">
        <template #reference>
          <div role="button" tabindex="0" aria-label="列设置" class="button">
            <ArtSvgIcon icon="ri:align-right" />
          </div>
        </template>
        <div>
          <ElScrollbar max-height="380px">
            <VueDraggable
              v-model="columns"
              :disabled="false"
              filter=".fixed-column"
              :prevent-on-filter="false"
              @move="checkColumnMove"
            >
              <div
                v-for="item in columns"
                :key="item.prop || item.type"
                class="column-option flex-c"
                :class="{ 'fixed-column': item.fixed }"
              >
                <div
                  class="drag-icon mr-2 h-4.5 flex-cc text-g-500"
                  :class="item.fixed ? 'cursor-default text-g-300' : 'cursor-move'"
                >
                  <ArtSvgIcon
                    :icon="item.fixed ? 'ri:unpin-line' : 'ri:drag-move-2-fill'"
                    class="text-base"
                  />
                </div>
                <ElCheckbox
                  :model-value="getColumnVisibility(item)"
                  @update:model-value="(val) => updateColumnVisibility(item, val)"
                  :disabled="item.disabled"
                  class="flex-1 min-w-0 [&_.el-checkbox__label]:overflow-hidden [&_.el-checkbox__label]:text-ellipsis [&_.el-checkbox__label]:whitespace-nowrap"
                  >{{ item.label || (item.type === 'selection' ? '选择' : '')
                  }}{{ isViewportHidden(item) ? '（窄屏隐藏）' : '' }}</ElCheckbox
                >
              </div>
            </VueDraggable>
          </ElScrollbar>
        </div>
      </ElPopover>
      <!-- 其他设置 -->
      <ElPopover v-if="shouldShow('settings')" placement="bottom" trigger="click">
        <template #reference>
          <div role="button" tabindex="0" aria-label="表格设置" class="button">
            <ArtSvgIcon icon="ri:settings-line" />
          </div>
        </template>
        <div>
          <ElCheckbox v-if="showZebra" v-model="isZebra" :value="true">{{ '斑马纹' }}</ElCheckbox>
          <ElCheckbox v-if="showBorder" v-model="isBorder" :value="true">{{ '边框' }}</ElCheckbox>
          <ElCheckbox v-if="showHeaderBackground" v-model="isHeaderBackground" :value="true">{{
            '表头背景'
          }}</ElCheckbox>
        </div>
      </ElPopover>
      <slot name="right"></slot>
    </div>
  </div>
</template>

<script lang="ts" setup>
  import { computed, ref, onMounted, onUnmounted } from 'vue'
  import { storeToRefs } from 'pinia'
  import { TableSizeEnum } from '@/enums/formEnum'
  import { useTableStore } from '@/store/modules/table'
  import { useAppBreakpoints } from '@/hooks/core/useAppBreakpoints'
  import { isValidHideBelow } from '../responsive-columns'
  import { VueDraggable } from 'vue-draggable-plus'
  import type { ColumnOption } from '@/types/component'
  import { ElScrollbar } from 'element-plus'

  defineOptions({ name: 'ArtTableHeader' })

  interface Props {
    /** 斑马纹 */
    showZebra?: boolean
    /** 边框 */
    showBorder?: boolean
    /** 表头背景 */
    showHeaderBackground?: boolean
    /** 全屏 class */
    fullClass?: string
    /** 组件布局，子组件名用逗号分隔 */
    layout?: string
    /** 加载中 */
    loading?: boolean
    /** 搜索栏显示状态 */
    showSearchBar?: boolean
    /**
     * 是否有筛选生效（页面把已有的 hasFilter 传进来 —— 零新状态）。
     * 面板收起但筛选仍生效时，搜索钮据此保持激活态并挂「有筛选生效」小点。
     */
    hasActiveFilter?: boolean
  }

  const props = withDefaults(defineProps<Props>(), {
    showZebra: true,
    showBorder: true,
    showHeaderBackground: true,
    fullClass: 'art-page-view',
    layout: 'search,refresh,size,fullscreen,columns,settings',
    showSearchBar: undefined,
    hasActiveFilter: false
  })

  const columns = defineModel<ColumnOption[]>('columns', {
    required: false,
    default: () => []
  })

  const emit = defineEmits<{
    (e: 'refresh'): void
    (e: 'search'): void
    (e: 'update:showSearchBar', value: boolean): void
  }>()

  /**
   * 获取列的显示状态
   * 优先使用 visible 字段，如果不存在则使用 checked 字段
   */
  const getColumnVisibility = (col: ColumnOption): boolean => {
    if (col.visible !== undefined) {
      return col.visible
    }
    return col.checked ?? true
  }

  /**
   * 更新列的显示状态
   * 同时更新 checked 和 visible 字段以保持兼容性
   */
  const updateColumnVisibility = (col: ColumnOption, value: boolean | string | number): void => {
    const boolValue = !!value
    col.checked = boolValue
    col.visible = boolValue
  }

  /**
   * 列是否因当前视口低于 hideBelow 断点而暂不渲染
   * （用于在列设置里提示"勾选了但当前屏幕看不到"的原因）
   */
  const { smaller, heightAtMost } = useAppBreakpoints()
  /** 短视口（矮窗/手机横屏）：头部保持单行（不换行），阈值口径见 HEIGHT_BREAKPOINTS */
  const isShortViewport = heightAtMost('short')

  const isViewportHidden = (col: ColumnOption): boolean =>
    isValidHideBelow(col.hideBelow) && smaller(col.hideBelow).value

  /** 表格大小选项配置 */
  const tableSizeOptions = [
    { value: TableSizeEnum.SMALL, label: '紧凑' },
    { value: TableSizeEnum.DEFAULT, label: '默认' },
    { value: TableSizeEnum.LARGE, label: '宽松' }
  ]

  const tableStore = useTableStore()
  const { tableSize, isZebra, isBorder, isHeaderBackground } = storeToRefs(tableStore)

  /** 解析 layout 属性，转换为数组 */
  const layoutItems = computed(() => {
    return props.layout.split(',').map((item) => item.trim())
  })

  /**
   * 检查组件是否应该显示
   * @param componentName 组件名称
   * @returns 是否显示
   */
  const shouldShow = (componentName: string) => {
    return layoutItems.value.includes(componentName)
  }

  /**
   * 拖拽移动事件处理 - 防止固定列位置改变
   * @param evt move事件对象
   * @returns 是否允许移动
   */
  const checkColumnMove = (event: any) => {
    // 拖拽进入的目标 DOM 元素
    const toElement = event.related as HTMLElement
    // 如果目标位置是 fixed 列，则不允许移动
    if (toElement && toElement.classList.contains('fixed-column')) {
      return false
    }
    return true
  }

  /** 搜索事件处理 */
  const search = () => {
    // 切换搜索栏显示状态
    emit('update:showSearchBar', !props.showSearchBar)
    emit('search')
  }

  /** 刷新事件处理 */
  const refresh = () => {
    isManualRefresh.value = true
    emit('refresh')
  }

  /**
   * 表格大小变化处理
   * @param command 表格大小枚举值
   */
  const handleTableSizeChange = (command: TableSizeEnum) => {
    useTableStore().setTableSize(command)
  }

  /** 是否手动点击刷新 */
  const isManualRefresh = ref(false)

  /** 加载中 */
  const isFullScreen = ref(false)

  /** 保存原始的 overflow 样式，用于退出全屏时恢复 */
  const originalOverflow = ref('')

  /**
   * 切换全屏状态
   * 进入全屏时会隐藏页面滚动条，退出时恢复原状态
   */
  const toggleFullScreen = () => {
    const el = document.querySelector(`.${props.fullClass}`)
    if (!el) return

    isFullScreen.value = !isFullScreen.value

    if (isFullScreen.value) {
      // 进入全屏：保存原始样式并隐藏滚动条
      originalOverflow.value = document.body.style.overflow
      document.body.style.overflow = 'hidden'
      el.classList.add('el-full-screen')
      tableStore.setIsFullScreen(true)
    } else {
      // 退出全屏：恢复原始样式
      document.body.style.overflow = originalOverflow.value
      el.classList.remove('el-full-screen')
      tableStore.setIsFullScreen(false)
    }
  }

  /**
   * ESC键退出全屏的事件处理器
   * 需要保存引用以便在组件卸载时正确移除监听器
   */
  const handleEscapeKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && isFullScreen.value) {
      toggleFullScreen()
    }
  }

  /** 组件挂载时注册全局事件监听器 */
  onMounted(() => {
    document.addEventListener('keydown', handleEscapeKey)
  })

  /** 组件卸载时清理资源 */
  onUnmounted(() => {
    // 移除事件监听器
    document.removeEventListener('keydown', handleEscapeKey)

    // 如果组件在全屏状态下被卸载，恢复页面滚动状态
    if (isFullScreen.value) {
      document.body.style.overflow = originalOverflow.value
      const el = document.querySelector(`.${props.fullClass}`)
      if (el) {
        el.classList.remove('el-full-screen')
      }
    }
  })
</script>

<style scoped>
  @reference '@styles/core/tailwind.css';

  .button {
    @apply ml-2
    size-8
    flex
    items-center
    justify-center
    cursor-pointer
    rounded-md
    bg-g-300/55
    dark:bg-g-300/40
    text-g-700
    hover:bg-g-300
    md:ml-0
    md:mr-2.5;

    /* 键盘可见、鼠标不吵：与全站 focus 环同款（el-ui.scss 的 :focus-visible 批） */
    &:focus-visible {
      outline: 2px solid var(--el-color-primary);
      outline-offset: 2px;
    }
  }

  /* 搜索钮：定位上下文给「有筛选生效」的小点；点只在有筛选时出现。 */
  .search-button {
    position: relative;

    /* 有筛选生效（面板收起时最有用）：图标角一枚小点 —— 纯图标表达，不加解释句。
       环用卡片底色，落在灰底与主题底两种按钮底上都能分离出来。 */
    &.has-active-filter::after {
      content: '';
      position: absolute;
      top: 4px;
      right: 4px;
      width: 6px;
      height: 6px;
      border-radius: 50%;
      background: var(--el-color-danger);
      box-shadow: 0 0 0 1.5px var(--default-box-color);
    }
  }
</style>
