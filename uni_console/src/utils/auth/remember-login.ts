/**
 * 登录页「记住密码」工具
 *
 * 把用户勾选的登录凭据（账号 + 密码）保存到 localStorage，
 * 下次打开登录页时自动回填。
 *
 * ## 安全说明
 *
 * - 密码使用 UTF-8 安全 Base64 编码存储，仅做轻度混淆，**不是加密**，
 *   任何能访问浏览器本地存储的人都可以解码还原；
 * - 仅保存「实际登录成功」的账号密码（登录失败不会写入）；
 * - 用户取消勾选「记住密码」时清除已保存的凭据（登录时按勾选状态处理）；
 * - 退出登录**不会**清除凭据，记住密码跨登录/退出持续生效；
 * - 存储异常恢复等场景的 `localStorage.clear()` 会连带清除。
 *
 * 如果后续需要更强的保护，可替换为 Web Crypto（AES-GCM + 设备密钥）。
 *
 * @module utils/auth/remember-login
 */

import { StorageConfig } from '@/utils/storage/storage-config'

/** 已保存的登录凭据（密码为编码后的字符串） */
export interface RememberedLogin {
  username: string
  /** 编码后的密码 */
  encodedPassword: string
}

/** UTF-8 安全 Base64 编码（原生 btoa 不支持中文等多字节字符） */
function encodeBase64(value: string): string {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte)
  })
  return btoa(binary)
}

/** UTF-8 安全 Base64 解码 */
function decodeBase64(value: string): string {
  const binary = atob(value)
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

/** 读取已保存的登录凭据（账号 + 解码后的密码），无凭据返回 null */
export function loadRememberedLogin(): { username: string; password: string } | null {
  try {
    const raw = localStorage.getItem(StorageConfig.REMEMBER_LOGIN_KEY)
    if (!raw) return null

    const data = JSON.parse(raw) as Partial<RememberedLogin>
    if (!data.username || !data.encodedPassword) return null

    return { username: data.username, password: decodeBase64(data.encodedPassword) }
  } catch (error) {
    // 数据损坏时忽略，并顺手清掉避免反复解析失败
    console.warn('[RememberLogin] 读取记住密码失败:', error)
    clearRememberedLogin()
    return null
  }
}

/** 保存登录凭据（登录成功后调用） */
export function saveRememberedLogin(username: string, password: string): void {
  try {
    const value: RememberedLogin = {
      username,
      encodedPassword: encodeBase64(password)
    }
    localStorage.setItem(StorageConfig.REMEMBER_LOGIN_KEY, JSON.stringify(value))
  } catch (error) {
    // 隐私模式等场景下 localStorage 可能不可用，失败静默处理
    console.warn('[RememberLogin] 保存记住密码失败:', error)
  }
}

/** 清除已保存的登录凭据 */
export function clearRememberedLogin(): void {
  try {
    localStorage.removeItem(StorageConfig.REMEMBER_LOGIN_KEY)
  } catch (error) {
    console.warn('[RememberLogin] 清除记住密码失败:', error)
  }
}
