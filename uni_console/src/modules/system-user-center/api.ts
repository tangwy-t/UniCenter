import request from '@/utils/http'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 获取当前用户信息(个人中心冷启动数据源) */
export function fetchMyProfile() {
  return request.get<Api.Auth.UserInfo>({ url: `${PREFIX}/user/info` })
}

/**
 * 个人中心总览:身份(含部门/角色显示名)、登录统计、近 30 天逐日活动与最近登录记录。
 * 与 /user/info 分离:总览含多次聚合查询,不应拖慢全局用户信息加载。
 */
export function fetchMyOverview() {
  return request.get<Api.Auth.UserOverview>({ url: `${PREFIX}/user/overview` })
}

/** 更新个人资料(仅姓名/邮箱/手机号,后端 PUT /user/info) */
export function updateMyProfile(data: Api.Auth.UpdateProfileParams) {
  return request.put<void>({ url: `${PREFIX}/user/info`, data })
}

/** 修改自己的密码(后端 POST /user/password,成功后所有会话失效) */
export function changeMyPassword(data: Api.Auth.ChangePasswordParams) {
  return request.post<void>({ url: `${PREFIX}/user/password`, data })
}

/** 验证当前登录密码(锁屏解锁;后端 POST /user/password/verify,错误 400 不写登录日志) */
export function verifyMyPassword(data: Api.Auth.VerifyPasswordParams) {
  return request.post<Api.Auth.VerifyPasswordResp>({ url: `${PREFIX}/user/password/verify`, data })
}

/**
 * 上传头像(multipart 表单域 `file`)。
 * - 关闭默认超时:慢网络下 2MB 图片也应成功,服务端有大小/类型校验兜底
 * - 返回新的头像访问路径(含 API 前缀),同源/反代部署下可被 <img> 直接引用
 */
export function uploadMyAvatar(file: File) {
  const form = new FormData()
  form.append('file', file, file.name)
  return request.post<Api.Auth.AvatarResp>({
    url: `${PREFIX}/user/info/avatar`,
    data: form,
    timeout: 0
  })
}
