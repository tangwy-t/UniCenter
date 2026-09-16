<template>
  <div class="device-detail art-full-height overflow-y-auto">
    <div class="device-detail__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：返回 + 主机名 + 在线/启停态 ============ -->
      <div class="dd-hero">
        <ArtButtonTable
          icon="ri:arrow-left-line"
          iconClass="bg-g-300/55 text-g-700"
          title="返回"
          @click="back"
        />
        <div class="dd-hero__text">
          <h2 class="dd-hero__title">
            {{ device?.hostname || '设备详情' }}
            <ElTag v-if="device" size="small" :type="device.online ? 'success' : 'info'">
              {{ device.online ? '在线' : '离线' }}
            </ElTag>
            <ElTag v-if="device" size="small" :type="device.status === 1 ? 'primary' : 'info'">
              {{ device.status === 1 ? '已启用' : '已停用' }}
            </ElTag>
          </h2>
          <p class="dd-hero__sub">
            设备 ID <code>{{ deviceId }}</code>
            <template v-if="device?.agentVersion"> · Agent {{ device.agentVersion }}</template>
            <template v-if="lastSeenText"> · 最后上报 {{ lastSeenText }}</template>
          </p>
        </div>
        <div class="dd-hero__actions">
          <ElButton size="small" :loading="loading" @click="loadDevice()">刷新</ElButton>
        </div>
      </div>

      <div v-if="errorText" class="dd-error">
        <ArtSvgIcon icon="ri:error-warning-line" />
        <span>{{ errorText }}</span>
        <ElButton size="small" type="primary" plain @click="loadDevice()">重试</ElButton>
      </div>

      <!-- ============ 水位卡片：CPU% / 内存% / 磁盘%（含 watermarkAt） ============ -->
      <div class="dd-cards">
        <div v-for="card in cards" :key="card.key" class="dd-card">
          <div class="dd-card__head">
            <ArtSvgIcon :icon="card.icon" />
            <span>{{ card.label }}</span>
          </div>
          <div class="dd-card__value" :style="{ color: usageTone(card.value) }">
            {{ formatPercent(card.value) }}
          </div>
          <ElProgress
            :percentage="clampPercent(card.value)"
            :stroke-width="6"
            :show-text="false"
            :color="usageTone(card.value)"
          />
          <!-- 水位采样时刻：缺值一律「—」 -->
          <div class="dd-card__foot">
            {{ watermarkAtText ? `采样于 ${watermarkAtText}` : '暂无水位采样时间' }}
          </div>
        </div>
      </div>

      <!-- ============ 设备信息 ============ -->
      <ElCard class="dd-info" shadow="never">
        <template #header>
          <span class="dd-info__title">设备信息</span>
        </template>
        <ElDescriptions :column="3" border size="small">
          <ElDescriptionsItem
            v-for="item in infoItems"
            :key="item.label"
            :label="item.label"
            :span="item.span ?? 1"
          >
            {{ item.value }}
          </ElDescriptionsItem>
        </ElDescriptions>
      </ElCard>

      <!-- ============ 整机趋势（无 kind/name = 整机口径） ============ -->
      <DeviceMetricsPanel class="dd-panel" :device-id="deviceId" />

      <!-- ============ 资源下钻 ============ -->
      <DeviceResourceDrill class="dd-panel" :device-id="deviceId" />
    </div>
  </div>
</template>

