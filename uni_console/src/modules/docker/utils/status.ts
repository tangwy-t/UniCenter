/**
 * 状态语汇单一来源（Wave 3a）：语义档（tone）→ 圆点色 + 文字色 + 领域中文标签。
 *
 * ## 为什么需要这个文件
 *
 * 「圆点 + 文字结论」这套状态呈现此前在模块里各写一份：容器统一表的状态列走
 * `utils/display.ts` 的纯文本 `containerStateZh`，而 `views/projects.vue`（项目态）
 * 与 `views/tasks.vue`（任务态）各自又写了一套点色/文案映射 —— 三处色值口径迟早
 * 漂移（project 用 --el-color-success、task 用 EP 基色、容器表根本没有点）。这里把
 * **色值口径与领域映射**收敛为纯函数，三处只消费、不各造一套。
 *
 * ## 纪律（无障碍）
 *
 * - **颜色永不单独传达**：每个 tone 旁边永远有中文文字结论（调用方负责渲染）；
 * - **点用 EP 语义基色**（非文本图形，WCAG 1.4.11 门槛 3:1）；**文字用 AA token**
 *   （`--aa-{success,warning,danger,primary}-text`，过 4.5:1 正文线，见
 *   `assets/styles/core/aa-text.scss`）—— 「点鲜、字深」是本模块的既有收口。
 * - 中性档没有语义色：点退到次要灰（图形 3:1），文字退到 regular 档（AA 达标）。
 */
import type { DockerContainerItem } from '../api'
import { containerStateZh } from './display'

/** 语义档（五个；与 EP 语义基色一一对应，中性档退灰）。 */
export type StatusTone = 'success' | 'warning' | 'danger' | 'primary' | 'neutral'

/** 圆点底色：EP 语义基色（非文本图形，3:1 门槛；避免写死色值）。 */
const DOT_COLOR: Record<StatusTone, string> = {
  success: 'var(--el-color-success)',
  warning: 'var(--el-color-warning)',
  danger: 'var(--el-color-danger)',
  primary: 'var(--el-color-primary)',
  neutral: 'var(--el-text-color-secondary)'
}

/** 文字色：AA token（正文 4.5:1；中性退 regular 档 —— 该档在全模块已升为达标色）。 */
const TEXT_COLOR: Record<StatusTone, string> = {
  success: 'var(--aa-success-text)',
  warning: 'var(--aa-warning-text)',
  danger: 'var(--aa-danger-text)',
  primary: 'var(--aa-primary-text)',
  neutral: 'var(--el-text-color-regular)'
}

/** tone → 圆点底色。 */
export function statusDotColor(tone: StatusTone): string {
  return DOT_COLOR[tone]
}

/** tone → 文字色。 */
export function statusTextColor(tone: StatusTone): string {
  return TEXT_COLOR[tone]
}

// ── 容器状态（Docker 原生状态码 + statusText 的 health/退出码解析）──────────────

/**
 * 容器状态档（判据见 spec §11.1 与评审 D §1）：
 *   - running：成功；但 `statusText` 含 `(unhealthy)` / `(health: starting)` → 警告
 *     （「在跑但健康检查没通过/还没通过」是需要注意的事实）；
 *   - exited：退出码非 0 → 危险（异常退出）；0 → 中性（自己人停的）；
 *     退出码认不出（老 agent 没给）→ 中性（不冒充危险）；
 *   - created → 主色；paused / restarting / removing → 警告；dead → 危险；
 *   - 其余/未知 → 中性（服务端加了新状态时页面不冒充认识）。
 */
export function containerStatusTone(c: DockerContainerItem): StatusTone {
  switch (c.state) {
    case 'running': {
      const status = c.statusText ?? ''
      if (/\(unhealthy\)/i.test(status) || /health:\s*starting/i.test(status)) return 'warning'
      return 'success'
    }
    case 'created':
      return 'primary'
    case 'paused':
    case 'restarting':
    case 'removing':
      return 'warning'
    case 'dead':
      return 'danger'
    case 'exited': {
      const m = (c.statusText ?? '').match(/\((\d+)\)/)
      const code = m ? Number(m[1]) : null
      return code !== null && code !== 0 ? 'danger' : 'neutral'
    }
    default:
      return 'neutral'
  }
}

export interface ContainerStatus {
  tone: StatusTone
  /** 中文状态句（复用 `containerStateZh`：状态码/时长/health 的中文口径单一来源）。 */
  label: string
}

/** 容器状态的「色 + 文」一拍（容器统一表状态列与镜像详情关联容器表消费）。 */
export function containerStatus(c: DockerContainerItem): ContainerStatus {
  return { tone: containerStatusTone(c), label: containerStateZh(c) }
}

// ── Compose 项目态（后端归纳的 running/partial/stopped）──────────────────────

/** 项目态 → tone：运行中成功、部分运行警告、其余中性。 */
export function projectStatusTone(state?: string): StatusTone {
  if (state === 'running') return 'success'
  if (state === 'partial') return 'warning'
  return 'neutral'
}

/** 项目态中文标签（未知给「—」）。 */
export function projectStatusText(state?: string): string {
  if (state === 'running') return '运行中'
  if (state === 'partial') return '部分运行'
  if (state === 'stopped') return '已停止'
  return '—'
}

// ── 长任务态（pending/succeeded/failed/timeout）───────────────────────────────

/** 任务态 → tone：成功成功、失败/超时危险、进行中中性、未知中性。
    进行中保持中性：旋转图标已经在说「在动」，文字再上色只会喧宾夺主（旧口径保留）。 */
export function taskStatusTone(status: string): StatusTone {
  switch (status) {
    case 'succeeded':
      return 'success'
    case 'failed':
    case 'timeout':
      return 'danger'
    case 'pending':
      return 'neutral'
    default:
      return 'neutral'
  }
}

/** 任务态中文标签（未知状态原样直显 —— 服务端加了新状态时页面不冒充认识）。 */
export function taskStatusText(status: string): string {
  switch (status) {
    case 'pending':
      return '进行中'
    case 'succeeded':
      return '成功'
    case 'failed':
      return '失败'
    case 'timeout':
      return '超时'
    default:
      return status || '—'
  }
}
