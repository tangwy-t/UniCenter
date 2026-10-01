<template>
  <div class="docker-page art-full-height">
    <!-- 主机条：切换器 + 同步状态 + 刷新。陈旧时状态文字变琥珀色（唯一一处颜色即结论）。
         拉取失败压过新鲜度结论（syncTextWithError）：失败时绝不显示「刚刚同步」。 -->
    <div class="docker-page__bar">
      <HostSwitcher>
        <!-- 任务中心入口（6b）：长任务的跨主机收口面板。挂在主机条上是因为它回答的
             是「这台机器相关的操作都去哪了」—— 但列表本身跨主机（hostname 列给归属），
             不随当前主机切换收窄。权限与 GET /docker/tasks 同档（docker:list），
             无权限不渲染（分期控件矩阵：不渲染 ≠ 禁用）。不挂 pending 徽标：计数
             需要为它单独拉一次列表，且任何时刻展示的都是过期数字 —— 入口保持零开销，
             进行中的数量进抽屉里看（过滤即「进行中」）。 -->
        <ElButton v-if="canList" size="small" @click="tasksVisible = true">任务</ElButton>
        <ElButton :loading="loading" size="small" @click="$emit('refresh')">刷新</ElButton>
        <span class="docker-page__sync" :class="staleClass(stale)">
          {{ syncTextWithError(loadError, hasState, stale, ageSeconds, neverReported) }}
        </span>
        <span v-if="ctx.host && !ctx.host.online" class="docker-page__offline">
          Agent 离线，数据为最后已知状态
        </span>
      </HostSwitcher>
    </div>

    <!-- 主机自报 Docker 不可用：整页只给结论与原因，不渲染任何资源表格。 -->
    <ElEmpty
      v-if="ctx.host && !ctx.host.dockerOk"
      :description="ctx.host.error || '该主机 Docker 不可用'"
    >
      <ElButton size="small" @click="$emit('refresh')">重新检测</ElButton>
    </ElEmpty>
    <template v-else>
      <slot name="search" />
      <ElCard class="art-table-card" shadow="never">
        <slot name="table" />
        <slot name="footer" />
      </ElCard>
    </template>

    <!-- 任务中心抽屉（6b）：Docker 不可用的主机也有任务史（排障恰恰需要看「刚才对它
         发过什么」），故挂在 v-if/v-else 之外、页面根节点之内。抽屉状态随页走
         （本组件每个使用它的页面各挂一份实例，切换页面即回到关闭态）。 -->
    <TaskCenterDrawer v-if="canList" v-model="tasksVisible" />
  </div>
</template>

<script setup lang="ts">
  import { computed, ref } from 'vue'
  import { ElButton, ElCard, ElEmpty } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerList } from '@/enums/permission'
  import HostSwitcher from './host-switcher.vue'
  import TaskCenterDrawer from './task-center-drawer.vue'
  import { staleClass, syncTextWithError } from '../utils/host'
  import { useDockerHost } from '../utils/host-context'

  withDefaults(
    defineProps<{
      loading?: boolean
      stale?: boolean
      ageSeconds?: number
      neverReported?: boolean
      /** 最近一次快照拉取是否失败（D-1：失败时同步文案不得回落到「刚刚同步」）。 */
      loadError?: boolean
      /** 是否握有快照：失败时区分「数据获取失败」与「上次同步 + 本次刷新失败」。 */
      hasState?: boolean
    }>(),
    {
      loading: false,
      stale: false,
      ageSeconds: 0,
      neverReported: false,
      loadError: false,
      hasState: false
    }
  )
  defineEmits<{ refresh: [] }>()

  // 解构会丢掉 getter 的响应性（host 只在此刻求值一次，主机清单到达后永远看不到），
  // 故保留 ctx 对象在模板里按 `ctx.host` 访问 —— 首次加载 host 为 undefined 时
  // 由模板的 `v-if="ctx.host && …"` 守卫。
  const ctx = useDockerHost()

  // ── 任务中心入口（6b） ─────────────────────────────────────────
  // 权限与任务列表端点同档（docker:list）；入口与抽屉共用同一判定 —— 无权限时
  // 连抽屉实例都不挂（v-if 在组件上，setup 也不跑）。
  const { hasAuth } = useAuth()
  const canList = computed(() => hasAuth(PermDockerList))
  const tasksVisible = ref(false)
</script>

<style lang="scss" scoped>
  // 页头主机条：flex 行、居中、窄屏换行（行内元素在 host-switcher 的 .docker-host）。
  // 下边距对齐模块节奏（12px），不与 ArtSearchBar 自带的留白叠加。
  .docker-page__bar {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: center;
    margin-bottom: 12px;
  }

  // 同步文案：辅助信息，小号次要色（颜色即结论，唯一的例外见 --warn）。
  .docker-page__sync {
    color: var(--el-text-color-secondary);
    font-size: 12px;

    // 陈旧：琥珀即结论（与模块内陈旧/保护标注同一套颜色语言）。
    &.docker-sync--warn {
      color: var(--el-color-warning);
    }
  }

  // Agent 离线：数据是最后已知状态，不是故障——琥珀（需要注意）而非红。
  .docker-page__offline {
    color: var(--el-color-warning);
    font-size: 12px;
  }
</style>
