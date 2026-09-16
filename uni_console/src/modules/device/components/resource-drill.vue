<template>
  <ElCard class="rd-card" shadow="never">
    <div class="rd-head">
      <div class="rd-head__title">
        <ArtSvgIcon icon="ri:scan-2-line" />
        <span>资源下钻</span>
      </div>
      <div class="rd-head__meta" v-if="selected">
        <ElTag size="small" effect="plain">{{ kindLabelOf(selected.kind) }}</ElTag>
        <ElTag size="small" effect="plain" type="info">{{ selected.name }}</ElTag>
      </div>
    </div>

    <!-- ============ 资源种类 + 资源名：kind/name 成对传给后端 ============ -->
    <div class="rd-picker">
      <ElSelect v-model="kind" size="small" class="rd-picker__kind" placeholder="资源类型">
        <ElOption v-for="k in RESOURCE_KINDS" :key="k.value" :label="k.label" :value="k.value" />
      </ElSelect>

      <ElSelect
        v-model="name"
        size="small"
        class="rd-picker__name"
        filterable
        clearable
        :loading="loading"
        :placeholder="loading ? '正在加载资源…' : '请选择资源'"
        @visible-change="onDropdownToggle"
      >
        <ElOption
          v-for="item in items"
          :key="`${item.kind}|${item.name}`"
          :label="item.name"
          :value="item.name"
        >
          <!-- stale=true 的资源必须**可见地**标出「已消失」：
               后端专为「已卸载的挂载点不再永久堆在下拉里」而设。 -->
          <span class="rd-option">
            <span class="rd-option__name">{{ item.name }}</span>
            <ElTag v-if="item.stale" size="small" type="warning" effect="light">已消失</ElTag>
            <span class="rd-option__time">{{ formatSeenAt(item.lastSeenAt) }}</span>
          </span>
        </ElOption>
      </ElSelect>

      <span v-if="selected && isStaleResource(selected)" class="rd-stale-hint">
        该资源最近一次出现于 {{ formatSeenAt(selected.lastSeenAt) }}，已超过标记阈值 ——
        下拉中标注「已消失」的资源仍可查看其历史，但不应被当作仍在采集。
      </span>
    </div>

    <div v-if="errorText" class="rd-error">
      <ArtSvgIcon icon="ri:error-warning-line" />
      <span>{{ errorText }}</span>
      <ElButton size="small" type="primary" plain @click="loadResources()">重试</ElButton>
    </div>
    <div v-else-if="!loading && !items.length" class="rd-empty">
      该资源类型下暂无可用资源（后端枚举窗口内的资源才会出现）。
    </div>

    <!-- ============ 复用**同一个** metrics-panel（传 kind + name） ============ -->
    <div class="rd-panel">
      <DeviceMetricsPanel
        v-if="selected"
        :key="`${props.deviceId}|${selected.kind}|${selected.name}`"
        :device-id="props.deviceId"
        :kind="selected.kind"
        :name="selected.name"
      />
      <div v-if="!selected" class="rd-placeholder">
        选择左侧资源后，这里会显示该资源的下钻趋势。
        <p class="rd-placeholder__hint">
          下钻只保留 30 天（后端子表只有 5min 档）：面板里 90 天 / 180 天档位已被禁用， 避免点了再吃
          400。
        </p>
      </div>
    </div>
  </ElCard>
</template>

<script lang="ts">
  // Task 5：本文件的纯逻辑（isStaleResource / formatSeenAt）连同共用常量与
  // kindLabelOf 一起搬到了 `../utils/metrics`。这里**必须**用普通 import（而非
  // `export … from` 中转）：经 SFC 实测，普通块的 import 才会注册为模板可见
  // 绑定，纯再导出不会 → 模板绑定会编译报错。
  import { RESOURCE_KINDS, formatSeenAt, isStaleResource, kindLabelOf } from '../utils/metrics'

  // 既有 import 方（单测）照旧走组件路径，故一并再导出。
  export { RESOURCE_KINDS, formatSeenAt, isStaleResource, kindLabelOf }
</script>

<script setup lang="ts">
  import { computed, onMounted, ref, watch } from 'vue'
  import { fetchDeviceResources } from '../api'
  import DeviceMetricsPanel from './metrics-panel.vue'

  defineOptions({ name: 'DeviceResourceDrill' })

  const props = defineProps<{
    /** 设备 id（后端 `id,string`）。 */
    deviceId: string
  }>()

  const kind = ref<string>(RESOURCE_KINDS[0].value)
  const name = ref<string>('')
  const items = ref<Api.Device.DeviceResourceItem[]>([])
  const loading = ref(false)
  const errorText = ref('')

  /** 当前选中项（含 stale / lastSeenAt），供下钻面板与「已消失」提示使用。 */
  const selected = computed(() => items.value.find((i) => i.name === name.value) ?? null)

  /**
   * 资源枚举。`kind` 变化或手动刷新时重调 —— 下拉的数据源**只有**这个接口，
   * 前端不缓存也不推断资源清单。
   */
  async function loadResources() {
    if (!props.deviceId) return
    loading.value = true
    errorText.value = ''
    try {
      const res = await fetchDeviceResources(props.deviceId, kind.value)
      items.value = res.list ?? []
      // 当前选中项在新清单里不存在（切 kind / 资源已不在枚举窗口内）→ 清空，
      // 避免面板拿到一个后端已经没有名字的资源去查（只会得到空图）。
      if (name.value && !items.value.some((i) => i.name === name.value)) name.value = ''
    } catch (e) {
      items.value = []
      name.value = ''
      errorText.value = e instanceof Error && e.message ? e.message : '加载资源列表失败'
    } finally {
      loading.value = false
    }
  }

  /** 首次展开下拉时若是空清单则拉一次：不在挂载时就打后端，减少无谓请求。 */
  function onDropdownToggle(visible: boolean) {
    if (visible && !items.value.length && !loading.value) loadResources()
  }

  watch(kind, () => {
    name.value = ''
    items.value = []
    loadResources()
  })

  onMounted(() => {
    // 挂载即加载一次：用户常是「先选资源、再看趋势」，空下拉会让人以为没有资源。
    loadResources()
  })
</script>

<style lang="scss" scoped>
  .rd-card {
    --rd-gap: 12px;
  }

  .rd-head {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;

    &__title {
      display: flex;
      gap: 6px;
      align-items: center;
      font-size: 15px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__meta {
      display: flex;
      gap: 6px;
      margin-left: auto;
    }
  }

  .rd-picker {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    margin-top: var(--rd-gap);

    &__kind {
      width: 140px;
    }

    &__name {
      width: 260px;
    }
  }

  .rd-option {
    display: flex;
    gap: 8px;
    align-items: center;

    &__name {
      flex: 1;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    &__time {
      font-size: 11px;
      color: var(--el-text-color-secondary);
    }
  }

  .rd-stale-hint {
    width: 100%;
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-color-warning-dark-2);
  }

  .rd-error {
    display: flex;
    gap: 6px;
    align-items: center;
    margin-top: var(--rd-gap);
    font-size: 12px;
    color: var(--el-color-danger);
  }

  .rd-empty {
    margin-top: var(--rd-gap);
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .rd-panel {
    margin-top: var(--rd-gap);
  }

  .rd-placeholder {
    padding: 24px 0;
    font-size: 13px;
    color: var(--el-text-color-secondary);
    text-align: center;

    &__hint {
      margin-top: 6px;
      font-size: 12px;
      color: var(--el-text-color-placeholder);
    }
  }
</style>
