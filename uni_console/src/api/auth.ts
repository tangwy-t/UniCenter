import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 登录 */
export function fetchLogin(params: Api.Auth.LoginParams) {
  return request.post<Api.Auth.TokenResp>({ url: `${PREFIX}/login`, params })
}

/** 刷新令牌 */
export function fetchRefresh(refreshToken: string) {
  return request.post<Api.Auth.TokenResp>({ url: `${PREFIX}/refresh`, params: { refreshToken } })
}

/** 登出 */
export function fetchLogout() {
  return request.post<void>({ url: `${PREFIX}/logout` })
}

/** 获取验证码 */
export function fetchCaptcha() {
  return request.post<Api.Auth.CaptchaResp>({ url: `${PREFIX}/captcha/generate` })
}

/** 获取当前用户信息 */
export function fetchGetUserInfo() {
  return request.get<Api.Auth.UserInfo>({ url: `${PREFIX}/user/info` })
}
