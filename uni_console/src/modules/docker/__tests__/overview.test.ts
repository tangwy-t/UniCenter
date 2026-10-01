// @vitest-environment jsdom
/**
 * Docker 总览页（控制塔）的守卫。
 *
 * 两层：
 *   ① 纯函数（utils/overview）：KPI 口径、结论句、状态机 —— 每条都写明「错了会看到
 *      什么」（口径错误不抛异常，只表现为页面说错话）；
 *   ② 渲染冒烟（page-render 同款：mock '../api' 真挂页面）：KPI 数值来自 mock 的
 *      fleet、部分失败的主机如实显示、四态切换、以及**自动刷新的启停与卸载清理**
 *      （interval 泄漏会让切走后的页面每 10s 打一次全量聚合，必须钉住）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const api = vi.hoisted(() => ({
  fetchDockerOverview: vi.fn(),
  fetchDockerHosts: vi.fn(),
  fetchDockerState: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  // 6b 任务中心抽屉（hero 入口）挂载但不开 —— import 面必须齐全。
  fetchDockerTasks: vi.fn(),
  openDockerPullStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api }))

// overview.vue 的 hero 入口（6b）按 docker:list 门控 —— 挂载真 useAuth 需要 pinia，
// 与 page-render 同款替身绕开（门控行为的用例在 task-center.test.ts）。
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import Overview from '../views/overview.vue'
import {
  anomalyStateText,
  anomalyStateTone,
  anomalyTruncation,
  buildOverviewKpis,
  diskBarPercent,
  diskPanelSubtitle,
  diskRowModel,
  diskRowModels,
  hostSyncText,
  hostSyncTone,
  hostVerdict,
  overviewPageState,
  overviewSubtitle
} from '../utils/overview'
import type { DockerHostItem, DockerOverviewResp } from '../api'

/* ── 夹具 ─────────────────────────────────────────────── */

const nowSec = () => Math.floor(Date.now() / 1000)

function makeHost(over: Partial<DockerHostItem> = {}): DockerHostItem {
  return {
    id: 'h1',
    hostname: 'bogon',
    primaryIp: '192.168.12.105',
    online: true,
    dockerOk: true,
    containers: 6,
    images: 15,
    lastSync: nowSec() - 3,
    stale: false,
    ...over
  }
}

/** 磁盘账目夹具：单位 MB，数值取整好让 GB 进位后是可读的整数。 */
const DISK_H1 = {
  imagesMb: 25600,
  volumesMb: 5120,
  buildCacheMb: 8192,
  imagesDanglingMb: 367.25,
  danglingImages: 2,
  unusedVolumes: 1
}

function makeOverview(over: Partial<DockerOverviewResp> = {}): DockerOverviewResp {
  return {
    fleet: {
      hosts: { total: 3, dockerOk: 2 },
      containers: { total: 12, running: 9, stopped: 3, protected: 2 },
      images: { total: 30, unused: 5 },
      volumes: { total: 8, unused: 2 },
      networks: { total: 5 },
      projects: { total: 2, running: 1 },
      // 3 台里 2 台报了磁盘账（h1 与 h3；h2 是部分失败主机，无 df）——「1 台未上报」
      // 的 warning 分项与磁盘面板的「数据不可用」行都由这个缺口驱动。
      disk: {
        hosts: 2,
        imagesTotalMb: 25600,
        volumesTotalMb: 5120,
        buildCacheMb: 8192,
        imagesDanglingMb: 367.25
      }
    },
    hosts: [
      makeHost({ disk: { ...DISK_H1 } }),
      // 单台部分失败：行还在、error 如实 —— 页面不因此炸掉（聚合端点的取舍之一）。
      // 磁盘账缺席：那台连快照都没读出来，磁盘行如实显示「数据不可用」。
      makeHost({ id: 'h2', hostname: 'node-2', dockerOk: false, error: '读取资源快照失败' }),
      // 离线 + 陈旧的主机仍带着最后已知的磁盘账（stale ≠ 事实不存在）；
      // 小占用主机（合计 1.5 GB）：跨主机大小比较由数值文本承载，不靠条长。
      makeHost({
        id: 'h3',
        hostname: 'node-3',
        primaryIp: '10.0.0.3',
        online: false,
        stale: true,
        disk: {
          imagesMb: 512,
          volumesMb: 1024,
          buildCacheMb: 0,
          imagesDanglingMb: 0,
          danglingImages: 0,
          unusedVolumes: 0
        }
      })
    ],
    anomalies: {
      total: 3,
      items: [
        {
          id: 'c1',
          hostId: 'h1',
          hostname: 'bogon',
          name: 'uni-center-core',
          image: 'uni-center-core:latest',
          state: 'exited',
          statusText: 'Exited (137) 3 days ago',
          protected: true
        },
        {
          id: 'c2',
          hostId: 'h2',
          hostname: 'node-2',
          name: 'mysql',
          image: 'mysql:8',
          state: 'dead',
          protected: false
        }
      ]
    },
    ...over
  }
}

