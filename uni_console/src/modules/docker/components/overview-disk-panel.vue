<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根）。整卡不是按钮：
       每行有自己的两个清理入口按钮，整卡再变成一个大热区会让行内按钮点不准。 -->
  <div class="dov-card dov-disk">
    <div v-for="row in rows" :key="row.host.id" class="dov-disk__row">
      <div class="dov-disk__head">
        <!-- 在线点与主机卡同款：绿=在线；灰=离线（磁盘账是最后已知事实，照常展示） -->
        <span
          class="dov-dot"
          :class="row.host.online ? 'is-success' : 'is-off'"
          aria-hidden="true"
        />
        <span class="dov-disk__host truncate">{{ hostLabel(row.host) }}</span>
        <span class="dov-disk__total dov-mono">{{ row.totalText }}</span>
      </div>

      <!-- 整机磁盘水位（Wave 4d 真分母）：与下面的 docker 占用条是两个独立的账 ——
           水位答「这台机器盘快满了吗」（宿主已用/总量），占用条答「docker 花在哪」。
           **缺省即缺省**：无整机指标时不回落成 0%、也不拿 docker 占用冒充水位，只显示
           「—」（填充严重度 = diskUsageTone 按真实 diskUsedPercent 分档；轨道同 ramp
           更浅一档，同 meter 纪律）。 -->
      <div
        class="dov-disk__water"
        :class="row.water.available ? `is-${row.water.tone}` : 'is-na'"
      >
        <span class="dov-disk__water-label">整机</span>
        <span
          v-if="row.water.available"
          class="dov-disk__water-track"
          role="img"
          :aria-label="`整机磁盘 ${row.water.text}`"
          :title="`整机磁盘 ${row.water.text}`"
        >
          <span class="dov-disk__water-fill" :style="{ width: `${row.water.percent}%` }" />
        </span>
        <span v-else class="dov-disk__water-none">未上报</span>
        <span class="dov-disk__water-value dov-mono">{{ row.water.text }}</span>
      </div>

      <!-- 三条数据条：比例 = 占本行合计（parts-of-whole），**单一色相、不随比例着色**
           —— 构成不承载严重度（严重度归上面的整机水位，Wave 3b 的代理着色已废除）。
           未填充轨道恒在（同 ramp 更浅一档），故 0 MB 也画出一条空轨道而与「没有这条」
           区分开 —— 数值全部在场，色觉缺陷/打印下无信息丢失（EP 变量自适应，不写死色值）。 -->
      <template v-if="row.available">
        <div v-for="b in row.bars" :key="b.key" class="dov-disk__bar">
          <span class="dov-disk__bar-label">{{ b.label }}</span>
          <span
            class="dov-disk__bar-track"
            role="img"
            :aria-label="`${b.label}占用 ${b.text}，占本机 docker 盘 ${Math.round(b.percent)}%`"
            :title="`${b.label}占用 ${b.text}（占本机 docker 盘约 ${Math.round(b.percent)}%）`"
          >
            <span class="dov-disk__bar-fill" :style="{ width: `${b.percent}%` }" />
          </span>
          <span class="dov-disk__bar-value dov-mono">{{ b.text }}</span>
        </div>
      </template>
      <!-- 无 df 数据：给行级结论而不是零值条形 ——「没有数据」不能被读成「没有占用」。
           有删除权限时清理入口照常给出（列表页的清理流不依赖 df，走的是资源清单）。 -->
      <div v-else class="dov-disk__na">磁盘数据不可用（该主机未上报 system df 占用）</div>

      <div class="dov-disk__foot">
        <span v-if="row.available" class="dov-disk__reclaim truncate" :title="row.reclaimText">
          {{ row.reclaimText }}
        </span>
        <!-- 清理入口的权限门 = docker:delete（两个条目通向的列表页清理按钮同档）：
             无权限不渲染 ——「不渲染 ≠ 禁用」的分期控件矩阵纪律，QA 实测此前照常渲染。 -->
        <span v-if="canReclaim" class="dov-disk__actions">
          <button
            type="button"
            class="dov-disk__go"
            :title="`去 ${hostLabel(row.host)} 的镜像列表执行清理（悬空镜像）`"
            @click="emit('openImages', row.host)"
          >
            镜像清理…
          </button>
          <button
            type="button"
            class="dov-disk__go"
            :title="`去 ${hostLabel(row.host)} 的卷列表执行清理（匿名未用卷）`"
            @click="emit('openVolumes', row.host)"
          >
            卷清理…
          </button>
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 磁盘面板（6a 磁盘治理 + Wave 4d 真实水位）：每台主机一行 ——「这台机器盘快满了
   * 吗、docker 吃了多少、花在哪、怎么安全收回」的账目页。
   *
   * 四块各答一问：整机水位答「盘快满了吗」（宿主已用/总量，按真实 diskUsedPercent
   * 分档，缺省显示「—」不回落成 0%）；三条数据条答「docker 花在哪」（镜像/数据卷/
   * 构建缓存，行内相对刻度、单一色相不承载严重度，绝对量由条旁文字承载，刻度口径见
   * utils/overview 的 diskBarPercent）；结论行答「怎么收回」（悬空镜像体积 =
   * image:prune 默认目标、未用卷计数）；两个入口按钮把人交棒给镜像/卷列表页的**既有
   * 清理确认档流** —— 本面板只给入口与结论，不在总览页另做一套清理面板（清理的强
   * 确认/保护/权限链路在列表页已经完整，复制一份只会多一处口径分叉的机会）。
   *
   * 纯展示件：行模型由 utils/overview 的 diskRowModels 算好（口径可测），点击只
   * emit，导航（带 ?host= 的 router.push）在页面层收口 —— 与主机卡片的分工同款。
   */
  import { computed } from 'vue'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerDelete } from '@/enums/permission'
  import type { DockerHostItem } from '../api'
  import { hostLabel } from '../utils/host'
  import { diskRowModels, type DiskRowModel } from '../utils/overview'

  defineOptions({ name: 'DockerOverviewDiskPanel' })

  const props = defineProps<{ hosts: DockerHostItem[] }>()
  const emit = defineEmits<{
    (e: 'openImages', host: DockerHostItem): void
    (e: 'openVolumes', host: DockerHostItem): void
  }>()

  /** 行序按值得注意程度排序（占用大者在前、无 df 者殿后），口径在 diskRowModels。 */
  const rows = computed<DiskRowModel[]>(() => diskRowModels(props.hosts))

  // 清理入口的权限门（与镜像/卷列表页工具条的清理按钮同档 docker:delete）：无权限
  // 不渲染 ——「不渲染 ≠ 禁用」（分期控件矩阵纪律，与同页 hero 任务中心入口同一口径）。
  const { hasAuth } = useAuth()
  const canReclaim = computed(() => hasAuth(PermDockerDelete))
