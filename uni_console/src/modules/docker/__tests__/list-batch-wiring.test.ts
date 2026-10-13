// @vitest-environment jsdom
/**
 * 「列表页与批量能力」修复波的接线守卫（源码扫描 + 列集合的纯行为）。
 *
 * 钉住的是**接线**而不是渲染细节（渲染细节在各自页面的挂载用例里）：四个资源页共用
 * 同一个资源无关的批量栏、整体动作一律两段式（第一步选目标主机，页面上不再挂前置
 * 提示句）、容器页恢复列设置、全局检索下钻的 ?keyword= 初值、术语统一。
 *
 * 形态变了先改这里 —— 每一条都对应本波坐实的一个缺陷，删掉任何一条都等于让缺陷
 * 静默回归。
 */
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { applyColumnChecks, workloadColumns } from '../components/workload-columns'
import { measureBatchOffset, stickyScrollContainer } from '../components/batch-actions'

const ROOT = resolve(process.cwd(), 'src/modules/docker')
const src = (rel: string) => readFileSync(resolve(ROOT, rel), 'utf8')

/** 列工厂的最小调用参数（权限全开、无在途指令）。 */
function columnsOpts() {
  return {
    canWrite: true,
    canManage: true,
    canDelete: true,
    canExec: true,
    hasAuth: () => true,
    pendingId: () => null as string | null,
    batchBusy: () => false,
    onOpenDetail: () => {},
    onMenuSelect: () => {}
  }
}

