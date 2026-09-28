<!-- 移动端竖屏守卫：手机竖屏时全屏提示横屏使用 -->
<template>
  <Teleport to="body">
    <Transition name="orientation-guard">
      <div
        v-if="isPortraitPhone"
        class="orientation-guard"
        role="alertdialog"
        aria-label="请将设备横屏使用"
      >
        <ArtSvgIcon class="orientation-guard__icon" icon="ri:smartphone-line" />

        <p class="orientation-guard__title">请将设备横屏使用</p>
        <p class="orientation-guard__desc">当前系统为宽屏布局，竖屏下无法完整展示，旋转设备后自动进入</p>

        <template v-if="canLockLandscape">
          <ElButton type="primary" class="orientation-guard__action" @click="lockLandscape">
            全屏并锁定横屏
          </ElButton>
          <p v-if="lockFailed" class="orientation-guard__hint">自动锁定失败，请手动旋转设备</p>
        </template>
        <p v-else class="orientation-guard__hint">若已开启屏幕方向锁定，请先在系统设置或控制中心中关闭</p>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
  defineOptions({ name: 'ArtOrientationGuard' })

  // 手机竖屏判定：宽度低于 tablet 断点(768)、竖屏、触屏主指针。
  // `pointer: coarse` 用于排除桌面端缩小窗口的误报（DevTools 移动端模拟会自动命中）。
  const isPortraitPhone = useMediaQuery(
    '(orientation: portrait) and (max-width: 767.98px) and (pointer: coarse)'
  )

  // Screen Orientation API 的 lock 方法在部分 TS lib 版本中未声明，这里做局部补充
  // （本项目仅用到 'landscape'，按最小可用面声明）
  interface LockableScreenOrientation extends ScreenOrientation {
    lock?: (orientation: 'landscape') => Promise<void>
  }

  const getScreenOrientation = (): LockableScreenOrientation | undefined =>
    typeof screen === 'undefined'
      ? undefined
      : (screen.orientation as LockableScreenOrientation | undefined)

  // 是否支持原生横屏锁定：需同时具备全屏能力与 Screen Orientation API
  // （Android Chrome 支持；iOS Safari 不支持，只能引导手动旋转）
  const canLockLandscape = computed(() => {
    if (typeof document === 'undefined') return false

    return (
      document.fullscreenEnabled &&
      typeof document.documentElement.requestFullscreen === 'function' &&
      typeof getScreenOrientation()?.lock === 'function'
    )
  })

  // 自动锁定是否失败（失败时在守卫内提示手动旋转，避免依赖第三方弹层的层级）
  const lockFailed = ref(false)

  /**
   * 全屏并锁定横屏（需用户手势触发；失败时提示手动旋转）
   */
  const lockLandscape = async (): Promise<void> => {
    try {
      lockFailed.value = false

      if (!document.fullscreenElement) {
        await document.documentElement.requestFullscreen()
      }

      await getScreenOrientation()?.lock?.('landscape')
    } catch {
      lockFailed.value = true
    }
  }
</script>

<style scoped>
  .orientation-guard {
    position: fixed;
    inset: 0;
    z-index: 4500;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    box-sizing: border-box;
    padding: 32px;
    text-align: center;
    background: var(--default-bg-color);
  }

  .orientation-guard__icon {
    font-size: 64px;
    color: var(--theme-color);
    animation: orientation-guard-rotate 2.2s ease-in-out infinite;
  }

  .orientation-guard__title {
    margin: 4px 0 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--art-gray-800);
  }

  .orientation-guard__desc {
    margin: 0;
    font-size: 14px;
    line-height: 1.6;
    color: var(--art-gray-600);
  }

  .orientation-guard__hint {
    margin: 0;
    font-size: 12px;
    line-height: 1.6;
    color: var(--art-gray-500);
  }

  .orientation-guard__action {
    margin-top: 8px;
  }

  /* 手机图标往复旋转 90°，提示用户旋转设备 */
  @keyframes orientation-guard-rotate {
    0%,
    15% {
      transform: rotate(0deg);
    }

    50%,
    65% {
      transform: rotate(90deg);
    }

    100% {
      transform: rotate(0deg);
    }
  }

  .orientation-guard-enter-active,
  .orientation-guard-leave-active {
    transition: opacity 0.2s ease;
  }

  .orientation-guard-enter-from,
  .orientation-guard-leave-to {
    opacity: 0;
  }
</style>