/* ── 纯函数：口径守卫 ───────────────────────────────────── */

describe('总览纯函数（口径错了只表现为页面说错话，必须钉住）', () => {
  describe('overviewPageState：失败优先，未就绪不是「没有主机」', () => {
    it('失败 → error（把请求失败渲染成空态 = 失败伪装成没有主机）', () => {
      expect(overviewPageState(true, false, 0)).toBe('error')
    })
    it('首拉中 → loading；meta 未到（-1）→ 也走 loading，empty 不可达', () => {
      expect(overviewPageState(false, true, -1)).toBe('loading')
      expect(overviewPageState(false, false, -1)).toBe('loading')
    })
    it('拿到响应且 0 台主机 → empty；有主机 → ready', () => {
      expect(overviewPageState(false, false, 0)).toBe('empty')
      expect(overviewPageState(false, false, 3)).toBe('ready')
    })
  })

  describe('buildOverviewKpis：数值与分项语义色', () => {
    const kpis = buildOverviewKpis(makeOverview().fleet)
    const by = (key: string) => kpis.find((k) => k.key === key)!

    it('七块磁贴（六资源维度 + 磁盘），主数值取自 fleet 合计', () => {
      expect(kpis).toHaveLength(7)
      expect(by('containers').value).toBe(12)
      expect(by('images').value).toBe(30)
      expect(by('volumes').value).toBe(8)
      expect(by('networks').value).toBe(5)
      expect(by('projects').value).toBe(2)
      expect(by('hosts').value).toBe(3)
    })

    it('五块下钻到对应列表页；主机/磁盘磁贴没有列表页 → 锚到本页面板', () => {
      expect(by('containers').to).toBe('/docker/containers')
      expect(by('images').to).toBe('/docker/images')
      expect(by('volumes').to).toBe('/docker/volumes')
      expect(by('networks').to).toBe('/docker/networks')
      expect(by('projects').to).toBe('/docker/projects')
      expect(by('hosts').anchor).toBe('dov-hosts')
      expect(by('disk').to).toBeUndefined()
      expect(by('disk').anchor).toBe('dov-disk')
    })

    it('分项色恒与「需要注意」绑定：停止 3 → danger，未使用 5 → warning，可用 2/3 → warning', () => {
      const containers = by('containers').sub
      expect(containers.map((s) => s.text)).toEqual(['运行 9', '停止 3', '受保护 2'])
      expect(containers[0]!.tone).toBe('success')
      expect(containers[1]!.tone).toBe('danger')
      expect(containers[2]!.tone).toBeUndefined()
      expect(by('images').sub[0]!.tone).toBe('warning')
      expect(by('hosts').sub[0]!.tone).toBe('warning')
    })

    it('磁盘磁贴：总量是容量文本（38 GB）；镜像/卷/缓存三段小字；1 台未上报给 warning', () => {
      const disk = by('disk')
      expect(disk.value).toBe('38 GB')
      expect(disk.sub.map((s) => s.text)).toEqual([
        '镜像 25 GB',
        '卷 5 GB',
        '缓存 8 GB',
        '1 台未上报'
      ])
      expect(disk.sub[3]!.tone).toBe('warning')
      expect(disk.sub.slice(0, 3).map((s) => s.tone)).toEqual([undefined, undefined, undefined])
    })

    it('全部主机都没有 df：主数值「—」+「数据不可用」（不是 0 —— 没有数据 ≠ 没有占用）', () => {
      const fleet = makeOverview().fleet
      fleet.disk = {
        hosts: 0,
        imagesTotalMb: 0,
        volumesTotalMb: 0,
        buildCacheMb: 0,
        imagesDanglingMb: 0
      }
      const disk = buildOverviewKpis(fleet).find((k) => k.key === 'disk')!
      expect(disk.value).toBe('—')
      expect(disk.sub).toEqual([{ text: '数据不可用', tone: 'warning' }])
    })

    it('df 有数据但合计为 0：显示 0 MB（真零），未上报计数消失而不是常驻', () => {
      const fleet = makeOverview().fleet
      fleet.disk = {
        hosts: 3,
        imagesTotalMb: 0,
        volumesTotalMb: 0,
        buildCacheMb: 0,
        imagesDanglingMb: 0
      }
      const disk = buildOverviewKpis(fleet).find((k) => k.key === 'disk')!
      expect(disk.value).toBe('0 MB')
      expect(disk.sub.map((s) => s.text)).toEqual(['镜像 0 MB', '卷 0 MB', '缓存 0 MB'])
    })

    it('零问题舰队不上色：颜色消失而不是变灰（扫描语义）', () => {
      const clean = buildOverviewKpis({
        hosts: { total: 2, dockerOk: 2 },
        containers: { total: 4, running: 4, stopped: 0, protected: 0 },
        images: { total: 9, unused: 0 },
        volumes: { total: 3, unused: 0 },
        networks: { total: 2 },
        projects: { total: 1, running: 1 },
        disk: {
          hosts: 2,
          imagesTotalMb: 1024,
          volumesTotalMb: 512,
          buildCacheMb: 0,
          imagesDanglingMb: 0
        }
      })
      const allTones = clean.flatMap((k) => k.sub.map((s) => s.tone ?? 'none'))
      expect(allTones.filter((t) => t === 'warning' || t === 'danger')).toHaveLength(0)
    })
  })

  describe('主机卡片的两条事实线', () => {
    it('结论句：error（服务端结论）优先；无 error 但 dockerOk=false 兜短句；正常 → success', () => {
      expect(hostVerdict(makeHost({ error: '读取资源快照失败' }))).toEqual({
        tone: 'danger',
        text: '读取资源快照失败'
      })
      expect(hostVerdict(makeHost({ dockerOk: false }))).toEqual({
        tone: 'danger',
        text: 'Docker 不可用'
      })
      expect(hostVerdict(makeHost())).toEqual({ tone: 'success', text: 'Docker 正常' })
    })

    it('同步文案：lastSync 换算 age；0/缺省 = 从未上报（不是「刚刚同步」）', () => {
      expect(hostSyncText(makeHost({ lastSync: nowSec() - 30 }), nowSec())).toBe('同步于 30 秒前')
      expect(hostSyncText(makeHost({ lastSync: 0 }), nowSec())).toBe('尚未收到该主机的数据')
      expect(hostSyncText(makeHost({ lastSync: undefined }), nowSec())).toBe('尚未收到该主机的数据')
      // 陈旧交给同步行（带分钟数），结论行不掺和 —— 两条线各说一个事实
      expect(hostSyncText(makeHost({ stale: true, lastSync: nowSec() - 300 }), nowSec())).toBe(
        '数据陈旧 5 分钟'
      )
      expect(hostSyncTone(makeHost({ stale: true }))).toBe('warning')
      expect(hostSyncTone(makeHost())).toBe('none')
    })
  })

  describe('异常清单口径', () => {
    it('状态文案：dead 独立成「已死亡」（与「已停止」是两种严重度）；未知态回退原始值', () => {
      expect(anomalyStateText('dead')).toBe('已死亡')
      expect(anomalyStateText('exited')).toBe('已停止')
      expect(anomalyStateText('weird-state')).toBe('weird-state')
    })

    it('色点：dead/removing → danger；paused/restarting → warning；exited/created → info', () => {
      expect(anomalyStateTone('dead')).toBe('danger')
      expect(anomalyStateTone('paused')).toBe('warning')
      expect(anomalyStateTone('exited')).toBe('info')
      expect(anomalyStateTone('created')).toBe('info')
    })

    it('截断口径：Total 与 Items 长度不等才提示；未截断返回 null', () => {
      expect(anomalyTruncation(51, 50)).toBe('共 51 条，仅显示前 50')
      expect(anomalyTruncation(50, 50)).toBeNull()
      expect(anomalyTruncation(0, 0)).toBeNull()
    })
  })

  it('副标题：舰队摘要一句 + 自动刷新标记；无数据不编造', () => {
    expect(overviewSubtitle(null, true, false, false)).toBe('正在拉取舰队总览…')
    expect(overviewSubtitle(null, false, true, false)).toBe('总览拉取失败 —— 点击右侧刷新重试')
    expect(overviewSubtitle(makeOverview(), false, false, true)).toBe(
      '共 3 台主机 · 2 台可用 · 容器 12（运行 9 / 停止 3） · 每 10 秒自动刷新'
    )
  })

  describe('磁盘面板行模型（6a：「吃了多少、花在哪、怎么收回」的口径）', () => {
    it('diskBarPercent：行内相对刻度 —— 最大项 100%，其余按占比；全零/零值归 0 不除零', () => {
      expect(diskBarPercent(25600, 25600)).toBe(100)
      expect(diskBarPercent(5120, 25600)).toBe(20)
      expect(diskBarPercent(0, 25600)).toBe(0)
      expect(diskBarPercent(5, 0)).toBe(0)
      // 负值/异常输入按 0 处理（条形不给假比例）
      expect(diskBarPercent(-1, 10)).toBe(0)
    })

    it('有 df 的主机行：三条数据条（镜像/数据卷/构建缓存）+ 合计文本 + 可回收结论', () => {
      const row = diskRowModel(makeHost({ disk: { ...DISK_H1 } }))
      expect(row.available).toBe(true)
      expect(row.totalText).toBe('38 GB')
      expect(row.bars.map((b) => b.label)).toEqual(['镜像', '数据卷', '构建缓存'])
      expect(row.bars.map((b) => b.text)).toEqual(['25 GB', '5 GB', '8 GB'])
      // 行内刻度：镜像最大 → 100%；5120/25600=20%；8192/25600=32%
      expect(row.bars.map((b) => b.percent)).toEqual([100, 20, 32])
      expect(row.reclaimText).toBe('悬空镜像 2 个 · 367.3 MB · 未用卷 1 个')
    })

    it('无悬空/未用：结论行说「无可回收项」（诚实空态，不是空白）', () => {
      const row = diskRowModel(
        makeHost({
          disk: {
            imagesMb: 512,
            volumesMb: 1024,
            buildCacheMb: 0,
            imagesDanglingMb: 0,
            danglingImages: 0,
            unusedVolumes: 0
          }
        })
      )
      expect(row.reclaimText).toBe('无可回收项')
      expect(row.totalText).toBe('1.5 GB')
    })

    it('未用卷计数单独成段（悬空为零时不拖出「悬空镜像 0 个」的噪音）', () => {
      const row = diskRowModel(
        makeHost({
          disk: {
            imagesMb: 10,
            volumesMb: 0,
            buildCacheMb: 0,
            imagesDanglingMb: 0,
            danglingImages: 0,
            unusedVolumes: 3
          }
        })
      )
      expect(row.reclaimText).toBe('未用卷 3 个')
    })

    it('无 df 数据的主机行：available=false、无条形无数值（「数据不可用」不是零账目）', () => {
      const row = diskRowModel(makeHost())
      expect(row.available).toBe(false)
      expect(row.bars).toEqual([])
      expect(row.totalText).toBe('—')
      expect(row.reclaimText).toBe('')
    })

    it('diskRowModels 保留 hosts 行序（与主机卡片区同一来源同一顺序）', () => {
      const hosts = makeOverview().hosts
      expect(diskRowModels(hosts).map((r) => r.host.id)).toEqual(['h1', 'h2', 'h3'])
    })

    it('diskPanelSubtitle：N 台里 M 台已上报，缺报数如实分开（不折算成零）', () => {
      expect(diskPanelSubtitle(makeOverview().hosts)).toBe(
        '共 3 台 · 2 台已上报磁盘账 · 清理入口在每行右侧'
      )
    })
  })
})

