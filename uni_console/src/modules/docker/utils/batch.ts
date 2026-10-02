/**
 * 批量波次（6d）的纯逻辑：按主机分波与波级结论句。
 *
 * 为什么单独立一份而不是塞进 workload-batch-bar：分波规则与结论句是**可单测的
 * 纯函数**（波怎么拆、句子怎么说），组件里只留「波怎么推进」的副作用编排 ——
 * 与 utils/actions 的注册表/确认档同一条「数据与判定进 utils、编排进组件」的分野。
 *
 * 语义边界（对齐切片纪律「只加分组推进与波级反馈，不改失败语义」）：
 *   - 波 = 同一主机上的全部选中行；跨主机选择拆成多波，**逐波推进**（下一波等
 *     上一波全部落定才开始 —— 一台主机的批量做完再动下一台，部分进度按主机可读）；
 *   - 波内逐行**照旧串行**：现状批量循环就是逐行 await（一次一条指令、轮询到终态
 *     再发下一条），波次只改分组与推进节奏，不引入波内并发 —— 并发会把同主机的
 *     指令队列从「顺序可解释」变成「交错难归因」，失败收集的口径也跟着变形；
 *   - 失败收集照旧跨全批累计、批末统一汇总（ElMessage，仍只报前三条原因 ——
 *     读不完的明细不倒出来）；P2 打磨批起波级结论句**点名到目标**：成功项在
 *     波级列名、失败项逐一可读（波是唯一留存的批后账目，只报计数会「不可追」，
 *     见 waveConclusion 的注释）。
 */
import type { DockerWorkloadItem } from '../api'

/** 一波 = 同一主机上的全部选中行（hostId 是分组键；hostname 仅用于展示）。 */
export interface BatchWave {
  hostId: string
  /** 波的主机名（行数据自带；空串时调用方回退 hostId 展示）。 */
  hostname: string
  rows: DockerWorkloadItem[]
}

/**
 * 按主机分波（混合主机选择拆成多波）。
 *
 * 主机间的次序 = 首次出现序（勾选顺序里谁先出现谁先执行）：不重排用户的意图，
 * 同主机的行无论勾选时是否相邻都归进同一波。Map 保证 hostId 唯一占波。
 */
export function groupBatchWaves(rows: DockerWorkloadItem[]): BatchWave[] {
  const waves: BatchWave[] = []
  const index = new Map<string, BatchWave>()
  for (const row of rows) {
    const existing = index.get(row.hostId)
    if (existing) {
      existing.rows.push(row)
    } else {
      const wave: BatchWave = { hostId: row.hostId, hostname: row.hostname, rows: [row] }
      index.set(row.hostId, wave)
      waves.push(wave)
    }
  }
  return waves
}

/** 一条失败（波级行与批末汇总共用的失败条目：目标名 + 结论句原文/人话）。 */
export interface BatchFailure {
  name: string
  message: string
}

/**
 * 波级结论句：点名到目标 —— 成功项在波级列名「主机 A：2/2 成功（web、cache）」，
 * 失败项逐一可读「主机 A：1/2 成功（web）；失败：cache（容器处于运行状态）」。
 *
 * 为什么点名（QA 路 2 P2「不可追」）：只报计数时，批量一过就说不清「这一批到底
 * 动了谁、谁没成」；行内账目是批后唯一留存的反馈（批末 toast 转瞬即逝），它必须
 * 能逐一核对。克制在别处兑现：批末汇总的失败明细仍只报前三条（读不完的不倒出来），
 * 全量失败名单（含原因）由按下分的波级行承载 —— 一波 = 一台主机上选中的行，本来
 * 就与用户的选择粒度对齐，不存在「全倒出来」的问题。
 *
 * okNames 按执行序传入（波内逐行成功的目标名）；failures 每项带定稿结论句
 * （受保护未发送的结论句同样如实展示）。全失败（okNames 空）时不出现空括号。
 */
export function waveConclusion(
  wave: BatchWave,
  okNames: string[],
  failures: BatchFailure[] = []
): string {
  const total = wave.rows.length
  const host = wave.hostname !== '' ? wave.hostname : wave.hostId
  const head = `${host}：${okNames.length}/${total} 成功`
  const okPart = okNames.length > 0 ? `（${okNames.join('、')}）` : ''
  if (failures.length === 0) return `${head}${okPart}`
  const failPart = failures.map((f) => `${f.name}（${f.message}）`).join('、')
  return `${head}${okPart}；失败：${failPart}`
}
