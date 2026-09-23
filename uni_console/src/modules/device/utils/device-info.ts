/**
 * 设备信息表的分组与投影（F-11）。
 *
 * ── 为什么放在 utils 而不是 `.vue` 的 script 块里 ─────────────────
 *
 * 本项目既有约定（见 `__tests__/metrics-panel.test.ts` 顶部注释）：**纯逻辑放
 * `utils/`，组件只做接线**。原因是 `.vue` 一旦被 import，`<script setup>` 里的
 * store / localStorage 依赖就会跟着被求值（实测会因 `localStorage is not defined`
 * 直接让整个测试文件挂掉），于是逻辑的可测性就被组件的依赖面绑死了。
 *
 * 本文件**不 import 任何 Vue / store / api**，可脱 DOM 直接单测。
 * 详情页的模板只用这里的返回值。
 */
import { EMPTY_TEXT, formatMb, formatUnixSeconds, formatUptime } from './display'

export interface InfoItem {
  label: string
  value: string
  /** ElDescriptionsItem 的跨列数（长字段如 CPU 型号需要更宽）。 */
  span?: number
  /** 是否提供「复制」按钮（ID / IP 这类需要粘到别处的值）。 */
  copyable?: boolean
}

export interface InfoGroup {
  name: string
  items: InfoItem[]
}

/** 详情页需要的设备字段子集（只声明用到的，避免与本模块无关的耦合）。 */
export interface DeviceInfoSource {
  id?: string
  hostname?: string
  os?: string
  arch?: string
  platform?: string
  platformVer?: string
  kernel?: string
  cpuModel?: string
  cpuCores?: number
  memTotalMb?: number
  bootTime?: number
  lastSeenAt?: number | null
  watermarkAt?: number | null
  createdAt?: number
  primaryIp?: string
  offlineThresholdSec?: number
}

export interface DeviceInfoOptions {
  /** 磁盘挂载点个数（来自 /resources）。undefined = 未查询，null = 查询失败。 */
  diskCount?: number | null
  /** 注入「现在」以便单测（运行时长依赖它）。 */
  nowMs?: number
}

/**
 * 由设备详情拼「设备信息」分组。
 *
 * 分组的意义：原来 11 项平铺，系统/硬件/时间混排；而线上实测有 8 项为空，
 * 整块看起来像「大面积空洞」。分组后即使全为空，用户也能一眼看出
 * 「缺的是硬件信息」，而不是「这页坏了」。
 *
 * 缺值一律 EMPTY_TEXT（不臆造 0/空串），与列表页/水位同一约定。
 */
export function deviceInfoGroups(
  device: DeviceInfoSource | null | undefined,
  opts: DeviceInfoOptions = {}
): InfoGroup[] {
  const d = device
  const nowMs = opts.nowMs
  // diskCount 三态：undefined=未查（占位符）、null=查失败（占位符）、数字=真实计数。
  // 注意 0 是**有效计数**（查到了，只是没有挂载点），不能用真值判断。
  const diskText = typeof opts.diskCount === 'number' ? `${opts.diskCount} 个` : EMPTY_TEXT

  return [
    {
      name: '系统',
      items: [
        { label: '操作系统', value: d?.os || EMPTY_TEXT },
        { label: '架构', value: d?.arch || EMPTY_TEXT },
        { label: '平台', value: d?.platform || EMPTY_TEXT },
        { label: '平台版本', value: d?.platformVer || EMPTY_TEXT },
        { label: '内核', value: d?.kernel || EMPTY_TEXT }
      ]
    },
    {
      name: '硬件',
      items: [
        { label: 'CPU 型号', value: d?.cpuModel || EMPTY_TEXT, span: 2 },
        {
          label: 'CPU 核数',
          // 0 核不是有效值，与缺值同处理
          value: d?.cpuCores ? `${d.cpuCores} 核` : EMPTY_TEXT
        },
        { label: '内存总量', value: formatMb(d?.memTotalMb) },
        { label: '磁盘挂载点', value: diskText }
      ]
    },
    {
      name: '运行',
      items: [
        { label: '运行时长', value: formatUptime(d?.bootTime, nowMs) },
        { label: '开机时间', value: formatUnixSeconds(d?.bootTime) },
        { label: '最后上报', value: formatUnixSeconds(d?.lastSeenAt) },
        { label: '水位采样', value: formatUnixSeconds(d?.watermarkAt) },
        {
          label: '在线判定',
          // I-5：阈值来自后端，前端不写死 30 —— 阈值可热更，
          // 写死会在运维改配置后与后端判定静默矛盾
          value: d?.offlineThresholdSec ? `${d.offlineThresholdSec} 秒内未上报即离线` : EMPTY_TEXT
        }
      ]
    },
    {
      name: '登记',
      items: [
        { label: '录入时间', value: formatUnixSeconds(d?.createdAt) },
        { label: '设备 IP', value: d?.primaryIp || EMPTY_TEXT, copyable: true },
        { label: '设备 ID', value: d?.id || EMPTY_TEXT, span: 2, copyable: true }
      ]
    }
  ]
}

/** 一组信息里「有值」的项数（隐藏空字段 / 缺失统计共用）。 */
export function countFilled(group: InfoGroup): number {
  return group.items.filter((i) => i.value !== EMPTY_TEXT).length
}

/** 全部信息项里「有值」的项数。 */
export function countFilledAll(groups: readonly InfoGroup[]): number {
  return groups.reduce((n, g) => n + countFilled(g), 0)
}

/** 全部信息项总数。 */
export function countTotal(groups: readonly InfoGroup[]): number {
  return groups.reduce((n, g) => n + g.items.length, 0)
}

/**
 * 「隐藏空字段」开关对应的投影。
 *
 * 整组为空时**整组消失**：只留下一个孤零零的组标题（如「硬件」下面什么都没有）
 * 比显示「—」更让人困惑。
 */
export function filterFilledGroups(groups: readonly InfoGroup[]): InfoGroup[] {
  return groups
    .map((g) => ({ ...g, items: g.items.filter((i) => i.value !== EMPTY_TEXT) }))
    .filter((g) => g.items.length > 0)
}
