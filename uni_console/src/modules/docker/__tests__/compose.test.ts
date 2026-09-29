/**
 * 四期配置编辑的纯函数测试（spec §8/§9）。
 *
 * 被测对象 `utils/compose.ts` 承载双模式编辑器的全部决策：
 *   - YAML ↔ 文档模型往返（服务/网络/卷三段 + `_raw` 原样透传）；
 *   - 变更集生成（原模型 vs 新模型 → 最小 patch，删除用 null）；
 *   - 注释 token 检测（切模式前的防丢失确认）；
 *   - diff 摘要与逐行 diff（保存预览弹窗）；
 *   - 服务模板（redis/mysql/nginx/postgres/空白）。
 *
 * 组件层（compose-editor / form-mode / yaml-mode）只做绑定与编排，决策留在本文件被
 * 测住 —— 与 action-confirm / log-viewer 的测试约定一致（vitest 环境是 node，
 * 不挂载组件）。文件末尾用源码扫描钉住「组件确实接线了这些决策」。
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  addSectionEntry,
  buildComposePatch,
  composeModelIssues,
  countYamlComments,
  diffDetails,
  diffLines,
  diffSummary,
  emptyComposeDoc,
  formatBackupTime,
  formatSize,
  isServiceModified,
  parseComposeDoc,
  parseComposeFilePayload,
  removeSectionEntry,
  serializeComposeDoc,
  SERVICE_TEMPLATES,
  setSectionField,
  setServiceField,
  templateService,
  uniqueServiceName
} from '../utils/compose'

const FIXTURE = `# 顶层注释
services:
  web: # 行内注释
    image: nginx:1.27 # 镜像
    container_name: web
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    environment:
      TZ: Asia/Shanghai
      DEBUG: 1
    depends_on:
      - db
    volumes:
      - uploads:/app/uploads
    command: ["nginx", "-g", "daemon off;"]
    build: ./web
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost"]
      interval: 30s
  db:
    image: mysql:8.0
    ports:
      - "3306:3306"
    volumes:
      - db-data:/var/lib/mysql
    # db 的注释
    deploy:
      resources:
        limits:
          cpus: "1.0"
networks:
  default:
    driver: bridge
  backend: {}
volumes:
  db-data: {}
  uploads:
    driver: local
x-custom: &shared
  key: value
configs:
  my-config:
    file: ./config.yml
version: "3.9"
`

/** 解析成功或测试失败（把 ok 收窄成 doc）。 */
function docOf(text: string) {
  const parsed = parseComposeDoc(text)
  if (!parsed.ok || !parsed.doc) throw new Error(`解析失败：${parsed.error ?? '未知原因'}`)
  return parsed.doc
}

/** 只比较语义内容（字段 + `_raw`），不比较书写顺序。 */
function semantic(doc: ReturnType<typeof docOf>) {
  const pickService = (s: Record<string, unknown>) => {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(s)) {
      if (k === '_raw' || k === '_order') continue
      out[k] = v
    }
    if (Object.keys((s._raw as Record<string, unknown>) ?? {}).length > 0) out._raw = s._raw
    return out
  }
  const section = (sec: { keys: string[]; items: Record<string, Record<string, unknown>> }) =>
    sec.keys.map((k) => [k, pickService(sec.items[k])])
  return {
    services: section(doc.services),
    networks: section(doc.networks),
    volumes: section(doc.volumes),
    _raw: doc._raw
  }
}

