// @vitest-environment jsdom
/**
 * 载入镜像对话框（image-load-dialog）的接线测试 —— 镜像页工具条「载入镜像…」的业务
 * 弹窗：来源二选一（主机本地文件 / 从浏览器上传）+ 载入进行中（状态 + 已用时）+ 终态。
 *
 * 钉的是四条：
 *   - 形态切换干净：切到上传清掉本地文件名；切回清掉已选文件与上传段状态；
 *   - 校验挡提交：未填文件名 / 未选文件时主按钮禁用 + **可见**理由（不藏 title）；
 *     超限/后缀在**选文件的瞬间**就拒（不白传一场）；
 *   - 上传链：选文件 → 上传（真实进度）→ **自动**以返回的 fileName 派发 image:load
 *     （用户不再手填）；载荷形态与既有口径逐字一致（无 target、无 confirm）；
 *   - 实时反馈：进行中给「状态 + 已用时」且**没有**载入进度条（docker load 无逐字节
 *     进度）；终态给结论句（成功 = 文件名 + 耗时；失败 = 原因原文）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { HttpError } from '@/utils/http/error'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  uploadDockerBuildContext: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => false, hasAnyAuth: () => false })
}))

import ImageLoadDialog from '../components/image-load-dialog.vue'

/** image:load 上传的字节上限（**与 uni_protocol.MaxDockerImageLoadBytes 同源、8GB 档**）。 */
const MAX_LOAD_BYTES = 8 * 1024 * 1024 * 1024

/** ArtSvgIcon 的轻量替身（真组件靠 unplugin 注册；测试环境里没有）。 */
const STUBS = {
  ArtSvgIcon: defineComponent({
    name: 'ArtSvgIcon',
    props: { icon: { type: String, default: '' } },
    setup: (props) => () => h('i', { class: 'il-icon-stub', 'data-icon': props.icon })
  })
}

const mounted: VueWrapper[] = []
const refresh = vi.fn()

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  api.sendDockerCmd.mockReset()
  api.fetchDockerCmdResult.mockReset()
  api.uploadDockerBuildContext.mockReset()
  refresh.mockClear()
  api.sendDockerCmd.mockResolvedValue({ ref: 'r1' })
  api.fetchDockerCmdResult.mockResolvedValue({ status: 'pending' })
})

afterEach(() => {
  for (const w of mounted.splice(0)) w.unmount()
  vi.clearAllMocks()
})

const flush = async () => {
  await nextTick()
  await new Promise((r) => setTimeout(r, 0))
  await nextTick()
}

async function mountDialog() {
  const w = mount(ImageLoadDialog, {
    props: { modelValue: true, hostId: 'h1', refresh },
    global: { stubs: STUBS }
  })
  mounted.push(w)
  await flush()
  return w
}

/** 主按钮（按文字），以及它的禁用态。 */
function btn(w: VueWrapper, label: string) {
  const el = w.findAll('button').find((b) => (b.text() ?? '').trim() === label)
  expect(el, `按钮「${label}」应已渲染`).toBeTruthy()
  return el!
}
function isDisabled(w: VueWrapper, label: string) {
  return (btn(w, label).element as HTMLButtonElement).disabled
}
async function click(w: VueWrapper, label: string) {
  await btn(w, label).trigger('click')
  await flush()
}

/** 切来源形态（对 radio group 发模型更新，与页面接线同款）。 */
async function switchMode(w: VueWrapper, mode: 'local' | 'upload') {
  const group = w.findComponent({ name: 'ElRadioGroup' })
  expect(group.exists(), '来源切换（radio）应已渲染').toBe(true)
  group.vm.$emit('update:modelValue', mode)
  await flush()
}

const localInput = (w: VueWrapper) => w.find('input[placeholder="例如 镜像名.tar"]')

/** 模拟选择文件（预检是同步的：后缀/8GB 档，选完立即落定）。 */
async function pickFile(w: VueWrapper, file: File) {
  const input = w.find('input[type="file"]')
  expect(input.exists(), '隐藏的文件选择器应已渲染（上传形态）').toBe(true)
  Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
  await input.trigger('change')
  await flush()
}

