/**
 * useTreeExpand：树形表格「受控展开 + 记忆折叠状态」组合式。
 *
 * 解决 Element Plus el-table 树形表格用 `default-expand-all` 时的问题：
 * 该属性只在树首次构建时生效一次，一旦 `data` 数组被整体替换（如编辑保存后
 * 重新拉取列表），树重建时又按 `default-expand-all` 把全部行重新展开，
 * 导致「改数据保存后树自动全展开」、丢失用户手动折叠的状态。
 *
 * 本组合式把展开状态改为受控（`expand-row-keys` + `@expand-change`），使其
 * 独立于数据刷新：
 * - 首次加载默认全展开（与历史行为一致，由 initExpanded 完成）；
 * - 之后随用户手动展开/折叠同步更新，并在数据刷新后保持不变；
 * - 提供 expanded 计算属性与 toggleExpandAll 一键全展开/全折叠。
 *
 * 用法（配合 ArtTable / el-table，row-key 必须是行的 id）：
 *   const data = ref<Api.System.Menu[]>([])
 *   const { expandedKeys, expanded, initExpanded, onExpandChange, toggleExpandAll } =
 *     useTreeExpand(data)
 *   // 模板：
 *   //   :expand-row-keys="expandedKeys"
 *   //   @expand-change="onExpandChange"
 *   //   :title="expanded ? '折叠' : '展开'"  @click="toggleExpandAll"
 *   // loadList 拉回 list 并 data.value = list 后调用 initExpanded()。
 */
import { ref, computed, type Ref } from 'vue'

/** 树形行节点：有 row-key（id）与可选的同构 children 即满足约束。 */
interface TreeRow {
  id: string
  children?: TreeRow[]
}

export function useTreeExpand<T extends TreeRow>(dataRef: Ref<T[]>) {
  /** 受控展开行 key 集合（对应 el-table 的 expand-row-keys，值为 row-key 即 id）。 */
  const expandedKeys = ref<string[]>([])
  /** 首次加载后是否已初始化过展开状态。 */
  const expandedInitialized = ref(false)

  /** 收集所有「有子节点」的行。 */
  function collectParentRows(list: TreeRow[], out: TreeRow[] = []): TreeRow[] {
    list.forEach((n) => {
      const children = n.children
      if (children?.length) {
        out.push(n)
        collectParentRows(children, out)
      }
    })
    return out
  }

  /** 是否已「全部展开」（供「展开/折叠」按钮标题与全量切换判断）。 */
  const expanded = computed(() => {
    const parentRows = collectParentRows(dataRef.value as TreeRow[])
    return parentRows.length > 0 && parentRows.every((n) => expandedKeys.value.includes(n.id))
  })

  /**
   * 首次加载后初始化展开状态：默认全展开（与历史行为一致）。
   * 仅生效一次；之后 loadList 重拉数据时不再调用，从而保留用户手动状态。
   * 调用时机：data.value = list 赋值之后。
   */
  function initExpanded() {
    if (expandedInitialized.value) return
    expandedKeys.value = collectParentRows(dataRef.value as TreeRow[]).map((n) => n.id)
    expandedInitialized.value = true
  }

  /**
   * el-table 受控展开事件回调：用户点击展开/折叠箭头时，把该行的展开态
   * 同步回 expandedKeys。
   * 注意：纯树形表格（未配置 type="expand" 展开列）的 expand-change 第二参数
   * 是该行「展开与否的布尔值」，而非展开行数组（element-plus ⩾2.x 实现，
   * 见其 table/src/store/tree.mjs 的 toggleTreeExpansion）。此前按数组处理
   * 会在每次点击箭头时抛 TypeError，导致受控 keys 从不更新、数据刷新后
   * 树被重新全部展开。
   */
  function onExpandChange(row: T, expanded: boolean) {
    const next = new Set(expandedKeys.value)
    if (expanded) next.add(row.id)
    else next.delete(row.id)
    expandedKeys.value = [...next]
  }

  /** 一键全展开/全折叠：已全部展开则收起到全折叠，否则展开所有父节点。 */
  function toggleExpandAll() {
    expandedKeys.value = expanded.value
      ? []
      : collectParentRows(dataRef.value as TreeRow[]).map((n) => n.id)
  }

  return {
    expandedKeys,
    expanded,
    initExpanded,
    onExpandChange,
    toggleExpandAll
  }
}
