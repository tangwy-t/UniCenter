/**
 * 指令通道（受理 → 轮询 → 结构化结果）的收口。
 *
 * 此前「受理 + 轮询」在 container-detail / image-detail / projects 里各写一份近似复制；
 * 二期写操作会让复制翻倍，故收在这里（2B 计划 §D3）。
 *
 * 受理期的错误分类照后端 `service/docker_cmd.go` 的**实际状态码**：
 *   - 403：无该 action 的权限（handler 的 RequiredPerm 闸；force=true 时还要求 docker:exec）；
 *   - 409：同 (device, action, target) 已有在飞（界面动作是「稍候/去看该目标的在途操作」）；
 *   - 400：参数不合法 / 期次未到 / 确认不符；
 *   - 设备离线 / Docker 不可用：core **没有 503 构造函数**，实际用 apperror.Internal
 *     承载（HTTP 500 + 结论句），故这里 500 与 503 同归「不可用」——文案以服务端 msg 优先。
 * 分类是纯函数（classifyAcceptError），便于单测钉住语义。
 *
 * 轮询节奏由 `pollDelay` 给（1 秒起指数退避、5 秒封顶）：轻量指令通常在 1 秒内有结果，
 * 固定 1 秒轮询会把亚秒级操作拖成 1 秒的体感；上限与一期 runRead 一致（约 20 次）。
 *
 * 行级禁用：`pendingId`（照 device 的 togglingId）在 run 期间置为该次操作的 key
 * （缺省 = target，无 target 时 = action），页面据此禁用该行按钮防重复提交；
 * `busy` 是全局在途判断（批量操作时禁用整条操作栏）。
 */
import { computed, getCurrentScope, onScopeDispose, ref } from 'vue'
import { isHttpError } from '@/utils/http/error'
import { fetchDockerCmdResult, sendDockerCmd } from '../api'
import { pollDelay } from '../utils/cmd'
import { useDockerHost, type DockerHostContext } from '../utils/host-context'

export type AcceptFailureKind = 'forbidden' | 'conflict' | 'unavailable' | 'invalid' | 'unknown'

export interface AcceptFailure {
  kind: AcceptFailureKind
  /** 展示用结论句：服务端 msg 优先（它比前端编的更准），缺失时按 kind 回退。 */
  message: string
  /** HTTP 状态（无响应时为 undefined）。 */
  status?: number
}

/** 受理期错误分类（纯函数）。 */
export function classifyAcceptError(e: unknown): AcceptFailure {
  const status = isHttpError(e) ? e.code : undefined
  const raw = (e as { message?: string } | null | undefined)?.message
  const message = (raw ?? '').trim()
  switch (status) {
    case 403:
      return { kind: 'forbidden', status, message: message || '没有执行该操作的权限' }
    case 409:
      return {
        kind: 'conflict',
        status,
        message: message || '该目标上已有同一条操作在执行，请稍候'
      }
    case 500:
    case 503:
      return {
        kind: 'unavailable',
        status,
        message: message || '设备或 Docker 当前不可用，指令未下发'
      }
    case 400:
      return { kind: 'invalid', status, message: message || '操作未被接受，请检查输入' }
    default:
      return { kind: 'unknown', status, message: message || '操作未受理，请稍后重试' }
  }
}

export type DockerCmdOutcome =
  | 'succeeded' // 后端终态 succeeded
  | 'failed' // 后端终态 failed（error 是 agent/服务端的结论句）
  | 'timeout' // 前端轮询超时（约 20 次）
  | 'not-accepted' // 受理被拒（accept 里有分类）
  | 'poll-error' // 轮询请求本身失败（网络/记录过期）

export interface DockerCmdRunResult {
  ok: boolean
  outcome: DockerCmdOutcome
  /** 后端 status（not-accepted/poll-error 时为空串）。 */
  status: string
  error?: string
  detail?: string
  /** image:save 第一段：目标文件已存在（前端据此弹「输入文件名」的第二段）。 */
  alreadyExists?: boolean
  payload?: unknown
  /** 受理期失败的分类（outcome === 'not-accepted' 时存在）。 */
  accept?: AcceptFailure
}

/** 结果 → 展示用结论句（views 统一口径：结果的 error 优先，其次受理分类，最后回退句）。 */
export function runErrorMessage(res: DockerCmdRunResult, fallback: string): string {
  return res.error || res.accept?.message || fallback
}

export interface DockerCmdInput {
  action: string
  target?: string
  options?: Record<string, unknown>
  /** 强确认档的逐字值（标准档与无需确认的动作留空）。 */
  confirm?: string
  /** 行级 pending 的键；缺省取 target（无 target 时取 action）。 */
  key?: string
}

export interface UseDockerCmdsOptions {
  /** 主机 id 来源；与 `host` 二选一（两者都给时以它为准）。 */
  hostId?: () => string
  /**
   * 页面自己的主机上下文（`provideDockerHost()` 的返回值）。
   *
   * **页面组件内调用时必须给**（与 useDockerHostState 同因）：Vue 的 inject 读
   * `parent.provides`，同组件 provide 之后 inject 拿不到自己的值；而且
   * `inject()` 只在 setup 期有效 —— 若把注入推迟到点击时（事件回调里没有实例），
   * 连子组件也会失效（`inject() can only be used inside setup()`）。故本 composable
   * **在 setup 期就把上下文定住**，之后所有指令都用这个快照。
   */
  host?: DockerHostContext
  /** 成功后的重拉（通常传 useDockerHostState().refresh）。 */
  refresh?: () => void | Promise<void>
  /** 轮询上限（默认 20，与一期 runRead 一致）；测试可调小。 */
  maxPollAttempts?: number
  /** 等待函数（默认 setTimeout）；测试注入即时实现。 */
  sleep?: (ms: number) => Promise<void>
  /** 「落定重拉」的延时（毫秒，默认 1500）；测试传 0 立即执行。 */
  settleMs?: number
}