describe('形态切换（两种来源互不残留）', () => {
  it('默认「主机本地文件」：文件名输入 + 注册表口径的提示', async () => {
    const w = await mountDialog()
    expect(localInput(w).exists()).toBe(true)
    expect(w.text()).toContain('文件需已放在该主机的 agent 下载目录')
    expect(w.find('input[type="file"]').exists()).toBe(false)
  })

  it('切到上传清掉本地文件名；切回清掉已选文件', async () => {
    const w = await mountDialog()
    await localInput(w).setValue('old.tar')
    await switchMode(w, 'upload')
    // 本地输入整块让位给上传控件。
    expect(localInput(w).exists()).toBe(false)
    expect(w.text()).toContain('选择文件…')

    await pickFile(w, new File([new Uint8Array([1, 2])], 'app.tar'))
    expect(w.text()).toContain('app.tar')

    // 切回本地：已选文件清零，输入框是空的。
    await switchMode(w, 'local')
    expect((localInput(w).element as HTMLInputElement).value).toBe('')

    // 再切回上传：没有残留的已选文件（重新回到「选择文件…」）。
    await switchMode(w, 'upload')
    expect(w.text()).toContain('选择文件…')
    expect(w.find('.il-field__reason').text()).toBe('请选择要上传的文件')
  })
})

describe('校验挡提交（可见理由，不藏 title）', () => {
  it('本地来源：未填文件名时禁用 + 理由可见；填了就放行', async () => {
    const w = await mountDialog()
    expect(isDisabled(w, '载入镜像')).toBe(true)
    expect(w.find('.il-field__reason').text()).toBe('请填写文件名')

    await localInput(w).setValue('img.tar')
    expect(isDisabled(w, '载入镜像')).toBe(false)
    expect(w.find('.il-field__reason').exists()).toBe(false)
  })

  it('上传来源：未选文件时禁用 + 理由可见', async () => {
    const w = await mountDialog()
    await switchMode(w, 'upload')
    expect(isDisabled(w, '载入镜像')).toBe(true)
    expect(w.find('.il-field__reason').text()).toBe('请选择要上传的文件')
  })

  it('上传提示是 8GB 档（不是构建那条 512MB）', async () => {
    const w = await mountDialog()
    await switchMode(w, 'upload')
    expect(w.text()).toContain('≤8GB')
    expect(w.text()).not.toContain('512MB')
  })

  it('后缀不在白名单当场拒，不上传', async () => {
    const w = await mountDialog()
    await switchMode(w, 'upload')

    await pickFile(w, new File([new Uint8Array([1])], 'img.zip'))
    expect(w.text()).toContain('只支持 tar / tar.gz / tgz 文件')
    expect(api.uploadDockerBuildContext).not.toHaveBeenCalled()
    expect(isDisabled(w, '载入镜像')).toBe(true)
  })

  it('尺寸预检用 8GB 档：512MB / 1GB 都放行，超 8GB 才拒（与构建那条 512MB 不同档）', async () => {
    const w = await mountDialog()
    await switchMode(w, 'upload')

    // 512MB —— 构建上下文那条通道的上限，对载入**不是**门槛（不再被本地拦下）。
    const halfGig = new File([new Uint8Array([1])], 'big.tar')
    Object.defineProperty(halfGig, 'size', { value: 512 * 1024 * 1024 })
    await pickFile(w, halfGig)
    expect(w.find('.il-field__error').exists()).toBe(false)
    expect(isDisabled(w, '载入镜像')).toBe(false)

    // 1GB 同样放行。
    const oneGig = new File([new Uint8Array([1])], 'bigger.tar')
    Object.defineProperty(oneGig, 'size', { value: 1024 * 1024 * 1024 })
    await pickFile(w, oneGig)
    expect(w.find('.il-field__error').exists()).toBe(false)
    expect(isDisabled(w, '载入镜像')).toBe(false)

    // 超 8GB 才被本地预检拦下（提示里的数字是 8GB，不是 512MB）。
    const huge = new File([new Uint8Array([1])], 'huge.tar')
    Object.defineProperty(huge, 'size', { value: MAX_LOAD_BYTES + 1 })
    await pickFile(w, huge)
    expect(w.text()).toContain('文件超过 8GB 上限')
    expect(w.text()).not.toContain('512MB')
    expect(api.uploadDockerBuildContext).not.toHaveBeenCalled()
    expect(isDisabled(w, '载入镜像')).toBe(true)
  })
})

