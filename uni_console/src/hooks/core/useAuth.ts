/**
 * useAuth - 权限校验
 * 基于后端返回的 permissions 列表进行按钮/操作级权限判断。
 */
import { storeToRefs } from 'pinia'
import { useUserStore } from '@/store/modules/user'

export const useAuth = () => {
  const userStore = useUserStore()
  const { info } = storeToRefs(userStore)

  /** 是否拥有某权限标识 */
  const hasAuth = (perm: string): boolean =>
    !!perm && (info.value?.permissions ?? []).includes(perm)

  /** 是否拥有任一权限（空数组视为放行） */
  const hasAnyAuth = (perms: string[]): boolean =>
    perms.length === 0 || perms.some((p) => hasAuth(p))

  return { hasAuth, hasAnyAuth }
}
