<!-- 调度器健康条：GET /jobs/health 数据，30s 轮询，页面隐藏时暂停；
     接口失败降级为"状态不可用"+重试，不影响任务列表主流程。 -->
<template>
  <div class="job-health-strip">
    <template v-if="health">
      <!-- 运行中 -->
      <div v-if="health.schedulerRunning" class="strip is-running">
        <span class="status-badge bg-success/12 text-success">
          <ArtSvgIcon icon="ri:pulse-line" />
          <span>调度器运行中</span>
        </span>
        <span class="metric"
          >任务 <b>{{ health.totalJobs }}</b></span
        >
        <span class="metric"
          >执行中 <b>{{ health.runningJobs }}</b></span
        >
        <span class="metric"
          >运行时长 <b>{{ health.uptime || '—' }}</b></span
        >
        <span class="metric hidden lg:inline-block"
          >实例 <b class="font-mono">{{ health.instanceId }}</b></span
        >
      </div>
      <!-- 调度器未运行（sys.scheduler.enabled 关闭或异常） -->
      <div v-else class="strip is-stopped">
        <span class="status-badge bg-warning/12 text-warning">
          <ArtSvgIcon icon="ri:close-circle-line" />
          <span>调度器未运行</span>
        </span>
        <span class="metric">请检查服务端 <b class="font-mono">sys.scheduler.enabled</b> 配置</span>
      </div>
      <div class="strip-actions">
        <span class="updated-at">更新于 {{ updatedAt || '—' }}</span>
        <ElTooltip content="刷新" placement="top">
          <div
            :class="['refresh-btn', { 'is-loading': loading }]"
            :aria-label="'刷新调度器状态'"
            @click="refresh"
          >
            <ArtSvgIcon icon="ri:refresh-line" :class="{ 'animate-spin': loading }" />
          </div>
        </ElTooltip>
      </div>
    </template>
    <!-- 首载失败/接口不可用：静默降级 -->
    <template v-else>
      <div class="strip is-unavailable">
        <span class="status-badge bg-g-300/55 text-g-700">
          <ArtSvgIcon icon="ri:plug-line" />
          <span>调度器状态不可用</span>
        </span>
        <span class="metric">任务列表不受影响</span>
      </div>
      <div class="strip-actions">
        <div
          :class="['refresh-btn', { 'is-loading': loading }]"
          :aria-label="'重试获取调度器状态'"
          @click="refresh"
        >
          <ArtSvgIcon icon="ri:refresh-line" :class="{ 'animate-spin': loading }" />
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { fetchJobHealth } from '../api'

  defineOptions({ name: 'JobHealthStrip' })

  const POLL_INTERVAL = 30_000

  const health = ref<Api.Job.Health | null>(null)
  const loading = ref(false)
  const updatedAt = ref('')
  let timer: ReturnType<typeof setInterval> | null = null

  async function refresh() {
    if (loading.value) return
    loading.value = true
    try {
      health.value = await fetchJobHealth()
      updatedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
    } catch {
      // 已 showErrorMessage:false；失败保留旧数据或继续显示"不可用"
      if (!health.value) updatedAt.value = ''
    } finally {
      loading.value = false
    }
  }

  function onVisibilityChange() {
    if (document.hidden) {
      stopPoll()
    } else {
      refresh()
      startPoll()
    }
  }

  function startPoll() {
    stopPoll()
    timer = setInterval(refresh, POLL_INTERVAL)
  }

  function stopPoll() {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  onMounted(() => {
    refresh()
    startPoll()
    document.addEventListener('visibilitychange', onVisibilityChange)
  })

  onUnmounted(() => {
    stopPoll()
    document.removeEventListener('visibilitychange', onVisibilityChange)
  })

  defineExpose({ refresh })
</script>

<style scoped>
  .job-health-strip {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 12px;
    padding: 0 16px;
    min-height: 44px;
    border-radius: 8px;
    background: var(--default-box-color, #fff);
    border: 1px solid var(--art-card-border, rgba(0, 0, 0, 0.08));
  }

  .strip {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 20px;
    min-height: 44px;
  }

  .status-badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border-radius: 6px;
    font-size: 13px;
    font-weight: 600;
  }

  .metric {
    font-size: 13px;
    color: var(--color-g-600, #7987a1);

    b {
      margin-left: 4px;
      color: var(--art-gray-900, #323251);
      font-weight: 600;
    }
  }

  .dark .metric b {
    color: var(--art-gray-900, #e3e3e8);
  }

  .strip-actions {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-left: auto;
  }

  .updated-at {
    font-size: 12px;
    color: var(--color-g-500, #949eb7);
  }

  .refresh-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border-radius: 6px;
    color: var(--color-g-600, #7987a1);
    cursor: pointer;
    transition:
      background-color 150ms ease,
      color 150ms ease;
  }

  .refresh-btn:hover {
    background: var(--art-hover-color, #edeff0);
    color: var(--color-g-800, #383853);
  }

  @media (prefers-reduced-motion: reduce) {
    .refresh-btn {
      transition: none;
    }
  }
</style>
