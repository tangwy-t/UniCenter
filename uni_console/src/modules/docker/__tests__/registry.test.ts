/**
 * 仓库凭据（4c）纯逻辑、API 封装与入口门控的钉子：
 *   - `utils/registry` 的地址形态校验（协议 IsDockerRegistryAddr 的前端镜像）；
 *   - `api.ts` 四条 CRUD 封装（端点、body 形状、错误口径 —— 就地给结论不弹 toast）；
 *   - images 页「仓库凭据…」入口的 docker:config 门控（源码守卫，与 phase-gate
 *     对 projects 页编辑入口同款口径 —— 本文件保持 node 环境，import.meta.url
 *     才是真实路径；jsdom 下的源码扫描见 phase-gate 的做法）。
 *
 * 组件交互（对话框/下拉）在 registry-dialog.test.ts 与 pull-dialog.test.ts
 * （jsdom + vi.mock）。
 *
 * ── 为什么地址校验要钉得这么细 ──────────────────────────────────────
 * 「凭据键」与拉取时 RegistryAuth 的 ServerAddress 是同一个值：存的时候放走
 * 一个 `https://` 前缀或带路径的形态，拉取受理处就会「存了却标不中」。后端是
 * 权威闸，前端这道是提前一句结论 —— 两边口径漂移才是这里要防的故障。
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@/utils/http', () => ({
  // 只替 request 这一个面：CRUD 封装消费的正是它（get/post/put/del）。
  default: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    del: vi.fn()
  }
}))

// node 环境没有 localStorage（user store 的 worktab 持久化在 import 期就取它）；
// CRUD 测试用不到用户态 —— 桩掉整条 import 链。
vi.mock('@/store/modules/user', () => ({ useUserStore: vi.fn() }))

import request from '@/utils/http'
import {
  createDockerRegistry,
  deleteDockerRegistry,
  fetchDockerRegistries,
  updateDockerRegistry
} from '../api'
import { isValidRegistryAddr } from '../utils/registry'

/** 与 api.ts 同源的口径取前缀（测试命令给 /api/v1；断言不赌具体环境值）。 */
const PREFIX = import.meta.env.VITE_API_PREFIX

const http = request as unknown as {
  get: ReturnType<typeof vi.fn>
  post: ReturnType<typeof vi.fn>
  put: ReturnType<typeof vi.fn>
  del: ReturnType<typeof vi.fn>
}

describe('仓库地址形态（协议镜像）', () => {
  it('主机名 / IP / 带端口都合法（这是键的合法形态全集）', () => {
    expect(isValidRegistryAddr('harbor.example.com')).toBe(true)
    expect(isValidRegistryAddr('Harbor.Example.COM')).toBe(true) // 大写合法（唯一键会规范化成小写）
    expect(isValidRegistryAddr('192.168.1.10:5000')).toBe(true)
    expect(isValidRegistryAddr('nas.lan:8443')).toBe(true)
    expect(isValidRegistryAddr('registry')).toBe(true) // 单标签主机名
    expect(isValidRegistryAddr('my-registry.io:443')).toBe(true)
  })

  it('协议头、路径、非法端口与空串都不合法', () => {
    expect(isValidRegistryAddr('')).toBe(false)
    expect(isValidRegistryAddr('https://harbor.example.com')).toBe(false)
    expect(isValidRegistryAddr('harbor.example.com/app')).toBe(false)
    expect(isValidRegistryAddr('harbor.example.com/')).toBe(false)
    expect(isValidRegistryAddr('harbor.example.com:')).toBe(false)
    expect(isValidRegistryAddr('harbor.example.com:0')).toBe(false) // 端口下界
    expect(isValidRegistryAddr('harbor.example.com:99999')).toBe(false) // 端口上界
    expect(isValidRegistryAddr('harbor.example.com:abc')).toBe(false)
    expect(isValidRegistryAddr('-host.lan')).toBe(false) // 标签不能以连字符开头
    expect(isValidRegistryAddr('host..lan')).toBe(false) // 空标签
    expect(isValidRegistryAddr('a'.repeat(256))).toBe(false) // 字节上限（255）
  })
})

describe('仓库凭据 CRUD 封装（api.ts）', () => {
  it('列表：GET /docker/registries，错误不弹 toast（对话框就地给结论）', async () => {
    http.get.mockResolvedValue({ list: [] })
    await fetchDockerRegistries()
    expect(http.get).toHaveBeenCalledWith({
      url: `${PREFIX}/docker/registries`,
      showErrorMessage: false
    })
  })

  it('创建：POST 四字段 body（remark 允许空串 —— 与后端零值同形）', async () => {
    http.post.mockResolvedValue({})
    await createDockerRegistry({
      registry: 'harbor.example.com',
      username: 'deploy',
      password: 'secret',
      remark: '生产 Harbor'
    })
    expect(http.post).toHaveBeenCalledWith({
      url: `${PREFIX}/docker/registries`,
      data: {
        registry: 'harbor.example.com',
        username: 'deploy',
        password: 'secret',
        remark: '生产 Harbor'
      },
      showErrorMessage: false
    })
  })

  it('更新：PUT 同一端点、与创建同形（registry 是定位键，密码必重输）', async () => {
    http.put.mockResolvedValue({})
    await updateDockerRegistry({
      registry: 'harbor.example.com',
      username: 'deploy2',
      password: 'new-secret',
      remark: ''
    })
    expect(http.put).toHaveBeenCalledWith({
      url: `${PREFIX}/docker/registries`,
      data: {
        registry: 'harbor.example.com',
        username: 'deploy2',
        password: 'new-secret',
        remark: ''
      },
      showErrorMessage: false
    })
  })

  it('删除：DELETE 路径带 registry（不进 body —— 它是资源名不是载荷）', async () => {
    http.del.mockResolvedValue(undefined)
    await deleteDockerRegistry('harbor.example.com:8443')
    expect(http.del).toHaveBeenCalledWith({
      url: `${PREFIX}/docker/registries/harbor.example.com:8443`,
      showErrorMessage: false
    })
  })
})

describe('镜像 tab 入口的门控（源码守卫）', () => {
  it('「仓库凭据…」按钮只对 docker:config 渲染（不渲染 ≠ 禁用）', () => {
    // 7a：镜像页收敛为 resources 页的镜像 tab，门控逻辑平移零改动，扫描目标随迁。
    const src = readFileSync(
      new URL('../components/resources/images-tab.vue', import.meta.url).pathname,
      'utf8'
    )
    expect(src).toContain('仓库凭据…')
    expect(
      /canConfig\s*=\s*computed\(\s*\(\)\s*=>\s*hasAuth\(PermDockerConfig\)\s*\)/.test(src)
    ).toBe(true)
    expect(/v-if="canConfig"/.test(src)).toBe(true)
    // 底栏的渲染条件并入了 canConfig：只有凭据权限（无 manage/delete）的账号
    // 也要进得了这条入口 —— 凭据入口不被镜像写权限档顺带挡掉。
    expect(/v-if="canWrite \|\| canConfig"/.test(src)).toBe(true)
  })
})
