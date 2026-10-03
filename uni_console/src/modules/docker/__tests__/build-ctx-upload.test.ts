// @vitest-environment jsdom
/**
 * 构建上下文上传控件（P3·② 端点的消费面）的接线测试 —— build-progress-dialog
 * 输入态「上下文」字段的两种形态（本机上传 / 主机已有文件）与上传段状态机。
 *
 * 纯函数层（后缀白名单 / 512MB 上限常量 / gzip 魔数）的单元测试也在本文件
 * （isGzipFile 要 FileReader，jsdom 才有）；组件侧钉的是：
 *   - 形态切换：默认手填（老路径不被动）；切进上传 = 手填值清零；上传成功后
 *     「改回手动输入」保留产物名（可编辑）、「重新选择」清空重来；
 *   - 选文件即预检（后缀 / 512MB / gzip 魔数）：拒在**发请求之前**，结论就地给；
 *   - 上传在途：进度回调驱动进度条；完成：产物名回填 context（开始构建的载荷
 *     里就是它）；失败：服务端结论句（离线 503 / 超限 400 的原文）就地显示；
 *   - 关对话框收口：在途上传被中止（abort），重开整段归零。
 *
 * 与 build-dialog.test.ts 同一套流/轮询替身；上传走 uploadDockerBuildContext
 * 的 mock（进度回调由 mock 手动驱动 —— XHR 的真实进度事件不在 jsdom 的管辖内）。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { HttpError } from '@/utils/http/error'

const api = vi.hoisted(() => ({
  sendDockerCmd: vi.fn(),
  fetchDockerCmdResult: vi.fn(),
  openDockerBuildStream: vi.fn(),
  uploadDockerBuildContext: vi.fn()
}))
vi.mock('../api', () => ({ ...api, default: undefined }))
vi.mock('@/hooks/core/useAuth', () => ({
  useAuth: () => ({ hasAuth: () => false, hasAnyAuth: () => false })
}))

import BuildProgressDialog from '../components/build-progress-dialog.vue'
import { hasBuildContextSuffix, isGzipFile, MAX_BUILD_CONTEXT_BYTES } from '../utils/build-push'

/** ArtSvgIcon 的轻量替身（与 build-dialog.test.ts 同款：真组件靠 unplugin 注册）。 */
const STUBS = {
  ArtSvgIcon: defineComponent({
    name: 'ArtSvgIcon',
    props: { icon: { type: String, default: '' } },
    setup: (props) => () => h('i', { class: 'bp-icon-stub', 'data-icon': props.icon })
  })
}

const GZIP_HEAD = new Uint8Array([0x1f, 0x8b, 0x08, 0x00])

/** gzip 魔数开头的文件（名字/内容可定制 —— 预检测的是两份不同的事实）。 */
function gzipFile(name = 'app.tar.gz', head = GZIP_HEAD): File {
  return new File([head, new Uint8Array(64)], name)
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
  api.openDockerBuildStream.mockReset()
  api.uploadDockerBuildContext.mockReset()
  refresh.mockClear()
  api.sendDockerCmd.mockResolvedValue({ ref: 'r200' })
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
  const w = mount(BuildProgressDialog, {
    props: { modelValue: true, hostId: 'h1', refresh },
    global: { stubs: STUBS }
  })
  mounted.push(w)
  await flush()
  return w
}

function buttons(w: VueWrapper) {
  return w.findAll('button').map((b) => ({ el: b, text: (b.text() ?? '').trim() }))
}

async function click(w: VueWrapper, label: string) {
  const btn = buttons(w).find((b) => b.text === label || b.text.includes(label))
  expect(btn, `按钮「${label}」应已渲染`).toBeTruthy()
  await btn!.el.trigger('click')
  await flush()
}

