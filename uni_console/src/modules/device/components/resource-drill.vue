<template>
  <!--
    资源下钻（F-9/F-10 重设计）。

    与旧版的差别（都源于实际使用中的问题）：
     1. **不再自带 ElCard 外壳**：它现在是详情页 Tab 的内容，旧版会形成
        「卡片套卡片」的双层边框，边框本身没有任何信息量。
     2. **左栏常驻资源清单**：旧版是「下拉选 + 右侧大字占位」，用户必须点开
        才知道有哪些资源；改为左栏一览，种类与资源数一眼可见。
     3. **种类带计数**：`磁盘分区 (3)`。没有计数时，用户无法判断「这个设备
        到底有没有传感器」，只能逐个种类点一遍。
     4. **kind 只请求一次**：资源清单接口支持 `kind` 为空（返回全部种类），
        故一次请求即可算出所有分类的计数 —— 不必为每个种类各打一次接口。
  -->
  <div class="rd">
    <!-- ══════════ 左栏：资源清单 ══════════ -->
    <aside class="rd-side">
      <div class="rd-side__head">
        <span class="rd-side__title">资源清单</span>
        <ElTooltip content="刷新资源清单" placement="top">
          <button
            type="button"
            class="rd-side__refresh"
            :disabled="loading"
            @click="loadResources()"
          >
            <ArtSvgIcon
              :icon="loading ? 'ri:loader-4-line' : 'ri:refresh-line'"
              :class="{ 'is-spin': loading }"
            />
          </button>
        </ElTooltip>
      </div>

      <!-- 资源清单加载失败：只影响左栏，右侧面板不受牵连（F-6 错误隔离） -->
      <div v-if="errorText" class="rd-side__error">
        <ArtSvgIcon icon="ri:error-warning-line" />
        <span>{{ errorText }}</span>
        <ElButton size="small" type="primary" plain @click="loadResources()">重试</ElButton>
      </div>

      <ElSkeleton v-else-if="loading && !allItems.length" :rows="6" animated />

      <ElEmpty v-else-if="!allItems.length" description="该设备暂无资源数据" :image-size="60" />

      <template v-else>
        <div v-for="group in kindGroups" :key="group.kind" class="rd-side__group">
          <!-- 种类标题也可点击选「第一个资源」，省一次点击 -->
          <button
            type="button"
            class="rd-side__group-head"
            :class="{ 'is-collapsed': collapsed.includes(group.kind) }"
            @click="toggleCollapse(group.kind)"
          >
            <ArtSvgIcon
              :icon="
                collapsed.includes(group.kind) ? 'ri:arrow-right-s-line' : 'ri:arrow-down-s-line'
              "
            />
            <span>{{ group.label }}</span>
            <span class="rd-side__count">{{ group.items.length }}</span>
          </button>

          <ul v-show="!collapsed.includes(group.kind)" class="rd-side__list">
            <li v-for="item in group.items" :key="item.name">
              <button
                type="button"
                class="rd-side__item"
                :class="{ 'is-active': isSelected(item) }"
                @click="select(item)"
              >
                <span class="rd-side__name" :title="item.name">{{ item.name }}</span>
                <!-- 已消失的资源必须可见地标出：它们仍可查历史，但别当成在采集 -->
                <span v-if="item.stale" class="rd-side__stale" title="该资源近期未再出现"
                  >已消失</span
                >
              </button>
            </li>
          </ul>
        </div>
      </template>
    </aside>

    <!-- ══════════ 右侧：下钻趋势 ══════════ -->
    <section class="rd-main">
      <div v-if="selected" class="rd-main__head">
        <ElTag size="small" effect="plain">{{ kindLabelOf(selected.kind) }}</ElTag>
        <span class="rd-main__name">{{ selected.name }}</span>
        <span class="rd-main__seen"> 最近出现 {{ formatSeenAt(selected.lastSeenAt) }} </span>
        <span v-if="isStaleResource(selected)" class="rd-main__stale-hint">
          已超过标记阈值，历史数据仍可查看，但不应被当作仍在采集。
        </span>
      </div>

      <DeviceMetricsPanel
        v-if="selected"
        :key="`${props.deviceId}|${selected.kind}|${selected.name}`"
        :device-id="props.deviceId"
        :kind="selected.kind"
        :name="selected.name"
      />

      <div v-else class="rd-placeholder">
        <ArtSvgIcon icon="ri:cursor-line" />
        <p class="rd-placeholder__title">从左侧选择一项资源</p>
        <p class="rd-placeholder__hint">
          下钻数据只保留 30 天（后端子表仅有 5min 档），因此 90 天 / 180 天档位在下钻中不可选 ——
          提前禁用比点了再吃 400 更好。
        </p>
      </div>
    </section>
  </div>