describe('YAML → 文档模型（三段 + _raw 透传）', () => {
  it('建模键进模型：镜像/端口/环境变量/命令/依赖/卷/重启/容器名', () => {
    const doc = docOf(FIXTURE)
    expect(doc.services.keys).toEqual(['web', 'db'])
    const web = doc.services.items.web
    expect(web.image).toBe('nginx:1.27')
    expect(web.container_name).toBe('web')
    expect(web.restart).toBe('unless-stopped')
    expect(web.ports).toEqual(['80:80', '443:443'])
    expect(web.environment).toEqual({ TZ: 'Asia/Shanghai', DEBUG: 1 })
    expect(web.depends_on).toEqual(['db'])
    expect(web.volumes).toEqual(['uploads:/app/uploads'])
    expect(web.command).toEqual(['nginx', '-g', 'daemon off;'])
  })

  it('未建模字段进 _raw 原样透传（build/healthcheck/deploy）', () => {
    const doc = docOf(FIXTURE)
    expect(doc.services.items.web._raw).toEqual({
      build: './web',
      healthcheck: { test: ['CMD', 'curl', '-f', 'http://localhost'], interval: '30s' }
    })
    expect(doc.services.items.db._raw).toEqual({
      deploy: { resources: { limits: { cpus: '1.0' } } }
    })
  })

  it('网络/卷分节进模型；顶层未建模键（version/x-*/configs）进 _raw', () => {
    const doc = docOf(FIXTURE)
    expect(doc.networks.keys).toEqual(['default', 'backend'])
    expect(doc.networks.items.default.driver).toBe('bridge')
    expect(doc.volumes.keys).toEqual(['db-data', 'uploads'])
    expect(doc.volumes.items.uploads.driver).toBe('local')
    expect(Object.keys(doc._raw)).toEqual(['x-custom', 'configs', 'version'])
    expect(doc._raw.version).toBe('3.9')
    expect(doc.order).toEqual(['services', 'networks', 'volumes', 'x-custom', 'configs', 'version'])
  })

  it('不进表单的形状落 _raw（长语法端口/条件依赖/缺 = 的环境变量列表）', () => {
    const doc = docOf(`services:
  a:
    image: x
    ports:
      - target: 80
        published: "8080"
    depends_on:
      db:
        condition: service_healthy
    environment:
      - NO_EQUALS
`)
    const a = doc.services.items.a
    expect(a.ports).toBeUndefined()
    expect(a.depends_on).toBeUndefined()
    expect(a.environment).toBeUndefined()
    expect(Object.keys(a._raw)).toEqual(['ports', 'depends_on', 'environment'])
  })

  it('环境变量的列表写法（K=V）归一成映射模型', () => {
    const doc = docOf(`services:
  a:
    image: x
    environment:
      - FOO=bar
      - BAZ=1
`)
    expect(doc.services.items.a.environment).toEqual({ FOO: 'bar', BAZ: '1' })
  })

  it('语法错误：ok=false 且带错误位置（行列）', () => {
    const parsed = parseComposeDoc('services:\n  web:\n    image: [1,2\n')
    expect(parsed.ok).toBe(false)
    expect(parsed.line).toBeGreaterThan(0)
    expect(parsed.error).not.toBe('')
  })

  it('最外层不是映射：ok=false，提示去 YML 模式编辑', () => {
    const parsed = parseComposeDoc('- a\n- b\n')
    expect(parsed.ok).toBe(false)
    expect(parsed.error).toContain('YML')
  })
})

describe('文档模型 → YAML（往返一致）', () => {
  it('parse → serialize → parse：语义内容完全一致', () => {
    const first = docOf(FIXTURE)
    const text = serializeComposeDoc(first)
    const second = docOf(text)
    expect(semantic(second)).toEqual(semantic(first))
  })

  it('序列化保留未建模字段（_raw 与顶层 _raw 都出现在文本里）', () => {
    const text = serializeComposeDoc(docOf(FIXTURE))
    expect(text).toContain('build: ./web')
    expect(text).toContain('healthcheck:')
    expect(text).toContain('x-custom:')
    expect(text).toContain('configs:')
  })

  it('新增段落/条目能序列化出来（原文件没有 volumes 时）', () => {
    const doc = docOf('services:\n  web:\n    image: nginx\n')
    addSectionEntry(doc, 'volumes', 'uploads', { driver: 'local' })
    const text = serializeComposeDoc(doc)
    expect(text).toContain('volumes:')
    expect(text).toContain('uploads:')
    const back = docOf(text)
    expect(back.volumes.keys).toEqual(['uploads'])
    expect(back.volumes.items.uploads.driver).toBe('local')
  })
})

describe('注释 token 检测（切换模式前的防丢失确认）', () => {
  it('数出全部注释行（含行尾注释与文件末尾注释）', () => {
    const text = `# 顶层
services:
  web: # 行内
    image: nginx # 镜像
# 文件末尾
`
    expect(countYamlComments(text)).toBe(4)
  })

  it('引号里的 # 与块标量里的 # 不算注释', () => {
    const text = `services:
  web:
    image: nginx
    command: "echo '# 不是注释'"
    script: |
      # 块标量内容
      echo hi
`
    expect(countYamlComments(text)).toBe(0)
  })

  it('无注释返回 0；语法无效时不误报（交给切换守卫拦）', () => {
    expect(countYamlComments('services:\n  web:\n    image: nginx\n')).toBe(0)
    expect(countYamlComments('a: [1,2\n')).toBe(0)
  })
})

