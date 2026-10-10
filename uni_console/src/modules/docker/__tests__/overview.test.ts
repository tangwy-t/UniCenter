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
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { useMenuStore } from '@/store/modules/menu'
import type { AppRouteRecord } from '@/types/router'

const api = vi.hoisted(() => ({
  fetchDockerOverview: vi.fn(),
  fetchDockerHosts: vi.fn(),
  fetchDockerState: vi.fn(),
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  // 任务中心入口（8c 整页路由）只 push path，不拉列表 —— 列表属于任务页自己的
  // 生命周期；mock 面保持齐全（页面一旦真的读它，测试环境里也得有个函数）。
  fetchDockerTasks: vi.fn(),
  openDockerPullStream: vi.fn()
}))
vi.mock('../api', () => ({ ...api }))

// overview.vue 的 hero 入口（6b）按 docker:list 门控 —— 挂载真 useAuth 需要 pinia，
// 与 page-render 同款替身绕开（门控行为的用例在 task-center.test.ts）。
// docker:delete 的门单独留一个可变开关：磁盘面板清理入口的门控用例要把它关上。
const authGate = vi.hoisted(() => ({ delete: true }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({
    hasAuth: (perm: string) => (perm === 'docker:delete' ? authGate.delete : true),
    hasAnyAuth: () => true
  })
}))

