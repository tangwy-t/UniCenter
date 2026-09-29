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
 * 响应式菜单状态
 *
 * - 桌面端（>= tablet）：完全遵循用户持久化的菜单展开偏好
 * - 手机端（< tablet）：侧栏是覆盖式抽屉，开合由瞬态状态控制
 *
 * 旧实现会在断点切换时直接改写 settingStore.menuOpen（该字段持久化到 localStorage），
 * 导致桌面端偏好被响应式逻辑覆盖；这里改为派生状态，用户偏好只由用户操作写入。
 *
 * @module hooks/core/useResponsiveMenu
 */
export function useResponsiveMenu() {
  const settingStore = useSettingStore()
  const { menuOpen } = storeToRefs(settingStore)
  const { smaller } = useAppBreakpoints()

  /** 手机端（侧栏为覆盖式抽屉），断点与 shell 样式、侧栏 CSS 保持一致 */
  const isPhone = smaller('tablet')

  /** 菜单当前是否展开（渲染用） */
  const isMenuVisible = computed(() => (isPhone.value ? mobileDrawerOpen.value : menuOpen.value))

  /** 手机端抽屉是否打开（用于遮罩层等） */
  const isMobileDrawerOpen = computed(() => isPhone.value && mobileDrawerOpen.value)

  /** 切换菜单展开/收起：手机端切抽屉，桌面端写入持久化偏好 */
  const toggleMenu = () => {
    if (isPhone.value) {
      mobileDrawerOpen.value = !mobileDrawerOpen.value
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

  return {
    isPhone,
    isMenuVisible,
    isMobileDrawerOpen,
    toggleMenu,
    closeMobileDrawer
  }
}
