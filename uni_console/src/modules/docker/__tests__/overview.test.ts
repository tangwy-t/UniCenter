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
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
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
import {
  buildHostGroups,
  diskVisibleRows,
  filterHosts,
  GROUP_COLLAPSE_AT,
  groupCollapsedByDefault,
  hostFilterActive,
  hostGroupKey,
  hostIsAbnormal,
  hostIsOffline,
  hostMatches,
  HOST_GROUP_UNNAMED,
  HOST_SORTS,
  sortHosts,
  type HostFilter
} from '../components/overview-board'
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
      expect(row.reclaimText).toBe('悬空镜像 2 个 · 367.3 MB · 未用数据卷 1 个')
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

    it('未用数据卷计数单独成段（悬空为零时不拖出「悬空镜像 0 个」的噪音）', () => {
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
      expect(row.reclaimText).toBe('未用数据卷 3 个')
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
      expect(diskWaterModel(makeHost({ diskUsedPercent: 95, diskTotalGb: 100 })).tone).toBe(
        'danger'
      )
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
    expect(h1.find('.dov-disk__reclaim').text()).toBe('悬空镜像 2 个 · 367.3 MB · 未用数据卷 1 个')
    expect(h1.findAll('.dov-disk__go').map((b) => b.text())).toEqual(['镜像清理…', '数据卷清理…'])

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

    const goVolumes = h1Row.findAll('.dov-disk__go').find((b) => b.text() === '数据卷清理…')!
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

describe('布局口径：主机区铺满 + gutter 与列表页一致', () => {
  // 主机区（含卡片栅格 CSS）已随 100 台规模改造收进子组件 overview-host-board.vue：
  // 源码级锚跟着它走（本用例守的仍是同一件事 —— 2 台不退化回「只占左半屏」）。
  const boardSrc = readFileSync(
    join(process.cwd(), 'src/modules/docker/components/overview-host-board.vue'),
    'utf8'
  )
  const src = readFileSync(join(process.cwd(), 'src/modules/docker/views/overview.vue'), 'utf8')

  it('主机区列数随主机数收敛：auto-fit + minmax，不再按断点钉 4 列（2 台不再只占左半屏）', async () => {
    // 修复前：`grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4` —— 2 台主机在
    // 2xl 下固定占 2/4 列 = 左半屏（2560 实测 49.5%，右侧空 1143px）。
    // 修法：auto-fit + minmax(...) —— 空轨道塌缩、剩余轨道等分，2 台即铺满整行。
    expect(boardSrc).toContain('repeat(auto-fit, minmax(min(100%, 360px), 1fr))')
    // 固定断点列数不得回归（否则又退化回「2 台只占左半屏」）。
    expect(boardSrc).not.toContain('2xl:grid-cols-4')
    expect(boardSrc).not.toMatch(/sm:grid-cols-2 lg:grid-cols-3/)
    expect(src).not.toContain('2xl:grid-cols-4')

    // DOM 面：栅格仍在、卡片数与 hosts 一致（夹具 3 台同在「未分组」一组、远在折叠
    // 阈值内 → 默认展开，卡片一张不少）。
    const { w } = await mountOverview()
    expect(w.find('.dov-hosts').exists()).toBe(true)
    expect(w.findAll('.dov-host')).toHaveLength(3)
  })

  it('页根不加附加内边距：与 .layout-content（已内缩 20px）同一左右口径（总览↔列表不跳 19px）', () => {
    // 修复前：`docker-overview__inner p-4 pb-8 md:p-5` —— 在 .layout-content 已内缩 20px
    // 之上**另加** 20px，左边缘落到 40px（列表页卡片 21px），切页内容边缘跳 19px。
    expect(src).toContain('class="docker-overview__inner"')
    expect(src).not.toContain('docker-overview__inner p-4')
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

  /**
   * 100 台规模最易踩的坑：静默重拉把 host 列表整个换掉时，**用户的筛选/排序/折叠
   * 状态**（它们与数据生命周期无关）不得被打回默认 —— 盯控制塔的人正是「开着自动
   * 刷新」的那批用户，每 10 秒被重置一次筛选等于这个功能不可用。
   * 状态自持在子组件里（overview-host-board.vue 的 ref + overrides），本用例把它钉住。
   */
  it('自动刷新不重置筛选与折叠状态（状态自持在就绪板子组件里，与数据解耦）', async () => {
    api.fetchDockerOverview.mockResolvedValue(makeScaleOverview())
    const { w } = await mountOverview()
    api.fetchDockerOverview.mockClear()

    // ① 筛到「只看有异常」；② 搜索定位到 prod 组里的 node-011（异常主机）。
    await findChip(w, '只看有异常').trigger('click')
    await flush()
    await w.find('.dov-tools__search input').setValue('node-011')
    await flush()

    // ③ 折叠：筛后只剩 1 台（≤ 阈值）默认展开；点一下组头**手动折叠** ——
    //    这一步得到的状态与默认值相反，只要刷新把状态打回默认，下面的断言就会翻。
    const head = w.findAll('.dov-group__head')[0]!
    expect(head.attributes('aria-expanded')).toBe('true')
    await head.trigger('click')
    await flush()
    expect(head.attributes('aria-expanded')).toBe('false')

    // 开自动刷新，推进一个周期（静默重拉，hosts prop 整个换新）。
    useTimerFakes()
    await findToggle(w).trigger('click')
    vi.advanceTimersByTime(10_000)
    // 假化计时器下不能用 flush()（它靠 setTimeout(0)，永远不会到点）—— 用微任务轮次铺平。
    await nextTick()
    await nextTick()
    expect(api.fetchDockerOverview).toHaveBeenCalledTimes(1)

    // 三项用户状态全部原样：筛选开关仍选中、搜索词还在、折叠态与刷新前一致。
    expect(findChip(w, '只看有异常').classes()).toContain('is-on')
    expect((w.find('.dov-tools__search input').element as HTMLInputElement).value).toBe('node-011')
    expect(w.findAll('.dov-group__head')[0]!.attributes('aria-expanded')).toBe('false')
  })
})

/* ── 100 台规模：分组 / 筛选 / 排序 / 折叠（就绪板的口径） ───────────── */

/**
 * 100 台规模的桩：两个命名分组（prod 40 / stage 30）+ 未分组 30，含离线与异常，
 * 水位各异。刻意用 ASCII 组名/主机名，让排序断言不依赖 ICU 的中文排序表。
 *
 * 确定性账（各断言依赖它）：
 *   · 离线（idx % 7 === 0）：14 台；prod 5 / stage 5 / 未分组 4
 *   · 异常（idx % 11 === 0）：9 台；prod 3 / stage 3 / 未分组 3
 *   · 77 号既离线又异常（两个筛选项正交的边界样本）
 */
function makeScaleHosts(): DockerHostItem[] {
  const out: DockerHostItem[] = []
  for (let idx = 1; idx <= 100; idx += 1) {
    const group = idx <= 40 ? 'prod' : idx <= 70 ? 'stage' : ''
    out.push(
      makeHost({
        id: `h${idx}`,
        hostname: `node-${String(idx).padStart(3, '0')}`,
        primaryIp: `10.0.0.${idx}`,
        group,
        online: idx % 7 !== 0,
        dockerOk: idx % 11 !== 0,
        containers: idx,
        images: idx * 2,
        // 水位 = idx % 78（0..77，**不含 80/90 的告警档**）：折叠态就是干净的前 10 行，
        // 「告警行不被 N 藏掉」由下面那条专门的用例（自造一台 97% 的机器）来钉。
        diskUsedPercent: idx % 78,
        diskTotalGb: 400,
        disk: {
          imagesMb: idx * 100,
          volumesMb: idx * 10,
          buildCacheMb: idx,
          imagesDanglingMb: idx,
          danglingImages: idx % 3,
          unusedVolumes: idx % 2
        }
      })
    )
  }
  return out
}

function makeScaleOverview(): DockerOverviewResp {
  return makeOverview({
    hosts: makeScaleHosts(),
    fleet: { ...makeOverview().fleet, hosts: { total: 100, dockerOk: 91 } }
  })
}

/** 工具条上的胶囊按钮（筛选开关与排序键同款按钮，按面上的字找）。 */
const findChip = (w: VueWrapper, label: string) =>
  w.findAll('button.dov-chip').find((b) => b.text() === label)!

describe('主机就绪板纯函数（口径错了只表现为「把人藏起来 / 排错位置」）', () => {
  const online = () => makeHost()
  const offline = () => makeHost({ id: 'off', hostname: 'node-off', online: false })
  const bad = () => makeHost({ id: 'bad', hostname: 'node-bad', dockerOk: false })

  it('两条主机级事实正交：离线 ≠ 异常，异常 = Docker 结论 danger（error 或 dockerOk=false）', () => {
    expect(hostIsOffline(online())).toBe(false)
    expect(hostIsAbnormal(online())).toBe(false)
    // 离线的机器若最后一次上报时 Docker 是好的 —— 它是「离线」而不是「异常」。
    expect(hostIsOffline(offline())).toBe(true)
    expect(hostIsAbnormal(offline())).toBe(false)
    // 异常的两条来源：结论句 error（服务端算好的原因）与裸的 dockerOk=false。
    expect(hostIsAbnormal(bad())).toBe(true)
    expect(hostIsAbnormal(makeHost({ error: '读取资源快照失败' }))).toBe(true)
    // 两者可以同时成立（77 号样本）。
    expect(hostIsAbnormal(makeHost({ online: false, dockerOk: false }))).toBe(true)
  })

  it('hostGroupKey：group 去空白即组键，空/缺省/全空白都是未分组（空串组键）', () => {
    expect(hostGroupKey(makeHost({ group: ' prod ' }))).toBe('prod')
    expect(hostGroupKey(makeHost({ group: '' }))).toBe('')
    expect(hostGroupKey(makeHost({ group: '   ' }))).toBe('')
    expect(hostGroupKey(makeHost())).toBe('')
    expect(HOST_GROUP_UNNAMED).toBe('未分组')
  })

  describe('筛选：只看离线 / 只看有异常 / 按名或 IP 搜索', () => {
    const filter = (over: Partial<HostFilter> = {}): HostFilter => ({
      query: '',
      onlyOffline: false,
      onlyAbnormal: false,
      ...over
    })
    const hosts = [makeHost(), offline(), bad()]

    it('未筛选不动集合、也不算「筛选中」（副标题保持分母口径）', () => {
      expect(filterHosts(hosts, filter())).toHaveLength(3)
      expect(hostFilterActive(filter())).toBe(false)
      // 全空白搜索不算筛选中（打字打出一个空格不该把页面切进「显示 M / 共 N」）。
      expect(hostFilterActive(filter({ query: '   ' }))).toBe(false)
    })

    it('只看离线 → 只留离线；只看有异常 → 只留异常；两条叠加是「与」', () => {
      expect(filterHosts(hosts, filter({ onlyOffline: true })).map((h) => h.id)).toEqual(['off'])
      expect(filterHosts(hosts, filter({ onlyAbnormal: true })).map((h) => h.id)).toEqual(['bad'])
      expect(filterHosts(hosts, filter({ onlyOffline: true, onlyAbnormal: true }))).toEqual([])
      // 两条同时成立的那台（既离线又异常）两条筛选都能捞到它 —— 正交不等于互斥。
      const both = makeHost({ id: 'both', hostname: 'node-both', online: false, dockerOk: false })
      const set = [both]
      expect(filterHosts(set, filter({ onlyOffline: true }))).toHaveLength(1)
      expect(filterHosts(set, filter({ onlyAbnormal: true }))).toHaveLength(1)
    })

    it('搜索：主机名 / IP 都用、大小写不敏感；组名不参与匹配（避免「搜组名把整组捞出来」的误读）', () => {
      const prodNode = makeHost({ id: 'p1', hostname: 'Prod-Node-A', group: 'production' })
      expect(hostMatches(prodNode, filter({ query: 'prod-node' }))).toBe(true)
      expect(hostMatches(prodNode, filter({ query: 'PROD-NODE' }))).toBe(true)
      // IP 子串（运维手里拿到的定位信息常常只有地址）
      expect(hostMatches(makeHost(), filter({ query: '12.105' }))).toBe(true)
      expect(hostMatches(makeHost(), filter({ query: '192.168.12.105' }))).toBe(true)
      // 搜组名不该命中（组名不是主机名）：production 这个组名本身不进 name/IP 任一字段。
      expect(
        hostMatches(
          makeHost({ hostname: 'x', primaryIp: '1.2.3.4' }),
          filter({ query: 'production' })
        )
      ).toBe(false)
      // 前后空白被 trim（粘贴带空格的搜索词照常命中）
      expect(hostMatches(prodNode, filter({ query: '  prod-node  ' }))).toBe(true)
    })
  })

  describe('组内排序', () => {
    const hosts = [
      makeHost({ id: 'a', hostname: 'b-host', diskUsedPercent: 10 }),
      makeHost({ id: 'b', hostname: 'a-host', online: false, diskUsedPercent: 95 }),
      makeHost({ id: 'c', hostname: 'c-host', dockerOk: false, diskUsedPercent: 60 }),
      makeHost({ id: 'd', hostname: 'd-host' }) // 未上报水位
    ]

    it('attention（默认，异常优先）：异常 → 离线 → 其余，同级按名称', () => {
      expect(sortHosts(hosts, 'attention').map((h) => h.id)).toEqual(['c', 'b', 'a', 'd'])
    })

    it('disk（磁盘水位）：整机水位降序，未上报殿后（不知道不抢占知道且很高的位置）', () => {
      expect(sortHosts(hosts, 'disk').map((h) => h.id)).toEqual(['b', 'c', 'a', 'd'])
    })

    it('name（名称）：名称升序', () => {
      expect(sortHosts(hosts, 'name').map((h) => h.id)).toEqual(['b', 'a', 'c', 'd'])
    })

    it('排序返回新数组：不改动入参顺序（入参来自 meta.hosts，原地排序会改掉服务端给的序）', () => {
      const input = [...hosts]
      const before = input.map((h) => h.id)
      sortHosts(input, 'attention')
      expect(input.map((h) => h.id)).toEqual(before)
    })

    it('三个排序键都在 UI 选项表里（控件与口径同源）', () => {
      expect(HOST_SORTS.map((s) => s.key)).toEqual(['attention', 'disk', 'name'])
      expect(HOST_SORTS.map((s) => s.label)).toEqual(['异常优先', '磁盘水位', '名称'])
    })
  })

  describe('分组与组头计数', () => {
    const hosts = makeScaleHosts()
    const groups = buildHostGroups(
      hosts,
      { query: '', onlyOffline: false, onlyAbnormal: false },
      'name'
    )

    it('按 host.group 落组，空组归「未分组」且殿后；命名组按名称升序', () => {
      expect(groups.map((g) => g.key)).toEqual(['prod', 'stage', ''])
      expect(groups.map((g) => g.name)).toEqual(['prod', 'stage', '未分组'])
      expect(groups.map((g) => g.hosts.length)).toEqual([40, 30, 30])
    })

    it('组头的三个数在原始集合上算对（共 N 台 / 离线 M / 异常 K）', () => {
      const by = (k: string) => groups.find((g) => g.key === k)!
      expect([by('prod').offline, by('prod').abnormal]).toEqual([5, 3])
      expect([by('stage').offline, by('stage').abnormal]).toEqual([5, 3])
      expect([by('').offline, by('').abnormal]).toEqual([4, 3])
      // 三个组的数加起来 = 全量（组头不丢人）
      expect(groups.reduce((n, g) => n + g.hosts.length, 0)).toBe(100)
      expect(groups.reduce((n, g) => n + g.offline, 0)).toBe(14)
      expect(groups.reduce((n, g) => n + g.abnormal, 0)).toBe(9)
    })

    it('筛选后组头计数随之更新；命中的组留下、没命中的组整组退场（不留空组头）', () => {
      const onlyAbnormal = buildHostGroups(
        hosts,
        { query: '', onlyOffline: false, onlyAbnormal: true },
        'attention'
      )
      expect(onlyAbnormal.map((g) => [g.name, g.hosts.length, g.abnormal])).toEqual([
        ['prod', 3, 3],
        ['stage', 3, 3],
        ['未分组', 3, 3]
      ])
      // 搜索到别处：没有命中的组不出现（也不留一个「共 0 台」的空组头）。
      const found = buildHostGroups(
        hosts,
        { query: 'node-037', onlyOffline: false, onlyAbnormal: false },
        'name'
      )
      expect(found.map((g) => [g.name, g.hosts.length])).toEqual([['prod', 1]])
      expect(found[0]!.hosts[0]!.id).toBe('h37')
    })
  })

  it('折叠默认态阈值：≤12 台全展开（2 台生产环境行为不变）、>12 台组头摘要', () => {
    expect(GROUP_COLLAPSE_AT).toBe(12)
    expect(groupCollapsedByDefault(2)).toBe(false)
    expect(groupCollapsedByDefault(12)).toBe(false)
    expect(groupCollapsedByDefault(13)).toBe(true)
    expect(groupCollapsedByDefault(100)).toBe(true)
  })

  describe('磁盘面板折叠取行（前 N 行 + 全部水位告警行）', () => {
    const row = (pct?: number) => ({
      host: makeHost({ id: `h${pct ?? 'na'}` }),
      water: diskWaterModel(pct === undefined ? makeHost() : makeHost({ diskUsedPercent: pct }))
    })

    it('行数 ≤ N：原样全量（2 台生产环境行序与条数一字不改）', () => {
      const rows = [row(10), row(95)]
      expect(diskVisibleRows(rows, 10).map((r) => r.water.percent)).toEqual([10, 95])
    })

    it('行数 > N：前 N 行 + 全部水位告警行；保持原序（展开只是「看见更多」，行不跳位）', () => {
      // 20 行：水位 0..19（都无告警）→ 只取前 10。
      const flat = Array.from({ length: 20 }, (_, i) => row(i * 4))
      expect(diskVisibleRows(flat, 10)).toHaveLength(10)
      // 第 15 行（下标 14）水位 95%（danger）→ 即便排在 N 之外也被拎出来，
      // 且位置仍在原序里（不提前、不后置）。
      const withAlarm = [...flat]
      withAlarm[14] = row(95)
      const shown = diskVisibleRows(withAlarm, 10)
      expect(shown).toHaveLength(11)
      expect(shown.map((r) => r.water.percent)).toEqual([0, 4, 8, 12, 16, 20, 24, 28, 32, 36, 95])
    })

    it('未上报水位（available=false）不算告警：不冒充 0%、也不抢占前 N 的位置', () => {
      const rows = [...Array.from({ length: 15 }, (_, i) => row(i * 5)), row(undefined)]
      const shown = diskVisibleRows(rows, 10)
      expect(shown).toHaveLength(10)
      expect(shown.some((r) => !r.water.available)).toBe(false)
    })
  })
})

describe('渲染冒烟：100 台规模（分组 / 折叠 / 筛选 / 定位）', () => {
  beforeEach(() => {
    api.fetchDockerOverview.mockResolvedValue(makeScaleOverview())
  })

  it('三个组头各带三个数；大组默认折叠（100 台首屏就是全局账，不是几千像素的卡片墙）', async () => {
    const { w } = await mountOverview()

    const heads = w.findAll('.dov-group__head')
    expect(heads).toHaveLength(3)
    expect(heads[0]!.text()).toContain('prod')
    expect(heads[0]!.text()).toContain('共 40 台')
    expect(heads[0]!.text()).toContain('离线 5')
    expect(heads[0]!.text()).toContain('异常 3')
    expect(heads[2]!.text()).toContain('未分组')
    // 三组都超过阈值 → 默认全折叠：一台卡片都不渲染（首屏高度不随台数线性膨胀）。
    expect(w.findAll('.dov-host')).toHaveLength(0)
    expect(heads.every((h) => h.attributes('aria-expanded') === 'false')).toBe(true)
    // 分区副标题：未筛选时报分母口径（与改造前同一句话）。
    expect(w.find('#dov-hosts .dov-section__sub').text()).toBe('共 100 台')
  })

  it('点组头展开：该组 40 台一次摊开，其余组不受牵连；再点收回', async () => {
    const { w } = await mountOverview()
    const head = w.findAll('.dov-group__head')[0]!

    await head.trigger('click')
    await flush()
    expect(head.attributes('aria-expanded')).toBe('true')
    expect(w.findAll('.dov-host')).toHaveLength(40)
    expect(w.findAll('.dov-hosts')).toHaveLength(1) // 只有展开的那一组有栅格

    await head.trigger('click')
    await flush()
    expect(head.attributes('aria-expanded')).toBe('false')
    expect(w.findAll('.dov-host')).toHaveLength(0)
  })

  it('「只看有异常」：组头计数随筛选更新、命中组自动摊开（范围已经缩小，不该再要多点一次）', async () => {
    const { w } = await mountOverview()

    await findChip(w, '只看有异常').trigger('click')
    await flush()

    const heads = w.findAll('.dov-group__head')
    // 未分组的 3 台异常机里 77 号同时是离线的（idx%7===0 且 idx%11===0）——
    // 组头两列各答一问，同一个集合上算，故这里离线是 1 而不是 0。
    expect(heads.map((h) => h.text().replace(/\s+/g, ''))).toEqual([
      'prod共3台离线0异常3',
      'stage共3台离线0异常3',
      '未分组共3台离线1异常3'
    ])
    // 筛后每组 3 台 ≤ 阈值 → 默认展开，9 台异常主机直接可见。
    expect(w.findAll('.dov-host')).toHaveLength(9)
    expect(w.find('#dov-hosts .dov-section__sub').text()).toBe('显示 9 / 共 100 台')
    // 筛选开关的选中态同步到 aria-pressed（视觉态与无障碍态同源）。
    expect(findChip(w, '只看有异常').attributes('aria-pressed')).toBe('true')
  })

  it('搜索直达：输入 node-037 后第 37 台主机当场可见（不再是「滚到底逐张找」）', async () => {
    const { w } = await mountOverview()
    // 未筛选时第 37 台确实不在面上（大组默认折叠）。
    expect(w.text()).not.toContain('node-037')

    await w.find('.dov-tools__search input').setValue('node-037')
    await flush()

    const cards = w.findAll('.dov-host')
    expect(cards).toHaveLength(1)
    expect(cards[0]!.text()).toContain('node-037')
    expect(w.find('#dov-hosts .dov-section__sub').text()).toBe('显示 1 / 共 100 台')
    // 同一台也能按 IP 找到（运维手里常常只有地址）。
    await w.find('.dov-tools__search input').setValue('10.0.0.37')
    await flush()
    expect(w.findAll('.dov-host')).toHaveLength(1)
  })

  it('手动折叠被后续筛选清空：搜索是定位动作，命中项无条件可见（最坏组合的守卫）', async () => {
    const { w } = await mountOverview()

    // 先把 prod 手动折叠（默认就是折叠 → 点一下反而展开，再点一下收回并留下开关）。
    const head = w.findAll('.dov-group__head')[0]!
    await head.trigger('click') // 展开
    await flush()
    await head.trigger('click') // 收回（此时开关表里 prod = 折叠）
    await flush()
    expect(head.attributes('aria-expanded')).toBe('false')

    // 搜索一台 **在 prod 里** 的主机：若沿用上面那个「手动折叠」，结果就是
    // 「搜到了、面上什么都没有」—— 这正是必须挡住的组合。
    await w.find('.dov-tools__search input').setValue('node-037')
    await flush()

    expect(w.findAll('.dov-group__head')).toHaveLength(1)
    expect(w.findAll('.dov-host')).toHaveLength(1)
    expect(w.find('.dov-host').text()).toContain('node-037')

    // 排序键的变化**不清空**折叠开关（它不改变哪些主机可见）。
    await findChip(w, '名称').trigger('click')
    await flush()
    expect(w.findAll('.dov-host')).toHaveLength(1)
  })

  it('筛到 0 台：一句硬结论（不是空白块），清掉筛选即刻回到全局账', async () => {
    const { w } = await mountOverview()
    await w.find('.dov-tools__search input').setValue('no-such-host')
    await flush()

    expect(w.find('.dov-board__none').text()).toBe('无匹配主机')
    expect(w.findAll('.dov-host')).toHaveLength(0)

    await w.find('.dov-tools__search input').setValue('')
    await flush()
    expect(w.find('.dov-board__none').exists()).toBe(false)
    expect(w.find('#dov-hosts .dov-section__sub').text()).toBe('共 100 台')
  })

  it('组内排序生效：默认「异常优先」把组里出事的顶到最前，切「名称」后按名排', async () => {
    const { w } = await mountOverview()
    await w.findAll('.dov-group__head')[0]!.trigger('click') // 展开 prod（40 台）
    await flush()

    const firstBy = () =>
      w.findAll('.dov-group__head')[0]!.element.parentElement!.querySelectorAll('.dov-host')[0]!
        .textContent!

    // prod 里 idx=11/22/33 是异常主机；姓名序下 prod 的第一台是 node-001。
    expect(firstBy()).toContain('node-011')

    await findChip(w, '名称').trigger('click')
    await flush()
    expect(firstBy()).toContain('node-001')

    await findChip(w, '磁盘水位').trigger('click')
    await flush()
    // 水位 = idx % 78：prod（1..40）里最大是 idx=40 → 40%，故它排最前
    //（按 docker 占用看它是中游 —— 排序键确实是「水位」不是「占用」）。
    expect(firstBy()).toContain('node-040')
  })

  it('100 台时 DOM 不随台数失控（默认折叠态只管 3 个组头）', async () => {
    const { w } = await mountOverview()
    // 折叠态：卡片节点为 0，整页节点数是「组头 + 工具条」的量级。
    expect(w.findAll('.dov-host')).toHaveLength(0)
    expect(w.element.querySelectorAll('#dov-hosts *').length).toBeLessThan(60)
  })

  it('就绪板不写死高度：不含 calc(…px) 与限高常数（本页随内容增高、整页滚动）', () => {
    // views/overview.vue 不在扫描面内：它唯一一处 calc(100% - 40px) 是**注释**里引用的
    // .layout-content 口径（不是本页声明的样式）。本页新增的两个容器（就绪板、磁盘面板）
    // 一律随内容增高。
    for (const rel of [
      'src/modules/docker/components/overview-host-board.vue',
      'src/modules/docker/components/overview-disk-panel.vue'
    ]) {
      const s = readFileSync(join(process.cwd(), rel), 'utf8')
      // 历史病灶：calc(100% - 256px) 这类写死像素高度常数（多分辨率下必栽）。
      expect(s, rel).not.toMatch(/calc\([^)]*px/)
      expect(s, rel).not.toMatch(/max-height/)
    }
    // 页根仍是整页滚动（内容再长也只是滚动，不做裁切）。
    const view = readFileSync(join(process.cwd(), 'src/modules/docker/views/overview.vue'), 'utf8')
    expect(view).toContain('docker-overview art-full-height overflow-y-auto')
    expect(view).not.toMatch(/max-height/)
  })
})

describe('磁盘面板：100 行规模（TOP-N + 展开全部，折叠的是呈现不是事实）', () => {
  beforeEach(() => {
    api.fetchDockerOverview.mockResolvedValue(makeScaleOverview())
  })

  it('>10 行：默认只摆 10 行 + 展开入口（入口上是总行数这句硬数字）', async () => {
    const { w } = await mountOverview()
    const panel = w.find('#dov-disk')

    expect(panel.findAll('.dov-disk__row')).toHaveLength(10)
    const more = panel.find('.dov-disk__more .dov-disk__go')
    expect(more.exists()).toBe(true)
    expect(more.text()).toBe('展开全部 100 行')
  })

  it('展开：100 行原序全摆出，每台的三条占比与结论一条不少；可再收起', async () => {
    const { w } = await mountOverview()
    await w.find('#dov-disk .dov-disk__more .dov-disk__go').trigger('click')
    await flush()

    const rows = w.findAll('.dov-disk__row')
    expect(rows).toHaveLength(100)
    for (const r of rows) {
      expect(r.findAll('.dov-disk__bar')).toHaveLength(3)
      expect(r.find('.dov-disk__reclaim').exists()).toBe(true)
    }
    const collapse = w.find('#dov-disk .dov-disk__more .dov-disk__go')
    expect(collapse.text()).toBe('收起')
    await collapse.trigger('click')
    await flush()
    expect(w.findAll('.dov-disk__row')).toHaveLength(10)
  })

  it('水位告警行不被 N 截断藏掉：盘快满的机器即便 docker 占用小也在折叠态里出现', async () => {
    // 造一台 docker 占用极小、但盘已 97% 的主机（按占用排序它永远进不了 TOP-10）。
    const overview = makeScaleOverview()
    overview.hosts = [
      ...overview.hosts,
      makeHost({
        id: 'tiny-but-full',
        hostname: 'tiny-full',
        group: 'prod',
        diskUsedPercent: 97,
        diskTotalGb: 100,
        disk: {
          imagesMb: 1,
          volumesMb: 1,
          buildCacheMb: 1,
          imagesDanglingMb: 0,
          danglingImages: 0,
          unusedVolumes: 0
        }
      })
    ]
    api.fetchDockerOverview.mockResolvedValue(overview)
    const { w } = await mountOverview()

    const shown = w.findAll('.dov-disk__row')
    expect(shown).toHaveLength(11) // 10 + 那台告警机
    const alarm = shown.find((r) => r.text().includes('tiny-full'))!
    expect(alarm.find('.dov-disk__water').classes()).toContain('is-danger')
  })
})
