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
 *   - 失败收集照旧跨全批累计、批末统一汇总（ElMessage），波级结论句只是行内反馈，
 *     不承担失败明细（明细里是哪个容器，看批末汇总句）。
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

/**
 * 波级结论句：「主机 A：12/12 成功」；有失败时如实分开（「主机 A：10/12 成功，
 * 2 项失败」）—— 波级只报**计数**，失败明细（哪个容器、什么原因）仍走批末的
 * 失败汇总，两处不说重复的话。
 *
 * okCount 是该波内成功执行的行数；受保护未发送与执行失败都算「未成功」
 * （它们在批末汇总里各有自己的结论句）。
 */
export function waveConclusion(wave: BatchWave, okCount: number): string {
  const total = wave.rows.length
  const failed = total - okCount
  const host = wave.hostname !== '' ? wave.hostname : wave.hostId
  if (failed <= 0) return `${host}：${total}/${total} 成功`
  return `${host}：${okCount}/${total} 成功，${failed} 项失败`
}