/* ── 渲染冒烟：挂载即验证（page-render 同款） ─────────────── */

/** ArtSvgIcon / ArtButtonTable 的轻量替身：保住可断言的 DOM（真组件靠 unplugin 注册）。 */
const STUBS = {
  ArtSvgIcon: defineComponent({
    name: 'ArtSvgIcon',
    props: { icon: { type: String, default: '' } },
    setup: () => () => h('i', { 'data-icon': 'stub' })
  }),
  ArtButtonTable: defineComponent({
    name: 'ArtButtonTable',
    inheritAttrs: false,
    setup:
      (_, { attrs }) =>
      () =>
        h('button', { type: 'button', ...attrs })
  })
}

/** 已挂载页面：用例后逐个卸载（Element Plus 的监听/定时器不留给下一个用例）。 */
const mounted: VueWrapper[] = []
afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

async function makeRouter(): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      {
        path: '/docker',
        name: 'DockerOverview',
        component: Overview,
        meta: { title: 'Docker 总览', icon: 'ri:ship-line' }
      },
      { path: '/docker/containers', component: { template: '<div />' } },
      { path: '/docker/images', component: { template: '<div />' } },
      { path: '/docker/volumes', component: { template: '<div />' } },
      {
        path: '/docker/container-detail/:id',
        name: 'DockerContainerDetail',
        component: { template: '<div />' }
      }
    ]
  })
  await router.push('/')
  await router.isReady()
  return router
}

