<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根）。整个「主机」分区
       （分区头 + 工具条 + 分组就绪板）由本组件自持 —— 筛选/排序/折叠都是**本组件
       内部的 ref**，10 秒自动刷新只是换掉 hosts prop，状态一个都不会被重置。 -->
  <section id="dov-hosts" class="dov-section">
    <div class="dov-section__head">
      <span class="dov-section__title">主机</span>
      <span class="dov-section__sub">{{ sub }}</span>
    </div>

    <!-- 工具条：搜索 + 两个筛选开关 + 三个排序键。全为硬数据控件（面上的字是控件
         自己的标签，不是解释句）。 -->
    <div class="dov-tools">
      <ElInput
        v-model="query"
        class="dov-tools__search"
        size="small"
        clearable
        placeholder="主机名 / IP"
      >
        <template #prefix>
          <ArtSvgIcon icon="ri:search-line" />
        </template>
      </ElInput>
      <button
        type="button"
        class="dov-chip"
        :class="{ 'is-on': onlyOffline }"
        :aria-pressed="onlyOffline"
        @click="onlyOffline = !onlyOffline"
      >
        只看离线
      </button>
      <button
        type="button"
        class="dov-chip"
        :class="{ 'is-on': onlyAbnormal }"
        :aria-pressed="onlyAbnormal"
        @click="onlyAbnormal = !onlyAbnormal"
      >
        只看有异常
      </button>
      <span class="dov-tools__gap" aria-hidden="true" />
      <span class="dov-tools__label">排序</span>
      <button
        v-for="s in HOST_SORTS"
        :key="s.key"
        type="button"
        class="dov-chip"
        :class="{ 'is-on': sortKey === s.key }"
        :aria-pressed="sortKey === s.key"
        @click="sortKey = s.key"
      >
        {{ s.label }}
      </button>
    </div>

    <!-- 筛到 0 台：如实说没有命中（不是空白块，也不是「还没有主机」）。
         回退入口由工具条本身承载（清空搜索/关掉开关），不另加一句引导文案。 -->
    <div v-if="groups.length === 0" class="dov-board__none">无匹配主机</div>

    <div v-else class="dov-groups">
      <div v-for="g in groups" :key="g.key === '' ? '__ungrouped__' : g.key" class="dov-group">
        <!-- 组头 = 该组在首屏的账：共 N 台 · 离线 M · 异常 K（计数在筛选后算）。
             0 不上色（颜色恒与「需要注意」绑定），有数才走语义色。 -->
        <button
          type="button"
          class="dov-group__head"
          :aria-expanded="!isCollapsed(g)"
          @click="toggleGroup(g)"
        >
          <ArtSvgIcon
            class="dov-group__caret"
            :class="{ 'is-collapsed': isCollapsed(g) }"
            icon="ri:arrow-down-s-line"
          />
          <span class="dov-group__name truncate">{{ g.name }}</span>
          <span class="dov-group__stat">共 {{ g.hosts.length }} 台</span>
          <span class="dov-group__stat" :class="{ 'is-off': g.offline > 0 }">
            离线 {{ g.offline }}
          </span>
          <span class="dov-group__stat" :class="{ 'is-bad': g.abnormal > 0 }">
            异常 {{ g.abnormal }}
          </span>
        </button>

        <div v-if="!isCollapsed(g)" class="dov-hosts grid gap-4">
          <OverviewHostCard
            v-for="h in g.hosts"
            :key="h.id"
            :host="h"
            @open="emit('open', $event)"
          />
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
  /**
   * 主机就绪板（100 台规模）：把「逐主机一张卡铺成一坨」变成「按设备分组 + 一眼看账 +
   * 就地定位」。
   *
   * 三件事各答一问：
   *   ① 分组：主机按 host.group 落组（空组归「未分组」），组头给出该组的三个数
   *      （共 N 台 / 离线 M / 异常 K），可折叠 —— 100 台时首屏先看清全局账，再决定
   *      往哪一组钻。
   *   ② 筛选：只看离线 / 只看有异常 / 按名或 IP 搜索，组头计数随之更新（计数在筛选后
   *      的集合上算，见 buildHostGroups）。
   *   ③ 排序：组内按「异常优先 / 磁盘水位 / 名称」重排。
   *
   * 状态自持（本组件的 ref）—— hosts 是 props，10 秒自动刷新只是换掉它，筛选/排序/
   * 折叠三项用户状态与数据生命周期解耦，刷新不会把它们打回默认。默认展开/折叠走
   * 「规模自适应」（口径与理由见 overview-board.ts 的 GROUP_COLLAPSE_AT）。
   *
   * 纯展示件：分组的账在 overview-board.ts 算好（口径可测），点击只 emit，
   * 导航（带 ?host= 的 router.push）在页面层收口 —— 与磁盘面板的分工同款。
   */
  import { computed, ref, watch } from 'vue'
  import { ElInput } from 'element-plus'
  import OverviewHostCard from './overview-host-card.vue'
  import type { DockerHostItem } from '../api'
  import {
    buildHostGroups,
    groupCollapsedByDefault,
    hostFilterActive,
    HOST_SORTS,
    type HostGroupModel,
    type HostSortKey
  } from './overview-board'

  defineOptions({ name: 'DockerOverviewHostBoard' })

  const props = defineProps<{ hosts: DockerHostItem[] }>()
  const emit = defineEmits<{ (e: 'open', host: DockerHostItem): void }>()

  // ── 用户状态（与 hosts 数据解耦，自动刷新不动它们） ──────────────
  const query = ref('')
  const onlyOffline = ref(false)
  const onlyAbnormal = ref(false)
  const sortKey = ref<HostSortKey>('attention')

  const filter = computed(() => ({
    query: query.value,
    onlyOffline: onlyOffline.value,
    onlyAbnormal: onlyAbnormal.value
  }))

  const groups = computed<HostGroupModel[]>(() =>
    buildHostGroups(props.hosts, filter.value, sortKey.value)
  )

  /**
   * 折叠态：**用户显式开关表** + 规模自适应的默认值。
   *
   * 为什么默认值按「当前可见台数」而不是组的总台数：筛选/搜索正是「就地定位」这个
   * 动作 —— 把一个 50 台的组筛到剩 3 台，那 3 台就该直接摊开（用户已经缩小了范围，
   * 再让他多点一次展开是多余的一步）；清掉筛选后组回到「大组默认折叠」。
   *
   * 为什么**筛选条件一变就清空开关表**：否则会出现最坏的一种组合 —— 先把大组手动
   * 折叠起来，再去搜索某台主机，结果是「搜到了、面上什么都没有」。搜索是定位动作，
   * 它的命中项必须无条件可见；手动折叠是「不筛选时的浏览姿势」，换情境即失效。
   * （10 秒自动刷新**不动**筛选条件，因此也不动这里的任何状态。）
   */
  const overrides = ref<Record<string, boolean>>({})

  watch([query, onlyOffline, onlyAbnormal], () => {
    overrides.value = {}
  })

  function isCollapsed(g: HostGroupModel): boolean {
    const chosen = overrides.value[g.key]
    return chosen === undefined ? groupCollapsedByDefault(g.hosts.length) : chosen
  }

  function toggleGroup(g: HostGroupModel) {
    overrides.value = { ...overrides.value, [g.key]: !isCollapsed(g) }
  }

  /** 分区副标题：不筛时报「共 N 台」；筛了就把「看得见的 / 全量」两个数都摆出来。 */
  const sub = computed(() => {
    const total = props.hosts.length
    const shown = groups.value.reduce((n, g) => n + g.hosts.length, 0)
    return hostFilterActive(filter.value) ? `显示 ${shown} / 共 ${total} 台` : `共 ${total} 台`
  })