describe('变更集生成（原模型 vs 新模型 → 最小 patch）', () => {
  it('无改动返回 null', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    expect(buildComposePatch(a, b)).toBeNull()
  })

  it('只含被改过的键（未触碰服务不进补丁）', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    setServiceField(b.services.items.web, 'image', 'nginx:1.29')
    const patch = buildComposePatch(a, b)
    expect(patch).toEqual({ services: { web: { image: 'nginx:1.29' } } })
  })

  it('删除键用 null；删除整个服务用条目 null', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    delete b.services.items.web.restart
    removeSectionEntry(b, 'services', 'db')
    const patch = buildComposePatch(a, b)
    expect(patch).toEqual({
      services: { web: { restart: null }, db: null }
    })
  })

  it('新增服务用整条字段（含 _raw 透传的未建模字段）', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    addSectionEntry(b, 'services', 'redis', {
      image: 'redis:7-alpine',
      ports: ['6379:6379']
    })
    const patch = buildComposePatch(a, b)
    expect(patch).toEqual({
      services: { redis: { image: 'redis:7-alpine', ports: ['6379:6379'] } }
    })
  })

  it('环境变量整段重发（patch 语义是替换该键的值块）', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    const env = { ...b.services.items.web.environment, DEBUG: 2 }
    setServiceField(b.services.items.web, 'environment', env)
    const patch = buildComposePatch(a, b)
    expect(patch).toEqual({ services: { web: { environment: { TZ: 'Asia/Shanghai', DEBUG: 2 } } } })
  })

  it('网络/卷分节同样产出最小补丁', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    addSectionEntry(b, 'networks', 'frontend', { driver: 'bridge' })
    removeSectionEntry(b, 'volumes', 'uploads')
    const patch = buildComposePatch(a, b)
    expect(patch).toEqual({
      networks: { frontend: { driver: 'bridge' } },
      volumes: { uploads: null }
    })
  })

  it('空模型（新建服务清空字段）的字段不产生空串补丁', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    setServiceField(b.services.items.db, 'image', '')
    const patch = buildComposePatch(a, b)
    expect(patch).toEqual({ services: { db: { image: null } } })
  })
})

describe('diff 摘要与明细（保存预览弹窗的文案来源）', () => {
  it('摘要：新增/移除/修改的计数结论', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    addSectionEntry(b, 'services', 'redis', { image: 'redis:7-alpine' })
    removeSectionEntry(b, 'services', 'db')
    setServiceField(b.services.items.web, 'image', 'nginx:1.29')
    setServiceField(b.services.items.web, 'restart', 'always')
    expect(diffSummary(a, b)).toBe('将新增 1 个网元、移除 1 个网元、修改 2 处')
  })

  it('无改动时摘要为「没有改动」', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    expect(diffSummary(a, b)).toBe('没有改动')
  })

  it('明细列出每个变更对象与被改的字段标签', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    addSectionEntry(b, 'services', 'redis', { image: 'redis:7-alpine' })
    removeSectionEntry(b, 'services', 'db')
    setServiceField(b.services.items.web, 'image', 'nginx:1.29')
    const details = diffDetails(a, b).join('\n')
    expect(details).toContain('redis')
    expect(details).toContain('新增网元')
    expect(details).toContain('db')
    expect(details).toContain('移除网元')
    expect(details).toContain('镜像')
  })

  it('isServiceModified 区分「已修改卡」与「未修改卡」', () => {
    const a = docOf(FIXTURE)
    const b = docOf(FIXTURE)
    setServiceField(b.services.items.web, 'image', 'nginx:1.29')
    expect(isServiceModified(a, b, 'web')).toBe(true)
    expect(isServiceModified(a, b, 'db')).toBe(false)
  })
})

describe('逐行 diff（YML 模式的差异预览）', () => {
  it('相同/新增/删除三类行', () => {
    const before = 'a: 1\nb: 2\nc: 3\n'
    const after = 'a: 1\nc: 3\nd: 4\n'
    const out = diffLines(before, after)
    expect(out).toEqual([
      { kind: 'same', text: 'a: 1' },
      { kind: 'del', text: 'b: 2' },
      { kind: 'same', text: 'c: 3' },
      { kind: 'add', text: 'd: 4' }
    ])
  })

  it('大文本不炸（超出行数阈值时退化为删除+新增）', () => {
    const before = Array.from({ length: 3000 }, (_, i) => `k${i}: 1`).join('\n')
    const after = Array.from({ length: 3000 }, (_, i) => `k${i}: 2`).join('\n')
    const out = diffLines(before, after)
    expect(out.length).toBe(6000)
  })
})

