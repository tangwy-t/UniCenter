import { describe, expect, it } from 'vitest'
import {
  buildOutput,
  checkRepo,
  collectProblems,
  collectUsedIconNames,
  extractIconNames,
  subsetCollection
} from './offline-icons.mjs'

/**
 * 图标守卫自身的单测。
 *
 * 断言 1–3 钉住"按上下文抽名字"这条命根子:全文抓 `a:b` 会把权限码
 * (`device:query`)、CSS 变体(`md:flex`)、Redis 键(`agent:device:1001:history`)
 * 一起抓进来 —— 实测全文抓法 587 个候选里一半是这类噪音,而噪音一旦进了
 * 清单,`--write` 就会去 `pnpm add -D @iconify-json/device`,把守卫变成笑话。
 *
 * 断言 6 是最后一道闸:拿**真实仓库**跑一遍,源码/种子/预设里出现了未内置
 * 或不存在的图标就红 —— 这就是"改了图标忘了 pnpm icons:sync"的拦截点。
 */
describe('offline-icons 守卫(离线图标清单)', () => {
  it('1. 从 vue 模板里只抽图标上下文的字面量,不吃权限码', () => {
    const text = `
      <ArtSvgIcon icon="ri:home-line" />
      <ArtSvgIcon :icon="drillMode ? 'ri:pie-chart-2-line' : 'ri:line-chart-line'" />
      <ElButton v-perm="'device:query'" icon="tabler:device-desktop-cog">停用</ElButton>
      <div class="md:flex before:content-['']">
    `
    const names = extractIconNames(text, 'source').map((x) => x.name)
    expect(names).toContain('ri:home-line')
    expect(names).toContain('ri:pie-chart-2-line')
    expect(names).toContain('ri:line-chart-line')
    expect(names).toContain('tabler:device-desktop-cog')
    expect(names).not.toContain('device:query')
    expect(names).not.toContain('md:flex')
  })

  it('2. 从 ts 对象属性里抽 icon:,不吃 authMark 与 Redis 键', () => {
    const text = `
      { key: 'cpu', icon: 'ri:cpu-line', authMark: 'system:user:list' }
      // agent:device:1001:history
      { value: 'boxed', label: '定宽', icon: 'ix:width' }
      const NOT_AN_ICON = 'foo:bar'
    `
    const names = extractIconNames(text, 'source').map((x) => x.name)
    // 裸字符串(`'foo:bar'`)不算图标引用:图标一定出现在 icon= / icon: 这两个上下文里
    expect(names).toEqual(['ri:cpu-line', 'ix:width'])
  })

  it('3. 后端菜单种子与选择器预设各有专用抽法', () => {
    const go = `
      {Key: "monitor:pprof", Parent: "monitor", Name: "pprof", Type: "menu",
        Path: "/monitor/pprof", Component: "monitor/pprof/index", Icon: "boxicons:hot"},
    `
    expect(extractIconNames(go, 'go').map((x) => x.name)).toEqual(['boxicons:hot'])

    const presets = `  {
    label: '通用 / 导航',
    icons: [
      'ri:home-line',
      'ri:apps-line'
    ]
  }`
    expect(extractIconNames(presets, 'presets').map((x) => x.name)).toEqual([
      'ri:home-line',
      'ri:apps-line'
    ])
  })

  it('4. 归并同名图标并保留出现位置(供报错定位)', () => {
    const used = collectUsedIconNames([
      { path: 'src/a.vue', text: `\n  <i icon="ri:home-line" />`, kind: 'source' },
      { path: 'src/b.vue', text: `<i icon="ri:home-line" />`, kind: 'source' }
    ])
    expect([...used.get('ri:home-line')].map((p) => p.split(':').pop())).toEqual(['2', '1'])
  })

  it('5. 别名连 parent 一起收,孤立的别名渲染不出来', () => {
    const set = {
      prefix: 'demo',
      width: 24,
      height: 24,
      icons: { parent: { body: '<path d="M0 0"/>' } },
      aliases: { child: { parent: 'parent' } }
    }
    const subset = subsetCollection('demo', ['child'], set)
    expect(Object.keys(subset.icons)).toEqual(['parent'])
    expect(Object.keys(subset.aliases)).toEqual(['child'])
  })

  it('6. 拼写错 / 未内置 / 陈旧 三类问题各自报出', () => {
    const setData = (prefix) => {
      if (prefix === 'ri')
        return { prefix, width: 24, height: 24, icons: { 'home-line': { body: '' } } }
      if (prefix !== 'demo') throw new Error(`图标集 ${prefix} 未安装`)
      return { prefix, width: 24, height: 24, icons: { ok: { body: '' } } }
    }
    const used = new Map([
      ['ri:home-line', ['src/a.vue:1']],
      ['demo:ok', ['src/a.vue:2']],
      ['demo:typo', ['src/a.vue:3']],
      ['other:thing', ['v012_seed.go:46']]
    ])
    const problems = collectProblems(used, ['demo:ok', 'demo:stale'], setData)
    const byKind = (kind) => problems.filter((p) => p.kind === kind).map((p) => p.name)

    // ri 是整套全量内置的集合,不进清单也不该被报"未内置"
    expect(byKind('missing')).toEqual([])
    expect(byKind('not-exist')).toEqual(['demo:typo'])
    expect(byKind('unknown-set')).toEqual(['other:thing'])
    expect(byKind('stale')).toEqual(['demo:stale'])
  })

  it('7. 生成物字节可复现(同样输入 → 同样文本)', () => {
    const setData = () => ({
      prefix: 'demo',
      width: 24,
      height: 24,
      icons: { b: { body: '' }, a: { body: '' } }
    })
    const used = new Map([
      ['demo:b', []],
      ['demo:a', []]
    ])
    const first = buildOutput(used, { loadSetData: setData })
    const second = buildOutput(used, { loadSetData: setData })
    expect(first.text).toBe(second.text)
    expect(first.names).toEqual(['demo:a', 'demo:b'])
  })

  it('8. 实况守卫:仓库里的图标全部有解,且生成物与源码同步', () => {
    const { used, generatedNames, problems } = checkRepo()
    const detail = problems
      .map((p) => `${p.kind}: ${p.name}${p.at.length ? ` @ ${p.at.join(', ')}` : ''}`)
      .join('\n')
    expect(detail).toBe('')
    // 生成物至少覆盖了非 ri 集合用到的图标(数目随源码浮动,只断言"非空且含种子图标")
    expect(generatedNames).toContain('material-symbols:monitoring')
    expect(used.size).toBeGreaterThan(generatedNames.length)
  })
})
