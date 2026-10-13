/**
 * Wave 3c「单元格单行不变量」守卫（本波坐实的视觉回归，必须当波落守卫）。
 *
 * 缺陷：Wave 3a 做「中段省略」时把若干数据列的 `showOverflowTooltip` 一并删掉，
 * 却不知道它同时提供 `white-space: nowrap` —— 于是超宽内容不再省略号截断，而是**折行**，
 * 把行高堆起来（真机截图：容器表镜像列 3 行、端口列 2 行，行高 49→86px）。这是
 * workload-table.vue 里那条纪律注释（「行内容一律单行」）被实现背叛的典型。
 *
 * 本守卫用**源码扫描**（不是挂载）钉两条不变量：
 *   ① 六个文件里每个「数据列」（有 `prop:` 且非 operation 的列对象）都必须带
 *      `showOverflowTooltip: true`（或显式的单行样式类 `className: '…nowrap…'`）。
 *      selection/index 这类结构性列（用 `type:`，无 `prop:`）与 operation 列豁免。
 *   ② workload-table.vue 的那条纪律注释仍在（防止有人删实现时连注释一起删，
 *      让下一个人以为「本来就该折行」）。
 * 另附：中段省略预算（IMAGE_/PORT_MID_*）的**上界**断言 —— 预算是按真机列宽
 * 校准的，若有人把它调大（重新引入溢出→尾部被 CSS 省略号切掉），这里红灯。
 */
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  IMAGE_MID_HEAD,
  IMAGE_MID_TAIL,
  midEllipsis,
  PORT_MID_HEAD,
  PORT_MID_TAIL
} from '../utils/display'

const DOCKER = join(process.cwd(), 'src/modules/docker')
const FILES = [
  'components/workload-columns.ts',
  'views/images.vue',
  'views/volumes.vue',
  'views/networks.vue',
  'views/projects.vue',
  'views/image-detail.vue'
]

/* ── 极简源码扫描器：提取所有 `{ … }` 对象字面量（跳过字符串/模板/注释） ───── */

/** 从 start（引号）跳到该字符串的收尾引号位置。 */
function skipString(text: string, start: number): number {
  const quote = text[start]!
  let i = start + 1
  while (i < text.length) {
    if (text[i] === '\\') {
      i += 2
      continue
    }
    if (text[i] === quote) return i
    i++
  }
  return text.length - 1
}

/** 从 start（'{'）匹配到配对的 '}' 位置（跳过字符串/模板/注释）。 */
function matchBrace(text: string, start: number): number {
  let depth = 0
  let i = start
  while (i < text.length) {
    const ch = text[i]!
    if (ch === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++
      continue
    }
    if (ch === '/' && text[i + 1] === '*') {
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i++
      i += 2
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      i = skipString(text, i) + 1
      continue
    }
    if (ch === '{') depth++
    else if (ch === '}') {
      depth--
      if (depth === 0) return i
    }
    i++
  }
  return -1
}

/** 文件里所有对象字面量的源码串（含嵌套，逐个递归式收集）。 */
function objectsIn(text: string): string[] {
  const out: string[] = []
  let i = 0
  while (i < text.length) {
    const ch = text[i]!
    if (ch === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++
      continue
    }
    if (ch === '/' && text[i + 1] === '*') {
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i++
      i += 2
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      i = skipString(text, i) + 1
      continue
    }
    if (ch === '{') {
      const end = matchBrace(text, i)
      if (end > i) out.push(text.slice(i, end + 1))
      i++ // 从 '{' 的下一格继续，以便也收集嵌套对象
      continue
    }
    i++
  }
  return out
}

/** 对象字面量是否含某**顶层**键（depth 0 处 `key:`）。 */
function hasTopLevelKey(objText: string, key: string): boolean {
  const n = objText.length - 1 // 排掉收尾 '}'
  let depth = 0
  let i = 1 // 排掉起始 '{'
  while (i < n) {
    const ch = objText[i]!
    if (ch === '/' && objText[i + 1] === '/') {
      while (i < n && objText[i] !== '\n') i++
      continue
    }
    if (ch === '/' && objText[i + 1] === '*') {
      i += 2
      while (i < n && !(objText[i] === '*' && objText[i + 1] === '/')) i++
      i += 2
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') {
      i = skipString(objText, i) + 1
      continue
    }
    if (ch === '{' || ch === '[' || ch === '(') {
      depth++
      i++
      continue
    }
    if (ch === '}' || ch === ']' || ch === ')') {
      depth--
      i++
      continue
    }
    if (depth === 0 && /[A-Za-z_$]/.test(ch)) {
      let j = i
      while (j < n && /[A-Za-z0-9_$]/.test(objText[j]!)) j++
      const ident = objText.slice(i, j)
      let k = j
      while (k < n && /\s/.test(objText[k]!)) k++
      // `prop?:` 的 '?' 会拦在这里 —— 类型字面量不算列定义。
      if (objText[k] === ':') {
        if (ident === key) return true
      }
      i = j
      continue
    }
    i++
  }
  return false
}