describe('载荷解析与展示格式', () => {
  it('解析 compose.file:read/write 的载荷（snake_case 的 backups）', () => {
    const view = parseComposeFilePayload({
      content: 'services: {}\n',
      hash: 'a'.repeat(64),
      path: '/opt/uni-center/docker-compose.yml',
      backups: [
        { token: '20260928-221530', hash: 'b'.repeat(64), size_bytes: 14137, at: 1759068930 }
      ]
    })
    expect(view.content).toBe('services: {}\n')
    expect(view.hash).toBe('a'.repeat(64))
    expect(view.path).toBe('/opt/uni-center/docker-compose.yml')
    expect(view.backups).toHaveLength(1)
    expect(view.backups[0]).toEqual({
      token: '20260928-221530',
      hash: 'b'.repeat(64),
      sizeBytes: 14137,
      at: 1759068930
    })
  })

  it('形状意外时返回空视图而不是抛错（页面给「未取到」提示）', () => {
    const view = parseComposeFilePayload(null)
    expect(view).toEqual({ content: '', hash: '', path: '', backups: [] })
  })

  it('体积与时间格式化', () => {
    expect(formatSize(512)).toBe('512 B')
    expect(formatSize(14137)).toBe('13.8 KB')
    expect(formatSize(2 * 1024 * 1024)).toBe('2.0 MB')
    expect(formatBackupTime(1759068930)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
  })
})

describe('服务模板与模型维护助手', () => {
  it('模板齐全（redis/mysql/nginx/postgres/空白），都有镜像或明确说明', () => {
    expect(SERVICE_TEMPLATES.map((t) => t.key)).toEqual([
      'redis',
      'mysql',
      'nginx',
      'postgres',
      'blank'
    ])
    for (const t of SERVICE_TEMPLATES) {
      expect(t.label).not.toBe('')
      expect(t.description).not.toBe('')
    }
    for (const key of ['redis', 'mysql', 'nginx', 'postgres']) {
      const svc = templateService(key)
      expect(svc?.image, `${key} 模板缺镜像`).toBeTruthy()
    }
  })

  it('模板返回深拷贝（改动新服务不会污染模板）', () => {
    const a = templateService('redis')!
    a.ports = []
    const b = templateService('redis')!
    expect(b.ports).toEqual(['6379:6379'])
  })

  it('uniqueServiceName 去重（已有 redis 时给 redis-2）', () => {
    const doc = docOf('services:\n  redis:\n    image: redis\n  redis-2:\n    image: redis\n')
    expect(uniqueServiceName(doc, 'redis')).toBe('redis-3')
    expect(uniqueServiceName(doc, 'web')).toBe('web')
  })

  it('setServiceField/removeSectionEntry 维护键顺序与键集合', () => {
    const doc = docOf(FIXTURE)
    setServiceField(doc.services.items.web, 'cpus', '1.5')
    expect(doc.services.items.web._order).toContain('cpus')
    removeSectionEntry(doc, 'services', 'web')
    expect(doc.services.keys).toEqual(['db'])
    expect(doc.services.items.web).toBeUndefined()
  })

  it('模型体检：缺镜像且没有 build 的服务给结论句', () => {
    const doc = emptyComposeDoc()
    addSectionEntry(doc, 'services', 'redis', {})
    expect(composeModelIssues(doc).join('\n')).toContain('redis')
    addSectionEntry(doc, 'services', 'web', { image: 'nginx' })
    expect(composeModelIssues(doc).join('\n')).toContain('redis')
    expect(composeModelIssues(doc).join('\n')).not.toContain('web')
  })

  it('setSectionField 维护网络/卷条目的键顺序', () => {
    const doc = docOf(FIXTURE)
    setSectionField(doc.networks.items.backend, 'driver', 'bridge')
    expect(doc.networks.items.backend.driver).toBe('bridge')
    expect(doc.networks.items.backend._order).toEqual(['driver'])
  })
})

describe('组件确实接线了这些决策（源码扫描）', () => {
  const read = (rel: string) => readFileSync(new URL(rel, import.meta.url).pathname, 'utf8')

  it('编辑容器：read → 校验 → 预览 → 强确认 → patch/write，应用独立', () => {
    const src = read('../components/compose-editor/compose-editor.vue')
    expect(src).toContain('COMPOSE_ACTIONS.read')
    expect(src).toContain('COMPOSE_ACTIONS.validate')
    expect(src).toContain('COMPOSE_ACTIONS.patch')
    expect(src).toContain('COMPOSE_ACTIONS.write')
    expect(src).toContain('baseHash')
    expect(src).toContain('DockerActionConfirm')
    expect(src).toContain('countYamlComments')
    // 保存与应用分离：应用是独立按钮（compose:up），不随保存自动触发。
    expect(src).toContain('compose:up')
  })

  it('表单模式：模板入口 + 已修改/未修改卡两种标注都在', () => {
    const src = read('../components/compose-editor/form-mode.vue')
    expect(src).toContain('SERVICE_TEMPLATES')
    expect(src).toContain('保存将重写此块')
    expect(src).toContain('未修改，保存时原样保留')
    expect(src).toContain('_raw')
  })

  it('YML 模式：CodeMirror + 语法校验拦截切回表单', () => {
    const src = read('../components/compose-editor/yaml-mode.vue')
    expect(src).toContain('@codemirror/lang-yaml')
    expect(src).toContain('update:valid')
  })
})
