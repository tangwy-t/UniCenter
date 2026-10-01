/**
 * container:create 的表单校验 / 载荷生成 / 命令预览测试（纯函数层）。
 *
 * ── 为什么正反例要「逐字对齐协议」──────────────────────────────────
 * 校验规则是 `uni_protocol/docker.go` 的 validateDockerContainerCreate 的前端镜像
 *（跨语言无法 import，只能靠用例钉住边界）。每个反例都在协议层有对应的拒绝：
 * 前端放行的，协议必然放行（否则用户会看到「表单绿了、提交被拒」的裂缝）；
 * 前端拦下的，协议也必拦（拦协议不拦的属于过度收紧，会挡住合法输入）。
 * 两边规则漂移时，这里与协议测试（docker_test.go 的 create 用例）会各自红灯。
 */
import { describe, expect, it } from 'vitest'
import {
  MAX_CPU_LIMIT,
  MAX_CREATE_LIST_ITEMS,
  MAX_MEM_LIMIT_MB,
  buildCreateOptions,
  buildRunPreview,
  emptyCreateForm,
  hasCreateIssue,
  parseCreatePayload,
  validateCreateForm
} from '../utils/container-create'

/** 快造一张表单（字段缺省 = 空形态）。 */
function form(over: Record<string, unknown> = {}) {
  return { ...emptyCreateForm(), ...over } as ReturnType<typeof emptyCreateForm>
}

/** 镜像本地存在（默认存在：缺失引导单测覆盖）。 */
const exists = () => true

describe('镜像（必填 + dockerImageRefRe 镜像）', () => {
  it('空 = 必填；合法引用（tag / digest / 仓库路径）放行', () => {
    expect(validateCreateForm(form(), exists).image).toBe('镜像必填')
    for (const image of [
      'mysql:8',
      'library/mysql:8',
      'mysql',
      'mysql@sha256:abc123',
      'reg.io:5000/app/web:1.2'
    ]) {
      expect(validateCreateForm(form({ image }), exists).image, `${image} 应合法`).toBe('')
    }
  })

  it('非法形态拦下（协议 dockerImageRefRe 同款边界）；首尾空白先 trim（UI 惯例）', () => {
    // 首字符必须字母数字；空格/中文/分号/前导斜杠/前导冒号都进不了白名单。
    for (const image of ['-mysql:8', '/mysql', '镜像:8', ':mysql', 'mysql 8', 'mysql:8;']) {
      expect(validateCreateForm(form({ image }), exists).image, `${image} 应拦下`).not.toBe('')
    }
    expect(validateCreateForm(form({ image: ' mysql:8 ' }), exists).image).toBe('')
  })

  it('镜像不在所选主机本地 → 给「先拉取」引导（agent 不自动拉取的同一句结论）', () => {
    const issues = validateCreateForm(form({ image: 'mysql:8' }), () => false)
    expect(issues.image).toContain('该主机本地没有这个镜像')
    expect(issues.image).toContain('拉取')
    // 检查器缺席（快照未到）= 不指控：宁可放行让 agent 给结论句。
    expect(validateCreateForm(form({ image: 'mysql:8' })).image).toBe('')
  })
})

describe('容器名（可选 + dockerNameRe 同款）', () => {
  it('空 = 不发；合法名放行', () => {
    expect(validateCreateForm(form({ image: 'mysql:8' }), exists).name).toBe('')
    for (const name of ['web', 'web-1', 'web_1', 'a.b', 'W3', 'uni-center-core']) {
      expect(validateCreateForm(form({ name }), exists).name, `${name} 应合法`).toBe('')
    }
  })

  it('非法名拦下（首字符必须字母数字；斜杠/冒号/下划线开头都不行）', () => {
    for (const name of ['_web', '-web', 'web/1', 'web:1', 'web 1', '1 web']) {
      expect(validateCreateForm(form({ name }), exists).name, `${name} 应拦下`).not.toBe('')
    }
  })
})