</template>

<script lang="ts">
  // 本文件的纯逻辑（isStaleResource / formatSeenAt / kindLabelOf）连同共用常量
  // 都在 `../utils/metrics`。这里**必须**用普通 import（而非 `export … from`
  // 中转）：经 SFC 实测，普通块的 import 才会注册为模板可见绑定，纯再导出不会
  // → 模板绑定会编译报错。
  import { RESOURCE_KINDS, formatSeenAt, isStaleResource, kindLabelOf } from '../utils/metrics'

  // 既有 import 方（单测）照旧走组件路径，故一并再导出。
  export { RESOURCE_KINDS, formatSeenAt, isStaleResource, kindLabelOf }
</script>

<script setup lang="ts">
  import { computed, ref, watch } from 'vue'
  import { fetchDeviceResources } from '../api'
  import DeviceMetricsPanel from './metrics-panel.vue'

  defineOptions({ name: 'DeviceResourceDrill' })

  const props = defineProps<{
    /** 设备 id（后端 `id,string`）。 */
    deviceId: string
  }>()

  /**
   * kind 留空的**全部**资源（一次请求覆盖四个种类）。
   *
   * 为什么一次拿全量而不是按种类分别请求：左栏要显示每个种类的**计数**
   * （`磁盘分区 (3)`）。若按种类请求，进页面就得打 4 个接口，且用户每展开
   * 一个种类又要再打 —— 而资源清单本来就不大（一个设备几十行）。
   * 后端 `kind=""` 时不加 kind 过滤（见 repository.ListByDevice），是官方支持的用法。
   */
  const allItems = ref<Api.Device.DeviceResourceItem[]>([])
  const loading = ref(false)
  const errorText = ref('')

  /** 当前选中项（含 kind/name/stale/lastSeenAt）。 */
  const selectedKey = ref<string>('')

  const collapsed = ref<string[]>([])

  /** key 用 `kind|name`：不同种类的同名资源（如两个 sda）必须区分。 */
  function keyOf(item: { kind: string; name: string }): string {
    return `${item.kind}|${item.name}`
  }

  const selected = computed(
    () => allItems.value.find((i) => keyOf(i) === selectedKey.value) ?? null
  )

  /**
   * 按 RESOURCE_KINDS 的**固定顺序**分组（与后端种类定义一致）。
   *
   * 只返回**非空**种类：显示「传感器 (0)」这种空分组只会让人以为加载失败了。
   * 某个种类在本设备上确实没有资源时，它不该出现在清单里 —— 这与「列表为空」
   * 是两种不同的信息。
   */
  const kindGroups = computed(() =>
    RESOURCE_KINDS.map((k) => ({
      kind: k.value,
      label: k.label,
      items: allItems.value.filter((i) => i.kind === k.value)
    })).filter((g) => g.items.length > 0)
  )

  function isSelected(item: Api.Device.DeviceResourceItem): boolean {
    return keyOf(item) === selectedKey.value
  }

  function select(item: Api.Device.DeviceResourceItem) {
    selectedKey.value = keyOf(item)
  }

  function toggleCollapse(kind: string) {
    const i = collapsed.value.indexOf(kind)
    if (i >= 0) collapsed.value.splice(i, 1)
    else collapsed.value.push(kind)
  }

  /**
   * 拉取资源清单（不传 kind → 全部种类）。
   */
  async function loadResources() {
    if (!props.deviceId) return
    loading.value = true
    errorText.value = ''
    try {
      const res = await fetchDeviceResources(props.deviceId)
      allItems.value = res.list ?? []
      // 当前选中项在新清单里已不存在 → 清空，避免拿一个后端已经不知道的
      // (kind,name) 去查趋势（只会得到空图，且用户不知道为什么）。
      if (selectedKey.value && !allItems.value.some((i) => keyOf(i) === selectedKey.value)) {
        selectedKey.value = ''
      }
      // 默认选中第一项：进页面就能看到一张图，而不是一个占位符。
      // 只在**用户尚未选择**时才自动选，避免刷新把用户的选中的资源顶掉。
      if (!selectedKey.value && allItems.value.length) {
        selectedKey.value = keyOf(allItems.value[0])
      }
    } catch (e) {
      allItems.value = []
      selectedKey.value = ''
      errorText.value = e instanceof Error && e.message ? e.message : '加载资源列表失败'
    } finally {
      loading.value = false
    }
  }

  // deviceId 变化（同一路由复用组件时）必须重取，否则会拿旧设备的资源去查新设备。
  watch(
    () => props.deviceId,
    () => {
      allItems.value = []
      selectedKey.value = ''
      void loadResources()
    }
  )

  void loadResources()
