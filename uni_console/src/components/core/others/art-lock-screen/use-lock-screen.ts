import { ref } from 'vue'

export interface LockScreenOptions {
  /** 密码校验:true=正确;false=错误(网络失败由调用方兜底为 false 并自行提示) */
  verify: (password: string) => Promise<boolean>
  /** 连续失败达到 maxFails 时的处置(组件注入:提示 + 登出) */
  onExceed: () => void
  /** 自动锁定空闲阈值(毫秒):支持 getter 动态读取;数值 <=0 表示不自动锁定 */
  idleMs: number | (() => number)
  /** 连续失败上限 */
  maxFails: number
}

/**
 * 锁屏状态机:与视图分离便于单测。
 * 规则:锁屏期间失败计数不清零;解锁成功才复位;达到 maxFails 触发一次 onExceed。
 */
export function useLockScreen(options: LockScreenOptions) {
  const locked = ref(false)
  const failCount = ref(0)
  const password = ref('')
  const verifying = ref(false)
  const lastActivity = ref(Date.now())

  const lock = (): void => {
    locked.value = true
    password.value = ''
  }

  const touch = (): void => {
    lastActivity.value = Date.now()
  }

  /** 解析当前空闲阈值(getter 每次实时读取,支持运行中调整) */
  const resolveIdleMs = (): number => {
    return typeof options.idleMs === 'function' ? options.idleMs() : options.idleMs
  }

  /** 空闲超过 idleMs 且未锁定 → 锁定并返回 true;idleMs<=0(关闭自动锁)恒不触发 */
  const autoLockCheck = (now: number): boolean => {
    if (locked.value) return false
    const idleMs = resolveIdleMs()
    if (!idleMs || idleMs <= 0) return false
    if (now - lastActivity.value >= idleMs) {
      lock()
      return true
    }
    return false
  }

  /** 尝试解锁:true=成功,false=失败(含校验中/未锁定的守卫返回) */
  const tryUnlock = async (): Promise<boolean> => {
    if (!locked.value || verifying.value) return false
    const input = password.value
    password.value = ''
    verifying.value = true
    try {
      const ok = await options.verify(input)
      if (ok) {
        locked.value = false
        failCount.value = 0
        return true
      }
      // 已达到上限:失败不再累计,由 onExceed 登录出流程接管(防重复触发)
      if (failCount.value >= options.maxFails) return false
      failCount.value += 1
      if (failCount.value === options.maxFails) {
        options.onExceed()
      }
      return false
    } finally {
      verifying.value = false
    }
  }

  return {
    locked,
    failCount,
    password,
    verifying,
    lastActivity,
    lock,
    touch,
    autoLockCheck,
    tryUnlock
  }
}