describe('端口行（dockerPortBindRe：0 与前导零不合法；范围 1-65535）', () => {
  it('合法行（tcp/udp、边界值 1 与 65535）无结论', () => {
    for (const [host, container] of [
      ['8080', '80'],
      ['1', '1'],
      ['65535', '65535'],
      ['53', '53']
    ]) {
      const issues = validateCreateForm(
        form({ image: 'mysql:8', ports: [{ host, container, proto: 'udp' }] }),
        exists
      )
      expect(issues.ports[0], `${host}:${container} 应合法`).toBe('')
    }
  })

  it('0 / 前导零 / 越界 / 半行都拦下', () => {
    for (const [host, container] of [
      ['0', '80'], // 宿主 0 = 随机端口，协议刻意不支持
      ['007', '80'], // 前导零不在 dockerPortBindRe 白名单
      ['65536', '80'], // 越界（5 位数字由数值判定收掉）
      ['99999', '80'],
      ['80', ''], // 半行：宿主:容器两端都要填
      ['', '80']
    ]) {
      const issues = validateCreateForm(
        form({ image: 'mysql:8', ports: [{ host, container, proto: 'tcp' }] }),
        exists
      )
      expect(issues.ports[0], `${host}:${container} 应拦下`).not.toBe('')
    }
  })
})

describe('环境变量行（dockerEnvRe + 整条 ≤512 字节）', () => {
  it('键是 POSIX 标识符、值可为空', () => {
    for (const [key, value] of [
      ['FOO', 'bar'],
      ['_FOO', ''],
      ['FOO_1', 'with=equals']
    ]) {
      const issues = validateCreateForm(form({ image: 'mysql:8', env: [{ key, value }] }), exists)
      expect(issues.env[0], `${key}=${value} 应合法`).toBe('')
    }
  })

  it('非法键 / 换行值 / 超长条目拦下（长度按字节：中文每字 3 字节）', () => {
    expect(validateCreateForm(form({ env: [{ key: '1FOO', value: '' }] }), exists).env[0]).not.toBe(
      ''
    )
    expect(validateCreateForm(form({ env: [{ key: 'FOO', value: '' }] }), exists).env[0]).toBe('')
    // 换行会污染结果展示、也是伪造「多条记录」的手法 —— 协议明令拒绝。
    expect(
      validateCreateForm(form({ env: [{ key: 'FOO', value: 'a\nb' }] }), exists).env[0]
    ).not.toBe('')
    // 200 个汉字 = 600 字节 > 512（按字符数判会漏掉这条）。
    expect(
      validateCreateForm(form({ env: [{ key: 'FOO', value: '值'.repeat(200) }] }), exists).env[0]
    ).not.toBe('')
  })

  it('重复键给出「后值覆盖前值」的结论（协议不拦，UI 拦手误）', () => {
    const issues = validateCreateForm(
      form({
        env: [
          { key: 'A', value: '1' },
          { key: 'A', value: '2' }
        ]
      }),
      exists
    )
    expect(issues.envDuplicate).toContain('A')
    expect(issues.envDuplicate).toContain('覆盖')
    expect(hasCreateIssue(issues)).toBe(true)
  })
})

describe('挂载行（validateDockerMount：源=命名卷或绝对路径；目的地绝对路径）', () => {
  it('合法形态放行（命名卷 / 绝对路径源；ro 与否）', () => {
    for (const [source, dest, ro] of [
      ['dbdata', '/var/lib/mysql', true],
      ['/data/mysql', '/var/lib/mysql', false],
      ['uni-center_uploads', '/app/uploads', false]
    ]) {
      const issues = validateCreateForm(
        form({ image: 'mysql:8', mounts: [{ source, dest, ro }] }),
        exists
      )
      expect(issues.mounts[0], `${source}:${dest} 应合法`).toBe('')
    }
  })

  it('非法形态拦下（相对路径源 / 非绝对目的地 / 半行 / 超长）', () => {
    for (const [source, dest] of [
      ['bad name', '/data'], // 源既不是合法卷名也不是绝对路径
      ['./data', '/data'],
      ['dbdata', 'data'], // 目的地必须 / 开头
      ['dbdata', ''],
      ['', '/data']
    ]) {
      const issues = validateCreateForm(
        form({ image: 'mysql:8', mounts: [{ source, dest, ro: false }] }),
        exists
      )
      expect(issues.mounts[0], `${source}:${dest} 应拦下`).not.toBe('')
    }
    expect(
      validateCreateForm(
        form({ mounts: [{ source: '/data', dest: `/${'深'.repeat(200)}` }] }),
        exists
      ).mounts[0]
    ).not.toBe('')
  })
})

