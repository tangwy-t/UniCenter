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
 * 宽度轴（`BREAKPOINTS`）外另有**高度轴** `HEIGHT_BREAKPOINTS`（矮视口/手机横屏，
 * 阈值口径见其注释）——两轴各自成表、各自断言，不可互相混入。
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

/**
 * 高度轴断点表（key 为语义名，value 为 px 阈值；`heightAtMost(name)` 表示视口高度 ≤ 阈值）
 *
 * ## 为什么另立一张表（不可混入 BREAKPOINTS）
 *
 * `BREAKPOINTS` 是宽度轴：命名对齐 Tailwind（phone=sm 640 …）、有升序断言与数值
 * 锁定，且由 `useBreakpoints`（只认 min/max-width 语法）消费。高度是**另一维语义**
 * ——「高度 640」与「宽度 640」是两件事，混表会让两侧断言互相污染，也会让
 * 「窄但高」（竖屏手机）与「宽但矮」（横屏手机/矮窗）的判定对不上。因此高度轴
 * 单独成表（样式侧同样是独立的 `$height-breakpoints`，见 _breakpoints.scss），
 * 一致性由 `src/config/breakpoints.test.ts` 断言。
 *
 * ## 阈值自校准（口径：390/360 档必中、620 视需要、900 不中）
 *
 * - `phoneShort` 420：手机横屏（844×390 / 740×360）必中，620/900 不中 —— 最激进的
 *   紧凑档（收页签栏、压应用头、hero 行内化、页根钉底）只落在真·横屏手机上。
 * - `short` 640：矮桌面窗（1280×620）也中 —— 表吃满剩余高度、页面零滚动
 *   对矮窗同样成立；900 高的常规桌面不中，桌面零回归。
 */
export const HEIGHT_BREAKPOINTS = {
  /** 手机横屏（最紧凑档；与宽度轴 tablet 同值不同轴，别混用） */
  phoneShort: 420,
  /** 矮视口（横屏手机 + 矮桌面窗）：hero 行内化 */
  short: 640
} as const

/** 高度轴断点名称 */
export type HeightBreakpointName = keyof typeof HEIGHT_BREAKPOINTS