</script>

<style lang="scss" scoped>
  /* 令牌数值复制自 monitor-tokens（经本模块 views/overview-tokens，见其文件头注释） */
  @use '../views/overview-tokens' as t;

  @include t.rise-keyframes;

  /* 分区头：与页面内其它分区（.dov-section__head）逐字同款。scoped 的边界让子组件
     贴不上父组件的选择器，故在子组件里再声明一份（与 .dov-dot 在主机卡/磁盘面板分别
     声明的既有先例同一条取舍）。 */
  .dov-section {
    &__head {
      display: flex;
      align-items: baseline;
      gap: 8px;
      flex-wrap: wrap;
      margin-bottom: 12px;
    }

    &__title {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__sub {
      color: var(--el-text-color-secondary);
      font-size: 11px;
    }
  }

  /* ---------- 工具条 ---------- */
  .dov-tools {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-bottom: 12px;
  }

  .dov-tools__search {
    width: 200px;
    max-width: 100%;
  }

  /* 撑开：筛选在左、排序在右（窄屏换行后这一格的宽度塌缩为 0，不占位）。 */
  .dov-tools__gap {
    flex: 1 1 auto;
  }

  .dov-tools__label {
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  /* 开关/排序共用的胶囊按钮：未选中为描边，选中为实心主色（aria-pressed 同步 ——
     视觉态与无障碍态同源）。 */
  .dov-chip {
    flex: none;
    padding: 0 10px;
    height: 24px;
    border-radius: 999px;
    border: 1px solid var(--default-border);
    background: var(--default-box-color);
    color: var(--el-text-color-regular);
    font: inherit;
    font-size: 12px;
    line-height: 1;
    cursor: pointer;
    transition:
      color 0.2s ease,
      border-color 0.2s ease,
      background-color 0.2s ease;
  }

  .dov-chip:hover {
    border-color: var(--el-color-primary-light-5);
    color: var(--aa-primary-text);
  }

  .dov-chip.is-on {
    border-color: var(--el-color-primary);
    background: var(--el-color-primary);
    color: #fff;
  }

  .dov-chip:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }

  /* ---------- 分组 ---------- */
  .dov-groups {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .dov-group__head {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    min-width: 0;
    padding: 6px 4px;
    border: none;
    border-radius: 8px;
    background: none;
    font: inherit;
    text-align: left;
    cursor: pointer;
    transition: background-color 0.2s ease;
  }

  .dov-group__head:hover {
    background: var(--el-fill-color-light);
  }

  .dov-group__head:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 2px;
  }

  .dov-group__caret {
    flex: none;
    font-size: 16px;
    color: var(--el-text-color-secondary);
    transition: transform 0.2s ease;
  }

  .dov-group__caret.is-collapsed {
    transform: rotate(-90deg);
  }

  .dov-group__name {
    min-width: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  /* 组头的三个数：台数恒中性，离线/异常只在 >0 时上语义色（0 不上色）。 */
  .dov-group__stat {
    flex: none;
    font-size: 11px;
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;

    &.is-off {
      color: var(--aa-warning-text);
    }

    &.is-bad {
      color: var(--aa-danger-text);
    }
  }

  /* 命中的台数 = 0（筛选结果为空）：一句硬结论，不是空白块。 */
  .dov-board__none {
    padding: 20px 0;
    font-size: 13px;
    color: var(--el-text-color-secondary);
  }

  /* ---------- 主机卡片栅格（口径与改造前逐字一致） ---------- */
  /* 列数**随主机数收敛**：auto-fit + minmax(360px,1fr) —— 有多少格填多少格、空轨道
     塌缩、剩余轨道等分余量（2 台即铺满整行；主机多则自动回落成多列网格）。
     min(100%,360px) 让窄容器（<360px）不产生横向溢出，退化成单列。 */
  .dov-hosts {
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 360px), 1fr));
  }

  @include t.reduced-motion('.dov-chip', '.dov-group__caret');
</style>