/** 切到「上传文件」形态（task-center 同款：对 radio group 发模型更新）。 */
async function switchToUpload(w: VueWrapper) {
  const group = w.findComponent({ name: 'ElRadioGroup' })
  expect(group.exists(), '形态切换（radio）应已渲染').toBe(true)
  group.vm.$emit('update:modelValue', 'upload')
  await flush()
}

/**
 * 模拟选择文件：把 File 塞进隐藏的原生 input 并触发 change。
 *
 * 预检是异步的（gzip 魔数经 FileReader 读切片），落定时机**不能数 flush 轮数**：
 * jsdom 的 FileReader 走三层 setImmediate（check 相位），而 flush 是 setTimeout
 * （timers 相位）—— 两族的相对次序取决于「此刻事件循环在哪个相位」，负载下会漂出
 * 「两轮 flush」的预算，于是偶发「上传还没发生 / 结论句还没上屏」。改成等**判据**：
 * 这一选的两条出路任一出现即算落定 —— 本选发起了上传（调用数增长），或就地换了
 * 结论句（后缀/大小/魔数的拒绝都走它）。
 */
async function pickFile(w: VueWrapper, file: File) {
  const input = w.find('input[type="file"]')
  expect(input.exists(), '隐藏的文件选择器应已渲染（上传形态）').toBe(true)
  Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
  const uploadsBefore = api.uploadDockerBuildContext.mock.calls.length
  const errorBefore = w.find('.bp-field__error').exists() ? w.find('.bp-field__error').text() : ''
  await input.trigger('change')
  await vi.waitUntil(
    () => {
      const errorNow = w.find('.bp-field__error').exists() ? w.find('.bp-field__error').text() : ''
      return (
        api.uploadDockerBuildContext.mock.calls.length > uploadsBefore ||
        (errorNow !== '' && errorNow !== errorBefore)
      )
    },
    { timeout: 5000 }
  )
  await flush()
}

/** 抓取最近一次上传调用的参数（mock 的调用记录）。 */
function lastUploadCall() {
  const call = api.uploadDockerBuildContext.mock.calls.at(-1)
  expect(call, 'uploadDockerBuildContext 应已被调用').toBeTruthy()
  return call as unknown as [
    string,
    File,
    ((p: number) => void) | undefined,
    AbortSignal | undefined
  ]
}

describe('纯函数：预检的尺（与协议/服务端同一个数）', () => {
  it('MAX_BUILD_CONTEXT_BYTES 镜像协议 512<<20（512MB）；后缀白名单含 tar/tar.gz/tgz/gz', () => {
    expect(MAX_BUILD_CONTEXT_BYTES).toBe(512 * 1024 * 1024)
    expect(hasBuildContextSuffix('app.tar')).toBe(true)
    expect(hasBuildContextSuffix('app.tar.gz')).toBe(true)
    expect(hasBuildContextSuffix('app.tgz')).toBe(true)
    expect(hasBuildContextSuffix('app.gz')).toBe(true)
    expect(hasBuildContextSuffix('APP.TAR.GZ')).toBe(true) // 大小写不敏感（文件系统语义）
    expect(hasBuildContextSuffix('app.zip')).toBe(false)
    expect(hasBuildContextSuffix('app.tar.bz2')).toBe(false)
    // 后缀检查不看路径成分 —— 那是 isValidBuildContextFilename 的管辖（两把尺分工）。
    expect(hasBuildContextSuffix('dir/app.tar')).toBe(true)
  })

  it('isGzipFile：头两字节 1f 8b 放行；文本/短文件/魔数错位都拒', async () => {
    expect(await isGzipFile(new File([new Uint8Array([0x1f, 0x8b])], 'a.gz'))).toBe(true)
    expect(await isGzipFile(new File([new Uint8Array([0x8b, 0x1f])], 'a.gz'))).toBe(false)
    expect(await isGzipFile(new File([new Uint8Array([0x50, 0x4b])], 'a.zip'))).toBe(false)
    expect(await isGzipFile(new File([new Uint8Array([0x1f])], 'a.gz'))).toBe(false)
  })
})