describe('条数上限（maxDockerCreateListItems = 32）', () => {
  it('32 条恰好放行，33 条给区块级结论', () => {
    const ok = Array.from({ length: 32 }, () => ({
      host: '8080',
      container: '80',
      proto: 'tcp' as const
    }))
    expect(validateCreateForm(form({ ports: ok }), exists).portsCount).toBe('')
    const over = [...ok, { host: '8081', container: '81', proto: 'tcp' as const }]
    expect(validateCreateForm(form({ ports: over }), exists).portsCount).toContain('32')
  })

  it('空行不计数（加了行但没填 ≠ 一条端口映射）', () => {
    const rows = Array.from({ length: 5 }, () => ({
      host: '',
      container: '',
      proto: 'tcp' as const
    }))
    expect(validateCreateForm(form({ ports: rows }), exists).portsCount).toBe('')
    // 但半行会当场显错（错误行不会被静默丢掉）—— 重新校验拿最新结论。
    rows[0]!.host = '8080'
    expect(validateCreateForm(form({ ports: rows }), exists).ports[0]).not.toBe('')
  })
})

describe('资源限额（cpu ≤ 32 核 / mem ≤ 32768MB；0 与留空 = 不限额）', () => {
  it('null / 0 / 合法值放行', () => {
    for (const cpuLimit of [null, 0, 1, 0.5, 16, 32]) {
      expect(
        validateCreateForm(form({ cpuLimit }), exists).cpuLimit,
        `cpu ${cpuLimit} 应合法`
      ).toBe('')
    }
    for (const memLimitMb of [null, 0, 64, 32768]) {
      expect(
        validateCreateForm(form({ memLimitMb }), exists).memLimitMb,
        `mem ${memLimitMb} 应合法`
      ).toBe('')
    }
  })

  it('越界拦下（32.5 核 / 32769MB；NaN 同拒 —— 与协议的数值判定同款）', () => {
    expect(validateCreateForm(form({ cpuLimit: 32.5 }), exists).cpuLimit).not.toBe('')
    expect(validateCreateForm(form({ cpuLimit: -1 }), exists).cpuLimit).not.toBe('')
    expect(validateCreateForm(form({ memLimitMb: 32769 }), exists).memLimitMb).not.toBe('')
    expect(validateCreateForm(form({ memLimitMb: -1 }), exists).memLimitMb).not.toBe('')
  })
})

describe('buildCreateOptions：options 平铺、只发已填字段', () => {
  it('最小载荷只有 image（协议 Required 也只有 image）', () => {
    expect(buildCreateOptions(form({ image: 'mysql:8' }))).toEqual({ image: 'mysql:8' })
  })

  it('全字段形态逐字段核对（snake_case / 条目拼接 / start 只在显式 false 时发）', () => {
    const options = buildCreateOptions(
      form({
        image: 'mysql:8',
        name: 'db',
        restartPolicy: 'always',
        start: false,
        ports: [
          { host: '3306', container: '3306', proto: 'tcp' },
          { host: '53', container: '53', proto: 'udp' }
        ],
        env: [
          { key: 'MYSQL_ROOT_PASSWORD', value: 'secret' },
          { key: 'EMPTY', value: '' }
        ],
        mounts: [
          { source: 'dbdata', dest: '/var/lib/mysql', ro: false },
          { source: '/data/conf', dest: '/etc/mysql/conf.d', ro: true }
        ],
        cpuLimit: 2,
        memLimitMb: 512,
        network: 'app-net'
      })
    )
    expect(options).toEqual({
      image: 'mysql:8',
      name: 'db',
      ports: ['3306:3306', '53:53/udp'], // tcp 是协议缺省，不发 /tcp 后缀
      env: ['MYSQL_ROOT_PASSWORD=secret', 'EMPTY='],
      mounts: ['dbdata:/var/lib/mysql', '/data/conf:/etc/mysql/conf.d:ro'],
      restart_policy: 'always',
      cpu_limit: 2,
      mem_limit_mb: 512,
      network: 'app-net',
      start: false
    })
    // create 无 target：载荷里出现 target 是字段归属错误（协议显式拒绝）。
    expect('target' in options).toBe(false)
  })

  it('默认形态不发 start（缺席 = 创建并启动）、零限额与空串不发', () => {
    const options = buildCreateOptions(
      form({
        image: 'mysql:8',
        start: true,
        cpuLimit: 0,
        memLimitMb: null,
        restartPolicy: '',
        network: ''
      })
    )
    expect(options).toEqual({ image: 'mysql:8' })
  })

  it('空行被跳过、首尾空白被 trim', () => {
    const options = buildCreateOptions(
      form({
        image: ' mysql:8 ',
        name: ' db ',
        ports: [
          { host: '', container: '', proto: 'tcp' },
          { host: '8080', container: '80', proto: 'tcp' }
        ]
      })
    )
    expect(options).toEqual({ image: 'mysql:8', name: 'db', ports: ['8080:80'] })
  })
})

