// @vitest-environment jsdom
/**
 * 行为级守卫：任务中心「参数」复制在**非安全上下文**下仍要复制成功并提示「已复制」。
 *
 * 生产用 http 明文访问：`navigator.clipboard.writeText()` 会 reject。这里把 writeText
 * 模拟成 reject、execCommand 模拟成成功，点复制钮后断言：走了回退（execCommand('copy')
 * 被调用）**且**提示为成功（ElMessage.success('已复制')，不是失败提示）。若有人把
 * 实现退回「writeText 失败即弹失败」，本文件必红。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ElMessage } from 'element-plus'

// 轻量替身：图标组件在测试环境无自动注册，替换为空渲染即可。
vi.mock('@/components/core/base/art-svg-icon/index.vue', () => ({
  default: { name: 'ArtSvgIcon', render: () => null }
}))

import TaskOptions from '../components/task-options.vue'

function stubClipboardReject() {
  const writeText = vi.fn().mockRejectedValue(new DOMException('denied', 'NotAllowedError'))
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  return writeText
}

function stubExecCommand(returns: boolean) {
  const fn = vi.fn().mockReturnValue(returns)
  Object.defineProperty(document, 'execCommand', {
    value: fn,
    configurable: true,
    writable: true
  })
  return fn
}

describe('参数复制：非安全上下文回退', () => {
  beforeEach(() => {
    vi.spyOn(ElMessage, 'success').mockImplementation(() => undefined as never)
    vi.spyOn(ElMessage, 'warning').mockImplementation(() => undefined as never)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('writeText reject 时回退 execCommand 并提示「已复制」', async () => {
    const writeText = stubClipboardReject()
    const exec = stubExecCommand(true)

    const wrapper = mount(TaskOptions, {
      props: { options: JSON.stringify({ command: ['ls', '-la'] }), action: 'container:exec' }
    })

    await wrapper.get('.to__copy').trigger('click')
    await flushPromises()

    expect(writeText).toHaveBeenCalled()
    expect(exec).toHaveBeenCalledWith('copy')
    expect(ElMessage.success).toHaveBeenCalledWith('已复制')
    expect(ElMessage.warning).not.toHaveBeenCalled()
  })

  it('连回退都失败时才给失败提示', async () => {
    stubClipboardReject()
    stubExecCommand(false)

    const wrapper = mount(TaskOptions, {
      props: { options: JSON.stringify({ tail: 100 }), action: 'container:logs' }
    })

    await wrapper.get('.to__copy').trigger('click')
    await flushPromises()

    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.warning).toHaveBeenCalledWith('复制失败，请手动选择文本')
  })
})