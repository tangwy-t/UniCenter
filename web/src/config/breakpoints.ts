/**
 * 全局响应式断点（单一事实源）
 *
 * ## 说明
 *
 * - 命名与 Tailwind 断点语义对齐（phone=sm 640 / tablet=md 768 / desktop=lg 1024），
 *   因此 Tailwind 的 `max-md:` 等前缀类与 JS 判断不会错位
 * - `compact` / `wide` / `xwide` 为项目既有阈值，沿用历史取值
 * - Element Plus 的 `el-col` 响应式属性（xs/sm/md/lg/xl）使用 Element Plus
 *   自身的断点语义（xs<768、sm>=768、md>=992、lg>=1200、xl>=1920），
 *   由 `utils/form/responsive.ts` 单独处理，与本文件不是同一套
 *
 * ## 修改约定
 *
 * 本文件与 `src/assets/styles/core/_breakpoints.scss` 必须保持一致，
 * 一致性由 `src/config/breakpoints.test.ts` 断言：改任一侧，测试都会失败。
 *
 * @module config/breakpoints
 */

/**
 * 断点表（key 为语义名，value 为 px 阈值；`smaller(name)` 表示视口宽度 < 阈值）
 */
export const BREAKPOINTS = {
  /** 超窄手机：表单/搜索栏操作按钮堆叠 */
  phoneNarrow: 500,
  /** 手机（Tailwind sm） */
  phone: 640,
  /** 平板竖屏 / 侧栏抽屉模式（Tailwind md；统一了历史上 768/800/1000 三个魔数） */
  tablet: 768,
  /** 桌面（Tailwind lg；统一了历史上设置面板 1000 与用户管理页 1024） */
  desktop: 1024,
  /** 紧凑桌面：视口高度修正、分页尺寸、表格固定列、登录页 */
  compact: 1180,
  /** 宽屏（Tailwind xl） */
  xl: 1280,
  /** 大宽屏 */
  wide: 1440,
  /** 超宽屏 */
  xwide: 1600
} as const

/** 断点名称 */
export type BreakpointName = keyof typeof BREAKPOINTS
