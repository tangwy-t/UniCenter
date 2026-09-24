/**
 * 守卫：设备模块的**页面文案**不得出现内部术语。
 *
 * ── 为什么需要这条守卫 ─────────────────────────────────────────────
 *
 * 这一页曾经把实现原理直接印在界面上，例如：
 *   「资源下钻只保留 30 天（子表只有 5min 档）：range>2592000 秒会被后端
 *     400 拒绝，故 90 天 / 180 天档位在下钻场景已禁用。」
 *   「断线 / 显示「—」= 未采集（列在 available_metrics 内，但该桶没有样本；
 *     后端空桶不产行，故 t 有洞）。」
 *   「粒度 每 30 秒 · 数据源 热层 · 1827 个采样点」
 *
 * 这些是**给维护者看的**（表怎么分档、响应里有哪些字段、为什么禁用某个档位），
 * 放在代码注释里正合适，摆在页面上则要求读者先懂实现才能用界面 —— 而他只想
 * 知道「这图能信吗、能看多远」。清理时定下的口径是：
 *   页面只留结论（「资源明细仅保留 30 天」「断线表示该时刻未采集」），
 *   原理留在代码里。
 *
 * 靠人记住这条口径是不可靠的（上一条同类文案就是「注释里已经写清楚了，顺手
 * 也放到页面上吧」长出来的），故用源码扫描把它变成红灯。
 *
 * ── 三层检查与各自的边界 ───────────────────────────────────────────
 *
 * 1. **模板文本**：剥掉 `<!--注释-->` 与标签后，剩下的就是会渲染成文字的内容
 *    （`{{ }}` 插值的取值不在扫描范围 —— 它们是运行时值，由第 2 层负责）。
 *    注释里的术语是**允许**的：那正是「原理进注释」的落点。
 * 2. **纯函数产出的文案**：档位禁用原因、数据来源名称等由 `utils/metrics` 生成，
 *    直接对这些函数的返回值断言，比扫描字符串更准确。
 * 3. **结构性不渲染**：图表选型理由（`rationale`）是设计论证，只允许留在数据里；
 *    一旦 `overview-chart-card.vue` 再引用它，立刻红灯。
 *
 * 未覆盖：组件 `<script>` 块里内联的展示字符串（如 chip 的 hover 文案）。
 * 它们不在模板文本里、也不经过纯函数，扫描会与代码注释混淆；这类文案的回归
 * 由各自的行为测试承担（例如 `rangeOptions` 的 disabledReason）。
 *
 * 之所以用源码扫描而不是挂载测试：本仓库的测试环境是 node（无 jsdom /
 * @vue/test-utils），无法挂载组件；而「页面上不能出现某类字符串」本质上是
 * 源码级不变量，正是源码扫描最擅长的形态（与 overview.test.ts 的
 * chartVisible 守卫、根目录 check-permissions.mjs 同思路）。
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { dataSourceLabel, rangeOptions } from '../utils/metrics'

/** 会被渲染成文字的模块文件。 */
const VIEW_FILES = [
  '../components/metrics-panel.vue',
  '../components/resource-drill.vue',
  '../components/overview-chart-card.vue',
  '../components/watermark-bar.vue',
  '../views/detail.vue',
  '../views/overview.vue',
  '../views/index.vue',
  // 升级域的两个新页面（发布物 / 升级任务）：
  // 它们最容易长出的内部术语是原因码原文与「sha256 / HTTP 码」这类协议细节。
  '../views/release.vue',
  '../views/upgrade-task.vue'
]

/**
 * 页面文本里禁止出现的**内部术语**。
 *
 * 只收「读者看不懂、且暴露实现」的词，不收通用词（「采样」「时间窗」这类
 * 正常表述不在此列）。新增一个词就等于新增一条口径，宁可少收也不要收错 ——
 * 误报会让人绕过守卫，漏报只是少一层网。
 */
const FORBIDDEN_PROSE = [
  'available_metrics', // 响应字段名
  '热层', // 内部叫法（Redis 层），页面上只有「实时缓存 / 历史库」
  '空桶', // 后端产桶机制
  '产桶',
  '5min 档', // 表分档
  '采样点', // 会被误读成 Agent 上报条数；页面上统一称「数据点」
  '会被后端拒绝', // 拿 400 边界解释禁用原因
  'range>', // 同上（旧文案原文）
  // ── 升级域（新增）──────────────────────────────────────────────
  'sha256', // 摘要算法名：页面只给「文件校验不通过」这样的结论
  'SHA256',
  'HTTP', // 状态码是排障线索，不进页面（下载端点是设备侧的，页面看不到）
  'ETXTBSY', // 替换二进制时的系统错误名
  'exit code', // 退出码
  'request_id', // 协议字段名
  'reason_code', // 同上（页面只显示翻译后的结论）
  'from_version',
  'to_version',
  'agent_upgrade', // 表/字段前缀
  'pending', // 状态机原始值（页面有中文相位文案）
  'rolled_back',
  'superseded',
  'timeout' // 注意：它同时是一个原因码，页面文案是「超时未完成」
]

