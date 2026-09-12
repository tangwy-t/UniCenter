<!-- 30 天登录脉搏:每日一根竖条,高度=当日次数,红帽=当日有失败,零活动=灰色刻度 -->
<template>
  <div>
    <div class="flex-c justify-between gap-2">
      <h2 class="text-sm font-medium text-g-800">{{ '近 30 天登录活动' }}</h2>
      <div class="flex-c gap-3 text-[11px] text-g-500">
        <span class="flex-c gap-1.5">
          <i class="uc-legend uc-legend--ok" aria-hidden="true" />
          {{ '成功' }}
        </span>
        <span class="flex-c gap-1.5">
          <i class="uc-legend uc-legend--bad" aria-hidden="true" />
          {{ '失败' }}
        </span>
      </div>
    </div>

    <!-- 脉搏条:30 根,悬停显示当日明细(title) -->
    <div class="mt-4 flex h-9 items-end gap-[3px]" role="img" :aria-label="stripLabel">
      <div
        v-for="(day, i) in daily"
        :key="day.date"
        class="uc-bar"
        :title="barTitle(day)"
        :style="{ animationDelay: `${i * 12}ms` }"
      >
        <template v-if="day.success + day.failed > 0">
          <i v-if="day.failed > 0" class="uc-bar-fail" aria-hidden="true" />
          <i class="uc-bar-ok" :style="{ height: `${okHeight(day)}px` }" aria-hidden="true" />
        </template>
        <i v-else class="uc-bar-empty" aria-hidden="true" />
      </div>
    </div>

    <!-- 三项读数:累计登录 / 30 天失败 / 加入天数 -->
    <div class="mt-5 grid grid-cols-3 border-t border-g-300/60 pt-4">
      <div class="uc-vital">
        <span class="uc-vital-value">{{ stats.totalLogins }}</span>
        <span class="uc-vital-label">{{ '累计登录' }}</span>
      </div>
      <div class="uc-vital">
        <span class="uc-vital-value" :class="{ 'uc-vital-value--bad': stats.failed30d > 0 }">
          {{ stats.failed30d }}
        </span>
        <span class="uc-vital-label">{{ '30 天失败尝试' }}</span>
      </div>
      <div class="uc-vital">
        <span v-if="joined.today" class="uc-vital-value uc-vital-value--sm">
          {{ '今天加入' }}
        </span>
        <span v-else class="uc-vital-value">{{ joined.days }}</span>
        <span class="uc-vital-label">{{ '加入天数' }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { computed } from 'vue'
  import { parseServerTime } from '../utils'

  defineOptions({ name: 'UcActivityPulse' })

  const props = defineProps<{
    stats: Api.Auth.LoginStats
    daily: Api.Auth.DailyActivity[]
    createdAt?: string
  }>()

  /** 当日总高度:8px 起步,每次登录 +4px,封顶 32px(容器 h-9=36px) */
  const barHeight = (day: Api.Auth.DailyActivity): number => {
    const total = day.success + day.failed
    return Math.min(8 + total * 4, 32)
  }
  const okHeight = (day: Api.Auth.DailyActivity): number =>
    barHeight(day) - (day.failed > 0 ? 4 : 0)

  const barTitle = (day: Api.Auth.DailyActivity): string => {
    const date = day.date.slice(5)
    if (day.success + day.failed === 0) return `${date} · 无登录`
    return `${date} · 成功 ${day.success} / 失败 ${day.failed}`
  }

  const stripLabel = computed(
    () => `近 30 天登录活动: 成功 ${props.stats.logins30d}, 失败 ${props.stats.failed30d}`
  )

  /** 加入天数:创建日期到今天的自然日差 */
  const joined = computed<{ today: boolean; days: number }>(() => {
    const d = parseServerTime(props.createdAt)
    if (!d) return { today: false, days: 0 }
    const start = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
    const now = new Date()
    const today = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
    const days = Math.max(0, Math.round((today - start) / 86_400_000))
    return { today: days < 1, days }
  })
</script>

<style scoped>
  /* 单根条:列容器,自底向上堆叠(失败红帽在顶) */
  .uc-bar {
    display: flex;
    flex: 1 1 0;
    flex-direction: column;
    justify-content: flex-end;
    height: 100%;
    min-width: 4px;
    overflow: hidden;
    border-radius: 3px;
    animation: uc-grow 280ms cubic-bezier(0.22, 1, 0.36, 1) both;
    transform-origin: bottom;
  }

  .uc-bar-ok {
    width: 100%;
    background-color: var(--art-primary);
  }

  .uc-bar-fail {
    width: 100%;
    height: 4px;
    flex: none;
    background-color: var(--art-danger);
  }

  .uc-bar-empty {
    width: 100%;
    height: 4px;
    background-color: var(--art-gray-300);
  }

  @keyframes uc-grow {
    from {
      opacity: 0;
      transform: scaleY(0);
    }
    to {
      opacity: 1;
      transform: scaleY(1);
    }
  }

  .uc-legend {
    width: 8px;
    height: 8px;
    border-radius: 2px;
  }

  .uc-legend--ok {
    background-color: var(--art-primary);
  }

  .uc-legend--bad {
    background-color: var(--art-danger);
  }

  /* 读数单元:竖分隔线,数字用等宽 */
  .uc-vital {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
    padding-left: 16px;
    border-left: 1px solid color-mix(in srgb, var(--art-gray-300) 60%, transparent);
  }

  .uc-vital:first-child {
    padding-left: 0;
    border-left: none;
  }

  .uc-vital-value {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-size: 20px;
    font-weight: 600;
    line-height: 1;
    color: var(--art-gray-800);
    font-variant-numeric: tabular-nums;
  }

  .uc-vital-value--bad {
    color: var(--art-danger);
  }

  .uc-vital-value--sm {
    font-size: 13px;
    font-weight: 500;
    line-height: 20px;
  }

  .uc-vital-label {
    overflow: hidden;
    font-size: 11px;
    color: var(--art-gray-500);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  @media (prefers-reduced-motion: reduce) {
    .uc-bar {
      animation: none;
    }
  }
</style>
