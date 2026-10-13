/**
 * 跨主机统一工作负载表的**列集合**（从 workload-table.vue 下沉为纯工厂）。
 *
 * 为什么下沉：容器页要恢复「列设置」（工具条 columns 段需要页面的列集合），而
 * ArtTableHeader 的列弹层与 ArtTable 的列渲染必须**同一份**列定义 —— 列集合留在
 * 子组件里时，页面拿不到它（工具条会开出空弹层，这正是此前显式去掉 columns 段的
 * 原因）。工厂纯粹、无副作用，页面与子组件各自调用即可（列对象每次新建 —— 行内
 * 菜单的禁用态要跟着 pendingId/batchBusy 变，见下面 formatter 的取用口径）。
 *
 * 数据列一律 minWidth（宽屏按比例分摊），固定宽度只留给 selection/index/操作这类
 * 结构性列，见 responsive-columns.ts 的约定。
 */
import { h } from 'vue'
import type { ColumnOption } from '@/types/component'
import { getColumnKey, getColumnVisibility } from '@/hooks/core/useTableColumns'
import { PermDockerInspect } from '@/enums/permission'
import ArtButtonMore from '@/components/core/forms/art-button-more/index.vue'
import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
import type { DockerWorkloadItem } from '../api'
import { lookupDockerAction, protectedGate } from '../utils/actions'
import {
  cpuText,
  IMAGE_MID_HEAD,
  IMAGE_MID_TAIL,
  memText,
  memUsage,
  midEllipsis,
  netText,
  PORT_MID_HEAD,
  PORT_MID_TAIL,
  portsText
} from '../utils/display'
import { hostLabel } from '../utils/host'
import { containerStatus, statusDotColor, statusTextColor } from '../utils/status'
import MemMeter from './mem-meter.vue'

/** ⋯ 菜单条目（与 action-menu 的条目形态一致：key/label/icon/auth/color/disabled）。 */
export interface WorkloadMenuItem {
  key: string
  label: string
  icon?: string
  auth?: string
  color?: string
  disabled?: boolean
}

/**
 * 行 ⋯ 菜单条目。
 *
 * 只读的「日志」（D-3：带 docker:inspect 权限，无权限不再看到死项）+ 注册表写动作
 * （启动/停止按状态二选一、重启、删除）。受保护 + 无强制权限：受保护档约束的动作
 * 不可执行 —— 条目禁用并把结论写在条目上（禁用的条目点不动，结论只能在看得见的
 * 地方给）。
 */