/** 点击后的路由跳转是异步的（router.push 的导航链要跨几个微任务），用宏任务兜平。 */
const flush = async () => {
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

async function mountOverview(): Promise<{ w: VueWrapper; router: Router }> {
  const router = await makeRouter()
  await router.push('/docker')
  // attachTo：主机磁贴的下钻走 document.getElementById —— VTU 默认把组件挂在
  // 脱离文档的 div 上，不 attach 就查不到（getElementById 恒 null）。
  const el = document.createElement('div')
  document.body.appendChild(el)
  const w = mount(Overview, { attachTo: el, global: { plugins: [router], stubs: STUBS } })
  mounted.push(w)
  await flush()
  return { w, router }
}

/** jsdom 缺失/需断言的 DOM 能力。 */
beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  // jsdom 没有 matchMedia（useResponsiveMenu.test 同款替身）：主机磁贴下钻的
  // prefers-reduced-motion 判断依赖它。
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false
  }))
  Element.prototype.scrollIntoView = vi.fn()
  api.fetchDockerOverview.mockReset()
  api.fetchDockerOverview.mockResolvedValue(makeOverview())
})

describe('渲染冒烟：挂载成功 + KPI 数值来自 mock fleet', () => {
  it('页面挂起、七块 KPI 磁贴的数值与分项渲染到位（磁盘磁贴是容量文本）', async () => {
    const { w } = await mountOverview()

    expect(w.find('.docker-overview').exists()).toBe(true)
    const values = w.findAll('.kpi-tile__value').map((n) => n.text())
    expect(values).toEqual(['12', '30', '8', '5', '2', '3', '38 GB'])
    // 分项与语义色（「停止 3」要红 —— 那是这页最该被看见的数字）
    const segs = w.findAll('.kpi-tile__seg').map((n) => n.text())
    expect(segs).toContain('运行 9')
    expect(segs).toContain('停止 3')
    expect(segs).toContain('镜像 25 GB')
    expect(segs).toContain('1 台未上报')
    const stopped = w.findAll('.kpi-tile__seg').find((n) => n.text() === '停止 3')
    expect(stopped!.classes()).toContain('is-danger')
    const notReported = w.findAll('.kpi-tile__seg').find((n) => n.text() === '1 台未上报')
    expect(notReported!.classes()).toContain('is-warning')
    // 副标题是舰队摘要
    expect(w.find('.dov-hero').text()).toContain('共 3 台主机 · 2 台可用')
  })

  it('主机卡片：三台各一张；部分失败的那台如实显示 error，不炸整页', async () => {
    const { w } = await mountOverview()

    const cards = w.findAll('.dov-host')
    expect(cards).toHaveLength(3)
    const failed = cards.find((c) => c.text().includes('node-2'))!
    expect(failed.text()).toContain('读取资源快照失败')
    // 其余主机不受牵连
    expect(cards.find((c) => c.text().includes('bogon'))!.text()).toContain('Docker 正常')
    expect(w.find('.dov-hero').text()).not.toContain('失败')
  })

  it('磁盘面板：三行（两行有账 + 一行数据不可用），分区副标题分开两个数', async () => {
    const { w } = await mountOverview()

    const section = w.find('#dov-disk')
    expect(section.exists()).toBe(true)
    expect(section.find('.dov-section__sub').text()).toBe(
      '共 3 台 · 2 台已上报磁盘账 · 清理入口在每行右侧'
    )
    const rows = w.findAll('.dov-disk__row')
    expect(rows).toHaveLength(3)

    // 有 df 的行：三条数据条 + 合计 + 结论行 + 两个入口。
    const h1 = rows.find((r) => r.text().includes('bogon'))!
    expect(h1.findAll('.dov-disk__bar')).toHaveLength(3)
    expect(h1.find('.dov-disk__total').text()).toBe('38 GB')
    expect(h1.find('.dov-disk__bar-fill').attributes('style')).toContain('width: 100%')
    expect(h1.find('.dov-disk__reclaim').text()).toBe('悬空镜像 2 个 · 367.3 MB · 未用卷 1 个')
    expect(h1.findAll('.dov-disk__go').map((b) => b.text())).toEqual(['镜像清理…', '卷清理…'])

    // 无 df 的行（部分失败主机）：行级结论而不是零值条形，入口照常在。
    const h2 = rows.find((r) => r.text().includes('node-2'))!
    expect(h2.find('.dov-disk__na').exists()).toBe(true)
    expect(h2.find('.dov-disk__na').text()).toContain('磁盘数据不可用')
    expect(h2.findAll('.dov-disk__bar')).toHaveLength(0)
    expect(h2.findAll('.dov-disk__go')).toHaveLength(2)
  })

  it('异常表：收到 2 行数据；表尾给截断口径（total=3 > items=2）', async () => {
    const { w } = await mountOverview()

    const table = w.findComponent({ name: 'ElTable' })
    expect(table.exists()).toBe(true)
    const vm = table.vm as unknown as { $props: Record<string, unknown> }
    expect((vm.$props.data as unknown[]).length).toBe(2)
    expect(w.find('.dov-anoms__more').text()).toBe('共 3 条，仅显示前 2')
  })

  it('零异常：给绿点结论句，而不是一块空表', async () => {
    api.fetchDockerOverview.mockResolvedValue(makeOverview({ anomalies: { total: 0, items: [] } }))
    const { w } = await mountOverview()
    expect(w.find('.dov-anoms__ok').text()).toContain('全部容器均在运行')
    expect(w.findComponent({ name: 'ElTable' }).exists()).toBe(false)
  })
})

