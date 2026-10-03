import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useSettingStore } from '@/store/modules/setting'
import { useAppBreakpoints } from './useAppBreakpoints'

/**
 * 手机端侧栏抽屉的开合状态
 *
 * 模块级瞬态状态：手机端的抽屉开合与桌面端菜单偏好（settingStore.menuOpen）分离，
 * 断点切换只影响渲染，不写入持久化配置。
 */
const mobileDrawerOpen = ref(false)

/**
 * 矮视口（手机横屏/矮窗）下图标栏的临时展开态
 *
 * 与手机抽屉同一纪律：瞬态、不写持久化偏好。矮视口默认收为图标栏（宽度让给表），
 * 用户点菜单键可临时展开；离开矮视口复位。
 */
const iconBarExpanded = ref(false)

/**
 * 响应式菜单状态
 *
 * - 桌面端（>= tablet 且视口不矮）：完全遵循用户持久化的菜单展开偏好
 * - 手机端（< tablet）：侧栏是覆盖式抽屉，开合由瞬态状态控制
 * - 横屏图标栏态（矮视口 + 窄桌面宽，如 844×390 手机横屏）：侧栏默认收为图标栏，
 *   展开与否只改瞬态状态 —— 手机横屏下 230px 展开侧栏几乎吃掉三分之一屏宽，
 *   而这一档的用户偏好（桌面端的 menuOpen）不该被一次横屏借用改写
 *
 * 旧实现会在断点切换时直接改写 settingStore.menuOpen（该字段持久化到 localStorage），
 * 导致桌面端偏好被响应式逻辑覆盖；这里改为派生状态，用户偏好只由用户操作写入。
 *
 * @module hooks/core/useResponsiveMenu
 */
export function useResponsiveMenu() {
  const settingStore = useSettingStore()
  const { menuOpen } = storeToRefs(settingStore)
  const { smaller, heightAtMost } = useAppBreakpoints()

  /** 手机端（侧栏为覆盖式抽屉），断点与 shell 样式、侧栏 CSS 保持一致 */
  const isPhone = smaller('tablet')

  /** 矮视口（阈值口径见 HEIGHT_BREAKPOINTS）：手机横屏与矮桌面窗都在内 */
  const isShortViewport = heightAtMost('short')

  /**
   * 横屏图标栏态：矮视口 + 窄桌面宽（tablet <= 宽 < desktop）。
   * 为什么加宽度上限：更宽的矮窗（如 1280×620）宽度不是瓶颈，展开的侧栏是
   * 用户自己的偏好，不该被高度抢走；768 以下本就是覆盖式抽屉（isPhone 分支），
   * 图标栏概念不适用。
   */
  const isIconBarViewport = computed(
    () => isShortViewport.value && !isPhone.value && smaller('desktop').value
  )

  /** 菜单当前是否展开（渲染用） */
  const isMenuVisible = computed(() => {
    if (isPhone.value) return mobileDrawerOpen.value
    if (isIconBarViewport.value) return iconBarExpanded.value
    return menuOpen.value
  })

  /** 手机端抽屉是否打开（用于遮罩层等） */
  const isMobileDrawerOpen = computed(() => isPhone.value && mobileDrawerOpen.value)

  /** 切换菜单展开/收起：手机端切抽屉，图标栏态切瞬态展开，桌面端写入持久化偏好 */
  const toggleMenu = () => {
    if (isPhone.value) {
      mobileDrawerOpen.value = !mobileDrawerOpen.value
    } else if (isIconBarViewport.value) {
      iconBarExpanded.value = !iconBarExpanded.value
    } else {
      settingStore.setMenuOpen(!menuOpen.value)
    }
  }

  /** 关闭手机端抽屉（桌面端为 no-op，避免误改用户偏好） */
  const closeMobileDrawer = () => {
    mobileDrawerOpen.value = false
  }

  // 离开手机断点后复位抽屉，保证下次进入手机断点时抽屉是关闭的
  watch(isPhone, (phone: boolean) => {
    if (!phone) {
      mobileDrawerOpen.value = false
    }
  })

  // 离开图标栏态后复位临时展开，下次进入矮视口仍是收起的图标栏
  watch(isIconBarViewport, (iconBar: boolean) => {
    if (!iconBar) {
      iconBarExpanded.value = false
    }
  })

  return {
    isPhone,
    isShortViewport,
    isIconBarViewport,
    isMenuVisible,
    isMobileDrawerOpen,
    toggleMenu,
    closeMobileDrawer
  }
}
