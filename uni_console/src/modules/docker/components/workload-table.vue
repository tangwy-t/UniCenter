<template>
  <!-- 单根包装（single-root 守卫扫全模块 .vue）：下面三态互斥但源码层是兄弟节点。 -->
  <div class="wkl-table">
    <ArtTable
      v-if="showTable"
      :loading="loading"
      :data="rows"
      :columns="columns"
      :pagination="pagination"
      @selection-change="(rows: DockerWorkloadItem[]) => emit('selection-change', rows)"
      @pagination:size-change="(n: number) => emit('pagination:size-change', n)"
      @pagination:current-change="(n: number) => emit('pagination:current-change', n)"
      @sort-change="(p: SortChangePayload) => emit('sort-change', p)"
    />
    <ElEmpty v-else-if="hasFilter" class="wkl-empty" description="没有符合筛选条件的容器">
      <ElButton size="small" @click="emit('reset-filter')">清除筛选</ElButton>
    </ElEmpty>
    <ElEmpty v-else class="wkl-empty" description="所有主机上都还没有容器" />
  </div>
</template>

<script setup lang="ts">
  /**
   * 跨主机统一工作负载表：列渲染 + 三态（表 / 筛没了 / 还没容器）。
   *
   * 列集合**由页面持有**（useTableColumns + components/workload-columns.ts 的工厂，
   * 列设置钮要用同一份列定义）；本组件只负责把列渲染出来，并把「用户点了哪一行的
   * 哪个动作」emit 出去 —— 指令通道与确认档由页面（单行）与批量栏（多行）各自持有。
   * 导航（详情 / 日志）同理只 emit：8a 起它们是整页路由，跳转由页面做（表格不认路由）。
   *
   * 两种空态分开是 overview.vue 的既有纪律（把「没有」与「筛没了」说成一句会让人
   * 以为机器空了）；空态不写进 ArtTable 的 `#empty` 插槽 —— ArtTable 不转发该插槽
   * （内部把 ElTable 空态写死成「暂无数据」），写进去会被静默丢弃。
   */
  import { computed } from 'vue'
  import { ElButton, ElEmpty } from 'element-plus'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import type { ColumnOption } from '@/types/component'
  import type { DockerWorkloadItem } from '../api'

  defineOptions({ name: 'DockerWorkloadTable' })

  const props = withDefaults(
    defineProps<{
      rows: DockerWorkloadItem[]
      /**
       * 列集合（页面持有：列设置的勾选/拖序结果要与渲染的列同一份）。
       * 逐列的单行不变量与 formatter 见 components/workload-columns.ts。
       */
      columns: ColumnOption<DockerWorkloadItem>[]
      /** 表格加载遮罩（拉取在途）。 */
      loading?: boolean
      /** 筛选是否在途（有条件时空态给「清除筛选」）。 */
      hasFilter?: boolean
      /** 单行指令的行级 pending 键（useDockerCmds 的 pendingId）：命中行菜单禁用。 */
      pendingId?: string | null
      /** 批量在途：批量期间行菜单整体禁用（防与批量并发提交同一目标）。 */
      batchBusy?: boolean
      /** 服务端分页态（页面持有；透传给 ArtTable —— 不传则分页器永不出现）。 */
      pagination?: { current: number; size: number; total: number }
    }>(),
    {
      loading: false,
      hasFilter: false,
      pendingId: null,
      batchBusy: false,
      pagination: undefined
    }
  )

  /** ElTable 的 sort-change 载荷（页面据 prop 映射成后端 sortBy 键）。 */
  interface SortChangePayload {
    prop?: string
    order?: string | null
  }

  const emit = defineEmits<{
    (e: 'menu-select', payload: { row: DockerWorkloadItem; key: string }): void
    (e: 'open-detail', row: DockerWorkloadItem): void
    (e: 'selection-change', rows: DockerWorkloadItem[]): void
    (e: 'reset-filter'): void
    (e: 'pagination:size-change', size: number): void
    (e: 'pagination:current-change', current: number): void
    (e: 'sort-change', payload: SortChangePayload): void
  }>()

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => props.loading || props.rows.length > 0)
</script>

<style lang="scss" scoped>
  @use '../views/overview-tokens' as t;

  // 单根包装层：只为通过 single-root 守卫，不参与布局（display: contents 不产生盒）。
  .wkl-table {
    display: contents;
  }

  // 保护列的锁 + 文字（布局只在图标与文字之间，与模块状态点同类的小挂件）。
  :global(.wkl-lock) {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  // 状态列：圆点 + 中文结论（与 projects.vue 的 docker-proj-state 同一范式）。
  // 布局类走 :global —— formatter 产出的 vnode 在 ArtTable 渲染上下文里取不到
  // 本组件的 scope id，scoped 规则贴不上（色值由内联 style 给，此处只画形）。
  :global(.wkl-state) {
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }

  :global(.wkl-state-dot) {
    display: inline-block;
    width: 8px;
    height: 8px;
    flex: none;
    border-radius: 50%;
  }

  // 主机 chip（胶囊，制同 projects/镜像/卷/网络页的同一全局类 docker-host）：
  // 主机是事实上的分组键，跨主机表里不再是最弱的灰文本。
  :global(.docker-host) {
    @include t.pill;
  }

  // 端口等宽（`/tcp` 与端口号对齐可读）：等宽 token 单一来源在
  // assets/styles/core/tailwind.css 的 --art-font-family-mono。
  :global(.wkl-mono) {
    font-family: var(--art-font-family-mono);
  }

  // 空态不渲染在 ArtTable 里（见文件头注释）：给它接近表格空态的留白。
  .wkl-empty {
    padding: 56px 0;
  }
</style>