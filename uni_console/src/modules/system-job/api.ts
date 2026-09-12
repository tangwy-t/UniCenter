import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'

const PREFIX = import.meta.env.VITE_API_PREFIX

export function fetchJobs(params: Api.Job.Query) {
  return request.get<PageResponse<Api.Job.Job>>({ url: `${PREFIX}/jobs`, params })
}
export function fetchJob(id: string) {
  return request.get<Api.Job.Job>({ url: `${PREFIX}/jobs/${id}` })
}
export function createJob(data: Api.Job.Form) {
  return request.post<{ id: string }>({ url: `${PREFIX}/jobs`, data })
}
export function updateJob(id: string, data: Api.Job.Form) {
  return request.put<void>({ url: `${PREFIX}/jobs/${id}`, data })
}
export function removeJob(id: string) {
  return request.del<void>({ url: `${PREFIX}/jobs/${id}` })
}
export function pauseJob(id: string) {
  return request.post<void>({ url: `${PREFIX}/jobs/${id}/pause` })
}
export function resumeJob(id: string) {
  return request.post<void>({ url: `${PREFIX}/jobs/${id}/resume` })
}
export function runJob(id: string) {
  return request.post<void>({ url: `${PREFIX}/jobs/${id}/run` })
}
export function fetchJobLogs(params: Api.Job.LogQuery) {
  return request.get<PageResponse<Api.Job.JobLog>>({ url: `${PREFIX}/jobs/logs`, params })
}
/** 可用任务目标清单（含显示名与参数 schema） */
export function fetchJobTargets() {
  return request.get<Api.Job.TargetInfo[]>({ url: `${PREFIX}/jobs/targets` })
}
/** 调度器健康状态（轮询用，失败静默降级） */
export function fetchJobHealth() {
  return request.get<Api.Job.Health>({
    url: `${PREFIX}/jobs/health`,
    showErrorMessage: false
  })
}
/** 清理指定时间之前的任务日志 */
export function deleteJobLogs(before: string) {
  return request.del<void>({ url: `${PREFIX}/jobs/logs`, params: { before } })
}
