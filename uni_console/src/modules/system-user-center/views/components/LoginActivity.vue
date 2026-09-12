<!-- 最近登录活动:审计日志风格的行式列表(状态点 + 等宽时间/IP + 客户端 + 结果) -->
<template>
  <section class="uc-card flex flex-col">
    <header class="uc-card-head flex items-center justify-between gap-3 px-5 py-4">
      <div class="flex-c min-w-0">
        <ArtSvgIcon icon="ri:history-line" class="mr-2.5 shrink-0 text-lg text-g-700" />
        <div class="min-w-0">
          <h2 class="text-sm font-medium text-g-800">{{ '最近登录活动' }}</h2>
          <p class="mt-0.5 truncate text-xs text-g-500">
            {{ `仅你可见的最近 ${RECENT_LIMIT} 条登录记录` }}
          </p>
        </div>
      </div>
      <button v-if="canViewAll" type="button" class="uc-link shrink-0" @click="goFullLog">
        {{ '全部日志' }}
        <ArtSvgIcon icon="ri:arrow-right-line" class="text-sm" />
      </button>
    </header>

    <ul v-if="logins.length" class="uc-rows">
      <li v-for="(l, i) in logins" :key="`${l.time}-${i}`" class="uc-row flex-c gap-3 px-5 py-3">
        <i
          class="uc-dot shrink-0"
          :class="l.code === 0 ? 'uc-dot--ok' : 'uc-dot--bad'"
          aria-hidden="true"
        />
        <span class="uc-mono w-[82px] shrink-0 text-xs text-g-600" :title="l.time">
          {{ shortTime(l.time) }}
        </span>
        <span
          class="uc-mono hidden w-[112px] shrink-0 truncate text-xs text-g-800 sm:block"
          :title="l.ip"
        >
          {{ l.ip || '—' }}
        </span>
        <span class="hidden min-w-0 flex-1 flex-c gap-2 text-xs text-g-600 md:flex">
          <ArtSvgIcon :icon="browserIcon(l.browser)" class="shrink-0 text-sm" />
          <ArtSvgIcon :icon="osIcon(l.os)" class="shrink-0 text-sm" />
          <span class="truncate">{{ clientLabel(l) }}</span>
        </span>
        <span class="flex-1 md:hidden" />
        <span v-if="l.code === 0" class="shrink-0 text-xs text-success">
          {{ '成功' }}
        </span>
        <span
          v-else
          class="max-w-[45%] shrink-0 truncate text-xs text-danger"
          :title="l.msg || '失败'"
        >
          {{ l.msg || '失败' }}
        </span>
      </li>
    </ul>

    <div v-else class="flex flex-1 flex-col items-center justify-center px-5 py-12 text-center">
      <ArtSvgIcon icon="ri:shield-check-line" class="text-3xl text-g-400" />
      <p class="mt-3 text-sm text-g-600">{{ '暂无登录记录' }}</p>
      <p class="mt-1 text-xs text-g-500">{{ '新的登录会出现在这里' }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
  import { computed } from 'vue'
  import { useRouter } from 'vue-router'
  import { useUserStore } from '@/store/modules/user'
  import { browserIcon, clientText, osIcon, shortTime } from '../utils'

  defineOptions({ name: 'UcLoginActivity' })

  defineProps<{ logins: Api.Auth.RecentLogin[] }>()

  const router = useRouter()
  const userStore = useUserStore()

  /** 与后端 GetUserOverview 的 recentLogins 截断上限保持一致 */
  const RECENT_LIMIT = 10

  /** 「全部日志」入口:仅对拥有登录日志查询权限者展示(后端同源校验 system:log:login:list) */
  const canViewAll = computed(() => {
    const perms = (userStore.info?.permissions ?? []) as string[]
    return perms.includes('admin') || perms.includes('system:log:login:list')
  })

  /** 跳转登录日志页并预填自己的用户名(目标页从 query 初始化过滤条件) */
  const goFullLog = () => {
    const username = (userStore.info as Api.Auth.UserInfo | undefined)?.username?.trim() || ''
    router.push({
      path: '/monitor/logininfor',
      query: username ? { username } : undefined
    })
  }

  const clientLabel = (l: Api.Auth.RecentLogin) => clientText(l.browser, l.os, '未知')
</script>

<style scoped>
  .uc-card-head {
    border-bottom: 1px solid color-mix(in srgb, var(--art-gray-300) 55%, transparent);
  }

  .uc-mono {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-variant-numeric: tabular-nums;
  }

  .uc-rows {
    flex: 1;
  }

  .uc-row {
    border-top: 1px solid color-mix(in srgb, var(--art-gray-300) 55%, transparent);
    transition: background-color 150ms ease;
  }

  .uc-row:hover {
    background-color: var(--art-hover-color);
  }

  .uc-dot {
    width: 8px;
    height: 8px;
    border-radius: 9999px;
  }

  .uc-dot--ok {
    background-color: var(--art-success);
  }

  .uc-dot--bad {
    background-color: var(--art-danger);
  }

  .uc-link {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;
    color: var(--art-primary);
    cursor: pointer;
    transition: opacity 150ms ease;
  }

  .uc-link:hover {
    opacity: 0.75;
  }

  .uc-link:focus-visible {
    outline: 2px solid var(--art-primary);
    outline-offset: 2px;
    border-radius: 4px;
  }
</style>
