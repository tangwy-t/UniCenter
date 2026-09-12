import defaultAvatar from '@imgs/user/avatar.webp'

/**
 * 头像显示地址解析：
 * - 空值/纯空白 → 应用默认头像
 * - http(s) 外链 → 原样返回
 * - 站内相对路径 → 拼接 VITE_API_URL 基础地址（自动补前导斜杠）
 */
export function resolveAvatar(avatar?: string): string {
  const value = avatar?.trim()
  if (!value) return defaultAvatar
  if (/^https?:\/\//i.test(value)) return value
  const apiBase = (import.meta.env.VITE_API_URL || '').replace(/\/+$/, '')
  return `${apiBase}${value.startsWith('/') ? value : `/${value}`}`
}