import Overview from '../views/overview.vue'
import {
  anomalyStateText,
  anomalyStateTone,
  anomalyStatusZh,
  anomalyTruncation,
  buildOverviewKpis,
  diskBarPercent,
  diskPanelSubtitle,
  diskRowModel,
  diskRowModels,
  diskUsageTone,
  diskWaterModel,
  hostSyncText,
  hostSyncTone,
  hostVerdict,
  menuIconByPath,
  overviewPageState,
  overviewSubtitle
} from '../utils/overview'
import type { OverviewKpiTile as KpiTile } from '../utils/overview'
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

    it('六块磁贴（六个资源维度；磁盘移出 KPI 栅格），主数值取自 fleet 合计', () => {
      // 磁盘占用有独立分区（#dov-disk），不再是磁贴 —— 6 才是 2/3/6 列的公倍数。
      expect(kpis).toHaveLength(6)
      expect(kpis.map((k) => k.key)).toEqual([
        'containers',
        'images',
        'volumes',
        'networks',
        'projects',
        'hosts'
      ])
      expect(by('containers').value).toBe(12)
      expect(by('images').value).toBe(30)
      expect(by('volumes').value).toBe(8)
      expect(by('networks').value).toBe(5)
      expect(by('projects').value).toBe(2)
      expect(by('hosts').value).toBe(3)
    })

    it('图标底去装饰化：六块共用一枚中性色，无任一枚走语义状态色', () => {
      // 守卫①：此前七块各取 primary/secondary/warning/info/primary/success/warning ——
      // 把语义色名当分类色板（蓝×2 / 琥珀×2 / info 灰被读成禁用）。修法：图标不再
      // 承载分类语义，全部走同一枚中性 token（NEUTRAL_TILE = --el-color-primary）。
      const palette = new Set(kpis.map((k) => k.tile))
      expect(palette.size).toBe(1)
      expect([...palette][0]).toContain('--el-color-primary')
      for (const k of kpis) {
        // 集合内无重复/误用语义状态色：success/warning/danger/info 一个都不该出现
        expect(k.tile).not.toMatch(/success|warning|danger|error|info/)
      }
    })

    it('五块下钻到对应页面；主机磁贴没有页面 → 锚到本页面板', () => {
      expect(by('containers').to).toBe('/docker/containers')
      // 拆分切片：镜像/数据卷/网络磁贴直达各自的独立列表页（不带 query ——
      // 账目本来就是跨主机合计，进入后是全部主机视图）。
      expect(by('images').to).toBe('/docker/images')
      expect(by('volumes').to).toBe('/docker/volumes')
      expect(by('networks').to).toBe('/docker/networks')
      expect(by('projects').to).toBe('/docker/projects')
      expect(by('hosts').anchor).toBe('dov-hosts')
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

  describe('磁贴图标与菜单同源（iconByPath / menuIconByPath）', () => {
    const fleet = makeOverview().fleet
    const iconOf = (key: string, iconByPath?: Record<string, string>) =>
      buildOverviewKpis(fleet, iconByPath).find((k) => k.key === key)!.icon

    it('有覆盖：菜单同路径的 meta.icon 优先于硬编码回退值', () => {
      // 故意用一枚与回退值（cib:docker）不同的图标名：证明「跟随菜单当前值」
      // 而不是任何形式的硬编码。
      const iconByPath = { '/docker/containers': 'material-symbols:graph-2' }
      expect(iconOf('containers', iconByPath)).toBe('material-symbols:graph-2')
      // 未覆盖的邻居照常回退（覆盖是按 path 逐键生效的）
      expect(iconOf('volumes', iconByPath)).toBe('ri:hard-drive-3-line')
      expect(iconOf('images', iconByPath)).toBe('pajamas:container-image')
    })

    it('无覆盖：菜单里查不到（缺键/空映射/未传）→ 回退硬编码主题图标值', () => {
      expect(iconOf('containers')).toBe('cib:docker')
      expect(iconOf('images', {})).toBe('pajamas:container-image')
      expect(iconOf('volumes', {})).toBe('ri:hard-drive-3-line')
      expect(iconOf('networks', {})).toBe('material-symbols:graph-2')
      expect(iconOf('projects', {})).toBe('catppuccin:docker-compose')
    })

    it('空图标：菜单 icon 为空串 → 回退（磁贴永远有图标）', () => {
      expect(iconOf('containers', { '/docker/containers': '' })).toBe('cib:docker')
    })

    it('主机一枚无对应菜单，不参与同源', () => {
      const iconByPath = { '/docker/containers': 'material-symbols:graph-2' }
      expect(iconOf('hosts', iconByPath)).toBe('ri:server-line')
    })

    it('menuIconByPath：深度遍历收集；同 path 子节点覆盖父节点；空 icon 不进映射也不擦父值', () => {
      const map = menuIconByPath([
        {
          // 顶层目录的 path 由首个子菜单推导、与总览菜单同 path —— 菜单节点应胜出
          path: '/docker',
          meta: { icon: 'devicon-plain:docker-wordmark' },
          children: [
            { path: '/docker', meta: { icon: 'simple-icons:distrobox' } },
            { path: '/docker/volumes', meta: { icon: 'ri:hard-drive-3-line' } },
            // 空 icon：不进映射（调用侧对缺失键回退）
            { path: '/docker/empty', meta: {} },
            {
              path: '/docker/networks',
              meta: { icon: 'material-symbols:graph-2' },
              // 同 path 的空 icon 子节点不得擦掉父值
              children: [{ path: '/docker/networks', meta: {} }]
            },
            {
              path: '/docker/projects',
              meta: { icon: 'catppuccin:docker-compose' },
              children: [
                { path: '/docker/projects/:name', meta: { icon: 'catppuccin:docker-compose' } }
              ]
            }
          ]
        }
      ])
      expect(map['/docker']).toBe('simple-icons:distrobox')
      expect(map['/docker/volumes']).toBe('ri:hard-drive-3-line')
      expect(map['/docker/empty']).toBeUndefined()
      expect(map['/docker/networks']).toBe('material-symbols:graph-2')
      expect(map['/docker/projects']).toBe('catppuccin:docker-compose')
      expect(map['/docker/projects/:name']).toBe('catppuccin:docker-compose')
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

    it('状态句本地化：复用 containerStateZh（退出码 + 中文时长 + health），不再渲染原生英文串', () => {
      // 真机截图病灶：「状态句」列把 Docker 原生串 "Exited (143) 4 days ago" 与中文
      // 「状态」列同屏渲染（两种语言）。本列改走容器列表同一把尺（containerStateZh）。
      expect(anomalyStatusZh({ state: 'exited', statusText: 'Exited (143) 4 days ago' })).toBe(
        '已退出（退出码 143）4 天前'
      )
      // 运行中（异常表不产，但函数与容器列表逐字同源）：Up + 时长 + health 补充。
      expect(anomalyStatusZh({ state: 'running', statusText: 'Up 2 hours (healthy)' })).toBe(
        '运行中 2 小时（健康）'
      )
      // dead 无 statusText：退回状态码中文兜底（不编时长）——与「状态」列同分「已死亡」。
      expect(anomalyStatusZh({ state: 'dead' })).toBe('已死亡')
      // 空状态：给「—」（缺值口径，不渲染空串）。
      expect(anomalyStatusZh({ state: '' })).toBe('—')
    })
  })

  it('副标题：舰队摘要一句 + 自动刷新标记；无数据不编造', () => {
    expect(overviewSubtitle(null, true, false, false)).toBe('正在拉取舰队总览…')
    // 「点击右侧刷新重试」去向句已按「零解释文案」纪律删除（副标题只留失败结论）。
    expect(overviewSubtitle(null, false, true, false)).toBe('总览拉取失败')
    expect(overviewSubtitle(makeOverview(), false, false, true)).toBe(
      '共 3 台主机 · 2 台可用 · 容器 12（运行 9 / 停止 3） · 每 10 秒自动刷新'
    )
  })

  describe('磁盘面板行模型（6a：「吃了多少、花在哪、怎么收回」的口径）', () => {
    it('diskBarPercent：占分母（本行合计）的比例；零值/零分母归 0 不除零', () => {
      expect(diskBarPercent(25600, 25600)).toBe(100)
      expect(diskBarPercent(5120, 25600)).toBe(20)
      expect(diskBarPercent(0, 25600)).toBe(0)
      expect(diskBarPercent(5, 0)).toBe(0)
      // 负值/异常输入按 0 处理（条形不给假比例）
      expect(diskBarPercent(-1, 10)).toBe(0)
    })

    it('diskUsageTone（水位分档口径 80/90）：≥90 danger、≥80 warning、其余中性 primary', () => {
      // 守卫③：阈值与 device 概览 usageTone 同源（80 高 / 90 告警）。
      expect(diskUsageTone(100)).toBe('danger')
      expect(diskUsageTone(90)).toBe('danger')
      expect(diskUsageTone(89.9)).toBe('warning')
      expect(diskUsageTone(80)).toBe('warning')
      expect(diskUsageTone(79.9)).toBe('primary')
      expect(diskUsageTone(0)).toBe('primary')
    })

    it('有 df 的主机行：三条数据条（镜像/数据卷/构建缓存）+ 合计文本 + 可回收结论', () => {
      const row = diskRowModel(makeHost({ disk: { ...DISK_H1 } }))
      expect(row.available).toBe(true)
      expect(row.totalText).toBe('38 GB')
      expect(row.bars.map((b) => b.label)).toEqual(['镜像', '数据卷', '构建缓存'])
      expect(row.bars.map((b) => b.text)).toEqual(['25 GB', '5 GB', '8 GB'])
      // share-of-total：合计 25600+5120+8192=38912 → 65.79% / 13.16% / 21.05%
      const [img, vol, cache] = row.bars.map((b) => b.percent)
      expect(img).toBeCloseTo(65.79, 1)
      expect(vol).toBeCloseTo(13.16, 1)
      expect(cache).toBeCloseTo(21.05, 1)
      // 三条相加回 100%（parts-of-whole 的编码前提）
      expect(img! + vol! + cache!).toBeCloseTo(100, 1)
      // 条不带严重度字段（构成不承载严重度 —— Wave 3b 的代理着色已废除）；
      // 整机水位缺省（makeHost 未给 diskUsedPercent）→ available=false。
      expect(row.bars.every((b) => !('tone' in b))).toBe(true)
      expect(row.water.available).toBe(false)
      expect(row.reclaimText).toBe('悬空镜像 2 个 · 367.3 MB · 未用卷 1 个')
    })

    it('数据条不随比例着色（Wave 3b 代理着色废除）：单一类目主导也只给构成比例', () => {
      const row = diskRowModel(
        makeHost({
          disk: {
            imagesMb: 900,
            volumesMb: 50,
            buildCacheMb: 50,
            imagesDanglingMb: 0,
            danglingImages: 0,
            unusedVolumes: 0
          }
        })
      )
      // 900/1000=90% —— 构成比例照给，但**条不带任何严重度字段**（严重度归整机水位）。
      expect(row.bars.map((b) => b.percent)).toEqual([90, 5, 5])
      expect(row.bars.every((b) => !('tone' in b))).toBe(true)
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

    it('diskRowModels 按值得注意程度排序：占用大者在前、无 df 者殿后（守卫②）', () => {
      // 乱序输入：小占用、无 df、大占用 —— 输出应为 [大, 小, 无df]
      const hosts = [
        makeHost({
          id: 'small',
          disk: {
            imagesMb: 512,
            volumesMb: 0,
            buildCacheMb: 0,
            imagesDanglingMb: 0,
            danglingImages: 0,
            unusedVolumes: 0
          }
        }),
        makeHost({ id: 'nodata' }), // 无 df → 恒殿后
        makeHost({ id: 'big', disk: { ...DISK_H1 } }) // 合计 38912
      ]
      expect(diskRowModels(hosts).map((r) => r.host.id)).toEqual(['big', 'small', 'nodata'])
      // 真实夹具同样按合计降序：h1(38912) > h3(1536) > h2(无 df，殿后)
      expect(diskRowModels(makeOverview().hosts).map((r) => r.host.id)).toEqual(['h1', 'h3', 'h2'])
      // 合计相同：按可回收量（悬空镜像体积）降序
      const tie = [
        makeHost({
          id: 'less-reclaim',
          disk: {
            imagesMb: 100,
            volumesMb: 0,
            buildCacheMb: 0,
            imagesDanglingMb: 10,
            danglingImages: 1,
            unusedVolumes: 0
          }
        }),
        makeHost({
          id: 'more-reclaim',
          disk: {
            imagesMb: 100,
            volumesMb: 0,
            buildCacheMb: 0,
            imagesDanglingMb: 90,
            danglingImages: 9,
            unusedVolumes: 0
          }
        })
      ]
      expect(diskRowModels(tie).map((r) => r.host.id)).toEqual(['more-reclaim', 'less-reclaim'])
    })

    it('diskPanelSubtitle：两笔账各自如实分开（缺账/缺水位的机各计各的）', () => {
      // makeOverview 的 3 台：2 台有 df、0 台有整机水位（Wave 4d 前无此字段）。
      expect(diskPanelSubtitle(makeOverview().hosts)).toBe(
        '共 3 台 · 2 台已上报磁盘账 · 0 台已上报整机水位'
      )
      // 一台同时有两笔账 → M=K=1。
      expect(diskPanelSubtitle([makeHost({ disk: { ...DISK_H1 }, diskUsedPercent: 62 })])).toBe(
        '共 1 台 · 1 台已上报磁盘账 · 1 台已上报整机水位'
      )
    })
  })

  describe('整机磁盘水位（Wave 4d：真分母，与 docker 占用账目不同源）', () => {
    it('缺省（无 diskUsedPercent）：available=false、text「—」，不回落成 0%', () => {
      const w = diskWaterModel(makeHost())
      expect(w.available).toBe(false)
      expect(w.percent).toBe(0)
      expect(w.text).toBe('—')
    })

    it('有值：已用百分比 + 分母（已用/总量），分档按真实水位 80/90', () => {
      const w = diskWaterModel(makeHost({ diskUsedPercent: 62, diskTotalGb: 400 }))
      expect(w.available).toBe(true)
      expect(w.percent).toBe(62)
      // 400 × 62% = 248.0（已用 1 位小数、总量 0 位小数、单位只写一次）。
      expect(w.text).toBe('已用 62% · 248.0 / 400 GB')
      expect(w.tone).toBe('primary')
    })

    it('高水位：≥80 warning、≥90 danger（与 device 概览同阈值）', () => {
      expect(diskWaterModel(makeHost({ diskUsedPercent: 85, diskTotalGb: 100 })).tone).toBe(
        'warning'
      )
      expect(diskWaterModel(makeHost({ diskUsedPercent: 95, diskTotalGb: 100 })).tone).toBe('danger')
    })

    it('无分母（只有 diskUsedPercent）：只报已用百分比，不编总量', () => {
      expect(diskWaterModel(makeHost({ diskUsedPercent: 62 })).text).toBe('已用 62%')
    })

    it('超界值 clamp 到 [0,100]（条形/文本不给假比例）', () => {
      expect(diskWaterModel(makeHost({ diskUsedPercent: 150, diskTotalGb: 10 })).percent).toBe(100)
      expect(diskWaterModel(makeHost({ diskUsedPercent: -5, diskTotalGb: 10 })).percent).toBe(0)
    })

    it('≥1024GB 转 TB（分母进位不退化成 0.x）', () => {
      expect(diskWaterModel(makeHost({ diskUsedPercent: 50, diskTotalGb: 2048 })).text).toBe(
        '已用 50% · 1.00 / 2.00 TB'
      )
    })

    it('diskRowModel 把水位带上行：水位与 df 相互独立（无 df 也能有水位）', () => {
      const row = diskRowModel(makeHost({ diskUsedPercent: 91, diskTotalGb: 100 }))
      expect(row.available).toBe(false) // 该机无 df 账目
      expect(row.water.available).toBe(true)
      expect(row.water.tone).toBe('danger')
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
  authGate.delete = true // 权限开关复位，防止用例间串门
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
        meta: { title: 'Docker 总览', icon: 'simple-icons:distrobox' }
      },
      { path: '/docker/containers', component: { template: '<div />' } },
      // 8a：容器详情是整页路由（异常行下钻的目标），param 段单独登记。
      { path: '/docker/containers/:id', component: { template: '<div />' } },
      // 拆分切片：镜像/数据卷是独立整页（磁盘面板下钻的目标）。
      { path: '/docker/images', component: { template: '<div />' } },
      { path: '/docker/volumes', component: { template: '<div />' } },
      // Wave 4d：全局检索隐藏路由（hero 入口的下钻目标）。
      { path: '/docker/search', component: { template: '<div />' } }
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

/**
 * 等下钻导航落定：判据用「路由到没到目标」而不是「过了几轮宏任务」——
 * 页面/挂载流程里还可能有别的导航在飞（如 ?host 写回 query 的 replace），
 * 轮数会随负载漂；等事实则不漂，慢就多等几轮。
 */
async function waitFullPath(router: Router, fullPath: string) {
  await vi.waitUntil(() => router.currentRoute.value.fullPath === fullPath, { timeout: 5000 })
}

/** 同上，只钉 path（query 由用例自己接着断言）。 */
async function waitPath(router: Router, path: string) {
  await vi.waitUntil(() => router.currentRoute.value.path === path, { timeout: 5000 })
}

async function mountOverview(menu?: AppRouteRecord[]): Promise<{ w: VueWrapper; router: Router }> {
  const pinia = createPinia()
  // 菜单树先灌再接挂：页面的 KPI 磁贴图标读 useMenuStore().menuList（同源口径），
  // 不传 menu 时菜单为空 → 全部走硬编码回退值（既有用例的既有行为）。
  if (menu) useMenuStore(pinia).menuList = menu
  const router = await makeRouter()
  await router.push('/docker')
  // attachTo：主机磁贴的下钻走 document.getElementById —— VTU 默认把组件挂在
  // 脱离文档的 div 上，不 attach 就查不到（getElementById 恒 null）。
  const el = document.createElement('div')
  document.body.appendChild(el)
  const w = mount(Overview, {
    attachTo: el,
    global: { plugins: [router, pinia], stubs: STUBS }
  })
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
  it('页面挂起、六块 KPI 磁贴的数值与分项渲染到位', async () => {
    const { w } = await mountOverview()

    expect(w.find('.docker-overview').exists()).toBe(true)
    const values = w.findAll('.kpi-tile__value').map((n) => n.text())
    // 六块资源维度（磁盘移出栅格 → 不再是第 7 块的容量文本）
    expect(values).toEqual(['12', '30', '8', '5', '2', '3'])
    // 分项与语义色（「停止 3」要红 —— 那是这页最该被看见的数字）
    const segs = w.findAll('.kpi-tile__seg').map((n) => n.text())
    expect(segs).toContain('运行 9')
    expect(segs).toContain('停止 3')
    const stopped = w.findAll('.kpi-tile__seg').find((n) => n.text() === '停止 3')
    expect(stopped!.classes()).toContain('is-danger')
    // 副标题是舰队摘要
    expect(w.find('.dov-hero').text()).toContain('共 3 台主机 · 2 台可用')
  })

  it('KPI 图标底去装饰化：六块共性中性色，页面上不再出现语义状态色的磁贴底', async () => {
    const { w } = await mountOverview()
    const tiles = w
      .findAllComponents({ name: 'DockerOverviewKpiTile' })
      .map((c) => c.props('tile') as KpiTile)
    expect(tiles).toHaveLength(6)
    const palette = new Set(tiles.map((t) => t.tile))
    expect(palette.size).toBe(1)
    for (const t of tiles) expect(t.tile).not.toMatch(/success|warning|danger|error|info/)
  })

  it('KPI 磁贴图标与菜单同源：menuList 同 path 的 meta.icon 覆盖硬编码回退', async () => {
    const { w } = await mountOverview([
      {
        path: '/docker',
        name: 'dir-1',
        meta: { title: 'Docker 管理', icon: 'devicon-plain:docker-wordmark' },
        children: [
          {
            path: '/docker',
            name: 'menu-1',
            meta: { title: 'Docker 总览', icon: 'simple-icons:distrobox' }
          },
          {
            // 菜单值故意 ≠ 磁贴回退值（cib:docker）：证明磁贴跟随「菜单当前值」
            path: '/docker/containers',
            name: 'menu-2',
            meta: { title: '容器', icon: 'material-symbols:graph-2' }
          }
        ]
      }
    ])
    const tiles = w
      .findAllComponents({ name: 'DockerOverviewKpiTile' })
      .map((c) => c.props('tile') as KpiTile)
    const tileOf = (key: string) => tiles.find((t) => t.key === key)!
    // 菜单里有的：取菜单值（运维改「菜单管理」即联动）
    expect(tileOf('containers').icon).toBe('material-symbols:graph-2')
    // 菜单里没有的：回退硬编码主题图标值
    expect(tileOf('images').icon).toBe('pajamas:container-image')
    // 主机不参与同源（无对应菜单）
    expect(tileOf('hosts').icon).toBe('ri:server-line')
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
      '共 3 台 · 2 台已上报磁盘账 · 0 台已上报整机水位'
    )
    const rows = w.findAll('.dov-disk__row')
    expect(rows).toHaveLength(3)
    // 排序生效：占用最大的 bogon（38 GB）排在首行，无 df 的 node-2 殿后。
    expect(rows[0]!.text()).toContain('bogon')
    expect(rows[rows.length - 1]!.text()).toContain('node-2')

    // 有 df 的行：三条数据条 + 合计 + 结论行 + 两个入口。
    const h1 = rows.find((r) => r.text().includes('bogon'))!
    expect(h1.findAll('.dov-disk__bar')).toHaveLength(3)
    expect(h1.find('.dov-disk__total').text()).toBe('38 GB')
    // share-of-total：镜像 25600/38912 ≈ 65.79%（不再是 max-scale 的 100%）
    expect(h1.find('.dov-disk__bar-fill').attributes('style')).toMatch(/width: 65\.7/)
    // 数据条不带分档 class（构成不承载严重度 —— Wave 3b 的代理着色已废除）。
    const barClasses = h1.findAll('.dov-disk__bar')[0]!.classes()
    expect(barClasses).not.toContain('is-primary')
    expect(barClasses).not.toContain('is-warning')
    expect(barClasses).not.toContain('is-danger')
    // 整机水位缺省（夹具无 diskUsedPercent）→ 该行水位如实「未上报 / —」，不冒充 0%。
    expect(h1.find('.dov-disk__water.is-na').exists()).toBe(true)
    expect(h1.find('.dov-disk__water-none').text()).toBe('未上报')
    expect(h1.find('.dov-disk__water-value').text()).toBe('—')
    expect(h1.find('.dov-disk__reclaim').text()).toBe('悬空镜像 2 个 · 367.3 MB · 未用卷 1 个')
    expect(h1.findAll('.dov-disk__go').map((b) => b.text())).toEqual(['镜像清理…', '卷清理…'])

    // 无 df 的行（部分失败主机）：行级结论而不是零值条形，入口照常在。
    const h2 = rows.find((r) => r.text().includes('node-2'))!
    expect(h2.find('.dov-disk__na').exists()).toBe(true)
    expect(h2.find('.dov-disk__na').text()).toContain('磁盘数据不可用')
    expect(h2.findAll('.dov-disk__bar')).toHaveLength(0)
    expect(h2.findAll('.dov-disk__go')).toHaveLength(2)
  })

  it('磁盘面板：无 docker:delete 权限时清理入口不渲染（不渲染 ≠ 禁用）', async () => {
    authGate.delete = false
    const { w } = await mountOverview()

    // 行与结论是只读事实，照常渲染；只有两个清理入口整块缺席
    // （QA 实测此前对无权限者照常渲染，点进去只会撞列表页的权限墙）。
    expect(w.findAll('.dov-disk__row')).toHaveLength(3)
    expect(w.findAll('.dov-disk__go')).toHaveLength(0)
    expect(w.find('.dov-disk__reclaim').exists()).toBe(true)
  })

  it('磁盘面板：整机水位带真分母渲染（分档色），缺省机如实「未上报」（Wave 4d）', async () => {
    const overview = makeOverview()
    // 给 bogon（h1）一台真水位：400GB 已用 95%（≥90 → danger）。
    overview.hosts[0] = { ...overview.hosts[0]!, diskUsedPercent: 95, diskTotalGb: 400 }
    api.fetchDockerOverview.mockResolvedValue(overview)
    const { w } = await mountOverview()

    const row = w.findAll('.dov-disk__row').find((r) => r.text().includes('bogon'))!
    expect(row.find('.dov-disk__water').classes()).toContain('is-danger')
    expect(row.find('.dov-disk__water-value').text()).toBe('已用 95% · 380.0 / 400 GB')
    // 同行的 docker 占用条照常在（水位与 df 是两笔账）。
    expect(row.findAll('.dov-disk__bar')).toHaveLength(3)
    // 其余机水位缺省 → 如实「未上报」，不冒充 0%。
    const na = w.findAll('.dov-disk__row').find((r) => r.text().includes('node-3'))!
    expect(na.find('.dov-disk__water-none').exists()).toBe(true)
  })

  it('异常表保护列：锁图标 + 「受保护」，不再是 🔒 emoji', async () => {
    const { w } = await mountOverview()

    // 夹具第一行 protected: true —— 锁走 ArtSvgIcon 替身（data-icon=stub）+ 文字；
    // emoji 在无 emoji 字体的环境里是豆腐块，HTML 里不得再出现。
    expect(w.find('.dov-anoms__lock').exists()).toBe(true)
    expect(w.find('.dov-anoms__lock').text()).toContain('受保护')
    expect(w.find('.dov-anoms').html()).not.toContain('🔒')
  })

  it('异常表：收到 2 行数据；表尾给截断口径与「查看全部异常」出口（total=3 > items=2）', async () => {
    const { w, router } = await mountOverview()

    const table = w.findComponent({ name: 'ElTable' })
    expect(table.exists()).toBe(true)
    const vm = table.vm as unknown as { $props: Record<string, unknown> }
    expect((vm.$props.data as unknown[]).length).toBe(2)
    expect(w.find('.dov-anoms__more').text()).toContain('共 3 条，仅显示前 2')
    // 守卫④：截断时有真实出口，href 指向容器页「已停止」筛选（该页非运行态一档）。
    const exit = w.find('.dov-anoms__all')
    expect(exit.exists()).toBe(true)
    expect(exit.text()).toBe('查看全部异常')
    expect(exit.attributes('href')).toContain('/docker/containers')
    expect(exit.attributes('href')).toContain('state=stopped')
    await exit.trigger('click')
    await waitFullPath(router, '/docker/containers?state=stopped')
    expect(router.currentRoute.value.fullPath).toBe('/docker/containers?state=stopped')
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
    await waitPath(router, '/docker/containers')
    expect(router.currentRoute.value.path).toBe('/docker/containers')
  })

  it('主机磁贴（无列表页）→ 滚动到本页的主机卡片网格', async () => {
    const { w } = await mountOverview()

    const tile = w.findAll('.kpi-tile').find((n) => n.text().includes('主机'))!
    await tile.trigger('click')
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
  })

  it('磁盘面板的清理入口 → 对应列表页带 ?host=（复用列表页的确认档清理流）', async () => {
    const { w, router } = await mountOverview()

    const h1Row = w.findAll('.dov-disk__row').find((r) => r.text().includes('bogon'))!
    const goImages = h1Row.findAll('.dov-disk__go').find((b) => b.text() === '镜像清理…')!
    await goImages.trigger('click')
    await waitFullPath(router, '/docker/images?host=h1')
    // 拆分切片：磁盘面板下钻到独立镜像页（host 进 query 作该页主机筛选初始值）。
    expect(router.currentRoute.value.fullPath).toBe('/docker/images?host=h1')

    const goVolumes = h1Row.findAll('.dov-disk__go').find((b) => b.text() === '卷清理…')!
    await goVolumes.trigger('click')
    await waitFullPath(router, '/docker/volumes?host=h1')
    expect(router.currentRoute.value.fullPath).toBe('/docker/volumes?host=h1')
  })

  it('主机卡片点击 → 容器列表带 ?host=（模块主机上下文的 query 约定）', async () => {
    const { w, router } = await mountOverview()

    const card = w.findAll('.dov-host').find((c) => c.text().includes('bogon'))!
    await card.trigger('click')
    await waitFullPath(router, '/docker/containers?host=h1')
    expect(router.currentRoute.value.fullPath).toBe('/docker/containers?host=h1')
  })

  it('hero 全局检索入口 → /docker/search（Wave 4d 收尾件）', async () => {
    const { w, router } = await mountOverview()

    const btn = w.findAll('button').find((b) => b.attributes('title') === '全局检索')
    expect(btn, 'hero 应有「全局检索」入口').toBeTruthy()
    await btn!.trigger('click')
    await waitPath(router, '/docker/search')
    expect(router.currentRoute.value.path).toBe('/docker/search')
  })

  it('异常行打开 → 容器详情页 /docker/containers/:id（8a：深链改指详情页，host 随行）', async () => {
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
    await waitPath(router, '/docker/containers/c1')
    // 8a：容器详情是一条整页路由（host 随行 —— 详情页据此还原到同一台机器）；
    // 7b 的「列表页 + ?id 开抽屉」形态已随抽屉一起删除，query 里不该再有 id。
    expect(router.currentRoute.value.path).toBe('/docker/containers/c1')
    expect(router.currentRoute.value.query.host).toBe('h1')
    expect(router.currentRoute.value.query.id).toBeUndefined()
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
