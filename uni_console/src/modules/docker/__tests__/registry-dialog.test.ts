// @vitest-environment jsdom
/**
 * 仓库凭据管理对话框（4c 前端半边）与权限门控的接线测试。
 *
 * 纯逻辑（地址校验）与 CRUD 封装（端点/载荷）在 registry.test.ts；这里钉的是
 * **组件把哪条数据接到哪个控件**：
 *   - 打开 → 拉清单，行渲染（地址等宽/用户名/备注/创建时间/编辑/删除）；
 *   - 新增 → 表单四字段 → POST body 逐字（含 trim 口径）；非法地址/缺必填 →
 *     就地报错不发请求；409（地址已存在）→ 服务端结论句就地显示、留在表单可改；
 *   - 编辑 → 地址是定位键（禁改）、密码**从空开始且必须重输**（后端 PUT 没有
 *     「留空保持原密码」的语义 —— 空密码必被拒，表单先挡一道并讲清楚为什么）；
 *   - 删除 → 标准档确认（目标卡 = 仓库地址，结论句「删除即失效」）→ DELETE
 *     路径带 registry；删除失败走 toast（确认弹窗的回执口径与页面级一致）。
 *
 * 入口/下拉的权限门控在别处：images 页入口的源码守卫在 registry.test.ts；
 * 拉取对话框里凭据下拉的门控在 pull-dialog.test.ts（那里有完整的双通道替身）。
 *
 * 密码纪律的可断言面：输入框 type=password（明文只在输入的此刻存在）；组件
 * 源码不落任何日志（copy-no-internals 之外的纪律靠评审，这里钉能钉的）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { HttpError } from '@/utils/http/error'

const api = vi.hoisted(() => ({
  fetchDockerRegistries: vi.fn(),
  createDockerRegistry: vi.fn(),
  updateDockerRegistry: vi.fn(),
  deleteDockerRegistry: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))

// 删除失败的回执走 toast（与页面级确认弹窗同口径）—— 桩掉 ElMessage，其余 EP
// 组件用真件（对话框交互正是要测的东西）。
const elMessage = vi.hoisted(() => ({ error: vi.fn() }))
vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return { ...actual, ElMessage: elMessage }
})

// DockerActionConfirm 在 setup 里调 useAuth（保护档的「强制」开关判定）—— 桩成
// 全权限；本对话框自身不做权限门控（入口在 images 页，门控用例见最后一个 describe）。
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => true, hasAnyAuth: () => true })
}))

import RegistryCredentialsDialog from '../components/registry-credentials-dialog.vue'

/** 两条凭据的清单（password 恒为掩码 —— 后端契约，前端不渲染密码列）。 */
const ITEMS = [
  {
    registry: 'harbor.example.com',
    username: 'deploy',
    remark: '生产 Harbor',
    password: '****',
    createdAt: 1750000000
  },
  {
    registry: '192.168.1.10:5000',
    username: 'admin',
    remark: '',
    password: '****',
    createdAt: 1750000000
  }
]

const mounted: VueWrapper[] = []

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.fetchDockerRegistries.mockReset()
  api.createDockerRegistry.mockReset()
  api.updateDockerRegistry.mockReset()
  api.deleteDockerRegistry.mockReset()
  elMessage.error.mockClear()
  api.fetchDockerRegistries.mockResolvedValue({ list: ITEMS })
  api.createDockerRegistry.mockResolvedValue(ITEMS[0])
  api.updateDockerRegistry.mockResolvedValue(ITEMS[0])
  api.deleteDockerRegistry.mockResolvedValue(undefined)
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