</script>

<style lang="scss" scoped>
  /* 令牌数值复制自 monitor-tokens（经本模块 views/overview-tokens，见其文件头注释） */
  @use '../views/overview-tokens' as t;

  @include t.rise-keyframes;

  .dov-disk {
    @include t.card;
    @include t.rise;

    display: flex;
    flex-direction: column;
    padding: 6px 16px;
  }

  .dark .dov-disk {
    @include t.card-dark;
  }

  .dov-disk__row {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px 4px;

    & + & {
      border-top: 1px solid var(--default-border);
    }
  }

  .dov-disk__head {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .dov-disk__host {
    min-width: 0;
    font-weight: 600;
    font-size: 13px;
    color: var(--el-text-color-primary);
  }

  .dov-disk__total {
    flex: none;
    margin-left: auto;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }

  /* ---------- 整机水位（meter：填充承载严重度、轨道同 ramp 更浅一档） --------------
     与下面的 docker 占用条同一「标签 | 轨道 | 数值」三段式；数值右对齐 tabular-nums。 */

  .dov-disk__water {
    display: grid;
    grid-template-columns: 64px minmax(0, 1fr) auto;
    align-items: center;
    gap: 10px;
    /* 填充与轨道共取一组自定义属性（同 ramp）：默认中性主色档，真实水位 ≥80/≥90 走
       is-warning / is-danger（分档由纯函数 diskUsageTone 决定）。色值全走 EP 变量
       零写死，明暗模式各自成立。 */
    --disk-bar: var(--el-color-primary);
    --disk-bar-track: color-mix(in srgb, var(--el-color-primary) 14%, transparent);
  }

  .dov-disk__water.is-warning {
    --disk-bar: var(--el-color-warning);
    --disk-bar-track: color-mix(in srgb, var(--el-color-warning) 16%, transparent);
  }

  .dov-disk__water.is-danger {
    --disk-bar: var(--el-color-danger);
    --disk-bar-track: color-mix(in srgb, var(--el-color-danger) 16%, transparent);
  }

  .dov-disk__water-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--el-text-color-regular);
    text-align: right;
  }

  .dov-disk__water-track {
    position: relative;
    display: block;
    height: 12px;
    border-radius: 6px 4px 4px 6px;
    /* 轨道 = 填充色同 ramp 更浅一档（meter 纪律）：未填充部分恒可见。 */
    background: var(--disk-bar-track);
  }

  .dov-disk__water-fill {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 6px 4px 4px 6px;
    background: var(--disk-bar);
  }

  /* 缺省（未上报）：不画轨道、不给 0% 的假水位，只一个状态词 + 值列「—」。 */
  .dov-disk__water-none {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .dov-disk__water-value {
    font-size: 12px;
    color: var(--el-text-color-regular);
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  /* ---------- 数据条（构成：单一色相；4px 数据端圆角、基线端方角 —— 数据条纪律） ---------- */

  .dov-disk__bar {
    display: grid;
    grid-template-columns: 64px minmax(0, 1fr) 72px;
    align-items: center;
    gap: 10px;
    /* 单一色相为底（构成不承载严重度 —— Wave 3b 的代理着色已废除，严重度归整机水位）：
       填充与轨道同取一组 EP 变量、轨道为同 ramp 更浅一档（meter 纪律）。色值零写死。 */
    --disk-bar: var(--el-color-primary);
    --disk-bar-track: color-mix(in srgb, var(--el-color-primary) 14%, transparent);
  }

  .dov-disk__bar-label {
    font-size: 12px;
    color: var(--el-text-color-secondary);
    text-align: right;
  }

  .dov-disk__bar-track {
    position: relative;
    display: block;
    height: 10px;
    border-radius: 5px 4px 4px 5px;
    /* 轨道 = 填充色同 ramp 更浅一档（meter 纪律）：未填充部分恒可见 —— 0% 的条
       也画出一条空轨道，与「这条不存在」区分开（此前 0 MB 渲染成什么都没有）。 */
    background: var(--disk-bar-track);
  }

  .dov-disk__bar-fill {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 5px 4px 4px 5px;
    background: var(--disk-bar);
  }

  .dov-disk__bar-value {
    font-size: 12px;
    color: var(--el-text-color-regular);
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  /* ---------- 无数据行 / 结论行 ---------- */

  .dov-disk__na {
    font-size: 12px;
    color: var(--el-text-color-secondary);
    padding: 2px 0;
  }

  .dov-disk__foot {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    min-width: 0;
  }

  .dov-disk__reclaim {
    min-width: 0;
    flex: 1;
    font-size: 12px;
    color: var(--el-text-color-regular);
  }

  .dov-disk__actions {
    flex: none;
    display: flex;
    gap: 8px;
    margin-left: auto;
  }

  /* 清理入口：文字按钮（EP 变量上色，hover 提亮）。语义是「去别处执行」而不是
     「就地执行」—— 不用 danger 实心按钮，避免把总览页误读成操作页。 */
  .dov-disk__go {
    padding: 0 4px;
    border: none;
    background: none;
    font: inherit;
    font-size: 12px;
    // 主色文字对比度 AA（QA №9）：token 与数字见 @styles/core/aa-text.scss。
    color: var(--aa-primary-text);
    cursor: pointer;
    text-decoration: underline dotted;
    text-underline-offset: 3px;
    transition: color 0.2s ease;
  }

  .dov-disk__go:hover {
    // 文字对比度 AA（收尾批）：原 primary-light-3（白底 2.40，方向比 resting 更浅）
    // 改走 hover 档 token，数字见 @styles/core/aa-text.scss。
    color: var(--aa-primary-text-hover);
  }

  .dov-disk__go:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
    border-radius: 3px;
  }

  /* 状态圆点（主机卡片同款，scoped 限制下各自声明） */
  .dov-dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;

    &.is-success {
      background: var(--el-color-success);
    }

    &.is-off {
      background: var(--el-color-info);
    }
  }

  .dov-mono {
    font-family: var(--el-font-family-monospace, monospace);
  }

  @include t.reduced-motion('.dov-disk');
</style>
