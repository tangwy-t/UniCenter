<template>
  <!-- 单根包装（single-root 守卫扫全模块 .vue）：下面三态互斥但源码层是兄弟节点。 -->
  <div class="wkl-table">
    <ArtTable
      v-if="showTable"
      :loading="loading"
      :data="rows"
      :columns="columns"
      @selection-change="(rows: DockerWorkloadItem[]) => emit('selection-change', rows)"
    />
    <ElEmpty v-else-if="hasFilter" class="wkl-empty" description="没有符合筛选条件的容器">
      <ElButton size="small" @click="emit('reset-filter')">清除筛选</ElButton>
    </ElEmpty>
    <ElEmpty v-else class="wkl-empty" description="所有主机上都还没有容器" />
  </div>
</template>

<script setup lang="ts">
  /**
   * 跨主机统一工作负载表：列集合 + 行菜单生成 + 空态（从 containers.vue 下沉）。
   *
   * 两种空态分开是 overview.vue 的既有纪律（把「没有」与「筛没了」说成一句会
   * 让人以为机器空了）；空态不写进 ArtTable 的 `#empty` 插槽 —— ArtTable 不转发
   * 该插槽（内部把 ElTable 空态写死成「暂无数据」），写进去会被静默丢弃。
   *
   * 列在旧容器页的集合上**只加一列**：主机（hostname）—— 统一表里行的归属是
   * 排查的第一条线索（同名容器可能分布在多台机器上），紧跟「名称」这组身份列。
   * 写操作**按行主机派发**不在本组件：菜单只 emit 出去，指令通道与确认档由页面
   * （单行）与批量栏（多行）各自持有 —— 表格只负责把「用户点了哪一行的哪个动作」
   * 说清楚。导航（详情 / 日志）同理只 emit：8a 起它们是整页路由，跳转由页面做
   * （表格不认识路由）。
   */
  import { computed, h } from 'vue'
  import { ElButton, ElEmpty } from 'element-plus'
  import {
    PermDockerDelete,
    PermDockerExec,
    PermDockerInspect,
    PermDockerManage
  } from '@/enums/permission'
  import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import { useAuth } from '@/hooks/core/useAuth'
  import type { ColumnOption } from '@/types/component'
  import type { DockerWorkloadItem } from '../api'
  import { lookupDockerAction, protectedGate } from '../utils/actions'
  import { containerStateText, cpuText, memText, netText, portsText } from '../utils/display'

  defineOptions({ name: 'DockerWorkloadTable' })

  const props = withDefaults(
    defineProps<{
      rows: DockerWorkloadItem[]
      /** 表格加载遮罩（拉取在途）。 */
      loading?: boolean
      /** 筛选是否在途（有条件时空态给「清除筛选」）。 */
      hasFilter?: boolean
      /** 单行指令的行级 pending 键（useDockerCmds 的 pendingId）：命中行菜单禁用。 */
      pendingId?: string | null
      /** 批量在途：批量期间行菜单整体禁用（防与批量并发提交同一目标）。 */
      batchBusy?: boolean
    }>(),
    { loading: false, hasFilter: false, pendingId: null, batchBusy: false }
  )

  const emit = defineEmits<{
    (e: 'menu-select', payload: { row: DockerWorkloadItem; key: string }): void
    (e: 'open-detail', row: DockerWorkloadItem): void
    (e: 'selection-change', rows: DockerWorkloadItem[]): void
    (e: 'reset-filter'): void
  }>()

  const { hasAuth } = useAuth()

  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))
  /** 至少有一个写权限才渲染勾选列：没有可执行的动作，勾选只是困惑（照设备页纪律）。 */
  const canWrite = computed(() => canManage.value || canDelete.value)

  /** 有命中行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => props.loading || props.rows.length > 0)

  /** ⋯ 菜单条目：只读的「日志」（D-3：带 docker:inspect 权限，无权限不再看到死项）+ 注册表写动作。 */
  interface RowMenuItem {
    key: string
    label: string
    icon?: string
    auth?: string
    color?: string
    disabled?: boolean
  }

  function rowMenuItems(row: DockerWorkloadItem): RowMenuItem[] {
    // 受保护 + 无强制权限：受保护档约束的动作不可执行 —— 条目禁用并把结论写在条目上
    // （禁用的条目点不动，结论只能在看得见的地方给）。
    const blocked = !protectedGate({ protected: row.protected }, canExec.value).allowed
    const pending = props.pendingId === row.name
    const writeItem = (action: string): RowMenuItem | null => {
      const entry = lookupDockerAction(action)
      // 注册表里没有的动作不进菜单：宁可少一项，也不生成一个没权限/没确认档的裸按钮。
      if (!entry) return null
      return {
        key: action,
        label: blocked ? `${entry.label}（需要更高权限）` : entry.label,
        icon: entry.icon,
        auth: entry.perm,
        color: entry.danger === 'normal' ? undefined : 'var(--art-danger)',
        disabled: pending || blocked || props.batchBusy
      }
    }
    return [
      // 日志项走 docker:inspect 权限（与详情页路由、container:logs 指令同一档）；
      // 点击进容器详情页的日志 Tab（8a 起整页路由，页面读 ?tab=logs 落位）。
      { key: 'logs', label: '日志', icon: 'ri:file-list-3-line', auth: PermDockerInspect },
      // 启动/停止按容器状态二选一（spec §11.1）；其余状态（已停止/已创建）给「启动」。
      writeItem(row.state === 'running' ? 'container:stop' : 'container:start'),
      writeItem('container:restart'),
      writeItem('container:remove')
    ].filter((item): item is RowMenuItem => item !== null)
  }

  // ── 表格列 ──

  const columns = computed<ColumnOption<DockerWorkloadItem>[]>(() => {
    // 显式建立依赖：行内菜单的禁用态来自这些信号，而 formatter 要到表格渲染时才执行 ——
    // 不在这里读一次，列配置就不会随它们变化而重算（表格会停在旧状态）。
    void props.pendingId
    void props.batchBusy
    void canExec.value
    // 列优先级：手机横屏（<768）只留「名称 / 状态 / 操作」与勾选列，序号让位；
    // 平板竖屏（>=768）补上排查要看的事实（主机、CPU、内存、镜像、端口、保护）；
    // 网络吞吐只在桌面（>=1024）展示。数据列一律 minWidth（宽屏按比例分摊），
    // 固定宽度只留给 selection/index/操作这类结构性列，见 responsive-columns.ts 的约定。
    // 行内容一律**单行**（showOverflowTooltip：超长省略号截断、悬停看全文）——
    // 与模块「长 ID 短显 + title 全显」同一口径；任何单元格折行都会把行高堆起来，
    // 表就不再是清单（内存/网络的组合文案以前正是每行折成两行的来源）。
    return [
      // 勾选列只在有写权限时出现：没这个权限的人看到一个用不上的勾选框只会困惑。
      ...(canWrite.value ? [{ type: 'selection' as const, width: 46 }] : []),
      { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
      { prop: 'name', label: '名称', minWidth: 200, showOverflowTooltip: true },
      {
        // 主机列是统一表唯一的新增列：跨主机表的行归属（同名容器可能散在多台机器），
        // 排查时「先定位在哪台」是第一句。窄屏让位后，归属仍在抽屉/详情页可查。
        prop: 'hostname',
        label: '主机',
        minWidth: 120,
        showOverflowTooltip: true,
        hideBelow: 'tablet'
      },
      {
        prop: 'statusText',
        label: '状态',
        minWidth: 190,
        showOverflowTooltip: true,
        formatter: (row: DockerWorkloadItem) => containerStateText(row)
      },
      {
        prop: 'cpuPercent',
        label: 'CPU',
        minWidth: 90,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        formatter: (row: DockerWorkloadItem) => cpuText(row)
      },
      {
        prop: 'memUsageMb',
        label: '内存',
        minWidth: 170,
        showOverflowTooltip: true,
        hideBelow: 'tablet',
        formatter: (row: DockerWorkloadItem) => memText(row)
      },
      {
        prop: 'netTxBytesSec',
        label: '网络',
        minWidth: 190,
        showOverflowTooltip: true,
        // 吞吐是三类资源指标里最次要的（列也最宽），平板竖屏也隐藏，桌面起展示。
        hideBelow: 'desktop',
        formatter: (row: DockerWorkloadItem) => netText(row)
      },
      {
        prop: 'image',
        label: '镜像',
        minWidth: 180,
        showOverflowTooltip: true,
        // 版本/来源是排查身份的一部分，平板竖屏保留。
        hideBelow: 'tablet'
      },
      {
        prop: 'ports',
        label: '端口',
        minWidth: 160,
        showOverflowTooltip: true,
        // 连通性排查的第一线索（服务为什么进不去），平板竖屏保留；多映射在
        // portsText 里折叠成「首条 +N」单行，不再竖向堆高行。
        hideBelow: 'tablet',
        formatter: (row: DockerWorkloadItem) => portsText(row.ports)
      },
      {
        prop: 'protected',
        label: '保护',
        minWidth: 96,
        showOverflowTooltip: true,
        // 保护是「动手前必须看见」的事实（spec §11.1 草图的 🔒）：列表上给可见标记，
        // 权限不足时的结论句写在被禁用的菜单条目上（那里才是用户看得到的地方）。
        // 小屏横屏让位后，这条结论仍会出现在 ⋯ 菜单（禁用条目）与确认弹窗里。
        // 标记用锁图标 + 文字（不用 🔒 emoji：无 emoji 字体的环境里是豆腐块）；
        // formatter 产出的 vnode 在 ArtTable 的渲染上下文里取不到本组件的 scope id，
        // 布局类走 :global（与 projects.vue 的 docker-proj-state 同一手法）。
        hideBelow: 'tablet',
        formatter: (row: DockerWorkloadItem) =>
          row.protected
            ? h('span', { class: 'wkl-lock' }, [
                h(ArtSvgIcon, { icon: 'ri:lock-2-line' }),
                '受保护'
              ])
            : '—'
      },
      {
        prop: 'operation',
        label: '操作',
        width: 100,
        fixed: 'right' as const,
        formatter: (row: DockerWorkloadItem) =>
          // 主操作「详情」常驻图标按钮，次要操作收进 ⋯（spec §11.0：不把详情埋进下拉）。
          // 详情与日志同档（docker:inspect）：无权限的人看不到死按钮（D-3）。
          h('div', { class: 'flex items-center' }, [
            hasAuth(PermDockerInspect)
              ? h(ArtButtonTable, {
                  type: 'view',
                  title: '详情',
                  onClick: () => emit('open-detail', row)
                })
              : null,
            h(ArtButtonMore, {
              list: rowMenuItems(row),
              onClick: (item: { key: string | number }) =>
                emit('menu-select', { row, key: String(item.key) })
            })
          ])
      }
    ]
  })
</script>

<style lang="scss" scoped>
  @use '../views/overview-tokens' as t;

  // 「清除筛选」默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

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

  // 空态不渲染在 ArtTable 里（见文件头注释）：给它接近表格空态的留白。
  .wkl-empty {
    padding: 56px 0;
  }
</style>
