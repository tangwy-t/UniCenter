<!-- 个人中心:档案头(身份 + 30 天登录脉搏)/ 最近登录活动 / 账号设置 -->
<template>
  <div v-loading="pageLoading" class="art-full-height overflow-y-auto">
    <div class="mx-auto w-full max-w-6xl p-4 pb-8 md:p-5">
      <!-- ── 档案头:身份 + 脉搏 ─────────────────────────── -->
      <section class="uc-card uc-anim p-6 max-md:p-5">
        <div class="flex flex-col gap-8 xl:flex-row xl:items-stretch">
          <!-- 身份 -->
          <div class="flex min-w-0 flex-1 items-start gap-5">
            <!-- 头像(悬停/聚焦出现替换入口) -->
            <div
              class="uc-avatar relative shrink-0 cursor-pointer rounded-full ring-1 ring-g-300"
              role="button"
              tabindex="0"
              aria-label="更换头像"
              @click="openAvatarPicker"
              @keydown.enter.prevent="openAvatarPicker"
              @keydown.space.prevent="openAvatarPicker"
            >
              <img
                class="size-23 rounded-full object-cover max-md:size-20"
                :src="avatarSrc"
                :alt="displayName"
                @error="onAvatarError"
              />
              <div v-show="!avatarUploading" class="uc-avatar-mask">
                <ArtSvgIcon icon="ri:camera-line" class="text-xl" />
                <span class="mt-1 text-xs">{{ '更换头像' }}</span>
              </div>
              <div v-show="avatarUploading" class="uc-avatar-mask uc-avatar-mask--visible">
                <span class="uc-spinner" aria-hidden="true" />
                <span class="mt-1 text-xs">{{ '上传中…' }}</span>
              </div>
              <input
                ref="avatarInputRef"
                type="file"
                accept="image/jpeg,image/png,image/webp,image/gif"
                class="hidden"
                @change="onAvatarChange"
              />
            </div>

            <div class="min-w-0 flex-1">
              <p class="text-xs text-g-500">{{ greeting }}</p>
              <div class="mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-1">
                <h1 class="truncate text-xl font-semibold text-g-900" :title="displayName">
                  {{ displayName }}
                </h1>
                <span class="uc-mono text-sm text-g-500">@{{ username }}</span>
              </div>

              <!-- 部门 + 角色 -->
              <div v-if="deptName || roleBriefs.length" class="mt-2.5 flex flex-wrap gap-1.5">
                <span v-if="deptName" class="uc-chip" :title="deptName">
                  <ArtSvgIcon icon="ri:node-tree" />
                  <span class="truncate">{{ deptName }}</span>
                </span>
                <span
                  v-for="r in roleBriefs"
                  :key="r.code"
                  class="uc-chip uc-chip--role"
                  :title="r.code"
                >
                  {{ r.name || r.code }}
                </span>
              </div>

              <!-- 元数据(等宽数值) -->
              <dl class="mt-5 grid max-w-xl grid-cols-2 gap-x-8 gap-y-3.5">
                <div class="min-w-0">
                  <dt class="uc-meta-label">{{ '邮箱' }}</dt>
                  <dd class="uc-meta-value" :title="email">
                    {{ email || '未设置' }}
                  </dd>
                </div>
                <div class="min-w-0">
                  <dt class="uc-meta-label">{{ '手机号' }}</dt>
                  <dd class="uc-meta-value uc-mono" :title="phone">
                    {{ phone || '未设置' }}
                  </dd>
                </div>
                <div class="min-w-0">
                  <dt class="uc-meta-label">{{ '昵称' }}</dt>
                  <dd class="uc-meta-value" :title="nickname">
                    {{ nickname || '未设置' }}
                  </dd>
                </div>
                <div class="min-w-0">
                  <dt class="uc-meta-label">{{ '性别' }}</dt>
                  <dd class="uc-meta-value" :title="genderText">{{ genderText }}</dd>
                </div>
                <div class="min-w-0">
                  <dt class="uc-meta-label">{{ '用户 ID' }}</dt>
                  <dd class="uc-meta-value uc-mono" :title="userId">{{ userId }}</dd>
                </div>
                <div class="min-w-0">
                  <dt class="uc-meta-label">{{ '注册时间' }}</dt>
                  <dd class="uc-meta-value uc-mono">{{ joinedDate }}</dd>
                </div>
              </dl>

              <!-- 上次登录(当前会话之前的最近一次成功登录) -->
              <p class="mt-4 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-g-500">
                <i
                  class="uc-dot shrink-0"
                  :class="lastLogin ? 'uc-dot--ok' : 'uc-dot--idle'"
                  aria-hidden="true"
                />
                <span>{{ '上次登录' }}</span>
                <template v-if="lastLogin">
                  <span class="uc-mono text-g-700">{{ relText(lastLogin.time) }}</span>
                  <template v-if="lastLogin.ip">
                    <span class="text-g-400" aria-hidden="true">·</span>
                    <span class="uc-mono text-g-700">{{ lastLogin.ip }}</span>
                  </template>
                  <template v-if="lastLogin.client">
                    <span class="text-g-400" aria-hidden="true">·</span>
                    <span>{{ lastLogin.client }}</span>
                  </template>
                </template>
                <span v-else>{{ '首次登录' }}</span>
              </p>
            </div>
          </div>

          <!-- 30 天登录脉搏 -->
          <div
            class="shrink-0 border-g-300/60 max-xl:border-t max-xl:pt-8 xl:w-[340px] xl:border-l xl:pl-8"
          >
            <ActivityPulse
              v-if="overview"
              :stats="overview.stats"
              :daily="overview.dailyActivity"
              :created-at="overview.createdAt"
            />
            <div v-else class="h-[150px]" />
          </div>
        </div>
      </section>

      <!-- ── 活动 + 设置 ───────────────────────────────── -->
      <div class="mt-4 grid gap-4 xl:grid-cols-[minmax(0,1.35fr)_minmax(0,1fr)]">
        <LoginActivity class="uc-anim uc-anim-2 min-w-0" :logins="overview?.recentLogins ?? []" />
        <AccountSettings
          class="uc-anim uc-anim-3 min-w-0"
          :profile="profileSnapshot"
          @saved="reloadOverview"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import { computed, onMounted, ref, watch } from 'vue'
  import { ElMessage } from 'element-plus'
  import { useUserStore } from '@/store/modules/user'
  import { useDict } from '@/hooks/core/useDict'
  import { resolveAvatar } from '@/utils/avatar'
  import { fetchMyOverview, fetchMyProfile, uploadMyAvatar } from '../api'
  import ActivityPulse from './components/ActivityPulse.vue'
  import LoginActivity from './components/LoginActivity.vue'
  import AccountSettings from './components/AccountSettings.vue'
  import { clientText, datePart, relTime, type RelTime } from './utils'
  import defaultAvatar from '@imgs/user/avatar.webp'

  defineOptions({ name: 'SystemUserCenter' })

  const userStore = useUserStore()

  /* ── 数据加载:store 资料 + 页面总览并行 ────────────── */
  const pageLoading = ref(true)
  const overview = ref<Api.Auth.UserOverview | null>(null)
  const userInfo = computed(() => (userStore.info ?? {}) as Api.Auth.UserInfo)

  const loadAll = async () => {
    pageLoading.value = true
    try {
      const [fresh, ov] = await Promise.all([fetchMyProfile(), fetchMyOverview(), ensureGender()])
      userStore.setUserInfo(fresh)
      overview.value = ov
    } finally {
      pageLoading.value = false
    }
  }

  /** 资料保存后仅刷新总览(store 已由设置面板回源更新) */
  const reloadOverview = async () => {
    overview.value = await fetchMyOverview()
  }

  onMounted(loadAll)

  /* ── 身份展示 ───────────────────────────────────── */
  const displayName = computed(() => userInfo.value.realName || userInfo.value.username || '—')
  const username = computed(() => overview.value?.username || userInfo.value.username || '')
  const email = computed(() => userInfo.value.email || '')
  const phone = computed(() => userInfo.value.phone || '')
  const nickname = computed(() => userInfo.value.nickname || '')
  const { ensure: ensureGender, labelOf: genderLabelOf } = useDict('sys_user_gender')
  const genderText = computed(() => {
    const g = userInfo.value.gender
    return g ? genderLabelOf(g) : '未设置'
  })
  const userId = computed(() => userInfo.value.id || overview.value?.id || '—')
  const deptName = computed(() => overview.value?.deptName || '')
  const roleBriefs = computed<Api.Auth.RoleBrief[]>(() => overview.value?.roles ?? [])
  const joinedDate = computed(() => datePart(overview.value?.createdAt))

  const profileSnapshot = computed(() => {
    const ov = overview.value
    const u = userInfo.value
    return {
      realName: ov?.realName ?? u.realName ?? '',
      nickname: u.nickname ?? '',
      email: ov?.email ?? u.email ?? '',
      phone: ov?.phone ?? u.phone ?? '',
      gender: u.gender ?? ''
    }
  })

  /* ── 问候语(按当前时段) ──────────────────────────── */
  const greeting = computed(() => {
    const h = new Date().getHours()
    if (h < 6) return '夜深了，注意休息'
    if (h < 9) return '早上好'
    if (h < 12) return '上午好'
    if (h < 14) return '中午好'
    if (h < 18) return '下午好'
    return '晚上好'
  })

  /* ── 上次登录:跳过首条(通常是当前会话),取更早的成功记录 ── */
  const lastLogin = computed<{ time: string; ip: string; client: string } | null>(() => {
    const logs = overview.value?.recentLogins ?? []
    const prev = logs.slice(1).find((l) => l.code === 0)
    if (!prev) return null
    return {
      time: prev.time,
      ip: prev.ip,
      client: clientText(prev.browser, prev.os, '未知')
    }
  })

  const relText = (value?: string | null): string => {
    const r: RelTime = relTime(value)
    if (!r) return '—'
    switch (r.kind) {
      case 'now':
        return '刚刚'
      case 'minutes':
        return `${r.n} 分钟前`
      case 'hours':
        return `${r.n} 小时前`
      case 'days':
        return `${r.n} 天前`
      case 'date':
        return r.text
    }
  }

  /* ── 头像上传 ───────────────────────────────────── */
  const avatarInputRef = ref<HTMLInputElement>()
  const avatarUploading = ref(false)

  const avatarSrc = ref(defaultAvatar)

  watch(
    () => userInfo.value.avatar,
    (v) => {
      avatarSrc.value = resolveAvatar(v)
    },
    { immediate: true }
  )

  const onAvatarError = () => {
    avatarSrc.value = resolveAvatar()
  }

  const openAvatarPicker = () => {
    if (avatarUploading.value) return
    avatarInputRef.value?.click()
  }

  const onAvatarChange = async (event: Event) => {
    const input = event.target as HTMLInputElement
    const file = input.files?.[0]
    // 先清空 value:取消选择后重选同一文件也能触发 change
    input.value = ''
    if (!file) return

    // 客户端预校验(服务端仍有类型/大小兜底),避免无效请求
    if (!/^image\/(jpe?g|png|webp|gif)$/.test(file.type) || file.size > 2 * 1024 * 1024) {
      ElMessage.error('请选择 JPG/PNG/WebP/GIF 图片，且大小不超过 2MB')
      return
    }

    avatarUploading.value = true
    try {
      const { avatar } = await uploadMyAvatar(file)
      userStore.setUserInfo({ ...(userStore.info as Api.Auth.UserInfo), avatar })
      ElMessage.success('头像已更新')
    } finally {
      avatarUploading.value = false
    }
  }