</script>

<style lang="scss" scoped>
  .rd {
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr);
    gap: 16px;

    /* 窄屏：左栏折到上方，避免把图表挤成一条缝 */
    @media (max-width: 900px) {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .rd-side {
    padding-right: 12px;
    border-right: 1px solid var(--art-card-border);

    @media (max-width: 900px) {
      padding-right: 0;
      padding-bottom: 12px;
      border-right: 0;
      border-bottom: 1px solid var(--art-card-border);
    }

    &__head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 8px;
    }

    &__title {
      font-size: 13px;
      font-weight: 600;
      color: var(--el-text-color-secondary);
    }

    &__refresh {
      display: inline-flex;
      padding: 2px;
      color: var(--el-text-color-placeholder);
      cursor: pointer;
      background: none;
      border: 0;

      &:hover:not(:disabled) {
        color: var(--el-color-primary);
      }

      &:disabled {
        cursor: default;
      }
    }

    &__error {
      display: flex;
      flex-direction: column;
      gap: 8px;
      align-items: flex-start;
      font-size: 12px;
      color: var(--el-color-danger);
    }

    &__group + &__group {
      margin-top: 8px;
    }

    &__group-head {
      display: flex;
      gap: 4px;
      align-items: center;
      width: 100%;
      padding: 4px 0;
      font-size: 12px;
      font-weight: 600;
      color: var(--el-text-color-secondary);
      cursor: pointer;
      background: none;
      border: 0;

      &:hover {
        color: var(--el-color-primary);
      }
    }

    &__count {
      margin-left: auto;
      padding: 0 5px;
      font-size: 11px;
      font-weight: 400;
      color: var(--el-text-color-placeholder);
      background: var(--el-fill-color-light);
      border-radius: 8px;
    }

    &__list {
      margin: 0;
      padding: 0;
      list-style: none;
    }

    &__item {
      display: flex;
      gap: 6px;
      align-items: center;
      width: 100%;
      padding: 5px 8px;
      font-size: 13px;
      color: var(--el-text-color-regular);
      text-align: left;
      cursor: pointer;
      background: none;
      border: 0;
      border-radius: 4px;

      &:hover {
        background: var(--el-fill-color-light);
      }

      /* 选中态用主色左边条 + 浅底：比纯换色更能标出「当前项」 */
      &.is-active {
        font-weight: 600;
        color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
        box-shadow: inset 2px 0 0 var(--el-color-primary);
      }
    }

    &__name {
      flex: 1;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__stale {
      flex-shrink: 0;
      padding: 0 4px;
      font-size: 11px;
      color: var(--el-color-warning-dark-2);
      background: var(--el-color-warning-light-9);
      border-radius: 3px;
    }
  }

  .rd-main {
    min-width: 0;

    &__head {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin-bottom: 8px;
    }

    &__name {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__seen {
      font-size: 12px;
      color: var(--el-text-color-placeholder);
    }

    &__stale-hint {
      font-size: 12px;
      color: var(--el-color-warning-dark-2);
    }
  }

  .rd-placeholder {
    display: flex;
    flex-direction: column;
    gap: 6px;
    align-items: center;
    justify-content: center;
    min-height: 260px;
    padding: 24px;
    color: var(--el-text-color-placeholder);

    &__title {
      font-size: 14px;
      color: var(--el-text-color-secondary);
    }

    &__hint {
      max-width: 420px;
      font-size: 12px;
      line-height: 1.6;
      text-align: center;
    }
  }

  .is-spin {
    animation: rd-spin 1s linear infinite;
  }

  @keyframes rd-spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .is-spin {
      animation: none;
    }
  }
</style>
