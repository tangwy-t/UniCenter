/**
 * 守卫：docker 模块的**页面文案**不得出现内部术语。
 *
 * 与设备模块的同名守卫（`modules/device/__tests__/copy-no-internals.test.ts`）同一口径 ——
 * 那里的注释记录了这条纪律的来由（页面只留结论，原理与字段名进注释）。这里额外守住
 * Docker 特有的几个词（`docker_ok`、`payload`、`sha256`、`session_id`、`<none>` 这类
 * 原始记号，以及 action 名）。
 *
 * ── 作用面只有「模板渲染文本」一层 ─────────────────────────────────
 * 计入扫描的只有会渲染成文字的内容：先取 `<template>…</template>`，再剥掉
 * `<!--注释-->`、标签（含属性）与 `{{ }}` 插值。因此下列写法**不算页面文字**、不误报：
 *   - 属性与插值里出现的变量名：`{{ syncText(stale, ageSeconds, neverReported) }}`、
 *     `:stale="state?.stale ?? false"`、`:class="staleClass(stale)"`（`stale` 是协议
 *     字段名，页面文案本身说的是「数据陈旧」）；
 *   - 属性里的 class 名：`'HTTP'` 只会以类名/属性形态出现，不会成为给用户看的字。
 * `<script>` 块与代码注释同样不在扫描范围内 —— 原理、字段名与 action 名写在那里正是
 * 本模块的纪律（`utils/cmd.ts` 的 PHASE1_ACTIONS 就是 action 字面量的唯一落点）。
 *
 * ── 为什么跳过隐藏目录 ────────────────────────────────────────────
 * 模块目录里可能落下工具产物（如安全扫描 hook 的 `.mimosa/`，已在 .gitignore 但确实
 * 存在于磁盘上）。它不是源码，遍历它以 `.` 开头的目录一律跳过。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const ROOT = new URL('..', import.meta.url).pathname

const FORBIDDEN = [
  'docker_ok', // 协议字段名
  'payload', // 协议字段名
  'session_id',
  'request_id',
  'reason_code',
  'sha256', // 摘要算法名：页面只说「无标签」与大小
  'SHA256',
  '<none>', // Docker 的原始记号（页面用「无标签（短 id）」）
  'HTTP', // 状态码是排障线索，不进页面
  'exit code',
  'stale', // 协议字段名（页面文案是「数据陈旧」）
  'compose.file:read', // 动作名（页面只说「配置」/「编辑」）
  'container:logs',
  'container:inspect',
  'image:inspect'
]

function templateOf(src: string): string {
  const start = src.indexOf('<template>')
  const end = src.lastIndexOf('</template>')
  return start < 0 || end < 0 ? '' : src.slice(start, end)
}

/** 剥掉注释、标签与插值，只留会成为页面文字的内容。 */
function renderedText(template: string): string {
  return template
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/<[^>]*>/g, ' ')
    .replace(/\{\{[\s\S]*?\}\}/g, ' ')
}

function viewFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === '__tests__') continue
      if (name.startsWith('.')) continue // 工具产物目录（如 .mimosa）：不是源码
      viewFiles(p, out)
    } else if (name.endsWith('.vue')) {
      out.push(p)
    }
  }
  return out
}

describe('docker 模块页面文案：不得把实现细节写给用户看', () => {
  it('扫描不是空转：确实扫到了模板文件', () => {
    const files = viewFiles(ROOT)
    expect(files.length).toBeGreaterThan(0)
    expect(files.some((f) => f.endsWith('components/workload-table.vue'))).toBe(true)
  })

  it('属性名、类名、插值与注释都不算页面文字（分层口径的自检）', () => {
    // 这段模板同时含：属性里的 stale、插值里的 syncText(stale…)、注释里的 sha256，
    // 以及唯一一句真正的页面文字「数据陈旧」。
    const tpl = `<template>
      <div :class="staleClass(stale)" :stale="state?.stale">
        {{ syncText(stale, ageSeconds, neverReported) }}
        <!-- sha256 只该出现在注释里 -->
        <span>数据陈旧</span>
      </div>
    </template>`
    const text = renderedText(templateOf(tpl))
    expect(text).toContain('数据陈旧')
    expect(text).not.toContain('stale')
    expect(text).not.toContain('sha256')
  })

  it.each(viewFiles(ROOT))('%s 的模板文本不含内部术语', (file) => {
    const text = renderedText(templateOf(readFileSync(file, 'utf8')))
    for (const term of FORBIDDEN) {
      expect(text, `${file} 的页面文案出现了内部术语「${term}」`).not.toContain(term)
    }
  })

  it('模板渲染文本不得残留 markdown 强调记号（**）', () => {
    // 插值（{{ }}）吃不了 HTML 标签：文案里残留的 `**` 会原样显示在页面上
    // （同类漏网在设备模块的图卡「用途」提示里出现过一次）。这里只查模板静态
    // 文本 —— 插值取值的来源是模块 .ts 纯函数，不在本文件「只扫模板渲染文本」
    // 的作用面内（见文件头分层说明），那类字符串的回归由各自的行为测试承担。
    for (const file of viewFiles(ROOT)) {
      const text = renderedText(templateOf(readFileSync(file, 'utf8')))
      expect(text, `${file} 的模板文本残留了 markdown 星号（**）`).not.toContain('**')
    }
  })
})
