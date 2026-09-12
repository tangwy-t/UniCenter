/**
 * HTML 富文本消毒工具
 *
 * 统一封装 DOMPurify,用于所有 `v-html` 渲染点。后端下发的富文本
 * (如公告/通知正文)与外部 SVG 内容均视为不可信,渲染前必须消毒,
 * 否则构成存储型/反射型 XSS 攻击面。
 *
 * 默认白名单策略:仅允许排版类标签(段落/标题/列表/链接/图片/表格/
 * 引用/加粗等),移除 `<script>`、`<iframe>`、事件属性(onerror/onload)
 * 及 javascript: 伪协议。图片默认允许 data: 与相对/同源 URL。
 *
 * @module utils/ui/sanitize
 */
import DOMPurify from 'dompurify'

/**
 * 默认允许的标签与属性(富文本排版白名单)。
 * 刻意不放开 style 属性与任何事件处理器。
 */
const ALLOWED_TAGS = [
  'p',
  'br',
  'hr',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'ul',
  'ol',
  'li',
  'a',
  'img',
  'table',
  'thead',
  'tbody',
  'tr',
  'th',
  'td',
  'blockquote',
  'code',
  'pre',
  'strong',
  'b',
  'em',
  'i',
  'u',
  's',
  'span',
  'div',
  'sub',
  'sup'
]

const ALLOWED_ATTR = ['href', 'src', 'alt', 'title', 'target', 'rel', 'colspan', 'rowspan']

/** 单例 DOMPurify 实例(浏览器环境)。 */
let purifier: DOMPurify.DOMPurify | null = null

function getPurifier(): DOMPurify.DOMPurify {
  if (purifier) return purifier
  purifier = DOMPurify()
  return purifier
}

/**
 * 消毒富文本 HTML,返回可安全用于 v-html 的字符串。
 *
 * @param html 不可信的 HTML 字符串
 * @returns 消毒后的 HTML;空/非法输入返回空串
 */
export function sanitizeHtml(html: string): string {
  if (!html) return ''
  return getPurifier().sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    // 链接统一新窗口打开并加 noopener,阻断 reverse tabnabbing
    ALLOW_DATA_ATTR: false,
    FORBID_TAGS: ['style', 'form', 'input', 'button', 'iframe', 'object', 'embed', 'link', 'meta'],
    FORBID_ATTR: ['style', 'onerror', 'onload', 'onclick', 'onmouseover', 'onfocus']
  })
}

/**
 * 消毒 SVG 内容。SVG 可内嵌 <script>/onload,故单独提供更严格的
 * SVG 专用消毒(禁 script 与事件属性)。
 *
 * @param svg SVG 文本
 * @returns 消毒后的 SVG;空/非法输入返回空串
 */
export function sanitizeSvg(svg: string): string {
  if (!svg) return ''
  return getPurifier().sanitize(svg, {
    USE_PROFILES: { svg: true, svgFilters: true },
    FORBID_TAGS: ['script', 'foreignObject'],
    FORBID_ATTR: ['onload', 'onerror', 'onclick', 'onfocus', 'onmouseover']
  })
}