export function workloadRowMenuItems(
  row: DockerWorkloadItem,
  opts: { pendingId: string | null; batchBusy: boolean; canExec: boolean }
): WorkloadMenuItem[] {
  const blocked = !protectedGate({ protected: row.protected }, opts.canExec).allowed
  const pending = opts.pendingId === row.name
  const writeItem = (action: string): WorkloadMenuItem | null => {
    const entry = lookupDockerAction(action)
    // 注册表里没有的动作不进菜单：宁可少一项，也不生成一个没权限/没确认档的裸按钮。
    if (!entry) return null
    return {
      key: action,
      label: blocked ? `${entry.label}（需要更高权限）` : entry.label,
      icon: entry.icon,
      auth: entry.perm,
      // 文字对比度 AA（收尾批）：原 --art-danger（白底 3.29）改走 token，
      // 数字见 @styles/core/aa-text.scss。
      color: entry.danger === 'normal' ? undefined : 'var(--aa-danger-text)',
      disabled: pending || blocked || opts.batchBusy
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
  ].filter((item): item is WorkloadMenuItem => item !== null)
}

export interface WorkloadColumnsOptions {
  /** 至少一个写权限才渲染勾选列（没有可执行的动作，勾选只是困惑）。 */
  canWrite: boolean
  canManage: boolean
  canDelete: boolean
  canExec: boolean
  /** 权限判定（详情钮与行菜单的显隐）。 */
  hasAuth: (perm: string) => boolean
  /** 单行指令在途的 key（行菜单禁用态；**在 formatter 里现取**，见下）。 */
  pendingId: () => string | null
  /** 批量在途（批量期间行菜单整体禁用）。 */
  batchBusy: () => boolean
  onOpenDetail: (row: DockerWorkloadItem) => void
  onMenuSelect: (payload: { row: DockerWorkloadItem; key: string }) => void
}

/**
 * 列集合工厂（每次调用返回**全新的列对象**）。
 *
 * 行内容一律**单行**（showOverflowTooltip：超长省略号截断、悬停看全文）—— 与模块
 * 「长 ID 短显 + title 全显」同一口径；任何单元格折行都会把行高堆起来，表就不再是
 * 清单。Wave 3c 坐实：3a 曾把这句注释留下、却把 cpu/内存/网络/镜像/端口的
 * showOverflowTooltip 一并删掉，行内容于是折行。此不变量现由
 * __tests__/w3c-nowrap.test.ts 逐列断言（每个数据列须有 showOverflowTooltip）。
 *
 * 行内菜单的禁用态来自 pendingId/batchBusy/canExec 三个信号 —— 调用方每次新建列
 * 集合（formatter 换身份）时表格才会重渲单元格，故这三个信号在**工厂被调用那一刻**
 * 读一次（页面把工厂放进带这些依赖的 computed，语义与下沉前的计算属性一致）。
 */
/**
 * 按列设置的勾选/拖序结果取真列：columnChecks（ArtTableHeader 的 v-model）里是
 * 「显示哪几列、什么次序」，真列（带 formatter）在工厂侧 —— 两者按列键对齐。
 *
 * 与 useTableColumns 自己那份 columns 计算属性的分工：那份的列对象是**一次成型**
 * （formatter 身份不变），行菜单的禁用态就跟着 pendingId/batchBusy 变不动；本函数
 * 每次由调用方传入**新建**的列集合（工厂重跑），调用方把它放进带这两个依赖的
 * computed 里，单元格才会重渲 —— 与列集合下沉前 workload-table.vue 的计算属性
 * 语义完全一致。
 */
export function applyColumnChecks<T>(
  all: ColumnOption<T>[],
  checks: ColumnOption<T>[]
): ColumnOption<T>[] {
  const byKey = new Map(all.map((c) => [getColumnKey(c), c]))
  return checks
    .filter((c) => getColumnVisibility(c))
    .map((c) => byKey.get(getColumnKey(c)))
    .filter((c): c is ColumnOption<T> => Boolean(c))
}

export function workloadColumns(opts: WorkloadColumnsOptions): ColumnOption<DockerWorkloadItem>[] {
  const pendingId = opts.pendingId()
  const batchBusy = opts.batchBusy()
  // 列优先级：手机横屏（<768）只留「名称 / 状态 / 操作」与勾选列，序号让位；
  // 平板竖屏（>=768）补上排查要看的事实（主机、CPU、内存、镜像、端口、保护）；
  // 网络吞吐只在桌面（>=1024）展示。
  return [
    // 勾选列只在有写权限时出现：没这个权限的人看到一个用不上的勾选框只会困惑。
    ...(opts.canWrite ? [{ type: 'selection' as const, width: 46 }] : []),
    { type: 'index' as const, width: 60, label: '序号', hideBelow: 'tablet' },
    {
      prop: 'name',
      label: '名称',
      minWidth: 200,
      showOverflowTooltip: true,
      sortable: 'custom' as const
    },
    {
      // 主机列是统一表唯一的新增列：跨主机表的行归属（同名容器可能散在多台机器），
      // 排查时「先定位在哪台」是第一句。窄屏让位后，归属仍在抽屉/详情页可查。
      // 渲染成胶囊 chip（Wave 3a）：主机是事实上的分组键，不再是最弱的灰文本。
      prop: 'hostname',
      label: '主机',
      minWidth: 120,
      showOverflowTooltip: true,
      hideBelow: 'tablet',
      sortable: 'custom' as const,
      formatter: (row: DockerWorkloadItem) =>
        h('span', { class: 'docker-host' }, hostLabel({ id: row.hostId, hostname: row.hostname }))
    },
    {
      prop: 'statusText',
      label: '状态',
      minWidth: 190,
      showOverflowTooltip: true,
      sortable: 'custom' as const,
      // 状态列改「圆点 + 中文结论」（Wave 3a）：色语汇收敛到 utils/status.ts（与
      // projects/tasks 同一来源）。颜色永不单独传达 —— 旁边永远有文字结论（label）。
      // formatter 产出的 vnode 取不到本组件 scope id，布局类走 :global。
      formatter: (row: DockerWorkloadItem) => {
        const { tone, label } = containerStatus(row)
        return h('span', { class: 'wkl-state', style: { color: statusTextColor(tone) } }, [
          h('span', {
            class: 'wkl-state-dot',
            style: { background: statusDotColor(tone) },
            'aria-hidden': 'true'
          }),
          label
        ])
      }
    },
    {
      prop: 'cpuPercent',
      // 单位进列头（正文只留数值，Wave 3a）；右对齐 + tabular-nums 让一列数字对齐。
      label: 'CPU (%)',
      minWidth: 90,
      // 单行不变量（Wave 3c）：Wave 3a 误将数字列的 showOverflowTooltip 一并删掉 ——
      // 它同时提供 `white-space:nowrap`，删掉后超宽内容会折行堆高行。数字列虽短，
      // 也照纪律补回（形式统一，守卫可逐列断言）。
      showOverflowTooltip: true,
      align: 'right' as const,
      className: 'is-num',
      hideBelow: 'tablet',
      formatter: (row: DockerWorkloadItem) => cpuText(row)
    },
    {
      prop: 'memUsageMb',
      // 单位进列头；meter 呈现「已用/上限」比值（缺值仍画「—」，不画空条）。
      label: '内存 (MB)',
      // minWidth 170→180（Wave 3c 真机实测）：meter 轨道 56 + gap 8 + 文本「17.2 /
      // 9946.21」≈91px，原 170（可用文本宽 146）装不下 → 分母被截。180 给 156 可用宽。
      minWidth: 180,
      // 单行不变量（Wave 3c）：measure 段与文本同处一行不折行；meter 组件本身让
      // 轨道可收缩（保文本），配合本项的 nowrap 保证分母始终可见。
      showOverflowTooltip: true,
      align: 'right' as const,
      className: 'is-num',
      hideBelow: 'tablet',
      formatter: (row: DockerWorkloadItem) => {
        const u = memUsage(row)
        return u ? h(MemMeter, { pct: u.pct, text: memText(row) }) : '—'
      }
    },
    {
      prop: 'netTxBytesSec',
      label: '网络',
      minWidth: 190,
      // 单行不变量（Wave 3c）：Wave 3a 删掉了它的 showOverflowTooltip ——「↑ … ↓ …」
      // 组合文案因此折成两行（真机截图第 6 行可见），补回 nowrap。
      showOverflowTooltip: true,
      align: 'right' as const,
      className: 'is-num',
      // 吞吐是三类资源指标里最次要的（列也最宽），平板竖屏也隐藏，桌面起展示。
      hideBelow: 'desktop',
      formatter: (row: DockerWorkloadItem) => netText(row)
    },
    {
      prop: 'image',
      label: '镜像',
      minWidth: 180,
      // 单行不变量（Wave 3c）：Wave 3a 删掉了 showOverflowTooltip → 镜像列真机折成
      // 3 行（截图里 sha256 分成三段）。补回 nowrap；中段省略只负责「可见那截」，
      // 全文由 title 承载（超预算时 EP 溢出 tooltip 兼作兜底）。
      showOverflowTooltip: true,
      sortable: 'custom' as const,
      // 版本/来源是排查身份的一部分，平板竖屏保留。
      // 中段省略（Wave 3a）：尾部省略会切掉 tag（唯一有信息的那段），改保留头+尾。
      hideBelow: 'tablet',
      formatter: (row: DockerWorkloadItem) => {
        const full = row.image ?? ''
        return h('span', { title: full }, midEllipsis(full, IMAGE_MID_HEAD, IMAGE_MID_TAIL))
      }
    },
    {
      prop: 'ports',
      label: '端口',
      minWidth: 160,
      // 单行不变量（Wave 3c）：Wave 3a 删掉了 showOverflowTooltip → 端口列真机折成
      // 两行（「20443 → 20443/tcp」与「+3」分家）。补回 nowrap。
      showOverflowTooltip: true,
      align: 'right' as const,
      className: 'is-num',
      // 连通性排查的第一线索（服务为什么进不去），平板竖屏保留；多映射在
      // portsText 里折叠成「首条 +N」单行，不再竖向堆高行。中段省略保住宿主
      // 端口与 `/tcp`（尾部省略曾切进 `/tc…` 中间）；等宽让 `/tcp` 对齐可读。
      hideBelow: 'tablet',
      formatter: (row: DockerWorkloadItem) => {
        const full = portsText(row.ports)
        return h(
          'code',
          { class: 'wkl-mono', title: full },
          midEllipsis(full, PORT_MID_HEAD, PORT_MID_TAIL)
        )
      }
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
          ? h('span', { class: 'wkl-lock' }, [h(ArtSvgIcon, { icon: 'ri:lock-2-line' }), '受保护'])
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
          opts.hasAuth(PermDockerInspect)
            ? h(ArtButtonTable, {
                type: 'view',
                title: '详情',
                onClick: () => opts.onOpenDetail(row)
              })
            : null,
          h(ArtButtonMore, {
            list: workloadRowMenuItems(row, { pendingId, batchBusy, canExec: opts.canExec }),
            onClick: (item: { key: string | number }) =>
              opts.onMenuSelect({ row, key: String(item.key) })
          })
        ])
    }
  ]
}