describe('本地来源：进行中 + 终态（无载入进度条）', () => {
  it('提交 → 载荷形态一致；进行中给状态 + 已用时且**无进度条**；终态给结论', async () => {
    let resolvePoll!: (v: { status: string; detail?: string }) => void
    api.fetchDockerCmdResult.mockImplementation(
      () => new Promise((resolve) => (resolvePoll = resolve))
    )
    const w = await mountDialog()
    await localInput(w).setValue('my img.tar')
    await click(w, '载入镜像')

    // 载荷：无 target、无 confirm，主参数是文件名（与既有 runWrite 口径逐字一致）。
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:load',
      target: undefined,
      options: { filename: 'my img.tar' }
    })

    // 进行中：状态 + 已用时秒表；**没有**进度条（docker load 不提供逐字节进度）。
    await vi.waitUntil(() => w.find('.il-loading').exists(), { timeout: 5000 })
    expect(w.find('.il-loading__status').text()).toBe('执行中')
    expect(w.find('.il-loading__elapsed').text()).toBe('已用时 0 秒')
    expect(w.findComponent({ name: 'ElProgress' }).exists()).toBe(false)

    // 终态：结论句（文件名 + 耗时）——由轮询结果收尾。
    resolvePoll({ status: 'succeeded', detail: 'ok' })
    await vi.waitUntil(() => w.find('.il-result').exists(), { timeout: 5000 })
    expect(w.text()).toContain('已载入 my img.tar')
    expect(w.text()).toContain('耗时')
    expect(w.find('.il-result').classes()).toContain('is-ok')
  })

  it('终态失败：给服务端的结论句原文', async () => {
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'failed', error: 'open 镜像归档失败' })
    const w = await mountDialog()
    await localInput(w).setValue('broken.tar')
    await click(w, '载入镜像')
    await vi.waitUntil(() => w.find('.il-result').exists(), { timeout: 5000 })
    expect(w.text()).toContain('open 镜像归档失败')
    expect(w.find('.il-result').classes()).toContain('is-fail')
  })

  it('受理失败：留在输入态就地给结论（可改可重试），不进终态', async () => {
    api.sendDockerCmd.mockRejectedValue(new HttpError('该目标上已有同一条操作在执行', 409))
    const w = await mountDialog()
    await localInput(w).setValue('img.tar')
    await click(w, '载入镜像')
    expect(w.find('.il-result').exists()).toBe(false)
    expect(w.find('.il-input').exists()).toBe(true)
    expect(w.text()).toContain('该目标上已有同一条操作在执行')
  })
})

describe('上传来源：上传 → 自动载入', () => {
  it('选文件 → 上传进度（真实百分比）→ 自动以返回的 fileName 派发 load → 进行中/终态', async () => {
    let resolveUpload!: (v: { filename: string }) => void
    api.uploadDockerBuildContext.mockImplementation(
      (_h: string, _f: Blob, onProgress?: (p: number) => void) =>
        new Promise<{ filename: string }>((resolve) => {
          onProgress?.(42)
          resolveUpload = resolve
        })
    )
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })

    const w = await mountDialog()
    await switchMode(w, 'upload')
    await pickFile(w, new File([new Uint8Array([1, 2, 3])], 'app.tar'))
    await click(w, '载入镜像')

    // 上传在途：进度条吃的是回调给的数（与构建对话框同款口径）；此时还没派发载入。
    const call = api.uploadDockerBuildContext.mock.calls.at(-1)!
    expect(call[0]).toBe('h1')
    expect((call[1] as File).name).toBe('app.tar')
    // 用途 = load：服务端据此选 8GB 档尺寸闸（构建上传那条不带该参数 / 缺省 build）。
    expect(call[4]).toBe('load')
    expect(w.findComponent({ name: 'ElProgress' }).props('percentage')).toBe(42)
    expect(api.sendDockerCmd).not.toHaveBeenCalled()

    // 上传成功 → **自动**以服务端产物名派发 image:load（用户不再手填）。
    resolveUpload({ filename: 'load-ctx-9.tar' })
    await vi.waitUntil(() => api.sendDockerCmd.mock.calls.length > 0, { timeout: 5000 })
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:load',
      target: undefined,
      options: { filename: 'load-ctx-9.tar' }
    })

    // 进行中 → 终态。
    await vi.waitUntil(() => w.find('.il-result').exists(), { timeout: 5000 })
    expect(w.text()).toContain('已载入 load-ctx-9.tar')
  })

  it('上传失败：回输入态就地给服务端结论句（可重选重试）', async () => {
    api.uploadDockerBuildContext.mockRejectedValueOnce(
      new HttpError('设备当前离线，无法接收文件', 503)
    )
    const w = await mountDialog()
    await switchMode(w, 'upload')
    await pickFile(w, new File([new Uint8Array([1, 2])], 'app.tar'))
    await click(w, '载入镜像')

    await vi.waitUntil(() => w.text().includes('设备当前离线，无法接收文件'), { timeout: 5000 })
    expect(w.find('.il-input').exists()).toBe(true)
    expect(api.sendDockerCmd).not.toHaveBeenCalled()
  })
})

describe('收口：成功关闭触发重拉', () => {
  it('终态成功后关闭对话框触发页面重拉（新镜像要进列表）', async () => {
    api.fetchDockerCmdResult.mockResolvedValue({ status: 'succeeded' })
    const w = await mountDialog()
    await localInput(w).setValue('img.tar')
    await click(w, '载入镜像')
    await vi.waitUntil(() => w.find('.il-result').exists(), { timeout: 5000 })
    await click(w, '关闭')
    expect(refresh).toHaveBeenCalled()
  })
})
