/**
 * useLayoutHeight - 页面布局高度管理
 *
 * 自动计算和管理页面内容区域的高度，确保内容区域能够正确填充剩余空间。
 * 监听页根上方各块（应用头 / 全局告警条 / 页签栏）的尺寸变化，动态调整内容
 * 区域高度，避免出现滚动条或布局错乱。
 *
 * ## 主要功能
 *
 * 1. 实测高度计算 - 以**页根 offsetTop 实测值**为口径计算内容区高度
 * 2. 响应式监听 - 自动监听上方各块尺寸变化并更新高度
 * 3. CSS 变量同步 - 自动更新 CSS 变量，方便全局使用
 * 4. 灵活配置 - 支持自定义间距、CSS 变量名等
 * 5. 自动查找模式 - 提供通过 ID 自动查找元素的便捷方式
 *
 * ## 为什么是「实测 offsetTop」而不是「头部高度求和」
 *
 * 旧口径是 `100dvh − (应用头高 + 页签栏高 + 常数)`：它假定页根上方只有那两块，
 * 页根自身也正好从内容区顶端起算。两个假定都会被打破 ——
 *
 * - **全局密码告警条**（`components/core/others/art-password-reminder`）是
 *   `#app-content` 里 `.layout-content` 之前的兄弟节点，80px 高，页面侧看不见它，
 *   于是页根被整体下推 80px，底部那一截永远滚不到（844×390 实测：页根 342px 从
 *   y=140 起 → 末尾内容卡被压成 20px 空壳）；
 * - 常数间距（曾经的 15px 呼吸位）与各档页签栏的真实高度随时会变，而求和式
 *   不会跟着变。
 *
 * 实测口径把这三个变量一次性收掉：量出页根（`.layout-content`）距内容区顶端的
 * 静态距离，`--art-full-height = 100dvh − 该距离 − 呼吸位`。页根上方再多一块
 * 东西（新告警条、新页签栏），量出来的值自己就对了。
 *
 * ## 为什么用 dvh 而不是 vh
 *
 * 容器高度是 `calc(100dvh - 实测占高)`。真机浏览器（尤其手机横屏）的 `vh`
 * 是**大视口**高度，地址栏/手势条会再吃掉 40-90px —— 用 vh 算出的页根比实际
 * 可视区高，底部那一截永远滚不到；dvh 跟着可视视口走，与 `#app-main` 在
 * compact 档的 `height: 100dvh` 同口径。桌面浏览器 dvh === vh，零差异。
 *
 * @module useLayoutHeight
 * @author Art Design Pro Team
 */

import { ref, computed, watch, onMounted, type Ref } from 'vue'
import { useElementSize, useResizeObserver, useEventListener, useMutationObserver } from '@vueuse/core'
import { HEIGHT_BREAKPOINTS } from '@/config/breakpoints'

/**
 * 页面容器高度配置
 */
interface LayoutHeightOptions {
  /** 底部呼吸位（默认 15px；phoneShort 高度档归零，见 measureOffset） */
  extraSpacing?: number
  /** 是否自动更新 CSS 变量（默认 true） */
  updateCssVar?: boolean
  /** CSS 变量名称（默认 '--art-full-height'） */
  cssVarName?: string
}

/** 内容区滚动容器（桌面/矮窗档是它自己在滚） */
const SCROLLER_ID = 'app-main'
/** 页根容器：它上方的实际占高就是要扣掉的那部分 */
const CONTENT_ROOT_SELECTOR = '.layout-content'
/** 全局密码告警条（页根上方的可变块之一） */
const REMINDER_SELECTOR = '.password-reminder'

/**
 * 实测「页根上方占高」与底部呼吸位，得出一份可直接写进 CSS 变量的高度表达式。
 *
 * 除以 100dvh 为基准外，这里没有任何写死的档位常数 —— 档位只影响呼吸位：
 * phoneShort（≤420 高）上 15px 是 3.8% 的可见高度，正好是一行表的高度差，
 * 故该档归零（旧口径的钉死高度也是这么定的一档）。
 */