export function useDockerCmds(options: UseDockerCmdsOptions = {}) {
  // 主机来源在 **setup 期**定住（惰性注入是错的：指令在事件回调里发出，那时没有
  // 组件实例，inject 必然失败）。三种来源优先级：显式 hostId > 显式 host > inject。
  // **给了 hostId 就不碰上下文**：调用方（含测试）自带来源时不该被迫要求 provide，
  // 否则纯函数式用法会被一条与它无关的约束打死。
  let resolveHostId: () => string
  if (options.hostId) {
    resolveHostId = options.hostId
  } else {
    const host = options.host ?? useDockerHost()
    resolveHostId = () => host.hostId
  }
  const sleep = options.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)))
  const maxPollAttempts = options.maxPollAttempts ?? 20

  const pendingId = ref<string | null>(null)
  // inflight 必须是 ref：busy 是 computed，若底层计数器是普通 let，Vue 无从知道它变了 ——
  // computed 没有响应式依赖就不再重算，busy 会永久停在**首次读取**时的值（实测：批量栏
  // 一直显示「操作中」）。
  const inflight = ref(0)
  const busy = computed(() => inflight.value > 0)

  /**
   * 「落定重拉」定时器的句柄集：**作用域销毁（调用方组件卸载）时全部撤销**。
   *
   * 为什么必须撤销：这些定时器长在组件之外 —— 页面卸载后它们照常到点，对已经不存在的
   * 页面白发若干次 refresh（真实 UX：写完操作后 1.5s 内切页 → 请求还是飞出去了）。
   * 测试里它们还会**跨用例存活**：放大复现时实测到下一条用例的窗口里冒出上一条用例的
   * 重拉请求（调用栈落在 useResourceList 的 load 上，来源是旧实例）；
   * 那种「上一条用例的尾巴打进下一条用例」的噪声正是闪烁类问题最难查的形态之一。
   * 用集合而不是单个句柄：批量操作会并发跑多条指令，每条成功各自排一次落定重拉，
   * 只留最后一个句柄会让先排的那几次在卸载后照旧飞出去（漏一半）。
   * 作用域即调用方组件（本 composable 在 setup 期调用）；无作用域时（纯函数式用法）
   * 不注册清理，行为与之前一致。
   */
  const settleTimers = new Set<ReturnType<typeof setTimeout>>()
  const scope = getCurrentScope()
  if (scope) {
    onScopeDispose(() => {
      for (const t of settleTimers) clearTimeout(t)
      settleTimers.clear()
    })
  }

  async function run(input: DockerCmdInput): Promise<DockerCmdRunResult> {
    const key = input.key ?? input.target ?? input.action
    // 主机 id 在**入口处固定**：指令在途时用户切了主机，轮询若跟着新主机走，
    // 会拿老主机的 ref 去查新主机（记录不存在）→ 误报「轮询失败」，
    // 而那条操作其实可能已经成功。一次指令只属于受理它的那台主机。
    const hostId = resolveHostId()
    pendingId.value = key
    inflight.value++
    try {
      // ── 1. 受理 ──────────────────────────────────────────────────────
      let ref: string
      try {
        const accepted = await sendDockerCmd(hostId, {
          action: input.action,
          target: input.target,
          options: input.options,
          confirm: input.confirm
        })
        ref = accepted.ref
      } catch (e) {
        return { ok: false, outcome: 'not-accepted', status: '', accept: classifyAcceptError(e) }
      }

      // ── 2. 轮询到终态 ────────────────────────────────────────────────
      let attempt = 0
      for (;;) {
        let res: Awaited<ReturnType<typeof fetchDockerCmdResult>>
        try {
          res = await fetchDockerCmdResult(hostId, ref)
        } catch (e) {
          return {
            ok: false,
            outcome: 'poll-error',
            status: '',
            error: classifyAcceptError(e).message
          }
        }
        if (res.status !== 'pending') {
          const ok = res.status === 'succeeded'
          if (ok) {
            // 操作成功 → 立即重拉快照 + 一次**落定重拉**。
            //
            // agent 在结果落定时已 push 一帧（不必等 30s 周期），但「立即重拉」与那一帧
            // 会擦肩：重拉读的是 core 缓存，而缓存要等新帧到达才换 —— 只拉一次时页面
            // 常有约一半概率仍显示旧状态（点了停止列表还写着 Up），这正是 §11.0 要消除
            // 的信任摧毁。故再补一次短延时重拉（settleMs，默认 1.5s）覆盖两种到达次序。
            // 重拉失败不影响指令结论（它只是展示层动作）。
            try {
              await options.refresh?.()
              const settle = options.settleMs ?? 1500
              if (options.refresh && settle >= 0) {
                const t = setTimeout(() => {
                  settleTimers.delete(t)
                  void options.refresh?.()
                }, settle)
                settleTimers.add(t)
              }
            } catch {
              /* 静默：陈旧/离线由页面头部标注 */
            }
          }
          return {
            ok,
            outcome: ok ? 'succeeded' : 'failed',
            status: res.status,
            error: res.error,
            detail: res.detail,
            alreadyExists: res.alreadyExists,
            payload: res.payload
          }
        }
        await sleep(pollDelay(attempt++))
        if (attempt > maxPollAttempts) {
          return { ok: false, outcome: 'timeout', status: 'timeout', error: '操作超时，请稍后重试' }
        }
      }
    } finally {
      inflight.value--
      if (inflight.value === 0) pendingId.value = null
    }
  }

  return { run, pendingId, busy }
}
