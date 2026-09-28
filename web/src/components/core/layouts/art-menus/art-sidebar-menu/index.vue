<!-- 左侧菜单 或 双列菜单 -->
<template>
  <div
    class="layout-sidebar"
    v-if="showLeftMenu || isDualMenu"
    :class="{ 'no-border': menuList.length === 0 }"
  >
    <!-- 双列菜单（左侧） -->
    <div
      v-if="isDualMenu"
      class="dual-menu-left"
      :style="{ width: dualMenuShowText ? '80px' : '64px', background: getMenuTheme.background }"
    >
      <ArtLogo class="logo" @click="navigateToHome" />

      <ElScrollbar style="height: calc(100% - 135px)">
        <ul>
          <li v-for="menu in firstLevelMenus" :key="menu.path" @click="handleMenuJump(menu, true)">
            <ElTooltip
              class="box-item"
              effect="dark"
              :content="menu.meta.title"
              placement="right"
              :offset="15"
              :hide-after="0"
              :disabled="dualMenuShowText"
            >
              <div
                :class="{
                  'is-active': menu.meta.isFirstLevel
                    ? menu.path === route.path
                    : menu.path === firstLevelMenuPath
                }"
                :style="{
                  height: dualMenuShowText ? '60px' : '46px'
                }"
              >
                <ArtSvgIcon
                  class="menu-icon text-g-700 dark:text-g-800"
                  :icon="menu.meta.icon"
                  :style="{
                    marginBottom: dualMenuShowText ? '5px' : '0'
                  }"
                />
                <span v-if="dualMenuShowText" class="text-md text-g-700">
                  {{ menu.meta.title }}
                </span>
                <div v-if="menu.meta.showBadge" class="art-badge art-badge-dual" />
              </div>
            </ElTooltip>
          </li>
        </ul>
      </ElScrollbar>

      <ArtIconButton
        class="switch-btn size-10"
        icon="ri:arrow-left-right-fill"
        @click="toggleDualMenuMode"
      />
    </div>

    <!-- 左侧菜单 || 双列菜单（右侧） -->
    <div
      v-show="menuList.length > 0"
      class="menu-left"
      :class="`menu-left-${getMenuTheme.theme} menu-left-${!isMenuVisible ? 'close' : 'open'}`"
      :style="{
        background: getMenuTheme.background,
        width: isDualMenuCollapsed ? MENU_CLOSE_WIDTH : undefined
      }"
    >
      <!-- Logo、系统名称 -->
      <div
        class="header"
        @click="navigateToHome"
        :style="{
          background: getMenuTheme.background
        }"
      >
        <ArtLogo v-if="!isDualMenu" class="logo" />

        <p
          :class="{ 'is-dual-menu-name': isDualMenu }"
          :style="{
            color: getMenuTheme.systemNameColor,
            opacity: !isMenuVisible ? 0 : 1
          }"
        >
          {{ AppConfig.systemInfo.name }}
        </p>
      </div>
      <ElScrollbar :style="scrollbarStyle">
        <ElMenu
          :class="'el-menu-' + getMenuTheme.theme"
          :collapse="!isMenuVisible"
          :default-active="routerPath"
          :text-color="getMenuTheme.textColor"
          :unique-opened="uniqueOpened"
          :background-color="getMenuTheme.background"
          :default-openeds="defaultOpenedMenus"
          :popper-class="`menu-left-popper menu-left-${getMenuTheme.theme}-popper`"
          :show-timeout="50"
          :hide-timeout="50"
        >
          <SidebarSubmenu
            :list="menuList"
            :isMobile="isPhone"
            :theme="getMenuTheme"
            @close="handleMenuClose"
          />
        </ElMenu>
      </ElScrollbar>

      <!-- 双列菜单右侧折叠按钮 -->
      <div class="dual-menu-collapse-btn" v-if="isDualMenu" @click="toggleMenuVisibility">
        <ArtSvgIcon
          class="text-g-500/70"
          :icon="isMenuVisible ? 'ri:arrow-left-wide-fill' : 'ri:arrow-right-wide-fill'"
        />
      </div>

      <div
        class="menu-model"
        @click="toggleMenuVisibility"
        :style="{
          opacity: !isMenuVisible ? 0 : 1,
          transform: showMobileModal ? 'scale(1)' : 'scale(0)'
        }"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
  import AppConfig from '@/config'
  import { useSettingStore } from '@/store/modules/setting'
  import { MenuTypeEnum, MenuWidth } from '@/enums/appEnum'
  import { useMenuStore } from '@/store/modules/menu'
  import { isIframe } from '@/utils/navigation'
  import { handleMenuJump } from '@/utils/navigation'
  import SidebarSubmenu from './widget/SidebarSubmenu.vue'
  import { useCommon } from '@/hooks/core/useCommon'
  import { useResponsiveMenu } from '@/hooks/core/useResponsiveMenu'
  import { useTimeoutFn } from '@vueuse/core'

  defineOptions({ name: 'ArtSidebarMenu' })

  const ANIMATION_DELAY = 350
  const MENU_CLOSE_WIDTH = MenuWidth.CLOSE

  const route = useRoute()
  const router = useRouter()
  const settingStore = useSettingStore()

  const { getMenuOpenWidth, menuType, uniqueOpened, dualMenuShowText, getMenuTheme } =
    storeToRefs(settingStore)

  // 菜单可见状态：手机端为瞬态抽屉，桌面端为用户持久化偏好（详见 composable 注释）
  const { isPhone, isMenuVisible, toggleMenu, closeMobileDrawer } = useResponsiveMenu()

  // 组件内部状态
  const defaultOpenedMenus = ref<string[]>([])
  const showMobileModal = ref(false)

  // 菜单宽度相关
  const menuopenwidth = computed(() => getMenuOpenWidth.value)
  const menuclosewidth = computed(() => MENU_CLOSE_WIDTH)

  // 菜单类型判断
  const isTopLeftMenu = computed(() => menuType.value === MenuTypeEnum.TOP_LEFT)
  const showLeftMenu = computed(
    () => menuType.value === MenuTypeEnum.LEFT || menuType.value === MenuTypeEnum.TOP_LEFT
  )
  const isDualMenu = computed(() => menuType.value === MenuTypeEnum.DUAL_MENU)
  const isDualMenuCollapsed = computed(() => isDualMenu.value && !isMenuVisible.value)

  // 路由相关
  const firstLevelMenuPath = computed(() => route.matched[0]?.path)
  const routerPath = computed(() => String(route.meta.activePath || route.path))

  // 菜单数据
  const firstLevelMenus = computed(() => {
    return useMenuStore().menuList.filter((menu) => !menu.meta.isHide)
  })

  const menuList = computed(() => {
    const menuStore = useMenuStore()
    const allMenus = menuStore.menuList

    // 如果不是顶部左侧菜单或双列菜单，直接返回完整菜单列表
    if (!isTopLeftMenu.value && !isDualMenu.value) {
      return allMenus
    }

    // 处理 iframe 路径
    if (isIframe(route.path)) {
      return findIframeMenuList(route.path, allMenus)
    }

    // 处理一级菜单
    if (route.meta.isFirstLevel) {
      return []
    }

    // 返回当前顶级路径对应的子菜单
    const currentTopPath = `/${route.path.split('/')[1]}`
    const currentMenu = allMenus.find((menu) => menu.path === currentTopPath)
    return currentMenu?.children ?? []
  })

  // 双列菜单收起时的滚动条样式
  const scrollbarStyle = computed(() => {
    if (isDualMenuCollapsed.value) {
      return {
        position: 'absolute',
        top: '60px',
        right: 0,
        left: 0,
        height: 'calc(100% - 10px)',
        transform: 'translateY(-50px)',
        transition: 'transform 0.3s ease'
      }
    }

    return {
      transform: 'translateY(0)',
      height: 'calc(100% - 60px)',
      transition: 'transform 0.3s ease'
    }
  })

  /**
   * 延迟隐藏移动端模态框（使用 VueUse 的 useTimeoutFn）
   */
  const { start: delayHideMobileModal } = useTimeoutFn(
    () => {
      showMobileModal.value = false
    },
    ANIMATION_DELAY,
    { immediate: false }
  )

  /**
   * 查找 iframe 对应的二级菜单列表
   */
  const findIframeMenuList = (currentPath: string, menuList: any[]) => {
    // 递归查找包含当前路径的菜单项
    const hasPath = (items: any[]): boolean => {
      for (const item of items) {
        if (item.path === currentPath) {
          return true
        }
        if (item.children && hasPath(item.children)) {
          return true
        }
      }
      return false
    }

    // 遍历一级菜单查找匹配的子菜单
    for (const menu of menuList) {
      if (menu.children && hasPath(menu.children)) {
        return menu.children
      }
    }
    return []
  }

  const { homePath } = useCommon()

  /**
   * 导航到首页
   */
  const navigateToHome = (): void => {
    router.push(homePath.value)
  }

  /**
   * 切换菜单显示/隐藏（手机端切抽屉，桌面端写用户偏好）
   */
  const toggleMenuVisibility = (): void => {
    toggleMenu()

    // 移动端模态框控制逻辑
    if (isPhone.value) {
      if (isMenuVisible.value) {
        // 菜单即将打开，立即显示模态框
        showMobileModal.value = true
      } else {
        // 菜单即将关闭，延迟隐藏模态框确保动画完成
        delayHideMobileModal()
      }
    }
  }

  /**
   * 处理菜单关闭（来自子组件；仅手机端需要自动收起抽屉）
   */
  const handleMenuClose = (): void => {
    if (isPhone.value) {
      closeMobileDrawer()
      delayHideMobileModal()
    }
  }

  /**
   * 切换双列菜单模式
   */
  const toggleDualMenuMode = (): void => {
    settingStore.setDualMenuShowText(!dualMenuShowText.value)
  }

  /**
   * 监听菜单可见状态，控制移动端遮罩层
   */
  watch(isMenuVisible, (visible: boolean) => {
    if (!isPhone.value) {
      // 大屏幕设备上，模态框始终隐藏
      showMobileModal.value = false
    } else if (visible) {
      // 抽屉打开时立即显示模态框
      showMobileModal.value = true
    } else {
      // 抽屉关闭时延迟隐藏模态框，确保动画完成
      delayHideMobileModal()
    }
  })

  /**
   * 监听断点变化：离开手机端时隐藏遮罩（抽屉状态由 composable 复位）
   */
  watch(isPhone, (phone: boolean) => {
    if (!phone) {
      showMobileModal.value = false
    }
  })
</script>

<style lang="scss" scoped>
  @use './style';
</style>

<style lang="scss">
  @use './theme';

  .layout-sidebar {
    // 展开的宽度
    .el-menu:not(.el-menu--collapse) {
      width: v-bind(menuopenwidth);
    }

    // 折叠后宽度
    .el-menu--collapse {
      width: v-bind(menuclosewidth);
    }
  }
</style>
