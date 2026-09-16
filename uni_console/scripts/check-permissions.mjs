#!/usr/bin/env node
/**
 * 权限码漂移守卫(前端侧)。
 *
 * 扫描 uni_console/src 源码(.ts/.vue,排除测试与生成物)中出现的一切权限码
 * 字面量(形如 `<域>:<动作>[:<子动作>…]`),断言它们都在后端生成的
 * `src/enums/permission.ts` 常量子集内。
 * 用于兜底尚未迁移成常量的写法(render 函数的 auth:、hasAuth/hasPermission
 * 调用等):任何一处改名/笔误导致前端引用了一个后端已不存在的权限码,这里会
 * 立刻失败,而不是等到运行时按钮静默消失。
 *
 * 判定规则 = 候选形态(两条防线,缺一不可,见 collectInvalid):
 *  ① 右边界收紧:候选整段只能是 `<小写字母数字段>:<小写字母数字段>…`,
 *     且**后面不得再跟 `:` 或字母数字**。否则 `device:metrics:42:history`
 *     会被退化成前缀匹配 `device:metrics`,凭空造出一个并不存在、却看着
 *     很像权限码的候选(域 `device` 已知 → 误报)。
 *  ② 域过滤:候选的**域**必须出现在权限码生成物的域集合里(从 PERM_FILE
 *     动态抽取)。`agent:device:1001:history` 这类 Redis 键的域 `agent`
 *     不在集合内,直接跳过,不误报。副作用是后端新增域(如 `device`)后
 *     守卫**自动**开始守它,不需要改这个脚本。
 *
 * 与 `pnpm check:api-types` 一起构成跨语言契约守卫。
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const SRC = 'src'
export const PERM_FILE = 'src/enums/permission.ts'

/**
 * 候选权限码正则(方案 a 的「候选形态」)。
 *
 * 历史值 `/system:[a-z]+(?::[a-z]+)+/g` 只认 `system:` 前缀,`device:lst`
 * 这类非 system 域的笔误压根不被匹配;它还要求**两个以上**冒号段,连
 * 一段式的 `device:list` 都匹配不到。
 *
 * 现在:任意域 + 至少一个动作段(两段式 `<域>:<动作>` 起),域/段都是
 * `[a-z][a-z0-9]*`(与生成物里的权限码字符集一致;`system:dict:data:add`
 * 这种四段式照样整段匹配)。
 * 前置 `(?<![A-Za-z0-9_:])` 与后置 `(?![A-Za-z0-9_:])` 把整串钉死,避免命中
 * 一个更长串的前缀或后缀(后缀尤其重要:见文件头防线 ①;前置的 `A-Z` 是为了
 * 不把 `xx:analytics:track` 命中成 `analytics:track`)。
 */
export const TOKEN_RE = /(?<![A-Za-z0-9_:])[a-z][a-z0-9]*(?::[a-z][a-z0-9]*)+(?![A-Za-z0-9_:])/g

/** 递归收集待扫描文件(保持既有排除规则:测试文件/生成物/自身)。 */
export function walk(dir, out = []) {
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

/** 从权限码生成物里抽出全部权限码(既有做法:一切单引号字面量)。 */
export function readCodes(permSource) {
  const valid = new Set()
  for (const m of permSource.matchAll(/'([^']+)'/g)) valid.add(m[1])
  return valid
}

/** 从权限码集合里抽出**域**集合(去重且有序,便于断言与展示)。 */
export function extractDomains(validCodes) {
  const domains = new Set()
  for (const code of validCodes) {
    const i = code.indexOf(':')
    if (i > 0) domains.add(code.slice(0, i))
  }
  return [...domains].sort()
}

/** 候选串是否**整段**都是权限码允许的字符(`小写字母数字` 与 `:`)。 */
function isPermCodeShape(token) {
  return /^[a-z][a-z0-9]*(?::[a-z][a-z0-9]*)+$/.test(token)
}

/** 候选串的域。 */
function domainOf(token) {
  return token.slice(0, token.indexOf(':'))
}

/** 命中位置 → 1 基行号。 */
function lineOf(text, index) {
  let line = 1
  for (let i = 0; i < index; i++) if (text[i] === '\n') line++
  return line
}

/**
 * 纯函数:在 `files`(形如 `{ path, text }`)里找出漂移的权限码。
 *
 * 每个命中返回 `{ token, file, line }`,保持源码顺序。
 * `domains` 缺省时从 `validCodes` 现推。
 */
export function collectInvalid(files, validCodes, domains = extractDomains(validCodes)) {
  const knownDomains = domains instanceof Set ? domains : new Set(domains)
  const out = []
  for (const file of files) {
    for (const m of file.text.matchAll(TOKEN_RE)) {
      const token = m[0]
      if (!isPermCodeShape(token)) continue // 防线 ①(形态)
      if (validCodes.has(token)) continue // 合法码,放行
      if (!knownDomains.has(domainOf(token))) continue // 防线 ②(域过滤)
      out.push({ token, file: file.path, line: lineOf(file.text, m.index) })
    }
  }
  return out
}

/**
 * 纯函数:迁移期兼容视图 —— `collectInvalidPermissions(files, validCodes)`
 * 就是「候选形态 + 域过滤」后的漂移清单,与 `collectInvalid` 同语义。
 */
export function collectInvalidPermissions(files, validCodes) {
  return collectInvalid(files, validCodes)
}

/** 按权限码归并,供脚本打印(既有输出结构:先码,后文件)。 */
export function groupByToken(invalid) {
  const grouped = new Map()
  for (const { token, file, line } of invalid) {
    const at = `${file}:${line}`
    if (!grouped.has(token)) grouped.set(token, [])
    const hits = grouped.get(token)
    if (!hits.includes(at)) hits.push(at)
  }
  return grouped
}

/** 主流程:读生成物 → 扫描 src → 报告。 */
export function run({ src = SRC, permFile = PERM_FILE } = {}) {
  const valid = readCodes(readFileSync(permFile, 'utf8'))
  const domains = extractDomains(valid)
  const files = walk(src).map((p) => ({ path: p, text: readFileSync(p, 'utf8') }))
  const invalid = groupByToken(collectInvalid(files, valid, domains))

  if (invalid.size > 0) {
    console.error('permission drift guard: 发现前端引用了未注册的权限码:')
    for (const [token, at] of invalid) {
      console.error(`  - ${token}`)
      for (const where of at) console.error(`      ${where}`)
    }
    console.error(
      '请在 uni_core/internal/pkg/permission 定义该权限码并重新 `pnpm gen:api`,或修正前端引用。'
    )
    return 1
  }

  console.log(
    `permission drift guard: OK (${valid.size} codes, ${domains.length} domains: ${domains.join('/')}, all frontend tokens valid)`
  )
  return 0
}

// 仅在被当作脚本执行时跑主流程 —— 被单测 import 时不得有任何副作用
// (历史实现直接在模块顶层 process.exit,导入即终止宿主进程)。
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(run())
}
