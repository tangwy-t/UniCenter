#!/usr/bin/env node
/**
 * 权限码漂移守卫(前端侧)。
 *
 * 扫描 uni_console/src 源码(.ts/.vue,排除测试与生成物)中出现的一切 `system:xxx:yyy`
 * 字面量,断言它们都在后端生成的 `src/enums/permission.ts` 常量子集内。
 * 用于兜底尚未迁移成常量的写法(render 函数的 auth:、hasAuth/hasPermission
 * 调用等):任何一处改名/笔误导致前端引用了一个后端已不存在的权限码,这里会
 * 立刻失败,而不是等到运行时按钮静默消失。
 *
 * 与 `pnpm check:api-types` 一起构成跨语言契约守卫。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const SRC = 'src'
const PERM_FILE = 'src/enums/permission.ts'
const TOKEN_RE = /system:[a-z]+(?::[a-z]+)+/g

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) {
      if (!['node_modules', 'dist', 'assets', '__tests__'].includes(name)) walk(p, out)
    } else if (/\.(ts|vue)$/.test(name) && !name.endsWith('.test.ts') && !name.endsWith('.d.ts')) {
      if (p !== PERM_FILE) out.push(p)
    }
  }
  return out
}

const permSource = readFileSync(PERM_FILE, 'utf8')
const valid = new Set()
for (const m of permSource.matchAll(/'([^']+)'/g)) valid.add(m[1])

const invalid = new Map()
for (const file of walk(SRC)) {
  const text = readFileSync(file, 'utf8')
  for (const m of text.matchAll(TOKEN_RE)) {
    const token = m[0]
    if (!valid.has(token)) {
      if (!invalid.has(token)) invalid.set(token, [])
      invalid.get(token).push(file)
    }
  }
}

if (invalid.size === 0) {
  console.log(`permission drift guard: OK (${valid.size} codes, all frontend tokens valid)`)
  process.exit(0)
}

console.error('permission drift guard: 发现前端引用了未注册的权限码:')
for (const [token, files] of invalid) {
  console.error(`  - ${token}`)
  for (const f of files) console.error(`      ${f}`)
}
console.error('请在 uni_core/internal/pkg/permission 定义该权限码并重新 `pnpm gen:api`,或修正前端引用。')
process.exit(1)