/**
 * 页面模板里禁止出现的**内部常量**。
 *
 * 它们只在「把后端边界写给用户看」时才会被引用（`MAX_BUCKETS` 桶数上限、
 * `RANGE_*` 秒数区间、下钻的 400 边界）。注意这里**不禁止**
 * `meta.range_seconds` 这类响应字段 —— 把原始秒数格式化成「24 小时」正是
 * 应当做的事。
 */
const FORBIDDEN_TEMPLATE_IDENTS = [
  'MAX_BUCKETS',
  'RANGE_MIN_SECONDS',
  'RANGE_MAX_SECONDS',
  'DRILL_MAX_RANGE_SECONDS'
]

const readSrc = (rel: string): string => readFileSync(new URL(rel, import.meta.url), 'utf8')

/** 取 SFC 的模板块（`<template>` 到最后一个 `</template>`，嵌套的 template 在内）。 */
function templateOf(src: string): string {
  const start = src.indexOf('<template>')
  const end = src.lastIndexOf('</template>')
  return start < 0 || end < 0 ? src : src.slice(start, end)
}

/**
 * 剥掉 HTML 注释、标签与插值，只留会成为页面文字的内容。
 *
 * 插值（`{{ … }}`）**必须**一并剥掉：里面是可以读响应字段的代码，而读字段
 * 计数（`{{ meta.available_metrics.length }} 列`）是正当用法，不是把字段名
 * 写给用户看 —— 第一版守卫把它判成违规，正是「扫描文案」与「扫描代码」混同
 * 的误报。插值里的取值由第 2 层（纯函数返回值的断言）覆盖；而 `{{ MAX_BUCKETS }}`
 * 这类「把内部常量印出来」的用法由下面那条常量扫描兜住。
 */
function renderedText(template: string): string {
  return template
    .replace(/<!--[\s\S]*?-->/g, '') // 注释允许写术语：那是「原理进注释」的落点
    .replace(/<[^>]*>/g, ' ')
    .replace(/\{\{[\s\S]*?\}\}/g, ' ')
}

describe('设备模块页面文案：不得把实现原理写给用户看', () => {
  it.each(VIEW_FILES)('%s 的模板文本不含内部术语', (rel) => {
    const text = renderedText(templateOf(readSrc(rel)))
    for (const term of FORBIDDEN_PROSE) {
      expect(text, `${rel} 的页面文案出现了内部术语「${term}」`).not.toContain(term)
    }
  })

  it.each(VIEW_FILES)('%s 的模板不引用后端边界常量', (rel) => {
    const template = templateOf(readSrc(rel))
    for (const ident of FORBIDDEN_TEMPLATE_IDENTS) {
      expect(template, `${rel} 的模板引用了内部常量 ${ident}`).not.toContain(ident)
    }
  })

  it('档位禁用原因只讲结论（不写表分档 / 400 边界）', () => {
    const reasons = rangeOptions(true)
      .filter((o) => o.disabled)
      .map((o) => o.disabledReason)
    expect(reasons.length).toBeGreaterThan(0)
    for (const reason of reasons) {
      for (const term of FORBIDDEN_PROSE) {
        expect(reason, `禁用原因「${reason}」含内部术语「${term}」`).not.toContain(term)
      }
    }
  })

  it('数据来源用「实时缓存 / 历史库」，未知来源返回空串（不编造）', () => {
    expect(dataSourceLabel('redis')).toBe('实时缓存')
    expect(dataSourceLabel('db')).toBe('历史库')
    expect(dataSourceLabel('')).toBe('')
    expect(dataSourceLabel(undefined)).toBe('')
    expect(dataSourceLabel('something-else')).toBe('')
  })

  it('图表选型理由（rationale）不被渲染', () => {
    const src = readSrc('../components/overview-chart-card.vue')
    // 只允许留在 utils/overview.ts 的数据里；页面一旦再引用它就红灯。
    expect(src, '选型理由又被渲染到图块提示里了').not.toContain('chart.rationale')
  })
})