<script lang="ts">
  /** 水位卡片的一项（纯函数产出，便于单测）。 */
  export interface WatermarkCard {
    key: string
    label: string
    icon: string
    value: number | null
  }

  /**
   * 由设备详情拼水位卡片。
   *
   * 三个百分比字段都是 `omitempty`（Redis latest 缺值即**整个字段不出现**），
   * 故 `undefined` 一律保留为 `null`（显示「—」），**不得**当成 0 ——
   * 0% 与「没有采集到」在容量判断上是完全相反的结论。
   */
  export function watermarkCards(
    device: Api.Device.DeviceResp | null | undefined
  ): WatermarkCard[] {
    const num = (v: number | null | undefined): number | null =>
      typeof v === 'number' && Number.isFinite(v) ? v : null
    return [
      {
        key: 'cpu',
        label: 'CPU 使用率',
        icon: 'ri:cpu-line',
        value: num(device?.cpuUsedPercent)
      },
      {
        key: 'mem',
        label: '内存使用率',
        icon: 'ri:ram-2-line',
        value: num(device?.memUsedPercent)
      },
      {
        key: 'disk',
        label: '磁盘使用率',
        icon: 'ri:hard-drive-2-line',
        value: num(device?.diskUsedPercent)
      }
    ]
  }

  /** 百分比展示：缺值「—」（不是 0%）。 */
  export function formatPercent(v: number | null | undefined): string {
    if (typeof v !== 'number' || !Number.isFinite(v)) return '—'
    return `${v.toFixed(1)}%`
  }

  /** ElProgress 需要数字：缺值退化成 0（进度条只表达「有多满」，文案另有「—」）。 */
  export function clampPercent(v: number | null | undefined): number {
    if (typeof v !== 'number' || !Number.isFinite(v)) return 0
    return Math.min(Math.max(v, 0), 100)
  }

  /** 水位阈值配色：<50 绿 / <80 蓝 / <90 琥珀 / ≥90 红（与 monitor 页同口径）。 */
  export function usageTone(v: number | null | undefined): string {
    if (typeof v !== 'number' || !Number.isFinite(v)) return 'var(--el-text-color-placeholder)'
    if (v >= 90) return 'var(--el-color-danger)'
    if (v >= 80) return 'var(--el-color-warning)'
    if (v >= 50) return 'var(--el-color-primary)'
    return 'var(--el-color-success)'
  }

  /** unix 秒 → 本地时间；缺值「—」。 */
  export function formatUnixSeconds(v?: number | null): string {
    if (v === undefined || v === null) return '—'
    const d = new Date(v * 1000)
    const p = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(
      d.getMinutes()
    )}:${p(d.getSeconds())}`
  }

  /** 字节级信息在详情里不展示，故 MB → 可读文本（>1024MB 转 GB）。 */
  export function formatMb(mb?: number | null): string {
    if (typeof mb !== 'number' || !Number.isFinite(mb) || mb <= 0) return '—'
    return mb >= 1024 ? `${(mb / 1024).toFixed(2)} GB` : `${mb.toFixed(1)} MB`
  }

  /** 开机时长文案（unix 秒 → 天/时/分）。 */
  export function formatUptime(bootTime?: number | null): string {
    if (typeof bootTime !== 'number' || !Number.isFinite(bootTime) || bootTime <= 0) return '—'
    const seconds = Math.floor(Date.now() / 1000) - bootTime
    if (seconds < 0) return '—'
    const d = Math.floor(seconds / 86400)
    const h = Math.floor((seconds % 86400) / 3600)
    const m = Math.floor((seconds % 3600) / 60)
    if (d > 0) return `${d} 天 ${h} 时`
    if (h > 0) return `${h} 时 ${m} 分`
    return `${m} 分`
  }
</script>

<script setup lang="ts">
  import { computed, ref } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { fetchDevice } from '../api'
  import DeviceMetricsPanel from '../components/metrics-panel.vue'
  import DeviceResourceDrill from '../components/resource-drill.vue'

  defineOptions({ name: 'DeviceDetail' })

  const route = useRoute()
  const router = useRouter()

  /** 路由参数 id（列表页用 `params: { id: row.id }` 跳转，是字符串）。 */
  const deviceId = computed(() => String(route.params.id ?? ''))

  const device = ref<Api.Device.DeviceResp | null>(null)
  const loading = ref(false)
  const errorText = ref('')

  async function loadDevice() {
    if (!deviceId.value) return
    loading.value = true
    errorText.value = ''
    try {
      device.value = await fetchDevice(deviceId.value)
    } catch (e) {
      device.value = null
      errorText.value = e instanceof Error && e.message ? e.message : '加载设备详情失败'
    } finally {
      loading.value = false
    }
  }

  const cards = computed(() => watermarkCards(device.value))

  const watermarkAtText = computed(() => {
    const at = device.value?.watermarkAt
    return at === undefined || at === null ? '' : formatUnixSeconds(at)
  })

  const lastSeenText = computed(() => {
    const at = device.value?.lastSeenAt
    return at === undefined || at === null ? '' : formatUnixSeconds(at)
  })

  /** 设备信息表：全部字段缺值一律「—」（不臆造 0/空串）。 */
  const infoItems = computed(() => {
    const d = device.value
    return [
      { label: '操作系统', value: d?.os || '—' },
      { label: '架构', value: d?.arch || '—' },
      { label: '平台', value: d?.platform || '—' },
      { label: '平台版本', value: d?.platformVer || '—' },
      { label: '内核', value: d?.kernel || '—' },
      { label: 'CPU 型号', value: d?.cpuModel || '—', span: 2 },
      { label: 'CPU 核数', value: d?.cpuCores ? String(d.cpuCores) : '—' },
      { label: '内存总量', value: formatMb(d?.memTotalMb) },
      { label: '开机时间', value: formatUnixSeconds(d?.bootTime) },
      { label: '已运行', value: formatUptime(d?.bootTime) },
      { label: '录入时间', value: formatUnixSeconds(d?.createdAt) }
    ]
  })

  function back() {
    router.push({ name: 'DeviceList' })
  }

  loadDevice()
</script>

<style lang="scss" scoped>
  .device-detail__inner {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .dd-hero {
    display: flex;
    gap: 12px;
    align-items: center;

    &__title {
      display: flex;
      gap: 8px;
      align-items: center;
      font-size: 18px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__sub {
      margin-top: 4px;
      font-size: 12px;
      color: var(--el-text-color-secondary);

      code {
        padding: 0 3px;
        font-family: var(--el-font-family-monospace, monospace);
        background: var(--el-fill-color-light);
        border-radius: 3px;
      }
    }

    &__actions {
      margin-left: auto;
    }
  }

  .dd-error {
    display: flex;
    gap: 8px;
    align-items: center;
    font-size: 13px;
    color: var(--el-color-danger);
  }

  .dd-cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 16px;
  }

  .dd-card {
    padding: 16px;
    background: var(--default-box-color);
    border-radius: 8px;

    &__head {
      display: flex;
      gap: 6px;
      align-items: center;
      font-size: 13px;
      color: var(--el-text-color-secondary);
    }

    &__value {
      margin: 8px 0;
      font-size: 26px;
      font-weight: 600;
    }

    &__foot {
      margin-top: 8px;
      font-size: 12px;
      color: var(--el-text-color-placeholder);
    }
  }

  .dd-info {
    &__title {
      font-size: 15px;
      font-weight: 600;
    }
  }
</style>
