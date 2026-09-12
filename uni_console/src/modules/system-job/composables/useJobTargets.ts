/**
 * 任务目标清单（GET /jobs/targets）模块级缓存。
 * 列表页（显示名映射）与弹窗（下拉/参数表单）共享同一份缓存，单飞避免重复请求。
 */
import { ref } from 'vue'
import { fetchJobTargets } from '../api'

let cache: Api.Job.TargetInfo[] | null = null
let pending: Promise<Api.Job.TargetInfo[]> | null = null

export function useJobTargets() {
  const targets = ref<Api.Job.TargetInfo[]>(cache ?? [])

  /** 确保清单已加载（force=true 强制刷新） */
  async function ensure(force = false): Promise<Api.Job.TargetInfo[]> {
    if (!force && cache) {
      targets.value = cache
      return cache
    }
    if (!pending) {
      pending = fetchJobTargets()
        .then((list) => {
          cache = list ?? []
          targets.value = cache
          return cache
        })
        .finally(() => {
          pending = null
        })
    }
    return pending
  }

  /** target id → displayName；不在清单内返回空串（调用方降级显示原始值） */
  function displayNameOf(target: string): string {
    return targets.value.find((t) => t.target === target)?.displayName ?? ''
  }

  return { targets, ensure, displayNameOf }
}
