/**
 * 设备详情的**异常分类**（F-5）。
 *
 * ── 为什么需要分类，而不是直接显示 `e.message` ────────────────────
 *
 * 现状（重设计前）所有失败都是同一行红字 + 一个「重试」按钮。但五类失败的
 * **正确动作完全不同**：
 *
 * | 场景 | 用户真正该做的事 |
 * |---|---|
 * | 设备不存在(404) | 别再点重试（重试一万次也不会出现）→ 回列表页 |
 * | 无权限(403) | 找管理员开权限 → 重试无意义 |
 * | 参数错误(400) | 改查询条件（如下钻 range 越界） |
 * | 服务异常(5xx) | **稍等后重试**（唯一该显示重试按钮的类别） |
 * | 网络异常 | 检查网络/稍后重试 |
 *
 * 给「设备不存在」配一个重试按钮是纯粹的误导：用户会反复点，然后怀疑系统。
 *
 * ── 判定依据 ────────────────────────────────────────────────
 * 业务码用 `HttpError.bizCode`（与 `BizCode` 比较），**不能**用 HTTP 状态码
 * 与 BizCode 比 —— 本项目后端未授权的业务码是 10001，永不等于 HTTP 401
 * （见 utils/http/status.ts 顶部说明）。HTTP 状态走 `isHttpStatus`。
 */
import { ApiStatus, BizCode } from '@/utils/http/status'
import { isHttpError } from '@/utils/http/error'

/** 详情的失败类别。 */
export type DeviceErrorKind =
  /** 设备不存在 / 已被删除（业务码 40400 或 HTTP 404） */
  | 'notFound'
  /** 无权限（业务码 10002 或 HTTP 403） */
  | 'forbidden'
  /** 参数错误（业务码 40000；如下钻 range 越界） */
  | 'badRequest'
  /** 服务端异常（业务码 50000 或 HTTP 5xx） */
  | 'server'
  /** 网络不可达 / 请求被取消（拿不到响应） */
  | 'network'
  /** 其余 */
  | 'unknown'

export interface DeviceErrorInfo {
  kind: DeviceErrorKind
  /** 给用户看的主文案（失败原因）。 */
  title: string
  /** 告诉用户**下一步做什么**；空串表示无需额外说明。 */
  hint: string
  /**
   * 是否值得显示「重试」按钮。
   *
   * 只有 server / network / unknown 为 true：notFound/forbidden 重试无意义，
   * badRequest 需要改条件而不是重试。
   */
  retryable: boolean
  /** 后端原始 msg（用于 tooltip / 详情，便于排障对上报错）。 */
  raw: string
}

const FALLBACK: Record<DeviceErrorKind, { title: string; hint: string; retryable: boolean }> = {
  notFound: {
    title: '设备不存在或已被删除',
    hint: '请返回设备列表确认该设备是否仍然存在。',
    retryable: false
  },
  forbidden: {
    title: '暂无权限查看该设备',
    hint: '需要「设备查询」权限，请联系管理员开通。',
    retryable: false
  },
  badRequest: {
    // 参数错误的原始 msg 通常已经说清了（如「资源明细只保留 30 天」），
    // 故 title 留空由调用方用 raw 覆盖。
    title: '请求参数不被接受',
    hint: '请调整查询条件后重试。',
    retryable: false
  },
  server: {
    title: '服务暂时不可用',
    hint: '后端出现异常，请稍后重试；若持续出现请联系管理员。',
    retryable: true
  },
  network: {
    title: '网络连接异常',
    hint: '请检查网络或稍后重试。',
    retryable: true
  },
  unknown: {
    title: '加载失败',
    hint: '请稍后重试。',
    retryable: true
  }
}

/**
 * 把任意异常分类成 DeviceErrorInfo。
 *
 * 判定顺序很重要：**先业务码，再 HTTP 状态**。因为后端成功/失败都返回
 * HTTP 200 + 信封 code（见 utils/http/index.ts），业务码才是权威；只有
 * 网络层/网关层错误才没有业务码、只能看 HTTP 状态。
 */
export function classifyDeviceError(e: unknown): DeviceErrorInfo {
  const raw = e instanceof Error && e.message ? e.message : ''

  if (isHttpError(e)) {
    let kind: DeviceErrorKind | null = null
    if (e.isBiz(BizCode.notFound)) kind = 'notFound'
    else if (e.isBiz(BizCode.forbidden)) kind = 'forbidden'
    else if (e.isBiz(BizCode.badRequest)) kind = 'badRequest'
    else if (e.isBiz(BizCode.internal)) kind = 'server'
    // 业务码未命中时退回 HTTP 状态（网关/代理层错误没有业务码）。
    else if (e.isHttpStatus(ApiStatus.notFound)) kind = 'notFound'
    else if (e.isHttpStatus(ApiStatus.forbidden)) kind = 'forbidden'
    else if (
      e.isHttpStatus(ApiStatus.internalServerError) ||
      e.isHttpStatus(ApiStatus.badGateway) ||
      e.isHttpStatus(ApiStatus.serviceUnavailable) ||
      e.isHttpStatus(ApiStatus.gatewayTimeout)
    ) {
      kind = 'server'
    }

    if (kind) {
      const base = FALLBACK[kind]
      return {
        kind,
        // badRequest 保留后端 msg：它通常是具体且可操作的
        // （如「资源明细只保留 30 天，请把时间范围缩短到 30 天以内」）。
        // 后端那些消息本身已按「只讲结论」的口径写过（后端侧守卫见
        // service.TestUserFacingErrorsSpeakHuman），故此处理解为原样透传。
        title: kind === 'badRequest' && raw ? raw : base.title,
        hint: base.hint,
        retryable: base.retryable,
        raw
      }
    }
  }

  // 非 HttpError 或业务码/状态都未命中：无响应体 → 视为网络异常。
  if (raw) {
    return {
      kind: 'unknown',
      title: FALLBACK.unknown.title,
      hint: FALLBACK.unknown.hint,
      retryable: true,
      raw
    }
  }
  return { ...FALLBACK.network, kind: 'network', raw }
}
