import { describe, expect, it } from 'vitest'
import {
  collectInvalid,
  collectInvalidPermissions,
  extractDomains,
  TOKEN_RE
} from './check-permissions.mjs'

/**
 * 守卫自身的单测。
 *
 * 第 2 条是本文件的核心:收紧前 `TOKEN_RE = /system:[a-z]+(?::[a-z]+)+/g` 只认
 * `system:` 前缀,`'device:lst'`(打错)压根不被匹配,于是守卫对整片 device 域
 * 形同虚设 —— 这条断言在收紧前必红。
 *
 * 断言 4 把「非权限码的冒号串不得误报」钉死。核实结论(用旧正则逐条实测):
 *   'agent:device:1001:history' 旧正则行为 = 不匹配(域不是 system);
 *   '12:34:56'                  旧正则行为 = 不匹配(纯数字,旧正则动作段要求 [a-z]+);
 *   'device:metrics:42:history' 旧正则行为 = 不匹配(含数字段);
 * 收紧后它们同样必须不被报出 —— 靠的是**两条防线**:「域不在权限域集合」与
 * 「右边界收紧 `(?![A-Za-z0-9_:])`」。`\b...\b` 写不出「下一字符不是 :」,所以
 * 整体用 `(?![A-Za-z0-9_:])` 收尾。若只写 `\b`,`agent:device:1001:history` 会
 * 退化成前缀匹配 `agent:device`,幸运地仍因域 `agent` 未知而跳过;但
 * `device:metrics:42:history` 会退化成 `device:metrics`,域已知且非合法码 ——
 * **误报**。断言 4 正是钉住这一点。
 */
describe('check-permissions 守卫(权限漂移)', () => {
  const VALID = new Set([
    'device:list',
    'device:query',
    'system:cache:list',
    'system:dept:list',
    'system:user:list'
  ])

  it('1. 正例:validCodes 内任一域的合法码不报错', () => {
    const files = [
      {
        path: 'src/modules/device/views/index.vue',
        text: `<el-button v-perm="'device:list'">启用</el-button>`
      },
      {
        path: 'src/modules/device/api.ts',
        text: `import { PermDeviceQuery } from '@/enums/permission'\n`
      }
    ]
    expect(collectInvalidPermissions(files, VALID)).toEqual([])
    expect(extractDomains(VALID)).toEqual(['device', 'system'])
  })

  it('2. 反例(核心):device 域打错的码必须被抓', () => {
    const files = [
      {
        path: 'src/modules/device/views/index.vue',
        text: `<el-button v-perm="'device:lst'">启用</el-button>`
      }
    ]
    expect(collectInvalidPermissions(files, VALID)).toEqual([
      { token: 'device:lst', file: 'src/modules/device/views/index.vue', line: 1 }
    ])
  })

  it('3. 反例(回归):system 域打错的码必须仍被抓', () => {
    const files = [
      {
        path: 'src/modules/system-dept/views/index.vue',
        text: `const auth = 'system:dept:lst'\n`
      }
    ]
    expect(collectInvalidPermissions(files, VALID)).toEqual([
      {
        token: 'system:dept:lst',
        file: 'src/modules/system-dept/views/index.vue',
        line: 1
      }
    ])
  })

  it('4. 不误报:合法码与形似权限码的冒号串不得被判为漂移', () => {
    const files = [
      { path: 'src/utils/redis-key.ts', text: `const k = 'agent:device:1001:history'` },
      { path: 'src/utils/time.ts', text: `const now = '12:34:56'` },
      { path: 'src/modules/device/api.ts', text: `const t = 'device:metrics:42:history'` },
      { path: 'src/modules/system-monitor/views/index.vue', text: `const ok = 'system:cache:list'` }
    ]
    expect(collectInvalidPermissions(files, VALID)).toEqual([])
    // 把实际行为钉进断言:四串在候选正则下的原始匹配结果
    expect('agent:device:1001:history'.match(TOKEN_RE)).toBeNull()
    expect('12:34:56'.match(TOKEN_RE)).toBeNull()
    expect('device:metrics:42:history'.match(TOKEN_RE)).toBeNull()
    expect('system:cache:list'.match(TOKEN_RE)).toEqual(['system:cache:list'])
  })

  it('5. 逐条报告的 helper 带行号与文件', () => {
    const files = [
      { path: 'src/a.ts', text: `const x = 1\nconst y = 'system:dept:lst'\n` },
      { path: 'src/b.ts', text: `'system:dept:lst'` }
    ]
    expect(collectInvalid(files, VALID)).toEqual([
      { token: 'system:dept:lst', file: 'src/a.ts', line: 2 },
      { token: 'system:dept:lst', file: 'src/b.ts', line: 1 }
    ])
  })

  it('6. 两条防线的边界钉死(域过滤 / 右边界 / 两段式)', () => {
    // 域过滤:域不认识 → 即便形态合规也不报(Redis 键/KV 键不得误报)
    expect(collectInvalid([{ path: 'src/k.ts', text: `'agent:device'` }], VALID)).toEqual([])
    expect(collectInvalid([{ path: 'src/k.ts', text: `'ri:add'` }], VALID)).toEqual([])
    // 形态:域已知、码未知、且是**两段式** → 必须报(旧正则连匹配都做不到)
    expect(collectInvalid([{ path: 'src/a.vue', text: `'device:lst'` }], VALID)).toEqual([
      { token: 'device:lst', file: 'src/a.vue', line: 1 }
    ])
    // 右边界:收紧后长串整段不匹配 → 单/多段候选都不报(否则 device:metrics 会误报)
    expect(
      collectInvalid([{ path: 'src/a.ts', text: `'device:metrics:42:history'` }], VALID)
    ).toEqual([])
    expect(
      collectInvalid([{ path: 'src/a.ts', text: `'xx:device:metrics:42:history'` }], VALID)
    ).toEqual([])
    // 多段式合法码整段匹配,不得被截断成前缀
    expect('system:dict:data:add'.match(TOKEN_RE)).toEqual(['system:dict:data:add'])
    // 前缀的候选若被型号字母/更长的标识符粘连,不得退化命中(如 uicons:ac 里切出 ac:device)
    expect('uicons:ac:device:metrics:42:history'.match(TOKEN_RE)).toBeNull()
  })
})
