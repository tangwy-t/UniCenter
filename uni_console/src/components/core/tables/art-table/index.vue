<!-- 表格组件 -->
<!-- 支持：el-table 全部属性、事件、插槽，同官方文档写法 -->
<!-- 扩展功能：分页组件、渲染自定义列、loading、表格全局边框、斑马纹、表格尺寸、表头背景配置 -->
<!-- 获取 ref：默认暴露了 elTableRef 外部通过 ref.value.elTableRef 可以调用 el-table 方法 -->
<template>
  <div class="art-table" :class="{ 'is-empty': isEmpty }" :style="containerHeight">
    <ElTable ref="elTableRef" v-loading="!!loading" v-bind="mergedTableProps">
      <template v-for="col in renderedColumns" :key="col.prop || col.type">
        <!-- 渲染全局序号列 -->
        <ElTableColumn v-if="col.type === 'globalIndex'" v-bind="cleanColumnProps(col)">
          <template #default="{ $index }">
            <span>{{ getGlobalIndex($index) }}</span>
          </template>
        </ElTableColumn>

        <!-- 渲染展开行 -->
        <ElTableColumn v-else-if="col.type === 'expand'" v-bind="cleanColumnProps(col)">
          <template #default="{ row }">
            <component :is="col.formatter ? col.formatter(row) : null" />
          </template>
        </ElTableColumn>

        <!-- 渲染普通列 -->
        <ElTableColumn v-else v-bind="cleanColumnProps(col)">
          <template v-if="col.useHeaderSlot && col.prop" #header="headerScope">
            <slot
              :name="col.headerSlotName || `${col.prop}-header`"
              v-bind="{ ...headerScope, prop: col.prop, label: col.label }"
            >
              {{ col.label }}
            </slot>
          </template>
          <template v-if="col.useSlot && col.prop" #default="slotScope">
            <slot
              v-if="shouldRenderSlotScope(slotScope)"
              :name="col.slotName || col.prop"
              v-bind="{
                ...slotScope,
                prop: col.prop,
                value: col.prop ? slotScope.row[col.prop] : undefined
              }"
            />
          </template>
        </ElTableColumn>
      </template>

      <template v-if="$slots.default" #default><slot /></template>

      <template #empty>
        <div v-if="loading"></div>
        <ElEmpty v-else :description="emptyText" :image-size="120" />
      </template>
    </ElTable>

    <div
      class="pagination custom-pagination"
      v-if="showPagination"
      :class="mergedPaginationOptions?.align"
      ref="paginationRef"
    >
      <ElPagination
        v-bind="mergedPaginationOptions"
        :total="pagination?.total"
        :disabled="loading"
        :page-size="pagination?.size"
        :current-page="pagination?.current"
        @size-change="handleSizeChange"
        @current-change="handleCurrentChange"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
  import { ref, computed, nextTick, watch, watchEffect, getCurrentInstance, useAttrs } from 'vue'
  import type { ElTable, TableProps } from 'element-plus'
  import { storeToRefs } from 'pinia'
  import { ColumnOption } from '@/types'
  import { useTableStore } from '@/store/modules/table'
  import { useCommon } from '@/hooks/core/useCommon'
  import { useTableHeight } from '@/hooks/core/useTableHeight'
  import { useAppBreakpoints } from '@/hooks/core/useAppBreakpoints'
  import { useResizeObserver } from '@vueuse/core'
  import {
    filterColumnsForViewport,
    warnInvalidHideBelow,
    warnNonPrefixFixedLeft
  } from '../responsive-columns'

  defineOptions({ name: 'ArtTable' })

  const elTableRef = ref<InstanceType<typeof ElTable> | null>(null)
  const paginationRef = ref<HTMLElement>()
  const tableHeaderRef = ref<HTMLElement>()
  const tableStore = useTableStore()
  const { isBorder, isZebra, tableSize, isFullScreen, isHeaderBackground } = storeToRefs(tableStore)

  /** 分页配置接口 */
  interface PaginationConfig {
    /** 当前页码 */
    current: number
    /** 每页显示条目个数 */
    size: number
    /** 总条目数 */
    total: number
  }

  /** 分页器配置选项接口 */
  interface PaginationOptions {
    /** 每页显示个数选择器的选项列表 */
    pageSizes?: number[]
    /** 分页器的对齐方式 */
    align?: 'left' | 'center' | 'right'
    /** 分页器的布局 */
    layout?: string
    /** 是否显示分页器背景 */
    background?: boolean
    /** 只有一页时是否隐藏分页器 */
    hideOnSinglePage?: boolean
    /** 分页器的大小 */
    size?: 'small' | 'default' | 'large'
    /** 分页器的页码数量 */
    pagerCount?: number
  }

  /** ArtTable 组件的 Props 接口 */
  interface ArtTableProps extends TableProps<Record<string, any>> {
    /** 加载状态 */
    loading?: boolean
    /** 列渲染配置 */
    columns?: ColumnOption[]
    /** 分页状态 */
    pagination?: PaginationConfig
    /** 分页配置 */
    paginationOptions?: PaginationOptions
    /** 空数据表格高度 */
    emptyHeight?: string
    /**
     * 表格最大高度（超限表体内滚）。默认安全阀见 DEFAULT_MAX_HEIGHT；
     * 显式传入即覆盖默认（页面自定上限的逃生门）。
     */
    maxHeight?: string | number
    /**
     * 表格高度（定高语义）。显式传入即由页面接管高度口径：
     * 默认安全阀不再生效 —— 需要「填满容器」旧口径的页面传 height="100%"。
     */
    height?: string | number
    /** 空数据时显示的文本 */
    emptyText?: string
    /** 是否开启 ArtTableHeader，解决表格高度自适应问题 */
    showTableHeader?: boolean
  }

  /**
   * 数据表的默认最大高度（全站表格统一的安全阀，报裁 2026-10「表格必须有高度
   * 上限」）：表体超限内滚、页面永不被清单行数无限撑高；未超限的短表按内容
   * 高度渲染，分毫不受影响。取值口径（1600×900 实测量校准）：
   *
   * - `calc(100vh - 420px)`：把页面上下固定开销整个扣掉。实测 docker 列表页
   *   表根上方 328px（应用头 60 + 页内上留白 20 + hero 64 + 卡片头 44 + 卡片
   *   上缘 32 + 表格上边距 10 等）、下方 141px（底部合计 33 + 操作栏 46 + 卡片
   *   与页尾留白 62）。取 420：表满格时底部合计与操作栏**全部留在首屏**，页面
   *   仅余 ~43px（页尾留白的滚动余量）；开销更小的页面（无 hero / 无底栏）这个
   *   值只会更宽裕——多扣的余量仅让长表更早内滚，不会破相。
   * - `max(240px, …)` 下限：兜住矮视口（手机横屏）——不设下限时 100vh-420
   *   在 375px 高的横屏上只剩负值，表体整个塌掉。
   *
   * **短视口档（阀让位）**：视口高度 ≤ HEIGHT_BREAKPOINTS.short（640，手机横屏
   * 与矮桌面窗）时默认阀整体让位（见 maxHeight 的短视口分支）——表体高度改由
   * 页面的 flex 链决定（「沉浸」形态：页面零滚动、表体接管滚动）。矮桌面窗由此
   * 从 4 行（阀 100vh-420 的产物）回到容器自然高度（1280×620 实测 ~6 行）。
   * 为什么用让位而不是再调一个 `calc(100dvh - N)`：固定 N 在 390 高的屏上会把
   * 表压到 2-3 行（页面上方的 chrome 高度不是常数），让容器 flex 链说话才恒等于
   * 「剩余高度」。
   *
   * 逃生门（页面接管高度口径，安全阀自动让位）：显式 `height`（含恢复旧
   * 「填满容器」语义的 height="100%"）、显式 `max-height`（自定上限；短视口也
   * 照从 —— 它是页面主动要的上限，优先级高于默认档位）。
   */
  const DEFAULT_MAX_HEIGHT = 'max(240px, calc(100vh - 420px))'

  const props = withDefaults(defineProps<ArtTableProps>(), {
    columns: () => [],
    fit: true,
    showHeader: true,
    stripe: undefined,
    border: undefined,
    size: undefined,
    emptyHeight: '100%',
    emptyText: '暂无数据',
    showTableHeader: true
  })
  const instance = getCurrentInstance()
  const attrs = useAttrs()

  const LAYOUT = {
    MOBILE: 'prev, pager, next, sizes, jumper, total',
    IPAD: 'prev, pager, next, jumper, total',
    DESKTOP: 'total, prev, pager, next, sizes, jumper'
  }

  // 分页布局随断点表切换（阈值见 src/config/breakpoints.ts）
  const { smaller, greaterOrEqual, heightAtMost } = useAppBreakpoints()

  /**
   * 短视口（矮窗/手机横屏，阈值口径见 HEIGHT_BREAKPOINTS）：
   * 默认安全阀让位、表体高度交给容器 flex 链的档位开关（见 maxHeight）。
   */
  const isShortViewport = heightAtMost('short')

  const layout = computed(() => {
    if (smaller('tablet').value) {
      return LAYOUT.MOBILE
    } else if (smaller('desktop').value) {
      return LAYOUT.IPAD
    } else {
      return LAYOUT.DESKTOP
    }
  })

  /**
   * 实际渲染的列：在用户列显隐（columnChecks）之上，再按视口过滤 hideBelow 列。
   * smaller() 返回缓存过的 computed，可安全在 computed 内复用。
   */
  const renderedColumns = computed(() =>
    filterColumnsForViewport(props.columns, (name) => smaller(name).value)
  )

  // 开发期校验：
  // - hideBelow 拼写（无效值会让该列静默不隐藏）
  // - 左固定列必须是渲染列表的前缀（否则 EP 会把固定列整体提前，列顺序与声明不一致）
  watchEffect(() => {
    warnInvalidHideBelow(props.columns)
    warnNonPrefixFixedLeft(renderedColumns.value)
  })

  // 默认分页常量
  const DEFAULT_PAGINATION_OPTIONS: PaginationOptions = {
    pageSizes: [10, 20, 30, 50, 100],
    align: 'center',
    background: true,
    layout: layout.value,
    hideOnSinglePage: false,
    size: 'default',
    pagerCount: greaterOrEqual('xl').value ? 7 : 5
  }

  // 合并分页配置
  const mergedPaginationOptions = computed(() => ({
    ...DEFAULT_PAGINATION_OPTIONS,
    ...props.paginationOptions
  }))

  // 边框 (优先级：props > store)
  const border = computed(() => props.border ?? isBorder.value)
  // 斑马纹
  const stripe = computed(() => props.stripe ?? isZebra.value)
  // 表格尺寸
  const size = computed(() => props.size ?? tableSize.value)
  // 数据是否为空
  const isEmpty = computed(() => props.data?.length === 0)

  const paginationHeight = ref(0)
  const tableHeaderHeight = ref(0)

  // 使用 useResizeObserver 监听分页器高度变化
  useResizeObserver(paginationRef, (entries) => {
    const entry = entries[0]
    if (entry) {
      // 使用 requestAnimationFrame 避免 ResizeObserver loop 警告
      requestAnimationFrame(() => {
        paginationHeight.value = entry.contentRect.height
      })
    }
  })

  // 使用 useResizeObserver 监听表格头部高度变化
  useResizeObserver(tableHeaderRef, (entries) => {
    const entry = entries[0]
    if (entry) {
      // 使用 requestAnimationFrame 避免 ResizeObserver loop 警告
      requestAnimationFrame(() => {
        tableHeaderHeight.value = entry.contentRect.height
      })
    }
  })

  // 分页器与表格之间的间距常量（计算属性，响应 showTableHeader 变化）
  const PAGINATION_SPACING = computed(() => (props.showTableHeader ? 6 : 15))

  // 使用表格高度计算 Hook
  const { containerHeight } = useTableHeight({
    showTableHeader: computed(() => props.showTableHeader),
    paginationHeight,
    tableHeaderHeight,
    paginationSpacing: PAGINATION_SPACING
  })

  // 表格高度逻辑
  const height = computed(() => {
    // 全屏模式下占满全屏
    if (isFullScreen.value) return '100%'
    // 空数据且非加载状态时固定高度
    if (isEmpty.value && !props.loading) return props.emptyHeight
    // 使用传入的高度
    if (props.height) return props.height
    // 默认占满容器高度
    return '100%'
  })

  /**
   * 实际生效的 max-height：显式 max-height > 短视口让位 > 默认安全阀（见
   * DEFAULT_MAX_HEIGHT）。
   *
   * 谁不设上限（返回 undefined，回到「填满容器」旧口径）：
   * - 全屏态：容器就是视口，条目本来就该占满；
   * - 页面显式接管高度（传了 height）：高度口径归页面，默认阀不插手；
   * - 空/加载中（一行都没有）：没有可滚的行，旧口径照旧（此时是 emptyHeight /
   *   '100%' 的地盘，加了上限只会让空态塌缩）；
   * - 短视口（阈值见 HEIGHT_BREAKPOINTS.short）：矮视口里 100vh-420 这个固定
   *   扣法会把表压到 2-3 行（390 高横屏上 100vh-420 只剩个位数），默认阀整体
   *   让位、由页面 flex 链把「剩余高度」交给表 —— 这就是单滚动（页面零滚动、
   *   表体接管滚动）的档位开关。显式 max-height 是页面主动要的上限，仍优先于
   *   本档位（逃生门 > 档位）。
   */
  const maxHeight = computed(() => {
    if (isFullScreen.value) return undefined
    if (props.height) return undefined
    if (isEmpty.value) return undefined
    if (props.maxHeight != null) return props.maxHeight
    if (isShortViewport.value) return undefined
    return DEFAULT_MAX_HEIGHT
  })

  /**
   * 实际传给 ElTable 的 height。
   *
   * 为什么 max-height 生效时必须**撤掉** height（而不是两者都给）：Element Plus 的
   * table-body 滚动条样式按 props.height 优先取 `{ height: '100%' }`
   * （style-helper.scrollbarStyle 的两个分支互斥），有 height 时 max-height 只留在
   * 表根的 CSS 上、内滚条不生效 —— 表体会被裁掉且没有滚动条。两者是互斥的两种
   * 「表体定高」机制，这里显式二选一：有上限走 max-height（内容不足随内容收缩，
   * 超限内滚），无上限才回落到旧的填满语义。
   */
  const tableHeight = computed(() => {
    if (maxHeight.value != null) return undefined
    return height.value
  })

  /**
   * 清理另一侧的内联样式残留。
   *
   * ElTable 的 layout.setHeight/setMaxHeight 只在「有值」时写内联样式，prop 置空
   * 时不回收（源码：setHeight 里 null 直接返回）——模式切换后（全屏开关、空态与
   * 有数据互切）旧的那一侧会留下来作祟：例如全屏时残留的 max-height 会把占满
   * 视口的表又钉回 100vh-420。这里在每次模式求值后把「当前不该存在」的一侧删掉；
   * 当前生效的一侧保持 ElTable 自己写的值。
   */
  watch(
    [tableHeight, maxHeight],
    () => {
      nextTick(() => {
        const el = elTableRef.value?.$el as HTMLElement | undefined
        if (!el) return
        if (tableHeight.value == null) el.style.removeProperty('height')
        if (maxHeight.value == null) el.style.removeProperty('max-height')
      })
    },
    { flush: 'post' }
  )

  // 表头背景颜色样式
  const headerCellStyle = computed(() => ({
    background: isHeaderBackground.value
      ? 'var(--el-fill-color-lighter)'
      : 'var(--default-box-color)',
    ...(props.headerCellStyle || {}) // 合并用户传入的样式
  }))

  // 只有显式传入时才覆盖 ElTable 的原生默认值，避免继承的 Boolean props 把官方默认值冲掉。
  const hasExplicitTableProp = (propName: string): boolean => {
    const rawProps = (instance?.vnode.props || {}) as Record<string, unknown>
    const kebabName = propName.replace(/[A-Z]/g, (match) => `-${match.toLowerCase()}`)
    return propName in rawProps || kebabName in rawProps
  }

  const mergedTableProps = computed(() => ({
    ...attrs,
    ...props,
    height: tableHeight.value,
    maxHeight: maxHeight.value,
    stripe: stripe.value,
    border: border.value,
    size: size.value,
    headerCellStyle: headerCellStyle.value,
    // Element Plus 默认值为 true，未显式传入时不应被 ArtTable 覆盖成 false。
    selectOnIndeterminate: hasExplicitTableProp('selectOnIndeterminate')
      ? props.selectOnIndeterminate
      : undefined
  }))

  // 是否显示分页器
  const showPagination = computed(() => props.pagination && !isEmpty.value)

  // Element Plus 在部分场景会先用 $index = -1 进行预渲染。
  // 这对普通展示无影响，但会让 ElForm 错误注册出 lineList.-1.xxx 这类字段。
  const shouldRenderSlotScope = (slotScope: { $index?: number }) => {
    return slotScope.$index === undefined || slotScope.$index >= 0
  }

  // 清理列属性，移除插槽相关的自定义属性，确保它们不会被 ElTableColumn 错误解释
  const cleanColumnProps = (col: ColumnOption) => {
    const columnProps = { ...col }
    // 删除自定义的插槽控制属性
    delete columnProps.useHeaderSlot
    delete columnProps.headerSlotName
    delete columnProps.useSlot
    delete columnProps.slotName
    // 断点隐藏由 ArtTable 在渲染前过滤，不应作为未知属性透传到 ElTableColumn 的 DOM
    delete columnProps.hideBelow
    return columnProps
  }

  // 分页大小变化
  const handleSizeChange = (val: number) => {
    emit('pagination:size-change', val)
  }

  // 分页当前页变化
  const handleCurrentChange = (val: number) => {
    emit('pagination:current-change', val)
    scrollToTop() // 页码改变后滚动到表格顶部
  }

  const { scrollToTop: scrollPageToTop } = useCommon()

  // 滚动表格内容到顶部，并可以联动页面滚动到顶部
  const scrollToTop = () => {
    nextTick(() => {
      elTableRef.value?.setScrollTop(0) // 滚动 ElTable 内部滚动条到顶部
      scrollPageToTop() // 调用公共 composable 滚动页面到顶部
    })
  }

  // 全局序号
  const getGlobalIndex = (index: number) => {
    if (!props.pagination) return index + 1
    const { current, size } = props.pagination
    return (current - 1) * size + index + 1
  }

  const emit = defineEmits<{
    (e: 'pagination:size-change', val: number): void
    (e: 'pagination:current-change', val: number): void
  }>()

  // 查找并绑定表格头部元素 - 使用 VueUse 优化
  const findTableHeader = () => {
    if (!props.showTableHeader) {
      tableHeaderRef.value = undefined
      return
    }

    const tableHeader = document.getElementById('art-table-header')
    if (tableHeader) {
      tableHeaderRef.value = tableHeader
    } else {
      // 如果找不到表格头部，设置为 undefined，useElementSize 会返回 0
      tableHeaderRef.value = undefined
    }
  }

  watchEffect(
    () => {
      // 访问响应式数据以建立依赖追踪
      void props.data?.length // 追踪数据变化
      const shouldShow = props.showTableHeader

      // 只有在需要显示表格头部时才查找
      if (shouldShow) {
        nextTick(() => {
          findTableHeader()
        })
      } else {
        // 不显示时清空引用
        tableHeaderRef.value = undefined
      }
    },
    { flush: 'post' }
  )

  defineExpose({
    scrollToTop,
    elTableRef
  })
</script>

<style lang="scss" scoped>
  @use './style';
</style>
