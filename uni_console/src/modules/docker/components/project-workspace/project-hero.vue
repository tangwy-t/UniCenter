<template>
  <!-- 单根（single-root 守卫：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="pwh">
    <!-- ═══ 实体头（与容器详情同一骨架：左身份、右动作）═══ -->
    <div class="pwh__head">
      <div class="pwh__identity">
        <div class="pwh__title-row">
          <ArtButtonTable
            icon="ri:arrow-left-line"
            icon-class="bg-g-300/55 text-g-700"
            title="返回项目列表"
            @click="emit('back')"
          />
          <h2 class="pwh__title">{{ project.name }}</h2>
          <!-- 状态点（StateDot 口径：颜色即结论，绿=运行中/琥珀=部分运行/灰=其余） -->
          <span class="pwh__dot" :class="`is-${stateTone}`" aria-hidden="true" />
          <span class="pwh__state">{{ stateText }}</span>
          <!-- 锁语义与列表保护列同款（锁图标 + 受保护）：不用 🔒 emoji ——
               无 emoji 字体的环境里会渲染成豆腐块；锁走 ArtSvgIcon 的图标范式。 -->
          <span v-if="project.protected" class="pwh__lock">
            <ArtSvgIcon icon="ri:lock-2-line" />
            受保护
          </span>
        </div>
        <!-- 元信息：容器/网元/配置文件/备份四件事一行说完（工作台的第一屏回答「这是什么」）。 -->
        <p class="pwh__meta">
          {{ containersText }} · 网元 {{ project.services }} · {{ configFilesText }} · 备份
          {{ backupCount }} 份
          <template v-if="missingCount > 0">
            · 另有 {{ missingCount }} 个网元没有容器（未列出）
          </template>
        </p>
        <!-- 主机行：主机是页面级上下文（项目是主机作用域的），切换 = 换一台机器看同一批事实。 -->
        <div class="pwh__host">
          <HostSwitcher>
            <ElButton size="small" :loading="loading" @click="emit('refresh')">刷新</ElButton>
            <span class="pwh__sync" :class="syncWarn ? 'pwh__sync--warn' : ''">
              {{ syncText }}
            </span>
            <span v-if="offline" class="pwh__offline">Agent 离线，数据为最后已知状态</span>
          </HostSwitcher>
        </div>
      </div>

      <div class="pwh__actions">
        <!-- 生命周期动作条：六个项目级动作照 projects.vue 展开区头部的语义 ——
             确认档/保护档的分流在父页（onProjectAction），这里只管渲染与禁用；
             受保护且无强制权限时整组禁用，结论句就在按钮旁（就近原则）。 -->
        <ElButton
          v-for="btn in buttons"
          :key="btn.action"
          size="small"
          :type="btn.entry.danger === 'normal' ? undefined : 'danger'"
          :plain="btn.entry.danger !== 'normal'"
          :disabled="actionDisabled"
          @click="emit('action', btn.action)"
        >
          {{ btn.label }}
        </ElButton>
        <span v-if="blockedConclusion" class="pwh__blocked">{{ blockedConclusion }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 工作台实体头（5b）：项目名 + 状态 + 元信息 + 生命周期动作条 + 主机行。
   *
   * 纯展示组件：确认档（Up/停止/下线走强确认）、保护档（force 开关）、补充勾选
   * （回收孤儿/删卷）全部在父页的确认弹窗里 —— 这里 emit('action', …) 一条了事，
   * 与 projects.vue 展开区头部按钮同一分工。按钮清单**注册表驱动**（perm/危险度
   * 照 DOCKER_ACTION_REGISTRY，不在此另写权限判断），文案沿用列表页的短标签。
   */
  import { computed } from 'vue'
  import { ElButton } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import HostSwitcher from '../host-switcher.vue'
  import { lookupDockerAction } from '../../utils/actions'
  import type { DockerProjectItem } from '../../api'

  defineOptions({ name: 'DockerProjectHero' })

  const props = defineProps<{
    project: DockerProjectItem
    /** 派生容器结论（「容器 N/M」，同一份快照归纳，父页给）。 */
    containersText: string
    /** 备份份数（配置区懒加载后回传；未到时如实显示 0）。 */
    backupCount: number
    /** 有网元没有容器（缩到 0 等）的缺口数（照列表页口径，父页给）。 */
    missingCount: number
    /** 动作条禁用：指令在途，或保护档不允许。 */
    actionDisabled: boolean
    /** 保护档结论句（允许时为空串）。 */
    blockedConclusion: string
    /** 主机同步结论句（D-1 口径，父页算好）。 */
    syncText: string
    /** 同步结论是否需要琥珀色（陈旧）。 */
    syncWarn: boolean
    /** Agent 离线（数据为最后已知状态，不是故障）。 */
    offline: boolean
    /** 快照在拉（刷新按钮的 loading）。 */
    loading: boolean
  }>()

  const emit = defineEmits<{
    (e: 'action', action: string): void
    (e: 'refresh'): void
    (e: 'back'): void
  }>()

  const { hasAuth } = useAuth()

  /** 动作条（照 projects.vue 展开区头部的六个动作；perm/危险度全部来自注册表条目）。 */
  const HERO_ACTIONS = [
    { action: 'compose:up', label: 'Up -d' },
    { action: 'compose:stop', label: '停止' },
    { action: 'compose:start', label: '启动' },
    { action: 'compose:restart', label: '重启' },
    { action: 'compose:pull', label: '拉取' },
    { action: 'compose:down', label: '下线…' }
  ] as const

  /** 按权限过滤后的按钮集（无权限不渲染 ≠ 禁用，spec §11.0 分期控件矩阵）。 */
  const buttons = computed(() =>
    HERO_ACTIONS.flatMap((h) => {
      // 注册表里没有的动作不渲染（与 DockerActionMenu 同一取向：宁可少一项，也不生成
      // 一个没权限/没确认档的裸按钮）。
      const entry = lookupDockerAction(h.action)
      return entry && hasAuth(entry.perm) ? [{ ...h, entry }] : []
    })
  )

  /** 项目状态（与列表页同一批文案；未知给「—」而不是猜）。 */
  const stateText = computed(() => {
    if (props.project.state === 'running') return '运行中'
    if (props.project.state === 'partial') return '部分运行'
    if (props.project.state === 'stopped') return '已停止'
    return '—'
  })

  const stateTone = computed<'success' | 'warn' | 'off'>(() => {
    if (props.project.state === 'running') return 'success'
    if (props.project.state === 'partial') return 'warn'
    return 'off'
  })

  /** 配置文件数（旧版 compose 不上报路径时如实说「未知」）。 */
  const configFilesText = computed(() => {
    const n = props.project.configFiles?.length ?? 0
    return n > 0 ? `配置文件 ${n} 个` : '配置文件未知'
  })
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../../views/overview-tokens' as t;

  // hero 的 danger 动作（停止/下线…）按注册表渲染成 plain danger：对比度 AA 的
  // 病灶与处方见 overview-tokens 的 danger-plain-aa（浅色 QA 实测 2.87:1）。
  @include t.danger-plain-aa;

  .pwh {
    @include t.card;
    @include t.rise;
    @include t.rise-keyframes;

    padding: 16px;

    .dark & {
      @include t.card-dark;
    }
  }

  .pwh__head {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: flex-start;
    justify-content: space-between;
  }

  .pwh__identity {
    min-width: 0;
  }

  .pwh__title-row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  .pwh__title {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
    word-break: break-all;
  }

  // 状态点：颜色即结论（绿=运行中、琥珀=部分运行、灰=已停止/未知），
  // 旁边永远有文字结论（不靠颜色单独传达 —— 无障碍）。
  .pwh__dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    flex: none;
    border-radius: 50%;

    &.is-success {
      background: var(--el-color-success);
    }

    &.is-warn {
      background: var(--el-color-warning);
    }

    &.is-off {
      background: var(--el-text-color-disabled);
    }
  }

  .pwh__state {
    font-size: 13px;
    color: var(--el-text-color-regular);
  }

  .pwh__lock {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    font-size: 13px;
  }

  .pwh__meta {
    margin: 6px 0 0;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .pwh__host {
    margin-top: 10px;
  }

  .pwh__sync {
    color: var(--el-text-color-secondary);
    font-size: 12px;

    // 陈旧：琥珀即结论（与模块内陈旧/保护标注同一套颜色语言）。
    // 文字对比度 AA（收尾批）：三处琥珀结论句（原 el-color-warning，白底
    // 1.85）改走 token，数字见 @styles/core/aa-text.scss。
    &.pwh__sync--warn {
      color: var(--aa-warning-text);
    }
  }

  .pwh__offline {
    color: var(--aa-warning-text);
    font-size: 12px;
  }

  .pwh__actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  // 保护档结论句：与陈旧标注同一套颜色语言（琥珀色 = 需要注意的结论）。
  .pwh__blocked {
    color: var(--aa-warning-text);
    font-size: 12px;
  }

  /* ── 响应式 ─────────────────────────────────────── */

  // 窄屏（<1024）：身份与动作分成上下两段 —— 并排时右侧按钮组会把项目名挤成窄条。
  @include respond-below('desktop') {
    .pwh__identity,
    .pwh__actions {
      width: 100%;
    }
  }

  @include respond-below('tablet') {
    .pwh {
      padding: 12px;
    }
  }
</style>