describe('四态：空 / 错误 / 重试', () => {
  it('空态：无主机时给引导文案与刷新入口（不是一块空白）', async () => {
    api.fetchDockerOverview.mockResolvedValue(
      makeOverview({
        hosts: [],
        fleet: { ...makeOverview().fleet, hosts: { total: 0, dockerOk: 0 } }
      })
    )
    const { w } = await mountOverview()

    expect(w.text()).toContain('尚无可管主机')
    expect(w.text()).toContain('可管主机来自已安装 agent')
    expect(w.find('.kpi-tile__value').exists()).toBe(false)
  })

  it('错误态：结论句 + 重试；重试成功后回到正常态', async () => {
    api.fetchDockerOverview.mockRejectedValueOnce(new Error('network down'))
    const { w } = await mountOverview()

    expect(w.text()).toContain('总览数据获取失败')
    expect(w.text()).not.toContain('共 3 台主机') // 错误页上没有旧数字可看

    api.fetchDockerOverview.mockClear()
    const retry = w.findAll('button').find((b) => b.text() === '重试')!
    await retry.trigger('click')
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()

    expect(api.fetchDockerOverview).toHaveBeenCalledTimes(1)
    expect(w.text()).toContain('共 3 台主机')
  })
})

describe('下钻（控制塔的本职：把人交棒给列表页 / 详情页）', () => {
  it('KPI 磁贴点击 → 对应列表页', async () => {
    const { w, router } = await mountOverview()

    const tile = w.findAll('.kpi-tile').find((n) => n.text().includes('容器'))!
    await tile.trigger('click')
    await flush()
    expect(router.currentRoute.value.path).toBe('/docker/containers')
  })

  it('主机磁贴（无列表页）→ 滚动到本页的主机卡片网格', async () => {
    const { w } = await mountOverview()

    const tile = w.findAll('.kpi-tile').find((n) => n.text().includes('主机'))!
    await tile.trigger('click')
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
  })

  it('磁盘磁贴（无列表页）→ 滚动到本页的磁盘面板', async () => {
    const { w } = await mountOverview()

    const tile = w.findAll('.kpi-tile').find((n) => n.text().includes('磁盘占用'))!
    await tile.trigger('click')
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
  })

  it('磁盘面板的清理入口 → 对应列表页带 ?host=（复用列表页的确认档清理流）', async () => {
    const { w, router } = await mountOverview()

    const h1Row = w.findAll('.dov-disk__row').find((r) => r.text().includes('bogon'))!
    const goImages = h1Row.findAll('.dov-disk__go').find((b) => b.text() === '镜像清理…')!
    await goImages.trigger('click')
    await flush()
    expect(router.currentRoute.value.fullPath).toBe('/docker/images?host=h1')

    const goVolumes = h1Row.findAll('.dov-disk__go').find((b) => b.text() === '卷清理…')!
    await goVolumes.trigger('click')
    await flush()
    expect(router.currentRoute.value.fullPath).toBe('/docker/volumes?host=h1')
  })

  it('主机卡片点击 → 容器列表带 ?host=（模块主机上下文的 query 约定）', async () => {
    const { w, router } = await mountOverview()

    const card = w.findAll('.dov-host').find((c) => c.text().includes('bogon'))!
    await card.trigger('click')
    await flush()
    expect(router.currentRoute.value.fullPath).toBe('/docker/containers?host=h1')
  })

  it('异常行打开 → 容器详情（与容器列表同一跳法：路由名 + params + query.host）', async () => {
    const { w, router } = await mountOverview()

    // jsdom 里 ElTable 不渲染行单元格：从组件面发 open（与列表页 onRowMenu 同口径）
    const table = w.findComponent({ name: 'DockerOverviewAnomalyTable' })
    table.vm.$emit('open', {
      id: 'c1',
      hostId: 'h1',
      hostname: 'bogon',
      name: 'uni-center-core',
      image: 'uni-center-core:latest',
      state: 'exited',
      statusText: 'Exited (137) 3 days ago',
      protected: true
    })
    await flush()
    expect(router.currentRoute.value.name).toBe('DockerContainerDetail')
    expect(router.currentRoute.value.params.id).toBe('c1')
    expect(router.currentRoute.value.query.host).toBe('h1')
  })
})