describe('形态切换', () => {
  it('默认手填（老路径不被动）：输入框与提示都是原文案', async () => {
    const w = await mountDialog()
    expect(w.find('input[placeholder="例如 app.tar"]').exists()).toBe(true)
    expect(w.text()).toContain('需已放在该主机的 agent 下载目录')
    expect(w.find('input[type="file"]').exists()).toBe(false)
  })

  it('切进上传：手填值清零（来源换成上传通道，旧值不留成暗事实）', async () => {
    const w = await mountDialog()
    await w.find('input[placeholder="例如 app.tar"]').setValue('old.tar')
    await switchToUpload(w)
    expect(w.find('input[placeholder="例如 app.tar"]').exists()).toBe(false)
    expect(w.text()).toContain('选择文件…')
    // 切回手动：值是空的（上传没发生，清过的就是空的）。
    const group = w.findComponent({ name: 'ElRadioGroup' })
    group.vm.$emit('update:modelValue', 'manual')
    await flush()
    expect((w.find('input[placeholder="例如 app.tar"]').element as HTMLInputElement).value).toBe('')
  })
})

describe('预检拒（发请求之前）', () => {
  it('后缀不在白名单：结论就地给，不上传', async () => {
    const w = await mountDialog()
    await switchToUpload(w)
    await pickFile(w, gzipFile('app.zip'))
    expect(w.text()).toContain('只支持 tar / tar.gz / tgz 文件')
    expect(api.uploadDockerBuildContext).not.toHaveBeenCalled()
  })

  it('超过 512MB：服务端同一句号前移（不白传一场）', async () => {
    const w = await mountDialog()
    await switchToUpload(w)
    const huge = gzipFile('big.tar.gz')
    Object.defineProperty(huge, 'size', { value: MAX_BUILD_CONTEXT_BYTES + 1 })
    await pickFile(w, huge)
    expect(w.text()).toContain('文件超过 512MB 上限')
    expect(api.uploadDockerBuildContext).not.toHaveBeenCalled()
  })

  it('内容不是 gzip（裸 tar 改了后缀也过不了魔数这关）：服务端同一句', async () => {
    const w = await mountDialog()
    await switchToUpload(w)
    await pickFile(w, new File([new Uint8Array(64)], 'plain.tar.gz'))
    expect(w.text()).toContain('内容不是 gzip 压缩的 tar 归档')
    expect(api.uploadDockerBuildContext).not.toHaveBeenCalled()
  })
})

