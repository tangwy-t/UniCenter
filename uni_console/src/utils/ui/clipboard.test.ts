// @vitest-environment jsdom
/**
 * 守卫：剪贴板复制在**非安全上下文**（http 明文访问，生产即如此）下必须仍能成功。
 *
 * 现场：http 访问时 `navigator.clipboard` **对象存在**，但 `writeText()` 会 reject
 * （NotAllowedError）——所以「检查对象在不在」判不出来，必须**先试、失败再回退**
 * `document.execCommand('copy')`。本文件把这条行为钉死：writeText 被模拟成 reject 时，
 * 仍要走回退、仍要返回成功；若有人把实现退回「只用 Clipboard API」，这里必红。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

/** jsdom 不实现 execCommand：把它挂成可控的桩。 */
function stubExecCommand(returns: boolean) {
  const fn = vi.fn().mockReturnValue(returns)
  Object.defineProperty(document, 'execCommand', {
    value: fn,
    configurable: true,
    writable: true
  })
  return fn
}

/** 用给定实现覆盖 navigator.clipboard.writeText（undefined = 对象不存在）。 */
function stubClipboard(writeText?: () => Promise<void>) {
  Object.defineProperty(navigator, 'clipboard', {
    value: writeText ? { writeText } : undefined,
    configurable: true
  })
}

describe('copyText：非安全上下文回退', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('Clipboard API 成功时直接成功，不走回退', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard(writeText)
    const exec = stubExecCommand(true)

    await expect(copyText('hello')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('hello')
    expect(exec).not.toHaveBeenCalled()
  })

  it('writeText reject（http 非安全上下文）时回退 execCommand 并成功', async () => {
    const writeText = vi.fn().mockRejectedValue(new DOMException('denied', 'NotAllowedError'))
    stubClipboard(writeText)
    const exec = stubExecCommand(true)

    await expect(copyText('hello')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalled()
    expect(exec).toHaveBeenCalledWith('copy')
  })

  it('clipboard 对象不存在时直接走回退', async () => {
    stubClipboard(undefined)
    const exec = stubExecCommand(true)

    await expect(copyText('hello')).resolves.toBe(true)
    expect(exec).toHaveBeenCalledWith('copy')
  })

  it('回退也失败（execCommand 返回 false）时返回 false', async () => {
    stubClipboard(vi.fn().mockRejectedValue(new Error('denied')))
    stubExecCommand(false)

    await expect(copyText('hello')).resolves.toBe(false)
  })

  it('回退文本落进临时 textarea（保真：复制的就是原文）', async () => {
    stubClipboard(vi.fn().mockRejectedValue(new Error('denied')))
    const seen: string[] = []
    Object.defineProperty(document, 'execCommand', {
      value: vi.fn(() => {
        const ta = document.querySelector('textarea')
        if (ta) seen.push((ta as HTMLTextAreaElement).value)
        return true
      }),
      configurable: true,
      writable: true
    })

    await copyText('多行\n文本 with spaces')
    expect(seen).toEqual(['多行\n文本 with spaces'])
    // 临时节点不残留。
    expect(document.querySelector('textarea')).toBeNull()
  })
})