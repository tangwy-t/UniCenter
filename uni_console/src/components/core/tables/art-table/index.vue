<!-- 表格组件 -->
<!-- 支持：el-table 全部属性、事件、插槽，同官方文档写法 -->
<!-- 扩展功能：分页组件、渲染自定义列、loading、表格全局边框、斑马纹、表格尺寸、表头背景配置 -->
<!-- 获取 ref：默认暴露了 elTableRef 外部通过 ref.value.elTableRef 可以调用 el-table 方法 -->
<template>
  <div
    ref="rootRef"
    class="art-table"
    :class="{ 'is-empty': isEmpty, 'is-x-overflow': hasXOverflow }"
    :style="containerHeight"
  >
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

      <!-- 空态：emptyText prop 控文案；#empty-bottom 插槽控制 ElEmpty 底部
           的动作区（如「清除筛选」按钮）—— ElEmpty 的 default 插槽就是底部
           内容位，转发它而不是整个 #empty（后者会连空图与描述一起接管，
           各页自绘一遍成本高且形态漂移）。 -->
      <template #empty>
        <div v-if="loading"></div>
        <ElEmpty v-else :description="emptyText" :image-size="120">
          <template v-if="$slots['empty-bottom']" #default>
            <slot name="empty-bottom" />
          </template>
        </ElEmpty>
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
  const rootRef = ref<HTMLElement>()
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
     * 表格最大高度（超限表体内滚）。默认不设上限（表随内容增高、页面滚动）；
     * 显式传入即由页面自定上限 —— 短视口/常规档一视同仁。
     */
    maxHeight?: string | number
    /**
     * 表格高度（定高语义）。显式传入即由页面接管高度口径，此时显式 max-height
     * 让位（EP 的 height / max-height 是互斥的两种表体定高机制，只能二选一）。
     */
    height?: string | number
    /** 空数据时显示的文本 */
    emptyText?: string
    /** 是否开启 ArtTableHeader，解决表格高度自适应问题 */
    showTableHeader?: boolean
  }

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
  const { smaller, greaterOrEqual } = useAppBreakpoints()

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

  /**
   * 横向溢出标记（窄屏可发现性修复）：列的 minWidth 之和超过视口时表体横向溢出
   * （1024 宽实测：可滚区 1612 vs 视口 711 —— 700 余 px 的列在视口之外），而
   * Element Plus 的横向滚动条默认只在**悬停**时才出现（bar 上是内联
   * `display: none`，实测 0×0），用户根本不知道右边还有列。这里在真的溢出时给
   * 表根挂上 is-x-overflow，由样式让横向滚动条常显；没有溢出就不挂 —— 普通表
   * 零回归（jsdom 里 scrollWidth/clientWidth 恒为 0，差值不大于 1，也不挂）。
   */
  const hasXOverflow = ref(false)

  /** 一帧后再量：ElTable 的列宽是在渲染后的 layout 阶段写上的。 */
  function scheduleXOverflowCheck(): void {
    const run = () => updateXOverflow()
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(run)
    else run()
  }

  function updateXOverflow(): void {
    const wrap = rootRef.value?.querySelector<HTMLElement>('.el-scrollbar__wrap')
    hasXOverflow.value = !!wrap && wrap.scrollWidth - wrap.clientWidth > 1
  }

  // 表根尺寸变化（断点切换 / 窗口缩放）与列集合、数据变化（列宽重算）都要重判。
  useResizeObserver(rootRef, () => scheduleXOverflowCheck())

  watch(
    [renderedColumns, () => props.data],
    () => {
      nextTick(() => scheduleXOverflowCheck())
    },
    { flush: 'post' }
  )

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
   * 实际生效的 max-height：仅显式传入的 max-height（默认不设上限）。
   *
   * 为什么默认无上限：表随内容增高、页面滚动（height 默认 100%，见上）。
   * 短视口档的「填充」形态是本默认口径 + 页面单滚动链（app.scss 的
   * art-single-scroll-chain）的产物，不依赖任何 JS 档位开关。
   *
   * 谁会让位（返回 undefined，走原生 height 口径）：
   * - 全屏态：容器就是视口，占满语义优先；
   * - 页面显式接管高度（传了 height）：两种定高机制互斥，height 优先；
   * - 空/加载中（一行都没有）：没有可滚的行，旧口径照旧（此时是 emptyHeight /
   *   '100%' 的地盘，加了上限只会让空态塌缩）。
   */
  const maxHeight = computed(() => {
    if (isFullScreen.value) return undefined
    if (props.height) return undefined
    if (isEmpty.value) return undefined
    return props.maxHeight ?? undefined
  })

  /**
   * 实际传给 ElTable 的 height。
   *
   * 为什么 max-height 生效时必须**撤掉** height（而不是两者都给）：Element Plus 的
   * table-body 滚动条样式按 props.height 优先取 `{ height: '100%' }`
   * （style-helper.scrollbarStyle 的两个分支互斥），有 height 时 max-height 只留在
   * 表根的 CSS 上、内滚条不生效 —— 表体会被裁掉且没有滚动条。两者是互斥的两种
   * 「表体定高」机制，这里显式二选一：显式 max-height 走 max-height（内容不足
   * 随内容收缩，超限内滚），否则走原生 height 口径（默认 100%）。
   */
  const tableHeight = computed(() => {
    if (maxHeight.value != null) return undefined
    return height.value
  })

  /**
   * 清理另一侧的内联样式残留。
   *
   * ElTable 的 layout.setHeight/setMaxHeight 只在「有值」时写内联样式，prop 置空
   * 时不回收（源码：setHeight 里 null 直接返回）——模式切换后（显式上限页开关
   * 全屏、空态与有数据互切）旧的那一侧会留下来作祟：例如全屏时残留的 max-height
   * 会把占满视口的表又钉回页面自定的上限。这里在每次模式求值后把「当前不该存在」
   * 的一侧删掉；当前生效的一侧保持 ElTable 自己写的值。
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

  /* ── 分页常驻表底（844×390 分页不可达的根因修复）─────────────────────────
     此前 `.art-table` 是普通块级流：ElTable 的高度由 JS 按「容器高 − 表头高 −
     分页高 − 间距」估出来，分页条排在 ElTable 之后、落在 `.art-table` 盒子**外面**
     （实测 844×390：`.art-table` 高 288，分页条在 484..512，而 `.art-table` 底
     471）。它之所以还能看见，全靠父级已经留出的那几十 px 余量；一旦估算与实际
     不符（分页高度首帧为 0、页面 flex 链把卡体压矮），分页条就落进
     `.el-card__body{overflow:hidden}`（app.scss）的裁剪区 —— 用户滚到底也够不到
     （实测停在 top=392 > vh=390，「每页条数/前往」一并不可达）。
     收成 flex 列后：ElTable 拿剩余高度并自滚，分页条 flex:none 常驻表底，
     两者永远在同一个盒子里，裁剪面再矮也先裁表体、不裁分页。 */
  .art-table {
    display: flex;
    flex-direction: column;

    // `flex: 1 1 auto`（不是 `1 1 0`）：卡片随内容收缩时 `.art-table` 没有确定
    // 高度（见 app.scss 的 .art-table-card），basis:0 会把表体压成 0；basis:auto
    // 在那种形态下用内联的 `height: 100%` 兜底（解析为 auto → 内容高），有确定
    // 高度时再按剩余空间分配并把超出的行收进表体自滚。
    .el-table {
      flex: 1 1 auto;
      min-height: 0;
    }

    .pagination {
      flex: none;
    }

    /* 横向溢出：把 Element Plus 的横向滚动条从「仅悬停可见」改成常显（判据与理由
       见脚本里的 hasXOverflow）。bar 上的 `display: none` 是 EP 用 v-show 写的
       内联样式，只有 !important 盖得住；thumb 同步提到悬停档的不透明度，否则
       0.3 的浅灰在窄屏上仍然读不出来。 */
    &.is-x-overflow {
      :deep(.el-scrollbar__bar.is-horizontal) {
        display: block !important;
      }

      :deep(.el-scrollbar__thumb) {
        opacity: var(--el-scrollbar-hover-opacity, 0.5);
      }
    }
  }
</style>
