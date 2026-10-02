<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根）。 -->
  <div class="dov-anoms dov-card">
    <template v-if="items.length">
      <!-- 行点击进详情；名称单元格同时是一个真按钮（行点击对键盘不可达，按钮补上）。
           max-height：50 条封顶的抽查清单不该把页面撑到两屏，表内滚动即可。 -->
      <ElTable
        :data="items"
        size="small"
        max-height="24rem"
        :row-class-name="() => 'dov-anoms__row'"
        @row-click="onRowClick"
      >
        <ElTableColumn label="名称" min-width="200">
          <template #default="{ row }">
            <button
              type="button"
              class="dov-anoms__name dov-mono"
              :title="`查看容器 ${row.name} 的详情`"
              @click.stop="emit('open', row)"
            >
              {{ row.name }}
            </button>
          </template>
        </ElTableColumn>
        <ElTableColumn label="镜像" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="dov-mono">{{ row.image || '—' }}</span>
          </template>
        </ElTableColumn>
        <ElTableColumn label="状态" width="110">
          <template #default="{ row }">
            <span class="dov-anoms__state" :title="row.state">
              <span class="dov-dot" :class="`is-${tone(row.state)}`" aria-hidden="true" />
              {{ text(row.state) }}
            </span>
          </template>
        </ElTableColumn>
        <ElTableColumn prop="statusText" label="状态句" min-width="190" show-overflow-tooltip>
          <template #default="{ row }">{{ row.statusText || '—' }}</template>
        </ElTableColumn>
        <ElTableColumn prop="hostname" label="主机" min-width="140" show-overflow-tooltip />
        <ElTableColumn label="保护" width="96">
          <!-- 锁语义与容器列表同款（锁图标 + 受保护）：动不得的容器要在动手前被看见。
               不用 🔒 emoji —— 无 emoji 字体的环境里会渲染成豆腐块；锁走 ArtSvgIcon
               的图标范式（ri:lock-2-line），文字语义不变。 -->
          <template #default="{ row }">
            <span v-if="row.protected" class="dov-anoms__lock">
              <ArtSvgIcon icon="ri:lock-2-line" />
              受保护
            </span>
            <span v-else>—</span>
          </template>
        </ElTableColumn>
      </ElTable>
      <!-- 截断口径由纯函数给出（Total 是截断前全量数；不靠前端猜上限） -->
      <div v-if="truncation" class="dov-anoms__more">{{ truncation }}</div>
    </template>
    <!-- 零异常是结论不是空缺：给一句可读的确认（绿点），而不是一块空表 -->
    <div v-else class="dov-anoms__ok">
      <span class="dov-dot is-success" aria-hidden="true" />
      没有非运行中的容器 —— 全部容器均在运行
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 异常容器表：控制塔的「要闻」清单 —— 跨主机收非 running 容器，按主机排好序
   * （后端排），上限 50 条（抽查不是全表，Total 会告诉读者「还有更多」）。
   * 列的取舍照后端 DTO 的注释：只收「哪台机、哪个容器、什么态」；CPU/端口这类
   * 完整条目字段对这张清单是噪音。行点击下钻到容器详情（跳法与容器列表逐字同源，
   * 导航动作在页面层收口 —— 组件只 emit，不直接碰 router）。
   */
  import { computed } from 'vue'
  import { ElTable, ElTableColumn } from 'element-plus'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { anomalyStateText, anomalyStateTone, anomalyTruncation } from '../utils/overview'

  defineOptions({ name: 'DockerOverviewAnomalyTable' })

  const props = defineProps<{ anomalies: Api.Docker.DockerOverviewAnomalies }>()
  const emit = defineEmits<{
    (e: 'open', row: Api.Docker.DockerOverviewAnomalyItem): void
  }>()

  const items = computed(() => props.anomalies?.items ?? [])
  const truncation = computed(() =>
    anomalyTruncation(props.anomalies?.total ?? 0, items.value.length)
  )
  const tone = (state: string) => anomalyStateTone(state)
  const text = (state: string) => anomalyStateText(state)

  /** 行点击（ElTable 的 row-click 会多传 column/event，收窄到第一个参数即可）。 */
  function onRowClick(row: Api.Docker.DockerOverviewAnomalyItem) {
    emit('open', row)
  }
</script>

<style lang="scss" scoped>
  /* 令牌数值复制自 monitor-tokens（经本模块 views/overview-tokens，见其文件头注释） */
  @use '../views/overview-tokens' as t;

  @include t.rise-keyframes;

  .dov-anoms {
    @include t.card;
    @include t.rise;
  }

  .dark .dov-anoms {
    @include t.card-dark;
  }

  /* ElTable 的 tr 在子组件内部渲染，scoped 选择器要 :deep 才能落上去 */
  .dov-anoms :deep(.dov-anoms__row) {
    cursor: pointer;
  }

  .dov-anoms__name {
    padding: 0;
    border: none;
    background: none;
    font: inherit;
    font-size: 13px;
    color: var(--el-color-primary);
    cursor: pointer;
    text-align: left;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .dov-anoms__name:hover,
  .dov-anoms__name:focus-visible {
    text-decoration: underline;
  }

  .dov-anoms__name:focus-visible {
    outline: 1px solid var(--el-color-primary);
    outline-offset: 1px;
    border-radius: 2px;
  }

  .dov-anoms__state {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }

  /* 保护列的锁 + 文字（图标与文字一条基线 —— 取代旧 🔒 emoji 的豆腐块风险） */
  .dov-anoms__lock {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 13px;
  }

  .dov-anoms__more {
    margin-top: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--default-border);
    color: var(--el-text-color-secondary);
    font-size: 12px;
    text-align: center;
  }

  .dov-anoms__ok {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 20px 8px;
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  .dov-dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;

    &.is-success {
      background: var(--el-color-success);
    }

    &.is-danger {
      background: var(--el-color-danger);
    }

    &.is-warning {
      background: var(--el-color-warning);
    }

    &.is-info {
      background: var(--el-color-info);
    }
  }

  .dov-mono {
    font-family: var(--el-font-family-monospace, monospace);
  }

  @include t.reduced-motion('.dov-anoms');
</style>
