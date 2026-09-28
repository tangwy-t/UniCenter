/**
 * 守卫：模块内每个 `.vue` 的模板**只能有一个根节点**。
 *
 * ── 为什么需要这条 ──────────────────────────────────────────────────
 *
 * 布局把页面放进 `<Transition mode="out-in">`
 *（`src/components/core/layouts/art-page-content/index.vue`），而 Vue 的 Transition
 * **只支持单根元素**。实测故障（2026-09-28，生产）：项目页当时是
 * `<DockerPage>` 与 `<ElDialog>` 两个兄弟根节点，它作为「离场方」参与一次 out-in
 * 切换后，过渡内部的元素记账坏掉 —— **从该页切到任何其它页面都白屏，必须刷新**，
 * 且此后本模块所有页面都切不出来（其它模块不受影响，因为它们都是单根）。
 * 连带的可见迹象：出口区给组件根节点加的 `.art-page-view` 类在 fragment 根上落不下来。
 *
 * 这类问题的形态很恶劣：控制台无报错（Transition 的告警只在开发构建里）、
 * 单页打开永远正常，只有在「切走」时才炸。故用源码扫描把它变成红灯。
 *
 * ── 检测口径 ────────────────────────────────────────────────────────
 *
 * 取 `<template>` 块 → 剥注释 → 顺序扫描标签，**depth 为 0 时开启的元素即根节点**。
 * 自检：给一段已知双根的模板，必须数出 2（证明检测器不空转）。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const MODULE_ROOT = new URL('..', import.meta.url).pathname
/** HTML 的空元素：写成 `<br>` 也不该让深度增长。 */
const VOID_TAGS = new Set([
  'area',
  'base',
  'br',
  'col',
  'embed',
  'hr',
  'img',
  'input',
  'link',
  'meta',
  'param',
  'source',
  'track',
  'wbr'
])

function vueFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name.startsWith('.')) continue // 工具产物目录（.mimosa 等）
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === '__tests__') continue
      vueFiles(p, out)
    } else if (name.endsWith('.vue')) {
      out.push(p)
    }
  }
  return out
}

/** 取 `<template>` 的**内部内容**（剥掉外层包裹标签本身）——
 *  不剥的话外层 `<template>` 会被当成唯一的根，守卫就空转了：双根文件也会数成 1。 */
function templateOf(src: string): string {
  const start = src.indexOf('<template>')
  if (start < 0) return ''
  const body = src.slice(start + '<template>'.length)
  const end = body.lastIndexOf('</template>')
  return end < 0 ? body : body.slice(0, end)
}

/** 剥掉模板注释（注释里出现标签不该被当成节点）。 */
function stripComments(src: string): string {
  let out = ''
  let i = 0
  while (i < src.length) {
    const at = src.indexOf('<!--', i)
    if (at < 0) {
      out += src.slice(i)
      break
    }
    out += src.slice(i, at)
    const end = src.indexOf('-->', at + 4)
    i = end < 0 ? src.length : end + 3
  }
  return out
}

/** 找标签的收尾 `>`：跳过引号内的内容（属性值里可能有 `>`）。 */
function findTagEnd(src: string, from: number): number {
  let quote = ''
  for (let i = from; i < src.length; i++) {
    const c = src[i]
    if (quote) {
      if (c === quote) quote = ''
      continue
    }
    if (c === '"' || c === "'") {
      quote = c
      continue
    }
    if (c === '>') return i
  }
  return -1
}

/** 取标签名（小写）；不是元素起始（如裸的 `<` 或 `< 1`）时返回空串。 */
function readTagName(raw: string): string {
  let i = raw[1] === '/' ? 2 : 1
  let name = ''
  while (i < raw.length) {
    const c = raw[i]
    if (
      (c >= 'a' && c <= 'z') ||
      (c >= 'A' && c <= 'Z') ||
      c === '-' ||
      c === '.' ||
      (name && c >= '0' && c <= '9')
    ) {
      name += c.toLowerCase()
      i++
      continue
    }
    break
  }
  return /^[a-z]/.test(name) ? name : ''
}

/** 数模板的根节点个数。 */
export function rootCount(template: string): { count: number; tags: string[] } {
  const src = stripComments(template)
  let depth = 0
  let count = 0
  const tags: string[] = []
  let i = 0
  while (i < src.length) {
    const lt = src.indexOf('<', i)
    if (lt < 0) break
    const gt = findTagEnd(src, lt)
    if (gt < 0) break
    const raw = src.slice(lt, gt + 1)
    i = gt + 1
    const name = readTagName(raw)
    if (!name) continue
    if (raw[1] === '/') {
      if (depth > 0) depth--
      continue
    }
    const selfClosed = raw.endsWith('/>') || VOID_TAGS.has(name)
    if (depth === 0) {
      count++
      tags.push('<' + name + '>')
    }
    if (!selfClosed) depth++
  }
  return { count, tags }
}

describe('docker 模块：模板必须单根（否则切页后白屏，必须刷新）', () => {
  it('检测器自身不空转', () => {
    // 传的是 templateOf 的产物（已剥掉外层 <template> 包裹），故这里也只给内部内容
    expect(rootCount('<div>a</div>').count).toBe(1)
    // 这正是修复前 projects.vue 的形态：两个兄弟根（页面 + 对话框）
    expect(rootCount('<DockerPage>a</DockerPage><ElDialog>b</ElDialog>').count).toBe(2)
    expect(rootCount('<!-- c --><div />').count).toBe(1)
    expect(rootCount('<div><br><img src="x"></div>').count).toBe(1)
    expect(rootCount('<div :t="a > b">x</div>').count).toBe(1)
  })

  it.each(vueFiles(MODULE_ROOT))('%s 只有一个根节点', (file) => {
    const { count, tags } = rootCount(templateOf(readFileSync(file, 'utf8')))
    expect(count, `${file} 有 ${count} 个根节点（${tags.join(' ')}）—— 双根会在切页时白屏`).toBe(1)
  })
})