async function flush() {
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

async function mountDialog() {
  const w = mount(RegistryCredentialsDialog, { props: { modelValue: true } })
  mounted.push(w)
  await flush()
  return w
}

function buttons(w: VueWrapper) {
  return w.findAll('button').map((b) => ({ el: b, text: (b.text() ?? '').trim() }))
}

async function click(w: VueWrapper, label: string) {
  const btn = buttons(w).find((b) => b.text === label)
  expect(btn, `按钮「${label}」应已渲染`).toBeTruthy()
  await btn!.el.trigger('click')
  await flush()
}

/** 凭据行（按仓库地址定位；表头行不含地址文本，天然被滤掉）。 */
function rowOf(w: VueWrapper, registry: string) {
  const row = w.findAll('.rg-row').find((r) => r.text().includes(registry))
  expect(row, `凭据行「${registry}」应已渲染`).toBeTruthy()
  return row!
}

async function clickRowButton(w: VueWrapper, registry: string, label: string) {
  const btn = rowOf(w, registry)
    .findAll('button')
    .find((b) => (b.text() ?? '').trim() === label)
  expect(btn, `行「${registry}」应有「${label}」按钮`).toBeTruthy()
  await btn!.trigger('click')
  await flush()
}

/** 表单输入：按 placeholder 定位（原生 input 的 setValue，与 pull-dialog 同款）。 */
async function fill(w: VueWrapper, placeholder: string, value: string) {
  const el = w.find(`input[placeholder="${placeholder}"]`)
  expect(el.exists(), `输入框「${placeholder}」应已渲染`).toBe(true)
  await el.setValue(value)
  await flush()
  return el
}

describe('列表态', () => {
  it('打开即拉清单；行渲染地址/用户名/备注/创建时间与操作', async () => {
    const w = await mountDialog()
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(1)
    const text = w.text()
    expect(text).toContain('harbor.example.com')
    expect(text).toContain('deploy')
    expect(text).toContain('生产 Harbor')
    expect(text).toContain('192.168.1.10:5000')
    expect(text).toContain('admin')
    expect(text).toContain('密码只进不出') // 顶部用途说明（替不存在的密码列）
    expect(w.find('.rg-row__registry').exists()).toBe(true) // 等宽列在（字体样式见组件 CSS）
  })

  it('空清单给空态（不是「读取失败」也不是空白）', async () => {
    api.fetchDockerRegistries.mockResolvedValue({ list: [] })
    const w = await mountDialog()
    expect(w.text()).toContain('还没有保存任何仓库凭据')
    expect(w.findAll('.rg-row').length).toBe(0)
  })

  it('清单读取失败：就地给结论句 + 重试；重试成功后行回来', async () => {
    api.fetchDockerRegistries.mockRejectedValueOnce(new Error('network down'))
    const w = await mountDialog()
    expect(w.text()).toContain('凭据清单读取失败')
    expect(w.text()).not.toContain('harbor.example.com')

    // 重试是行内链接（与 create-container-drawer 的 retry 同形态），不是按钮
    await w.find('.rg-list__retry').trigger('click')
    await flush()
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(2)
    expect(w.text()).toContain('harbor.example.com')
  })
})

describe('新增凭据', () => {
  it('四字段表单 → POST body 逐字（地址/用户名 trim，密码不 trim）；成功回列表并重拉', async () => {
    const w = await mountDialog()
    await click(w, '新增凭据')

    await fill(w, '例如 harbor.example.com 或 192.168.1.10:5000', '  harbor.example.com ')
    await fill(w, '仓库登录名', ' deploy ')
    await fill(w, '仓库密码', 'p@ss w0rd')
    await fill(w, '可选：这条凭据的用途说明', '测试仓库')
    await click(w, '保存')

    expect(api.createDockerRegistry).toHaveBeenCalledWith({
      registry: 'harbor.example.com', // 首尾空白裁掉（与后端规范化同向）
      username: 'deploy',
      password: 'p@ss w0rd', // 密码原样：空格可能是密码的一部分
      remark: '测试仓库'
    })
    expect(w.text()).toContain('密码只进不出') // 回到列表态（顶部说明回来了）
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(2) // 保存后重拉
  })

  it('密码输入框是 type=password（明文只在输入的这一刻存在）', async () => {
    const w = await mountDialog()
    await click(w, '新增凭据')
    const el = w.find('input[placeholder="仓库密码"]')
    expect((el.element as HTMLInputElement).type).toBe('password')
  })

  it('非法地址（带协议头/路径）就地报错，点保存不发请求', async () => {
    const w = await mountDialog()
    await click(w, '新增凭据')
    await fill(w, '例如 harbor.example.com 或 192.168.1.10:5000', 'https://harbor.example.com')
    expect(w.text()).toContain('仓库地址不合法')

    await fill(w, '仓库登录名', 'deploy')
    await fill(w, '仓库密码', 'pw')
    await click(w, '保存')
    expect(api.createDockerRegistry).not.toHaveBeenCalled()
  })

  it('必填字段空着：点过保存才报必填（没碰过不吭声），不发请求', async () => {
    const w = await mountDialog()
    await click(w, '新增凭据')
    await fill(w, '例如 harbor.example.com 或 192.168.1.10:5000', 'harbor.example.com')
    await fill(w, '仓库登录名', 'deploy')
    // 密码留空

    expect(w.text()).not.toContain('密码必填') // 没点过保存不骂人
    await click(w, '保存')
    expect(w.text()).toContain('密码必填')
    expect(api.createDockerRegistry).not.toHaveBeenCalled()
  })

  it('409（地址已存在）：服务端结论句就地显示，留在表单可改可重试', async () => {
    api.createDockerRegistry.mockRejectedValue(new HttpError('该仓库地址已有凭据', 409))
    const w = await mountDialog()
    await click(w, '新增凭据')
    await fill(w, '例如 harbor.example.com 或 192.168.1.10:5000', 'harbor.example.com')
    await fill(w, '仓库登录名', 'deploy')
    await fill(w, '仓库密码', 'pw')
    await click(w, '保存')

    expect(w.text()).toContain('该仓库地址已有凭据')
    expect(w.text()).toContain('保存') // 还在表单态（可改可重试）
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(1) // 没回列表
  })
})

describe('编辑凭据（密码必须重输 —— 后端 PUT 的实际语义）', () => {
  it('地址是定位键（禁改）、密码从空开始且显式说明「没有留空保持原密码」', async () => {
    const w = await mountDialog()
    await clickRowButton(w, 'harbor.example.com', '编辑')

    const registryInput = w.find(
      'input[placeholder="例如 harbor.example.com 或 192.168.1.10:5000"]'
    )
    expect((registryInput.element as HTMLInputElement).disabled).toBe(true)
    expect((registryInput.element as HTMLInputElement).value).toBe('harbor.example.com')

    const passwordInput = w.find('input[placeholder="必须重新输入（旧密码不可读回）"]')
    expect(passwordInput.exists()).toBe(true)
    expect((passwordInput.element as HTMLInputElement).value).toBe('') // 刻意从空开始
    expect(w.text()).toContain('没有「留空保持原密码」')
    // 列表读回的掩码「****」绝不能被当成密码带回表单
    expect((passwordInput.element as HTMLInputElement).value).not.toBe('****')
  })

  it('密码留空点保存：被挡在前端（后端对空密码必 400），结论句讲清为什么', async () => {
    const w = await mountDialog()
    await clickRowButton(w, 'harbor.example.com', '编辑')
    await click(w, '保存')

    expect(w.text()).toContain('更新必须重新输入密码（旧密码不可读回）')
    expect(api.updateDockerRegistry).not.toHaveBeenCalled()
  })

  it('重输密码后保存：PUT body 定位键 + 整体重写的三字段', async () => {
    const w = await mountDialog()
    await clickRowButton(w, 'harbor.example.com', '编辑')

    await fill(w, '仓库登录名', 'deploy2')
    await fill(w, '必须重新输入（旧密码不可读回）', 'new-pw')
    await fill(w, '可选：这条凭据的用途说明', '生产 Harbor（换号）')
    await click(w, '保存')

    expect(api.updateDockerRegistry).toHaveBeenCalledWith({
      registry: 'harbor.example.com', // 定位键原样带入（不可改）
      username: 'deploy2',
      password: 'new-pw',
      remark: '生产 Harbor（换号）'
    })
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(2) // 回列表重拉
  })

  it('更新 404（别处已删）：结论句就地显示，留在表单', async () => {
    api.updateDockerRegistry.mockRejectedValue(new HttpError('该仓库没有凭据记录', 404))
    const w = await mountDialog()
    await clickRowButton(w, 'harbor.example.com', '编辑')
    await fill(w, '必须重新输入（旧密码不可读回）', 'pw')
    await click(w, '保存')

    expect(w.text()).toContain('该仓库没有凭据记录')
    expect(w.text()).toContain('保存') // 表单还在
  })
})

describe('删除凭据（标准档确认）', () => {
  it('目标卡 = 仓库地址、结论句「删除即失效」；确认后 DELETE 路径带 registry、回列表重拉', async () => {
    const w = await mountDialog()
    await clickRowButton(w, 'harbor.example.com', '删除')

    const confirm = w.findComponent({ name: 'DockerActionConfirm' })
    expect(confirm.exists()).toBe(true)
    expect(confirm.find('.ac-card__target').text()).toBe('harbor.example.com')
    expect(confirm.text()).toContain('删除即失效')
    expect(confirm.text()).toContain('此操作不可恢复')

    // 确认弹窗自己的「删除」按钮（行内的「删除」仍在列表里 —— 按弹窗作用域取）
    const btn = confirm.findAll('button').find((b) => (b.text() ?? '').trim() === '删除')
    expect(btn, '确认弹窗应有「删除」主按钮').toBeTruthy()
    await btn!.trigger('click')
    await flush()

    expect(api.deleteDockerRegistry).toHaveBeenCalledWith('harbor.example.com')
    expect(api.fetchDockerRegistries).toHaveBeenCalledTimes(2) // 删除后重拉
  })

  it('删除失败：确认弹窗收起，结论句走 toast（页面级确认弹窗的回执口径）', async () => {
    api.deleteDockerRegistry.mockRejectedValue(new HttpError('该仓库没有凭据记录', 404))
    const w = await mountDialog()
    await clickRowButton(w, 'harbor.example.com', '删除')

    const confirm = w.findComponent({ name: 'DockerActionConfirm' })
    const btn = confirm.findAll('button').find((b) => (b.text() ?? '').trim() === '删除')
    await btn!.trigger('click')
    await flush()

    expect(elMessage.error).toHaveBeenCalledWith(expect.stringContaining('该仓库没有凭据记录'))
    // 确认弹窗已收起（EP 关闭不销毁 DOM，判 modelValue 而不是判节点存在）
    expect(confirm.props('modelValue')).toBe(false)
  })

  it('确认弹窗的取消：不发删除请求，列表不动', async () => {
    const w = await mountDialog()
    await clickRowButton(w, 'harbor.example.com', '删除')

    const confirm = w.findComponent({ name: 'DockerActionConfirm' })
    const cancel = confirm.findAll('button').find((b) => (b.text() ?? '').trim() === '取消')
    await cancel!.trigger('click')
    await flush()

    expect(api.deleteDockerRegistry).not.toHaveBeenCalled()
    expect(w.text()).toContain('harbor.example.com') // 列表原样
  })
})
