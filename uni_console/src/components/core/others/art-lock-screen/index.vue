<!-- 全屏锁屏浮层:毛玻璃透出底页,时钟+头像+密码框,解锁密码=登录密码 -->
<template>
  <Transition name="lock-fade">
    <div v-if="locked" class="lock-screen-root">
      <div class="lock-screen-time">{{ timeText }}</div>
      <div class="lock-screen-date">{{ dateText }}</div>
      <div class="lock-screen-card">
        <img :src="avatarSrc" class="lock-screen-avatar" alt="avatar" @error="onAvatarError" />
        <div class="lock-screen-name">{{ userInfo.realName || userInfo.username }}</div>
        <ElInput
          v-model="password"
          type="password"
          show-password
          :placeholder="'请输入解锁密码'"
          class="lock-screen-input"
          @keyup.enter="handleUnlock"
        />
        <ElButton type="primary" class="lock-screen-btn" :loading="verifying" @click="handleUnlock">
          {{ '解锁' }}
        </ElButton>
        <button class="lock-screen-back" @click="backToLogin">
          {{ '返回登录' }}
        </button>
      </div>
    </div>
  </Transition>

  <!-- 自动锁屏设置弹窗：空闲时长偏好(0=不自动锁定)，改动即时生效并持久化 -->
  <ElDialog v-model="settingVisible" title="自动锁屏设置" width="400px">
    <p class="lock-setting-desc">{{
      '无操作超过设定时长后自动锁定屏幕；选择「不自动锁定」则仅可手动锁定（设置即时生效）'
    }}</p>
    <ElRadioGroup
      :model-value="settingStore.lockIdleMinutes"
      class="lock-setting-group"
      @update:model-value="onIdleChange"
    >
      <div
        v-for="opt in lockIdleOptions"
        :key="opt.value"
        class="lock-setting-item"
        :class="{ 'is-active': settingStore.lockIdleMinutes === opt.value }"
      >
        <ElRadio :value="opt.value">{{ opt.label }}</ElRadio>
      </div>
    </ElRadioGroup>
    <template #footer>
      <div class="lock-setting-footer">
        <span class="lock-setting-current">{{ '当前' }}：{{ currentLabel }}</span>
        <ElButton type="primary" @click="settingVisible = false">
          {{ '关闭' }}
        </ElButton>
      </div>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref } from 'vue'
  import { ElMessage } from 'element-plus'
  import { useUserStore } from '@/store/modules/user'
  import { useSettingStore } from '@/store/modules/setting'
  import { resolveAvatar } from '@/utils/avatar'
  import { mittBus } from '@/utils/sys'
  import { verifyMyPassword } from '@/modules/system-user-center/api'
  import { useLockScreen } from './use-lock-screen'

  defineOptions({ name: 'ArtLockScreen' })

  const userStore = useUserStore()
  const settingStore = useSettingStore()
  const { getUserInfo: userInfo } = storeToRefs(userStore)

  const { locked, password, verifying, lock, touch, autoLockCheck, tryUnlock } = useLockScreen({
    // 阈值从偏好设置实时读取(getter):0=不自动锁定,默认 5 分钟
    idleMs: () => settingStore.lockIdleMinutes * 60_000,
    maxFails: 5,
    verify: async (pwd) => {
      try {
        const res = await verifyMyPassword({ password: pwd })
        if (!res.valid) {
          ElMessage.error('密码错误')
        }
        return res.valid
      } catch {
        ElMessage.error('解锁请求失败，请检查网络')
        return false
      }
    },
    onExceed: () => {
      ElMessage.error('错误次数过多，已退出登录')
      userStore.logOut()
    }
  })

  const avatarSrc = ref(resolveAvatar(userInfo.value.avatar))

  const onAvatarError = (): void => {
    avatarSrc.value = resolveAvatar()
  }

  /** 时钟:每秒刷新 */
  const now = ref(new Date())
  let clockTimer: ReturnType<typeof setInterval> | undefined
  const timeText = computed(() => now.value.toLocaleTimeString('zh-CN', { hour12: false }))
  const dateText = computed(() =>
    now.value.toLocaleDateString('zh-CN', {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit'
    })
  )

  /** 空闲检测:窗口事件节流刷新活动时间,interval 每秒核查是否该锁 */
  let idleTimer: ReturnType<typeof setInterval> | undefined
  let touchTrap: ReturnType<typeof setTimeout> | undefined

  const onActivity = (): void => {
    if (touchTrap) return
    touchTrap = setTimeout(() => {
      touch()
      touchTrap = undefined
    }, 1000)
  }

  const handleUnlock = async (): Promise<void> => {
    if (!password.value) return
    const ok = await tryUnlock()
    if (ok) {
      userStore.setLockStatus(false)
    }
  }

  const backToLogin = (): void => {
    userStore.logOut()
  }

  /** 手动锁屏（右上角用户菜单触发）：锁定并持久化到用户 store（localStorage） */
  const handleBusLock = (): void => {
    lock()
    userStore.setLockStatus(true)
  }

  /** 自动锁屏设置弹窗开关 */
  const settingVisible = ref(false)

  /** 空闲时长选项（分钟）：0 = 不自动锁定 */
  const lockIdleOptions = computed(() => [
    { value: 0, label: '不自动锁定' },
    { value: 1, label: '1 分钟' },
    { value: 5, label: '5 分钟' },
    { value: 15, label: '15 分钟' },
    { value: 30, label: '30 分钟' },
    { value: 60, label: '1 小时' }
  ])

  /** 选择空闲时长：即时生效并写入设置 store（本地持久化） */
  const onIdleChange = (v: string | number | boolean | undefined): void => {
    if (typeof v === 'boolean' || v === undefined || v === null) return
    const minutes = Number(v)
    if (!Number.isFinite(minutes) || minutes < 0) return
    settingStore.setLockIdleMinutes(minutes)
  }

  /** 底部工具栏当前值标签 */
  const currentLabel = computed(() => {
    const hit = lockIdleOptions.value.find((o) => o.value === settingStore.lockIdleMinutes)
    return hit ? hit.label : String(settingStore.lockIdleMinutes)
  })

  /** 打开自动锁屏设置弹窗（用户菜单触发） */
  const openSetting = (): void => {
    settingVisible.value = true
  }

  onMounted(() => {
    // 刷新恢复：强制刷新 / F5 后锁屏状态仍在（持久化于用户 store）
    if (userStore.isLock) {
      lock()
    }
    mittBus.on('openLockScreen', handleBusLock)
    mittBus.on('openLockScreenSetting', openSetting)
    window.addEventListener('click', onActivity)
    window.addEventListener('mousemove', onActivity)
    window.addEventListener('keydown', onActivity)
    clockTimer = setInterval(() => {
      now.value = new Date()
    }, 1000)
    idleTimer = setInterval(() => {
      if (autoLockCheck(Date.now())) {
        userStore.setLockStatus(true)
      }
    }, 1000)
  })

  onUnmounted(() => {
    mittBus.off('openLockScreen', handleBusLock)
    mittBus.off('openLockScreenSetting', openSetting)
    window.removeEventListener('click', onActivity)
    window.removeEventListener('mousemove', onActivity)
    window.removeEventListener('keydown', onActivity)
    if (clockTimer) clearInterval(clockTimer)
    if (idleTimer) clearInterval(idleTimer)
    if (touchTrap) clearTimeout(touchTrap)
  })
