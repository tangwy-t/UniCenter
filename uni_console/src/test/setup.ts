/**
 * Vitest 全局 setup：把「合成事件的时间戳守卫」从宿主墙钟上摘下来。
 *
 * ── 为什么需要它（2026-10-03 放大复现的现场证据）─────────────────────────
 * Vue 给每个 DOM 监听器装一个 invoker，并记下**监听器创建时刻的墙钟**
 * （runtime-dom 的 createInvoker：`invoker.attached = getNow()`）；事件派发到达时：
 *
 *     if (!e._vts) e._vts = Date.now()
 *     else if (e._vts <= invoker.attached) return   // ← 静默丢弃，处理器不跑
 *
 * @vue/test-utils 的 trigger/setValue 会给合成事件打 `_vts = Date.now() + 1`。
 * 这个守卫的初衷是「同一事件对象被重新派发时不重复处理」，它默认**墙钟单调**——
 * 而 WSL2 的宿主时间同步会把 VM 墙钟**往回拨**（实测一次 ~2.4s：同一进程里
 * `Date.now()` 后退而 `performance.now()` 继续前进，见放大日志的 wall/mono 两列）。
 * 回拨之后，「监听器创建于回拨之前」的元素收到的合成事件必然满足
 * `_vts（回拨后）<= attached（回拨前）` → 事件被静默丢弃：
 *   - 点击按钮**没有任何效果**（按钮明明可点、禁用态/loading 全是 false）；
 *   - `setValue` 不更新 v-model（输入框里字在、绑定值没动，等 enabled 的
 *     waitUntil 一直兜到超时）。
 * 放大协议下这就是一族随机单测红（点击类/输入类），现场「一切正常但什么都没发生」，
 * 极难归因 —— 残余两站（images-write 打标签、pull-dialog 开始拉取）与
 * resources 的 tab 点击超时都是它。
 *
 * ── 处置 ────────────────────────────────────────────────────────────
 * jsdom 测试里的事件永远是「此刻合成」的，守卫的防重派发语义没有价值；
 * 这里在派发口把 `_vts` 抬到恒真，让测试结果不再取决于宿主墙钟是否回拨。
 * **只影响测试环境**：产品代码与运行时行为一概不动。
 *
 * 回归守卫：src/test/event-clock-guard.test.ts（模拟墙钟回拨钉住本补丁，
 * 撤掉本文件或改坏补丁时那个文件必红）。
 */
const ALWAYS_FRESH = Number.MAX_SAFE_INTEGER

const originalDispatch = EventTarget.prototype.dispatchEvent

EventTarget.prototype.dispatchEvent = function dispatchEvent(event: Event): boolean {
  // 事件对象极少见地可能是只读封装（打不上就算了）—— 记录失败不该让派发本身出错。
  try {
    ;(event as Event & { _vts?: number })._vts = ALWAYS_FRESH
  } catch {
    /* 忽略：退化为原行为 */
  }
  return originalDispatch.call(this, event)
}