function useMeasuredHeight(options: {
  cssVarName: string
  extraSpacing: number
  updateCssVar: boolean
  headerRef: Ref<HTMLElement | undefined>
  contentHeaderRef: Ref<HTMLElement | undefined>
}) {
  const { cssVarName, extraSpacing, updateCssVar, headerRef, contentHeaderRef } = options

  // 页根容器与告警条的引用：前者是测量对象，后者是「页根上方还有一块」的可变来源
  // （它出现/消失不改变任何被监听的尺寸，必须单独盯住）。
  const contentRootRef = ref<HTMLElement>()
  const reminderRef = ref<HTMLElement>()
  // 内容区包裹（#app-content）：告警条与页根都是它的子节点，子节点增删即重测。
  // 挂载期才找得到（setup 期父级元素还没进 DOM），故先留空引用、挂载后接线。
  const contentWrapRef = ref<HTMLElement>()

  // 保留原有 API：头部两块的高度仍对外暴露（调用方与测试按名字读）。
  const { height: headerHeight } = useElementSize(headerRef)
  const { height: contentHeaderHeight } = useElementSize(contentHeaderRef)

  /** 页根上方实测占高（px，含应用头 + 告警条 + 页签栏 + 其间外边距）。 */
  const offsetTop = ref(0)

  /** phoneShort 档呼吸位归零（阈值口径见 config/breakpoints.ts）。 */
  function bottomGap(): number {
    if (typeof window === 'undefined') return extraSpacing
    return window.innerHeight <= HEIGHT_BREAKPOINTS.phoneShort ? 0 : extraSpacing
  }

  /**
   * 重新测量页根上方的静态占高。
   *
   * 用「页根 rect.top − 滚动容器 rect.top + 滚动位移」而不是裸 rect.top：#app-main
   * 自己就是滚动容器（桌面/矮窗档），页面滚一下裸 rect.top 就变小 —— 那会让高度
   * 随滚动抖动。加回 scrollTop 得到的是与滚动无关的静态距离。
   */
  function measure(): void {
    if (typeof document === 'undefined') return
    const root = contentRootRef.value
    if (!root) return
    const scroller = document.getElementById(SCROLLER_ID)
    const rect = root.getBoundingClientRect()
    let distance = rect.top
    if (scroller) {
      distance = rect.top - scroller.getBoundingClientRect().top + scroller.scrollTop
    }
    offsetTop.value = Math.max(0, Math.round(distance))
  }

  /** 元素是挂载后才有/才没的（告警条随「是否必须改密」出现或关闭消失），故每次测量前重找。 */
  function resolveElements(): void {
    if (typeof document === 'undefined') return
    contentWrapRef.value = document.getElementById('app-content') ?? undefined
    contentRootRef.value = document.querySelector<HTMLElement>(CONTENT_ROOT_SELECTOR) ?? undefined
    reminderRef.value = document.querySelector<HTMLElement>(REMINDER_SELECTOR) ?? undefined
  }

  const containerMinHeight = computed(
    () => `calc(100dvh - ${offsetTop.value + bottomGap()}px)`
  )

  if (updateCssVar) {
    watch(
      containerMinHeight,
      (newHeight) => {
        requestAnimationFrame(() => {
          document.documentElement.style.setProperty(cssVarName, newHeight)
        })
      },
      { immediate: true }
    )
  }

  // 上方任一可变块的尺寸变化都要重测（ResizeObserver 在 observe 时会先回调一次，
  // 挂载期的首测因此也落在这里，不必另开一次手动测量）。
  useResizeObserver([headerRef, contentHeaderRef, reminderRef], () => measure())

  // 视口变化：既可能改呼吸位档（phoneShort），也可能改应用头/告警条的换行高度。
  // 不传目标 = 挂在 window 上（vueuse 的默认目标）。
  useEventListener('resize', () => {
    requestAnimationFrame(measure)
  })

  // 告警条的出现/消失不在任何 ResizeObserver 的观察面上（换的是兄弟节点，
  // 被观察元素自身的盒子不变），用子节点变动把它接回来。
  // 只盯**直接子节点**（告警条与 .layout-content 就是 #app-content 的两个直接
  // 子节点）：subtree 一开，页内表格每次重渲染都会把这里叫醒一次，而页内的增删
  // 根本不影响页根上方的占高。
  useMutationObserver(
    contentWrapRef,
    () => {
      requestAnimationFrame(() => {
        resolveElements()
        measure()
      })
    },
    { childList: true }
  )

  onMounted(() => {
    if (typeof document === 'undefined') return
    requestAnimationFrame(() => {
      resolveElements()
      measure()
    })
  })

  return { containerMinHeight, offsetTop, headerHeight, contentHeaderHeight }
}

export function useLayoutHeight(options: LayoutHeightOptions = {}) {
  const { extraSpacing = 15, updateCssVar = true, cssVarName = '--art-full-height' } = options

  const headerRef = ref<HTMLElement>()
  const contentHeaderRef = ref<HTMLElement>()

  const measured = useMeasuredHeight({
    cssVarName,
    extraSpacing,
    updateCssVar,
    headerRef,
    contentHeaderRef
  })

  return {
    /** 容器高度（响应式，`calc(100dvh - 实测占高)`） */
    containerMinHeight: measured.containerMinHeight,
    /** 头部元素引用 */
    headerRef,
    /** 内容头部元素引用 */
    contentHeaderRef,
    /** 头部高度（响应式） */
    headerHeight: measured.headerHeight,
    /** 内容头部高度（响应式） */
    contentHeaderHeight: measured.contentHeaderHeight
  }
}

/**
 * 通过 ID 自动查找元素的布局高度管理
 * 适用于无法直接获取元素引用的场景
 *
 * @param headerIds 头部元素的 ID 数组
 * @param options 配置选项
 */
export function useAutoLayoutHeight(
  headerIds: string[] = ['app-header', 'app-content-header'],
  options: LayoutHeightOptions = {}
) {
  const { extraSpacing = 15, updateCssVar = true, cssVarName = '--art-full-height' } = options

  // 创建元素引用
  const headerRef = ref<HTMLElement>()
  const contentHeaderRef = ref<HTMLElement>()

  const measured = useMeasuredHeight({
    cssVarName,
    extraSpacing,
    updateCssVar,
    headerRef,
    contentHeaderRef
  })

  // 在 DOM 挂载后查找元素
  onMounted(() => {
    if (typeof document !== 'undefined') {
      // 使用 nextTick 确保 DOM 完全渲染
      requestAnimationFrame(() => {
        const header = document.getElementById(headerIds[0])
        const contentHeader = document.getElementById(headerIds[1])

        if (header) {
          headerRef.value = header
        }
        if (contentHeader) {
          contentHeaderRef.value = contentHeader
        }
      })
    }
  })

  return {
    /** 容器高度（响应式，`calc(100dvh - 实测占高)`） */
    containerMinHeight: measured.containerMinHeight,
    /** 头部元素引用 */
    headerRef,
    /** 内容头部元素引用 */
    contentHeaderRef,
    /** 头部高度（响应式） */
    headerHeight: measured.headerHeight,
    /** 内容头部高度（响应式） */
    contentHeaderHeight: measured.contentHeaderHeight
  }
}