</script>

<style scoped>
  .lock-screen-root {
    position: fixed;
    inset: 0;
    z-index: 4000;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
    background: rgba(255, 255, 255, 0.6);
    backdrop-filter: blur(14px);
    -webkit-backdrop-filter: blur(14px);
  }

  .dark .lock-screen-root {
    background: rgba(0, 0, 0, 0.55);
  }

  .lock-screen-time {
    font-size: 56px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);
  }

  .lock-screen-date {
    font-size: 14px;
    color: var(--el-text-color-secondary);
    margin-bottom: 18px;
  }

  .lock-screen-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 14px;
    width: min(320px, 86vw);
    padding: 28px 24px;
    border-radius: 16px;
    border: 1px solid var(--default-border);
    background: var(--el-bg-color);
    box-shadow: 0 8px 30px rgba(16, 24, 40, 0.12);
  }

  .lock-screen-avatar {
    width: 64px;
    height: 64px;
    border-radius: 50%;
    object-fit: cover;
  }

  .lock-screen-name {
    font-size: 15px;
    font-weight: 500;
    color: var(--el-text-color-primary);
  }

  .lock-screen-input {
    width: 100%;
  }

  .lock-screen-btn {
    width: 100%;
  }

  .lock-screen-back {
    border: none;
    background: none;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    cursor: pointer;
    text-decoration: underline;
  }

  .lock-setting-desc {
    margin: 0 0 16px;
    font-size: 13px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);
  }

  .lock-setting-group {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }

  .lock-setting-item {
    border: 1px solid var(--default-border);
    border-radius: 8px;
    background: var(--el-fill-color-blank);
    transition:
      border-color 150ms ease,
      background-color 150ms ease;
  }

  .lock-setting-item:hover {
    border-color: var(--el-color-primary-light-5);
  }

  .lock-setting-item.is-active {
    border-color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
  }

  .lock-setting-item.is-active:hover {
    border-color: var(--el-color-primary);
  }

  /* 每个卡片内的单选项铺满整卡（整卡可点击、点选状态一致） */
  .lock-setting-group :deep(.el-radio) {
    display: flex;
    align-items: center;
    width: 100%;
    margin-right: 0;
    padding: 10px 12px;
    box-sizing: border-box;
  }

  .lock-setting-group :deep(.el-radio__label) {
    font-size: 13px;
  }

  .lock-setting-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .lock-setting-current {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  @media (prefers-reduced-motion: reduce) {
    .lock-setting-item {
      transition: none;
    }
  }

  .lock-fade-enter-active,
  .lock-fade-leave-active {
    transition: opacity 0.25s ease;
  }

  .lock-fade-enter-from,
  .lock-fade-leave-to {
    opacity: 0;
  }
</style>