/** 对象顶层 `prop: 'xxx'` 的值（无则 null）。 */
function propOf(objText: string): string | null {
  const m = objText.match(/\bprop:\s*'([^']*)'/)
  return m ? m[1]! : null
}

/** 顶层是否挂了显式单行样式类（className 串里含 nowrap 词）。 */
function hasNowrapClass(objText: string): boolean {
  const m = objText.match(/\bclassName:\s*'([^']*)'/)
  return !!m && /(^|[-\s])nowrap\b/.test(m[1]!)
}

/** 某文件的「数据列」对象（有 prop 且非 operation）。 */
function dataColumns(src: string): { prop: string; obj: string }[] {
  return objectsIn(src)
    .filter((obj) => hasTopLevelKey(obj, 'prop'))
    .map((obj) => ({ prop: propOf(obj) ?? '', obj }))
    .filter((c) => c.prop !== '' && c.prop !== 'operation')
}

describe('Wave 3c 单行不变量：每个数据列都带 showOverflowTooltip（或显式单行类）', () => {
  it.each(FILES)('%s 的数据列逐列满足单行不变量', (rel) => {
    const src = readFileSync(join(DOCKER, rel), 'utf8')
    const cols = dataColumns(src)
    expect(cols.length, `${rel} 未扫到任何数据列（扫描器失效？）`).toBeGreaterThan(0)
    const offenders = cols
      .filter((c) => !hasTopLevelKey(c.obj, 'showOverflowTooltip') && !hasNowrapClass(c.obj))
      .map((c) => c.prop)
    expect(
      offenders,
      `${rel} 的这些数据列没有 showOverflowTooltip（会折行堆高行）：${offenders.join('、')}`
    ).toEqual([])
  })

  it('全模块数据列总数 = 39（列集合钉住：新增/删除列会被察觉）', () => {
    const total = FILES.reduce((n, rel) => {
      const src = readFileSync(join(DOCKER, rel), 'utf8')
      return n + dataColumns(src).length
    }, 0)
    expect(total).toBe(39)
  })
})

describe('Wave 3c 纪律注释与实现相符（workload-columns.ts：列集合的家）', () => {
  it('「行内容一律单行」这条纪律注释仍在，且指向本守卫', () => {
    const src = readFileSync(join(DOCKER, 'components/workload-columns.ts'), 'utf8')
    expect(src).toContain('行内容一律**单行**')
    expect(src).toContain('showOverflowTooltip')
    expect(src).toContain('w3c-nowrap.test.ts')
  })

  it('镜像/端口列的中段省略同时挂 title 全文（可见那截以外的信息不丢）', () => {
    const src = readFileSync(join(DOCKER, 'components/workload-columns.ts'), 'utf8')
    // 两列都写成 h('…', { … title: full }, midEllipsis(full, …)) —— 全文由 title 承载。
    expect((src.match(/title: full/g) ?? []).length).toBeGreaterThanOrEqual(2)
  })
})

describe('Wave 3c 中段省略预算是按真机列宽校准的（上界钉住）', () => {
  it('镜像列预算在实测可容纳区间内（真机 156px 可用宽 ≈ 19 字符）', () => {
    expect(IMAGE_MID_HEAD + IMAGE_MID_TAIL).toBeLessThanOrEqual(19)
    expect(IMAGE_MID_HEAD + IMAGE_MID_TAIL).toBeGreaterThanOrEqual(12)
  })

  it('端口列预算在实测可容纳区间内（真机 136px 可用宽 ≈ 16 字符）', () => {
    expect(PORT_MID_HEAD + PORT_MID_TAIL).toBeLessThanOrEqual(16)
    expect(PORT_MID_HEAD + PORT_MID_TAIL).toBeGreaterThanOrEqual(10)
  })

  it('按预算省略后，尾部（tag / `/tcp +N`）整段保留', () => {
    const ref = 'quay.io/minio/minio:RELEASE.2024-01-16T22-43-05Z'
    const imgOut = midEllipsis(ref, IMAGE_MID_HEAD, IMAGE_MID_TAIL)
    expect(imgOut).toContain('…')
    expect(imgOut.endsWith(ref.slice(-IMAGE_MID_TAIL))).toBe(true)

    const port = '20080 → 8088/tcp +3'
    const portOut = midEllipsis(port, PORT_MID_HEAD, PORT_MID_TAIL)
    expect(portOut).toContain('…')
    expect(portOut.endsWith(port.slice(-PORT_MID_TAIL))).toBe(true)
    expect(portOut).toContain('/tcp +3')
  })
})