describe('批量栏接线：四个资源页共用同一个资源无关组件（P1-4 / P1-6）', () => {
  it.each([
    ['views/containers.vue', '容器', 'CONTAINER_BATCH_ACTIONS'],
    ['views/images.vue', '镜像', 'IMAGE_BATCH_ACTIONS'],
    ['views/volumes.vue', '数据卷', 'VOLUME_BATCH_ACTIONS'],
    ['views/networks.vue', '网络', 'NETWORK_BATCH_ACTIONS']
  ])('%s：接了批量栏（资源名词 + 动作集 + 在途上报）', (file, noun, actions) => {
    const s = src(file)
    expect(s).toContain('<WorkloadBatchBar')
    expect(s, '资源名词要说清动的是哪种资源').toContain(`noun="${noun}"`)
    expect(s, '动作集要从页面传入（栏本身不认识资源）').toContain(actions)
    expect(s).toContain('@busy-change')
  })

  it('卷/网络页：勾选列随删除权限出现（没有批量动作的页面不给勾选框）', () => {
    for (const file of ['views/volumes.vue', 'views/networks.vue']) {
      expect(src(file)).toMatch(/canDelete\.value \? \[\{ type: 'selection'/)
    }
  })

  it('镜像页：勾选列随「管理或删除」任一权限出现（批量删除/导出都要用）', () => {
    expect(src('views/images.vue')).toMatch(
      /canManage\.value \|\| canDelete\.value \? \[\{ type: 'selection'/
    )
  })

  it('批量动作集只从注册表取形态（栏内不写死 action 标签）', () => {
    const s = src('components/workload-batch-bar.vue')
    expect(s, '标签/危险度/权限从注册表推导').toContain('lookupDockerAction')
    expect(s, '删除家族要走逐字 DELETE 档').toContain("'DELETE'")
    expect(s, '批量启停重启也要过一次确认（列出目标 + 主机）').toContain('dialog.confirmText')
  })
})

describe('整体动作两段式：工具条不挂前置提示句（P1-5 的反向）', () => {
  it.each(['views/images.vue', 'views/volumes.vue'])(
    '%s：工具条不再有「未选定主机」，整体动作也不整钮禁用',
    (file) => {
      const s = src(file)
      // 两段式之后那行小字无话可说：前置条件由流程第一屏回答，工具条只放硬数据。
      expect(s, '工具条不该再现「未选定主机」').not.toContain('未选定主机')
      expect(s, '那行提示的样式随之删除').not.toContain('docker-note')
      expect(s, '缺主机不该再禁用整钮（点了先选主机）').not.toContain('hostActionDisabled')
    }
  )

  it.each([
    [
      'views/images.vue',
      [
        '拉取镜像：选择目标主机',
        '构建镜像：选择目标主机',
        '载入镜像：选择目标主机',
        '清理悬空镜像：选择目标主机'
      ]
    ],
    ['views/volumes.vue', ['清理未使用数据卷：选择目标主机']]
  ])('%s：每条整体动作的入口都走同一套 withTargetHost（第一步是主机选择）', (file, titles) => {
    const s = src(file)
    expect(s, '两段式入口').toContain('withTargetHost')
    expect(s, '第一屏就是主机选择对话框').toContain('openHostPick')
    for (const title of titles) expect(s, `缺少第一屏标题「${title}」`).toContain(title)
  })

  it.each([
    'views/containers.vue',
    'views/images.vue',
    'views/volumes.vue',
    'views/networks.vue',
    'views/projects.vue'
  ])('%s：主机筛选面板默认展开（筛选源是页面状态的一部分）', (file) => {
    expect(src(file)).toMatch(/const showSearchBar = ref\(true\)/)
  })
})

describe('整体动作的目标主机在动作流程里选（P1-7）', () => {
  it('镜像页：四条整体动作都是「先选主机，再开业务弹窗」，确认弹窗里不再有主机选择器', () => {
    const s = src('views/images.vue')
    expect(s).toContain('withTargetHost')
    expect(s).toContain('openHostPick')
    expect(s, '弹窗里的主机选择器已撤（前置已选定）').not.toContain('confirmHostSelectable')
    expect(s, '对话框的主机是发起时锁定的那台').toContain(':host-id="actionHost"')
  })

  it('数据卷页：清理未使用数据卷同一套两段式（不再在确认弹窗里选主机）', () => {
    const s = src('views/volumes.vue')
    expect(s).toContain('withTargetHost')
    expect(s).toContain('清理未使用数据卷：选择目标主机')
    expect(s, '弹窗里的主机选择器已撤（前置已选定）').not.toContain('confirmHostSelectable')
  })
})

describe('全局检索下钻的 ?keyword= 初值（检索落到条目）', () => {
  it.each([
    'views/containers.vue',
    'views/volumes.vue',
    'views/networks.vue',
    'views/images.vue'
  ])('%s：把 query.keyword 读成筛选初始值', (file) => {
    expect(src(file)).toContain('route.query.keyword')
    expect(src(file)).toContain('keyword')
  })
})

describe('容器页列设置（P1-10）', () => {
  it('工具条绑列模型、列集合透传给表（同一份列定义才是可用的列设置）', () => {
    const s = src('views/containers.vue')
    expect(s).toContain('v-model:columns="columnChecks"')
    expect(s).toContain(':columns="columns"')
    expect(s, '不要再显式去掉 columns 段（那会让列设置开出空弹层）').not.toContain(
      'layout="search,refresh,size,fullscreen,settings"'
    )
    expect(s).toContain('workloadColumns(')
    expect(s).toContain('useTableColumns')
  })

  it('列设置真的能改渲染：隐藏的列从渲染集合里消失、其余列照旧', () => {
    const all = workloadColumns(columnsOpts())
    const props = all.map((c) => (c as { prop?: string }).prop)
    expect(props, '容器表的数据列集合应完整').toContain('ports')
    expect(applyColumnChecks(workloadColumns(columnsOpts()), all), '未动列设置时全在').toHaveLength(
      all.length
    )

    const hidden = all.filter((c) => (c as { prop?: string }).prop !== 'ports')
    const out = applyColumnChecks(workloadColumns(columnsOpts()), hidden)
    expect(out.map((c) => (c as { prop?: string }).prop)).not.toContain('ports')
    expect(out).toHaveLength(all.length - 1)
  })

  it('行菜单的禁用态随 pendingId/batchBusy 重算（列对象每次新建）', () => {
    const s = src('views/containers.vue')
    expect(s).toMatch(/void pendingId\.value/)
    expect(s).toMatch(/void batchBusy\.value/)
    // 工厂在读这两个信号（读得到才可能重算）。
    expect(src('components/workload-columns.ts')).toMatch(/opts\.pendingId\(\)/)
  })
})

describe('批量栏吸底 × 分页器：抬起量按分页器的实际位置算（不再整块盖住页码）', () => {
  it('抬起量 = 吸底块自己的滚动容器下缘 - 分页器上缘 + 间距', () => {
    const scroller = document.createElement('div')
    scroller.style.overflowY = 'hidden' // 吸底的约束矩形就是这个滚动容器
    const pag = document.createElement('div')
    pag.className = 'pagination'
    scroller.appendChild(pag)
    document.body.appendChild(scroller)
    const rect = (top: number, height: number, bottom: number) =>
      ({ top, height, bottom, left: 0, right: 100, width: 100, x: 0, y: top }) as DOMRect
    // 容器下缘 800、分页器上缘 700（下面 100px 是吸底块自身的流位置 + 卡体留白）——
    // 旧口径「分页器高度 + 间距」只抬 38px，整块压在分页器上；新口径抬 106px。
    scroller.getBoundingClientRect = () => rect(372, 428, 800)
    pag.getBoundingClientRect = () => rect(700, 32, 732)

    expect(measureBatchOffset(scroller)).toBe(800 - 700 + 6)
    // 无滚动容器 / 无分页器 / 量不出布局（jsdom 首拉）→ 0，退回 CSS 的 bottom: 0。
    expect(measureBatchOffset(null)).toBe(0)
    pag.getBoundingClientRect = () => rect(0, 0, 0)
    expect(measureBatchOffset(scroller)).toBe(0)
    document.body.removeChild(scroller)
  })

  it('滚动容器从**吸底块自己**往上找（别从分页器往上找：两者可能落在不同容器里）', () => {
    const outer = document.createElement('div')
    outer.style.overflowY = 'hidden'
    const inner = document.createElement('div') // 分页器所在、但不是吸底的约束矩形
    inner.style.overflowY = 'auto'
    const bar = document.createElement('div')
    outer.appendChild(inner)
    outer.appendChild(bar)
    document.body.appendChild(outer)

    expect(stickyScrollContainer(bar)).toBe(outer)
    expect(stickyScrollContainer(inner)).toBe(outer)
    expect(stickyScrollContainer(document.body)).toBe(null)
    document.body.removeChild(outer)
  })
})

describe('术语统一（P2）', () => {
  it('数据卷：工具条与注册表都用「数据卷」，不写光秃秃的「清理未使用卷」', () => {
    expect(src('views/volumes.vue')).toContain('清理未使用数据卷')
    expect(src('utils/actions.ts')).toContain("label: '清理未使用数据卷'")
  })

  it('Compose 项目：项目列表页的每一处 Compose 都写作「Compose 项目」', () => {
    const s = src('views/projects.vue')
    expect(s).toContain('Compose 项目')
    expect(s.match(/Compose(?! 项目)/g) ?? [], '「Compose」单独出现就是术语漂移').toEqual([])
  })
})