describe('上传：进度 → 回填 → 载荷', () => {
  it('进度回调驱动进度条；完成回填产物名（开始构建的载荷里就是它）', async () => {
    let resolveUpload!: (v: { filename: string }) => void
    api.uploadDockerBuildContext.mockImplementation(
      (_h: string, _f: Blob, onProgress?: (p: number) => void) =>
        new Promise<{ filename: string }>((resolve) => {
          onProgress?.(42)
          resolveUpload = resolve
        })
    )
    const w = await mountDialog()
    await switchToUpload(w)
    await pickFile(w, gzipFile('app.tar.gz'))

    // 调用形状：目标主机 + 文件本体 + 进度回调 + 断流柄（关对话框要能中止）。
    const [host, file, , signal] = lastUploadCall()
    expect(host).toBe('h1')
    expect(file.name).toBe('app.tar.gz')
    expect(signal).toBeInstanceOf(AbortSignal)
    // 进度条吃的是回调给的数（XHR 的 upload 进度经 http 层透传到这里）。
    expect(w.findComponent({ name: 'ElProgress' }).props('percentage')).toBe(42)

    resolveUpload({ filename: 'build-ctx-77.tar.gz' })
    await flush()
    // 完成：只读展示「已上传：产物名」，context 已回填。
    expect(w.text()).toContain('已上传：')
    expect(w.text()).toContain('build-ctx-77.tar.gz')

    // 走一场最小构建，钉「产物名就是 image:build 的 options.context」。
    api.openDockerBuildStream.mockResolvedValue({ ok: true, status: 200, body: null })
    await w.find('input[placeholder="例如 registry.example.com/app:v1"]').setValue('app:v1')
    await click(w, '开始构建')
    expect(api.sendDockerCmd).toHaveBeenCalledWith('h1', {
      action: 'image:build',
      options: { tag: 'app:v1', context: 'build-ctx-77.tar.gz' }
    })
  })

  it('失败：服务端结论句（离线 503 的原文）就地显示 + 重新选择重试', async () => {
    api.uploadDockerBuildContext.mockRejectedValueOnce(
      new HttpError('设备当前离线，无法接收构建上下文', 503)
    )
    const w = await mountDialog()
    await switchToUpload(w)
    await pickFile(w, gzipFile('app.tar.gz'))
    expect(w.text()).toContain('设备当前离线，无法接收构建上下文')

    // 重新选择 = 清空重来（结论与产物名都不残留）。
    api.uploadDockerBuildContext.mockResolvedValueOnce({ filename: 'build-ctx-78.tar.gz' })
    await click(w, '重新选择')
    expect(w.text()).not.toContain('设备当前离线')
    await pickFile(w, gzipFile('app2.tar.gz'))
    expect(w.text()).toContain('build-ctx-78.tar.gz')
  })

  it('完成后的两个次级动作：重新选择清空重来；改回手动输入保留产物名（可编辑）', async () => {
    api.uploadDockerBuildContext.mockResolvedValue({ filename: 'build-ctx-77.tar.gz' })
    const w = await mountDialog()
    await switchToUpload(w)
    await pickFile(w, gzipFile('app.tar.gz'))

    // 重新选择（完成态的次级动作）：产物名与进度结论都不残留。
    await click(w, '重新选择')
    expect(w.text()).not.toContain('build-ctx-77.tar.gz')
    expect(w.text()).toContain('选择文件…')

    // 重传一份 → 完成；切回手动：产物名保留在输入框里（可编辑可清空 —— 手动
    // 形态本来就是为「主机已有 tar」留的路）。
    await pickFile(w, gzipFile('app2.tar.gz'))
    expect(w.text()).toContain('build-ctx-77.tar.gz')
    const group = w.findComponent({ name: 'ElRadioGroup' })
    group.vm.$emit('update:modelValue', 'manual')
    await flush()
    expect((w.find('input[placeholder="例如 app.tar"]').element as HTMLInputElement).value).toBe(
      'build-ctx-77.tar.gz'
    )
  })
})

describe('收口（生命周期）', () => {
  it('上传在途关对话框：中止请求（断流柄触发 abort）；重开整段归零', async () => {
    let onProgress: ((p: number) => void) | undefined
    api.uploadDockerBuildContext.mockImplementation(
      async (_h: string, _f: Blob, cb?: (p: number) => void) => {
        onProgress = cb
        return new Promise(() => {}) // 挂住在途
      }
    )
    const w = await mountDialog()
    await switchToUpload(w)
    await pickFile(w, gzipFile('app.tar.gz'))
    const signal = lastUploadCall()[3]
    expect(signal?.aborted).toBe(false)
    onProgress?.(30)

    await w.setProps({ modelValue: false })
    await flush()
    expect(signal?.aborted, '关对话框即中止在途上传').toBe(true)

    await w.setProps({ modelValue: true })
    await flush()
    // 重开：形态回手填（默认值），上传状态机归零（没有残留的进度/结论）。
    expect(w.find('input[placeholder="例如 app.tar"]').exists()).toBe(true)
    expect(w.text()).not.toContain('已上传：')
    expect(api.uploadDockerBuildContext).toHaveBeenCalledTimes(1) // 新一场没有自动上传
  })
})