describe('自动刷新：启停与离开清理（泄漏 = 每 10s 一次全量聚合）', () => {
  /**
   * toFake 限缩到四个 timer API、**不含 Date**：vitest 默认连 Date 一起假化，而 Date
   * 假化 + 已挂载组件的组合会让 vitest 收尾时 Vite server 关不掉（close 超时挂起）；
   * 本组用例只推进 interval，不依赖时钟语义。
   */
  const useTimerFakes = () =>
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })

  /** hero 上的自动刷新开关（ArtButtonTable 替身渲染成原生 button，title 带语义）。 */
  const findToggle = (w: VueWrapper) =>
    w.findAll('button').find((b) => (b.attributes('title') ?? '').includes('自动刷新'))!

  it('默认关：不开启时过了 30s 也不发第二次请求', async () => {
    const { w } = await mountOverview()
    api.fetchDockerOverview.mockClear()

    useTimerFakes()
    vi.advanceTimersByTime(30_000)
    expect(api.fetchDockerOverview).not.toHaveBeenCalled()
    expect(w.text()).not.toContain('自动刷新')
  })

  it('开启后每 10s 静默重拉一次；关闭即停', async () => {
    const { w } = await mountOverview()
    api.fetchDockerOverview.mockClear()

    useTimerFakes()
    await findToggle(w).trigger('click')
    vi.advanceTimersByTime(10_000)
    expect(api.fetchDockerOverview).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(10_000)
    expect(api.fetchDockerOverview).toHaveBeenCalledTimes(2)
    // 静默刷新不把页面切成加载骨架（数字反复消失 = 自动刷新不可用）
    expect(w.text()).toContain('每 10 秒自动刷新')
    expect(w.find('.dov-state__spin').exists()).toBe(false)

    await findToggle(w).trigger('click')
    vi.advanceTimersByTime(30_000)
    expect(api.fetchDockerOverview).toHaveBeenCalledTimes(2)
  })

  it('卸载清掉 interval：切走后不再轮询（泄漏守卫）', async () => {
    const { w } = await mountOverview()
    api.fetchDockerOverview.mockClear()

    useTimerFakes()
    await findToggle(w).trigger('click')
    vi.advanceTimersByTime(10_000)
    expect(api.fetchDockerOverview).toHaveBeenCalledTimes(1)

    w.unmount()
    mounted.splice(mounted.indexOf(w), 1) // 本用例自己卸载，afterEach 不再重复卸
    vi.advanceTimersByTime(60_000)
    expect(api.fetchDockerOverview).toHaveBeenCalledTimes(1)
  })
})
