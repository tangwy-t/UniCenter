<!-- 授权页右上角组件 -->
<template>
  <div class="absolute w-full flex-c top-4.5 z-10 justify-end">
    <div class="auth-topbar-brand flex-cc ml-2 max-sm:ml-6">
      <ArtLogo class="icon" size="46" />
      <h1 class="text-xl ont-mediumf ml-2">{{ AppConfig.systemInfo.name }}</h1>
    </div>

    <div class="auth-topbar-actions flex-cc gap-1.5 mr-2 max-sm:mr-5">
      <div class="color-picker-expandable relative flex-c max-sm:!hidden">
        <div
          class="color-dots absolute right-0 rounded-full flex-c gap-2 rounded-5 px-2.5 py-2 pr-9 pl-2.5 opacity-0"
        >
          <div
            v-for="(color, index) in mainColors"
            :key="color"
            class="color-dot relative size-5 c-p flex-cc rounded-full opacity-0"
            :class="{ active: color === systemThemeColor }"
            :style="{ background: color, '--index': index }"
            @click="changeThemeColor(color)"
          >
            <ArtSvgIcon v-if="color === systemThemeColor" icon="ri:check-fill" class="text-white" />
          </div>
        </div>
        <div class="btn palette-btn relative z-[2] h-8 w-8 c-p flex-cc tad-300">
          <ArtSvgIcon
            icon="ri:palette-line"
            class="text-xl text-g-800 transition-colors duration-300"
          />
        </div>
      </div>
      <div
        v-if="shouldShowThemeToggle"
        class="btn theme-btn h-8 w-8 c-p flex-cc tad-300"
        @click="themeAnimation"
      >
        <ArtSvgIcon
          :icon="isDark ? 'ri:sun-fill' : 'ri:moon-line'"
          class="text-xl text-g-800 transition-colors duration-300"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { useSettingStore } from '@/store/modules/setting'
  import { useHeaderBar } from '@/hooks/core/useHeaderBar'
  import { themeAnimation } from '@/utils/ui/animation'
  import AppConfig from '@/config'

  defineOptions({ name: 'AuthTopBar' })

  const settingStore = useSettingStore()
  const { isDark, systemThemeColor } = storeToRefs(settingStore)
  const { shouldShowThemeToggle } = useHeaderBar()

  const mainColors = AppConfig.systemMainColor
  const color = systemThemeColor // css v-bind 使用

  const changeThemeColor = (color: string) => {
    if (systemThemeColor.value === color) return
    settingStore.setElementTheme(color)
    settingStore.reload()
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  /* 顶栏品牌（logo + 系统名）只在「品牌列已被 compact 收起」的窄视口出现，
     且短横档例外 —— 那一档品牌由 LoginLeftView 的品牌栏承载，顶栏再挂一枚就是重复。

     为什么品牌用 mr-auto 把自己顶到左边（而不是给容器加 justify-between）：
     品牌一藏（短横档/宽视口），space-between 会把仅剩的一组控件甩到左边去，
     而 mr-auto 的容器永远 justify-end —— 控件钉在右上，与品牌在不在无关。 */
  .auth-topbar-brand {
    display: none;
    margin-right: auto;

    @include respond-at-most('compact') {
      display: flex;

      @include respond-height-at-most('short') {
        @include respond-at-least('phone') {
          display: none;
        }
      }
    }
  }

  /* 手机横屏：主色/主题两个圆钮的触靶从 32 提到 44（图标尺寸不变） */
  @include respond-height-at-most('phoneShort') {
    .auth-topbar-actions {
      gap: 12px;
      margin-right: 12px;

      .btn {
        width: 44px;
        height: 44px;
      }
    }
  }

  .color-dots {
    pointer-events: none;
    backdrop-filter: blur(10px);
    box-shadow: 0 2px 12px var(--art-gray-300);
    transition:
      opacity 0.3s ease,
      transform 0.3s ease;
    transform: translateX(10px);
  }

  .color-dot {
    box-shadow: 0 2px 4px rgb(0 0 0 / 15%);
    transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
    transition-delay: calc(var(--index) * 0.05s);
    transform: translateX(20px) scale(0.8);
  }

  .color-dot:hover {
    box-shadow: 0 4px 8px rgb(0 0 0 / 20%);
    transform: translateX(0) scale(1.1);
  }

  .color-picker-expandable:hover .color-dots {
    pointer-events: auto;
    opacity: 1;
    transform: translateX(0);
  }

  .color-picker-expandable:hover .color-dot {
    opacity: 1;
    transform: translateX(0) scale(1);
  }

  .dark .color-dots {
    background-color: var(--art-gray-200);
    box-shadow: none;
  }

  .color-picker-expandable:hover .palette-btn :deep(.art-svg-icon) {
    color: v-bind(color);
  }
</style>