</script>

<style scoped>
  /* ---------- 卡片(与系统监控页同一视觉语言) ---------- */
  .uc-card {
    border: 1px solid var(--default-border);
    border-radius: 14px;
    background: var(--default-box-color);
    box-shadow: 0 1px 3px rgba(16, 24, 40, 0.05);
  }

  .dark .uc-card {
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.4);
  }

  /* ---------- 入场:轻微上移淡入,60ms 交错 ---------- */
  .uc-anim {
    animation: uc-rise 300ms ease both;
  }

  .uc-anim-2 {
    animation-delay: 60ms;
  }

  .uc-anim-3 {
    animation-delay: 120ms;
  }

  @keyframes uc-rise {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }

  /* ---------- 等宽数值(机器数据:ID/IP/时间/手机号) ---------- */
  .uc-mono {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Fira Code', monospace;
    font-variant-numeric: tabular-nums;
  }

  /* ---------- 徽章 ---------- */
  .uc-chip {
    display: inline-flex;
    max-width: 240px;
    align-items: center;
    gap: 5px;
    padding: 3px 10px;
    overflow: hidden;
    font-size: 12px;
    white-space: nowrap;
    border: 1px solid var(--default-border);
    border-radius: 999px;
    background: var(--art-gray-100);
    color: var(--art-gray-600);
  }

  .uc-chip :deep(svg) {
    flex: none;
    color: var(--art-primary);
  }

  .uc-chip--role {
    border-color: transparent;
    background: color-mix(in srgb, var(--art-primary) 10%, transparent);
    color: var(--art-primary);
    font-weight: 500;
  }

  /* ---------- 元数据 ---------- */
  .uc-meta-label {
    margin-bottom: 3px;
    font-size: 11px;
    color: var(--art-gray-500);
  }

  .uc-meta-value {
    overflow: hidden;
    font-size: 13px;
    color: var(--art-gray-800);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* ---------- 状态点 ---------- */
  .uc-dot {
    width: 8px;
    height: 8px;
    border-radius: 9999px;
  }

  .uc-dot--ok {
    background-color: var(--art-success);
  }

  .uc-dot--idle {
    background-color: var(--art-gray-300);
  }

  /* ---------- 头像 ---------- */
  .uc-avatar-mask {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    border-radius: 9999px;
    color: #fff;
    background-color: rgb(0 0 0 / 45%);
    opacity: 0;
    transition: opacity 200ms ease;
  }

  /* 上传中状态始终可见 */
  .uc-avatar-mask--visible {
    opacity: 1;
  }

  .uc-avatar:hover .uc-avatar-mask:not(.uc-avatar-mask--visible),
  .uc-avatar:focus-visible .uc-avatar-mask:not(.uc-avatar-mask--visible) {
    opacity: 1;
  }

  .uc-avatar:focus-visible {
    outline: 2px solid var(--art-primary);
    outline-offset: 2px;
  }

  /* 环形加载指示(仅头像上传态使用) */
  .uc-spinner {
    width: 20px;
    height: 20px;
    border: 2px solid rgb(255 255 255 / 40%);
    border-top-color: #fff;
    border-radius: 9999px;
    animation: uc-spin 700ms linear infinite;
  }

  @keyframes uc-spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .uc-anim {
      animation: none;
    }

    .uc-spinner {
      animation-duration: 1400ms;
    }
  }
</style>
