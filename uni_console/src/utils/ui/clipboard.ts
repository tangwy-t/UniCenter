/**
 * 剪贴板复制工具（含非安全上下文回退）
 *
 * ## 为什么不能只判 `navigator.clipboard` 在不在
 *
 * 生产用 **http 明文**访问（如 `http://192.168.12.105:20080`），这是浏览器的
 * **非安全上下文**：`navigator.clipboard` 对象**存在**，但 `writeText()` 会直接
 * reject（`NotAllowedError`）。因此「先检查对象是否存在」判不出问题 ——
 * 必须**先试、失败再回退**。
 *
 * ## 回退路径
 *
 * `document.execCommand('copy')`（配合临时隐藏 `<textarea>`）**在 http 下可用**，
 * 是现成的兜底手段；本仓库此前在 logs.vue / sql.vue 里各手写过一份，现统一收敛于此。
 *
 * @module utils/ui/clipboard
 */

/** 用临时隐藏 textarea + execCommand('copy') 兜底复制（http 非安全上下文可用）。 */
function legacyCopy(text: string): boolean {
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    // fixed + 透明：不撑开布局、不闪烁。
    ta.style.position = 'fixed'
    ta.style.top = '0'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

/**
 * 复制文本到剪贴板：先走异步 Clipboard API，失败（含非安全上下文里 reject）再回退。
 *
 * @param text 要复制的文本
 * @returns 是否复制成功（回退也失败时为 false，调用方据此给失败提示）
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 非安全上下文里 writeText 会 reject：吞掉，改走回退路径。
  }
  return legacyCopy(text)
}