describe('buildRunPreview：实时生成、未填项不出现', () => {
  it('空表单只有动词；start=false 是 docker create（CLI 的「只创建」写法）', () => {
    expect(buildRunPreview(form())).toBe('docker run')
    expect(buildRunPreview(form({ start: false }))).toBe('docker create')
  })

  it('全字段形态的段落顺序与取值（name → p → e → v → restart → cpus → memory → network → image）', () => {
    const cmd = buildRunPreview(
      form({
        image: 'mysql:8',
        name: 'db',
        restartPolicy: 'unless-stopped',
        ports: [
          { host: '3306', container: '3306', proto: 'tcp' },
          { host: '53', container: '53', proto: 'udp' }
        ],
        env: [{ key: 'A', value: 'b' }],
        mounts: [{ source: 'dbdata', dest: '/var/lib/mysql', ro: true }],
        cpuLimit: 2,
        memLimitMb: 512,
        network: 'app-net'
      })
    )
    expect(cmd).toBe(
      'docker run --name db -p 3306:3306 -p 53:53/udp -e A=b -v dbdata:/var/lib/mysql:ro ' +
        '--restart unless-stopped --cpus 2 --memory 512m --network app-net mysql:8'
    )
  })

  it('可选段缺席时不出现（重启策略空 / 限额零 / 网络空）', () => {
    const cmd = buildRunPreview(
      form({ image: 'mysql:8', ports: [{ host: '80', container: '80', proto: 'tcp' }] })
    )
    expect(cmd).toBe('docker run -p 80:80 mysql:8')
    expect(cmd).not.toContain('--restart')
    expect(cmd).not.toContain('--cpus')
    expect(cmd).not.toContain('--memory')
    expect(cmd).not.toContain('--network')
  })

  it('含空白的参数加引号（预览是给高级用户核对的，引号让参数边界可读）', () => {
    const cmd = buildRunPreview(
      form({
        image: 'mysql:8',
        mounts: [{ source: '/data dir', dest: '/var/lib/mysql', ro: false }]
      })
    )
    expect(cmd).toContain("-v '/data dir:/var/lib/mysql'")
  })
})

describe('parseCreatePayload：成功载荷（snake_case）的边界折叠', () => {
  it('id/short_id/started 逐字段读取', () => {
    expect(
      parseCreatePayload({ id: 'abcdef1234567890abcdef', short_id: 'abcdef123456', started: true })
    ).toEqual({ id: 'abcdef1234567890abcdef', shortId: 'abcdef123456', started: true })
  })

  it('形状不符给空结论而不是抛错（载荷来自 agent，页面不该因形状意外崩）', () => {
    expect(parseCreatePayload(null)).toEqual({ id: '', shortId: '', started: false })
    expect(parseCreatePayload({ nope: 1 })).toEqual({ id: '', shortId: '', started: false })
  })
})

describe('常量与协议同源', () => {
  it('条数/CPU/内存上限与协议常量一致（漂移即红灯）', () => {
    expect(MAX_CREATE_LIST_ITEMS).toBe(32)
    expect(MAX_CPU_LIMIT).toBe(32)
    expect(MAX_MEM_LIMIT_MB).toBe(32768)
  })
})
