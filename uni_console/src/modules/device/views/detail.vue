<template>
  <div class="device-detail art-full-height overflow-y-auto">
    <div class="device-detail__inner p-4 pb-8 md:p-5">
      <!-- ══════════ L0 设备身份栏（sticky）══════════ -->
      <div class="dd-hero">
        <div class="dd-hero__identity">
          <div class="dd-hero__title-row">
            <span class="dd-hero__iconbox flex-cc">
              <ArtSvgIcon :icon="devIcon" />
            </span>
            <h2 class="dd-hero__title">{{ device?.hostname || '设备详情' }}</h2>
            <!-- 在线态：离线时补「已离线 N 分钟」，而不只是一个灰标签 -->
            <ElTag
              v-if="device"
              size="small"
              :type="device.online ? 'success' : 'info'"
              :effect="device.online ? 'light' : 'plain'"
            >
              {{ device.online ? '在线' : offlineText }}
            </ElTag>
            <!-- 启停态：停用用 warning（原为 info）—— 它是人为主导的状态，需要被看见 -->
            <ElTag v-if="device" size="small" :type="device.status === 1 ? 'primary' : 'warning'">
              {{ device.status === 1 ? '已启用' : '已停用' }}
            </ElTag>
          </div>

          <p class="dd-hero__sub">
            <!-- 实时指示圆点：与服务监控页头同款（心跳态可见 = 数据是活的） -->
            <span class="live-dot" :class="{ 'is-loading': loading }" aria-hidden="true" />
            <span class="dd-hero__id" :title="deviceId">{{ deviceId }}</span>
            <ElTooltip content="复制设备 ID" placement="top">
              <button type="button" class="dd-hero__copy" aria-label="复制设备 ID" @click="copyId">
                <ArtSvgIcon icon="ri:file-copy-line" />
              </button>
            </ElTooltip>
            <template v-if="device?.agentVersion">
              · Agent {{ device.agentVersion }}
              <!-- 升级对话框：只列「已发布且本设备平台有程序包」的版本 -->
              <ElDialog v-model="upgradeVisible" title="升级 Agent" width="440px">
                <div class="dd-upgrade__dialog">
                  <div class="dd-upgrade__dialog-row">设备：{{ device?.hostname }}</div>
                  <ElSelect v-model="chosenVersion" class="w-full" placeholder="选择一个版本">
                    <ElOption v-for="v in versionOptions" :key="v" :label="v" :value="v" />
                  </ElSelect>
                  <div v-if="versionOptions.length === 0" class="dd-upgrade__dialog-empty">
                    没有可用于该设备的版本 —— 请先在「Agent 版本」页上传并发布程序包
                  </div>
                </div>
                <template #footer>
                  <ElButton @click="upgradeVisible = false">取消</ElButton>
                  <ElButton
                    type="primary"
                    :disabled="!chosenVersion"
                    :loading="submitting"
                    @click="submitUpgrade"
                  >
                    升级到 {{ chosenVersion || '…' }}
                  </ElButton>
                </template>
              </ElDialog>
            </template>
            <template v-if="lastSeenText"> · 最后上报 {{ lastSeenText }}</template>
            <!-- I-1：设备 IP。未观测到时不显示「—」，直接不占位 —— 
                 一个「—」会让人以为设备没有 IP，而真相是服务端还没观测到。 -->
            <template v-if="device?.primaryIp">
              · IP <code class="dd-hero__ip" :title="device.primaryIp">{{ device.primaryIp }}</code>
              <ElTooltip content="复制 IP" placement="top">
                <button type="button" class="dd-hero__copy" aria-label="复制 IP" @click="copyIp">
                  <ArtSvgIcon icon="ri:file-copy-line" />
                </button>
              </ElTooltip>
            </template>
          </p>
        </div>

        <div class="dd-hero__actions">
          <!-- 启停：接口早已存在（device:enable/disable），此前详情页完全没用上 -->
          <ElButton
            v-if="canToggle"
            size="small"
            :type="device?.status === 1 ? 'warning' : 'primary'"
            plain
            :loading="toggling"
            @click="onToggleStatus"
          >
            {{ device?.status === 1 ? '停用' : '启用' }}
          </ElButton>
          <ElButton size="small" :loading="loading" @click="refreshAll">刷新</ElButton>
          <!-- F-16 自动刷新：默认关闭 -->
          <ElDropdown trigger="click" @command="setAutoRefresh">
            <ElButton size="small" text :title="autoRefreshLabel">
              <ArtSvgIcon :icon="autoRefreshSec ? 'ri:timer-flash-line' : 'ri:timer-line'" />
              <span class="dd-hero__autotext">{{ autoRefreshLabel }}</span>
            </ElButton>
            <template #dropdown>
              <ElDropdownMenu>
                <ElDropdownItem :command="0">关闭自动刷新</ElDropdownItem>
                <ElDropdownItem :command="10">每 10 秒</ElDropdownItem>
                <ElDropdownItem :command="30">每 30 秒</ElDropdownItem>
                <ElDropdownItem :command="60">每 1 分钟</ElDropdownItem>
              </ElDropdownMenu>
            </template>
          </ElDropdown>
          <!--
            删除：与「停用」同族（size=small + plain，即 1px 同色描边 + 浅底 + 同色文字），
            只把语义色换成 danger —— 视觉语法一致，红色单独承载「破坏性」含义。

            为什么不再是 ⋮ 下拉里的隐藏项：那个下拉**只有这一个条目**，等价于把按钮藏起来，
            触发点又是无边框的，于是和「停用/刷新」这批有边框按钮并排时明显不是一类控件；
            单条目下拉本身也是多余的一次点击。

            为什么放最右、与「停用」隔开：两者相邻时误点「删除」的代价不可逆
            （删除设备不可恢复），中间隔着「刷新/自动」能拉开安全距离。
          -->
          <ElButton
            v-if="canDelete"
            size="small"
            type="danger"
            plain
            :loading="deleting"
            @click="onRemove"
          >
            删除
          </ElButton>
        </div>
      </div>

      <!-- ══════════ 首屏骨架屏（F-4）══════════ -->
      <template v-if="bootLoading">
        <div class="dd-cards">
          <div v-for="n in 4" :key="n" class="dd-card dd-card--skeleton">
            <ElSkeleton :rows="2" animated />
          </div>
        </div>
        <div class="dd-surface">
          <ElSkeleton :rows="3" animated />
        </div>
        <div class="dd-surface">
          <ElSkeleton :rows="6" animated />
        </div>
      </template>

      <template v-else>
        <!-- ══════════ 详情整体失败（F-5：按类别分流）══════════ -->
        <div v-if="detailError" class="dd-error-box">
          <ArtSvgIcon class="dd-error-box__icon" :icon="detailErrorIcon" />
          <div class="dd-error-box__body">
            <p class="dd-error-box__title">{{ detailError.title }}</p>
            <p v-if="detailError.hint" class="dd-error-box__hint">{{ detailError.hint }}</p>
            <p v-if="detailError.raw" class="dd-error-box__raw" :title="detailError.raw">
              {{ detailError.raw }}
            </p>
          </div>
          <div class="dd-error-box__actions">
            <ElButton
              v-if="detailError.retryable"
              size="small"
              type="primary"
              plain
              @click="loadDevice()"
            >
              重试
            </ElButton>
            <ElButton v-if="detailError.kind === 'notFound'" size="small" @click="back">
              返回列表
            </ElButton>
          </div>
        </div>

        <template v-else-if="device">
          <!-- ══════════ 水位过期告警（F-2）══════════ -->
          <ElAlert
            v-if="wmState === 'stale'"
            class="dd-stale-alert"
            type="warning"
            :closable="false"
            show-icon
            :title="`水位数据可能已过期（采样于 ${watermarkText}）`"
            :description="staleAlertDesc"
          />

          <!-- ══════════ L1 健康概览（F-7/F-12/F-14/F-21）══════════ -->
          <div class="dd-cards">
            <div
              v-for="card in cards"
              :key="card.key"
              class="dd-card"
              :class="{ 'is-stale': wmState === 'stale' && card.kind === 'percent' }"
            >
              <div class="dd-card__head">
                <ArtSvgIcon :icon="card.icon" />
                <span>{{ card.label }}</span>
                <ElTooltip v-if="card.tip" :content="card.tip" placement="top">
                  <ArtSvgIcon class="dd-card__info" icon="ri:question-line" />
                </ElTooltip>
                <!-- F-21：≥90% 的脉冲提示点 -->
                <span
                  v-if="isCriticalUsage(card.value)"
                  class="dd-card__pulse"
                  aria-label="水位偏高"
                />
              </div>

              <div class="dd-card__value" :style="{ color: card.tone }">{{ card.text }}</div>

              <!-- F-14：缺值**不画进度条**（空条会被读成「使用率 0」） -->
              <ElProgress
                v-if="card.kind === 'percent' && card.hasValue"
                :percentage="clampPercent(card.value)"
                :stroke-width="6"
                :show-text="false"
                :color="card.tone"
              />
              <div v-else-if="card.kind === 'percent'" class="dd-card__novalue">
                尚未上报水位数据
              </div>
              <div v-else class="dd-card__spacer" />

              <div class="dd-card__foot">
                <span>{{ card.footLeft }}</span>
                <span v-if="card.footRight" class="dd-card__foot-right">{{ card.footRight }}</span>
              </div>
            </div>
          </div>

          <!-- ══════════ L1.5 Agent 升级（W2）══════════
               这一块回答三个问题：现在是什么版本、期望它到哪、上次动它发生了什么
               （失败与回滚的原因必须就地可见 —— 那是运维最需要的一眼）。 -->
          <div class="dd-surface dd-upgrade">
            <div class="dd-upgrade__head">
              <span class="dd-upgrade__title">Agent 升级</span>
              <span class="dd-upgrade__ver">
                {{ device.agentVersion || '—' }}
                <template v-if="upgradeTarget && upgradeTarget !== device.agentVersion">
                  → {{ upgradeTarget }}
                </template>
              </span>
              <ElTag
                v-if="upgradeBadgeInfo"
                size="small"
                effect="light"
                :type="tagTypeOf(upgradeBadgeInfo.tone)"
              >
                {{ upgradeBadgeInfo.text }}
              </ElTag>
              <span v-if="device.upgradeReason" class="dd-upgrade__reason">
                {{ reasonText(device.upgradeReason) }}
              </span>
              <span v-if="device.upgradeAt" class="dd-upgrade__time">
                {{ formatUnixTime(device.upgradeAt) }}
              </span>
              <div class="dd-upgrade__actions">
                <ElButton
                  v-if="canUpgrade && device.agentUpgradeSupported"
                  size="small"
                  type="primary"
                  @click="openUpgrade"
                >
                  升级到…
                </ElButton>
                <ElButton
                  v-if="canUpgrade && device.agentUpgradeSupported && rollbackVersion"
                  size="small"
                  @click="rollback"
                >
                  回滚到 {{ rollbackVersion }}
                </ElButton>
                <ElButton
                  v-if="canUpgrade && device.agentUpgradeSupported"
                  size="small"
                  plain
                  @click="pinCurrent"
                >
                  固定在当前版本
                </ElButton>
                <ElButton
                  v-if="canUpgrade && device.targetVersion && !device.targetFromGlobal"
                  size="small"
                  link
                  @click="resumeFollow"
                >
                  恢复跟随全站
                </ElButton>
                <span v-if="!device.agentUpgradeSupported" class="dd-upgrade__hint">
                  该 Agent 版本不支持远程升级，需先手工安装新版本
                </span>
              </div>
            </div>
            <div v-if="upgradeRecords.length > 0" class="dd-upgrade__records">
              <div class="dd-upgrade__records-title">升级记录</div>
              <div v-for="r in upgradeRecords" :key="r.id" class="dd-upgrade__record">
                <span class="dd-upgrade__record-time">{{ formatUnixTime(r.createdAt) }}</span>
                <span class="dd-upgrade__record-ver"
                  >{{ r.fromVersion || '—' }} → {{ r.toVersion }}</span
                >
                <span class="dd-upgrade__record-state" :class="{ 'is-bad': isBadState(r.state) }">
                  {{ resultLabelOf(r.state) }}
                </span>
                <span v-if="r.reasonCode" class="dd-upgrade__record-reason">{{
                  reasonText(r.reasonCode)
                }}</span>
              </div>
            </div>
            <div v-else class="dd-upgrade__empty">还没有升级记录</div>
          </div>

          <!-- ══════════ L2 趋势分析（F-9：Tab 合并）══════════ -->
          <div class="dd-surface dd-panel">
            <ElTabs v-model="activeTab" class="dd-tabs" @tab-change="onTabChange">
              <ElTabPane label="整机趋势" name="machine">
                <DeviceMetricsPanel
                  :device-id="deviceId"
                  :offline-threshold-sec="thresholdSec"
                  @latest="onLatestSample"
                />
              </ElTabPane>
              <ElTabPane label="资源下钻" name="drill" lazy>
                <DeviceResourceDrill :device-id="deviceId" />
              </ElTabPane>
            </ElTabs>
          </div>

          <!-- ══════════ L3 设备信息（F-11）══════════ -->
          <div class="dd-surface dd-info">
            <!-- 原为 ElCard 的 #header 具名插槽；卡片改为 div 后插槽无处可挂，
                 故直接展开为普通节点（内容与层级不变）。 -->
            <div class="dd-info__header">
              <span class="dd-info__title">设备信息</span>
              <div class="dd-info__tools">
                <!-- 实测 8/11 项为空：默认全显，开关让关心的人一键收敛 -->
                <ElSwitch v-model="hideEmptyFields" size="small" />
                <span class="dd-info__switch-label">隐藏空字段</span>
              </div>
            </div>

            <div v-for="group in visibleInfoGroups" :key="group.name" class="dd-info__group">
              <p class="dd-info__group-name">{{ group.name }}</p>
              <ElDescriptions :column="infoColumns" border size="small">
                <ElDescriptionsItem
                  v-for="item in group.items"
                  :key="item.label"
                  :label="item.label"
                  :span="item.span ?? 1"
                >
                  <template v-if="item.copyable && item.value !== EMPTY_TEXT">
                    <span class="dd-info__value-with-copy">
                      <code :title="item.value">{{ item.value }}</code>
                      <ElTooltip content="复制" placement="top">
                        <button
                          type="button"
                          class="dd-hero__copy"
                          aria-label="复制"
                          @click="copyText(item.value)"
                        >
                          <ArtSvgIcon icon="ri:file-copy-line" />
                        </button>
                      </ElTooltip>
                    </span>
                  </template>
                  <template v-else>{{ item.value }}</template>
                </ElDescriptionsItem>
              </ElDescriptions>
            </div>

            <p v-if="infoMissingCount" class="dd-info__missing">
              有 {{ infoMissingCount }} 项未上报：该 Agent 未提供这些静态信息， 升级或重启 Agent
              后可补齐。
            </p>
            <ElEmpty
              v-if="hideEmptyFields && !visibleInfoGroups.length"
              description="该设备尚未上报设备信息"
            />
          </div>
        </template>
      </template>
    </div>
  </div>
</template>

<script lang="ts">
  /**
   * 详情页的纯逻辑全部在 `../utils/`（display / device-info / error / column-meta），
   * 可脱 DOM 单测 —— 见 `utils/device-info.ts` 顶部关于「纯逻辑放 utils」的说明。
   *
   * 这里**必须**用普通 import（而非 `export … from` 中转）：
   * 经 SFC 实测，普通块里的 import 会注册为模板可见绑定，
   * 纯再导出不会（bindings 里根本没有它们）→ 模板会编译报错。
   */
  import {
    EMPTY_TEXT,
    clampPercent,
    deviceIcon,
    formatCapacityMb,
    formatGb,
    formatPercent,
    formatRelative,
    formatUnixSeconds,
    formatUptime,
    isCriticalUsage,
    usageTone,
    watermarkState
  } from '../utils/display'
  import {
    countFilledAll,
    countTotal,
    deviceInfoGroups,
    filterFilledGroups
  } from '../utils/device-info'
  import type { InfoGroup, InfoItem } from '../utils/device-info'

  // 既有 import 方（单测经组件路径取用）照旧可用，故一并再导出。
  export {
    EMPTY_TEXT,
    clampPercent,
    countFilledAll,
    countTotal,
    deviceIcon,
    deviceInfoGroups,
    filterFilledGroups,
    formatPercent,
    formatRelative,
    formatUnixSeconds,
    formatUptime,
    isCriticalUsage,
    usageTone,
    watermarkState
  }
  export type { InfoGroup, InfoItem }
</script>

<script setup lang="ts">
  import { computed, onUnmounted, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElMessage, ElMessageBox } from 'element-plus'
  import {
    PermDeviceDelete,
    PermDeviceDisable,
    PermDeviceEnable,
    PermDeviceUpgrade
  } from '@/enums/permission'
  import { useAuth } from '@/hooks/core/useAuth'
  import {
    clearDeviceUpgradeTarget,
    disableDevice,
    dispatchDeviceUpgrade,
    enableDevice,
    fetchAgentReleases,
    fetchDevice,
    fetchDeviceResources,
    fetchDeviceUpgradeRecords,
    removeDevice,
    upgradeDevice
  } from '../api'
  import {
    availableVersions,
    formatUnixTime,
    reasonText,
    resultText,
    tagTypeOf,
    upgradeBadge
  } from '../utils/upgrade'
  import DeviceMetricsPanel from '../components/metrics-panel.vue'
  import DeviceResourceDrill from '../components/resource-drill.vue'
  import { classifyDeviceError } from '../utils/error'

  defineOptions({ name: 'DeviceDetail' })

  const route = useRoute()
  const router = useRouter()
  const { hasAuth } = useAuth()

  const deviceId = computed(() => String(route.params.id ?? ''))

  const device = ref<Api.Device.DeviceResp | null>(null)
  const loading = ref(false)
  /** 首次加载未完成 → 显示骨架屏（后续刷新只用局部 loading，不整页塌陷）。 */
  const booted = ref(false)
  const detailError = ref<ReturnType<typeof classifyDeviceError> | null>(null)

  /** 磁盘挂载点个数（来自 /resources，失败不影响主信息）。 */
  const diskCount = ref<number | null>(null)

  const hideEmptyFields = ref(false)
  /** 自动刷新间隔（秒）；0 = 关闭。 */
  const autoRefreshSec = ref(0)
  let autoTimer: ReturnType<typeof setInterval> | null = null

  const toggling = ref(false)
  const deleting = ref(false)

  const canToggle = computed(() => hasAuth(PermDeviceEnable) && hasAuth(PermDeviceDisable))
  const canDelete = computed(() => hasAuth(PermDeviceDelete))

  /** 骨架屏条件：首次加载中才显示。 */
  const bootLoading = computed(() => loading.value && !booted.value)

  const thresholdSec = computed(() => device.value?.offlineThresholdSec ?? 0)

  const devIcon = computed(() => deviceIcon(device.value?.os, device.value?.platform))

  const lastSeenText = computed(() => formatRelative(device.value?.lastSeenAt))

  /** 离线时长文案（在线时不显示）。 */
  const offlineText = computed(() => {
    const at = device.value?.lastSeenAt
    if (at === undefined || at === null) return '从未上报'
    return `已离线 ${formatRelative(at)}`.replace('前', '')
  })

  const watermarkText = computed(() => formatUnixSeconds(device.value?.watermarkAt))

  const percents = computed(() => [
    device.value?.cpuUsedPercent,
    device.value?.memUsedPercent,
    device.value?.diskUsedPercent
  ])

  const wmState = computed(() =>
    watermarkState(percents.value, device.value?.watermarkAt, thresholdSec.value)
  )

  const staleAlertDesc = computed(() => {
    if (wmState.value !== 'stale') return ''
    const th = thresholdSec.value
    return th
      ? `超过 ${th} 秒未上报即判为离线；该设备最后上报于 ${lastSeenText.value}，可能已离线或 Agent 未在上报。`
      : `该设备最后上报于 ${lastSeenText.value}，可能已离线或 Agent 未在上报。`
  })

  /**
   * 水位卡（4 张）。
   *
   * CPU/内存/磁盘三张用后端水位百分比；第四张「运行时长」用 bootTime。
   * 副标题把「采样于 X」收敛成**相对时间**（原来三张卡各写一遍绝对时刻，
   * 既重复又看不出新旧）。
   */
  const cards = computed(() => {
    const d = device.value
    const cpuN = d?.cpuCores ? `${d.cpuCores} 核` : ''
    const footLeft = d?.watermarkAt ? `采样 ${formatRelative(d.watermarkAt)}` : '无水位采样时间'
    return [
      {
        key: 'cpu',
        kind: 'percent' as const,
        label: 'CPU 使用率',
        icon: 'ri:cpu-line',
        tip: '设备最近一次上报的实时值',
        value: d?.cpuUsedPercent ?? null,
        hasValue: typeof d?.cpuUsedPercent === 'number',
        text: formatPercent(d?.cpuUsedPercent),
        tone: usageTone(d?.cpuUsedPercent),
        footLeft,
        footRight: cpuN
      },
      {
        key: 'mem',
        kind: 'percent' as const,
        label: '内存使用率',
        icon: 'ri:ram-2-line',
        tip: '百分比来自最近一次上报，绝对值来自趋势数据，两者可能不是同一时刻',
        value: d?.memUsedPercent ?? null,
        hasValue: typeof d?.memUsedPercent === 'number',
        text: formatPercent(d?.memUsedPercent),
        tone: usageTone(d?.memUsedPercent),
        footLeft,
        // F-7：绝对值由趋势面板的最新桶回填（0 额外请求）
        footRight: memAbsolute.value
      },
      {
        key: 'disk',
        kind: 'percent' as const,
        label: '磁盘使用率',
        icon: 'ri:hard-drive-2-line',
        tip: '整机聚合口径（所有挂载点合计），逐挂载点请看资源下钻',
        value: d?.diskUsedPercent ?? null,
        hasValue: typeof d?.diskUsedPercent === 'number',
        text: formatPercent(d?.diskUsedPercent),
        tone: usageTone(d?.diskUsedPercent),
        footLeft,
        footRight: diskAbsolute.value
      },
      {
        key: 'uptime',
        kind: 'text' as const,
        label: '运行时长',
        icon: 'ri:time-line',
        tip: '根据设备上报的开机时刻推算',
        value: null,
        hasValue: false,
        text: formatUptime(d?.bootTime),
        tone: 'var(--el-text-color-primary)',
        footLeft: d?.bootTime ? `开机 ${formatUnixSeconds(d.bootTime)}` : '未上报开机时间',
        footRight: ''
      }
    ]
  })

  /** 内存绝对值（由趋势最新桶回填；I-2 的零额外请求方案） */
  const memAbsolute = ref('')
  /** 磁盘绝对值 */
  const diskAbsolute = ref('')

  /** 趋势面板把最新采样点的绝对值回传，用于 L1 卡片副标题。 */
  function onLatestSample(payload: {
    memUsedMb?: number | null
    memTotalMb?: number | null
    diskUsedGb?: number | null
    diskTotalGb?: number | null
  }) {
    if (typeof payload.memUsedMb === 'number' && typeof payload.memTotalMb === 'number') {
      // 走统一单位映射（≥1024MB 转 GB）：此前这里手写 /1024 与 toFixed(1)，
      // 与图例、表格里的换算规则各写一套（实测出现过「7.3 GB」与「7523.4 MB」并存）。
      memAbsolute.value = `${formatCapacityMb(payload.memUsedMb)} / ${formatCapacityMb(payload.memTotalMb)}`
    }
    if (typeof payload.diskUsedGb === 'number' && typeof payload.diskTotalGb === 'number') {
      diskAbsolute.value = `${formatGb(payload.diskUsedGb)} / ${formatGb(payload.diskTotalGb)}`
    }
  }

  const infoGroups = computed(() => deviceInfoGroups(device.value, { diskCount: diskCount.value }))

  /** 缺失项数（用于「有 N 项未上报」提示）。 */
  const infoMissingCount = computed(
    () => countTotal(infoGroups.value) - countFilledAll(infoGroups.value)
  )

  /** 隐藏空字段：整组为空则整组消失。 */
  const visibleInfoGroups = computed(() =>
    hideEmptyFields.value ? filterFilledGroups(infoGroups.value) : infoGroups.value
  )

  const infoColumns = ref(3)
  function updateColumns() {
    if (typeof window === 'undefined') return
    const w = window.innerWidth
    infoColumns.value = w >= 1280 ? 4 : w >= 992 ? 3 : w >= 768 ? 2 : 1
  }

  const activeTab = ref<'machine' | 'drill'>('machine')

  const autoRefreshLabel = computed(() =>
    autoRefreshSec.value ? `${autoRefreshSec.value}s` : '自动'
  )

  const detailErrorIcon = computed(() => {
    switch (detailError.value?.kind) {
      case 'notFound':
        return 'ri:file-unknown-line'
      case 'forbidden':
        return 'ri:lock-2-line'
      case 'network':
        return 'ri:wifi-off-line'
      default:
        return 'ri:error-warning-line'
    }
  })

  async function loadDevice() {
    if (!deviceId.value) return
    loading.value = true
    detailError.value = null
    try {
      device.value = await fetchDevice(deviceId.value)
      // 升级记录与详情**并行**拉：它是独立的一段数据，失败不影响主体
      //（记录为空时卡片显示「还没有升级记录」）。
      void loadUpgradeRecords()
    } catch (e) {
      device.value = null
      detailError.value = classifyDeviceError(e)
    } finally {
      loading.value = false
      booted.value = true
    }
  }

  /**
   * 磁盘挂载点个数（供信息表）。
   *
   * 单独请求且**失败静默**：它是锦上添花的统计，不该因为它失败就让整页
   * 进异常态（F-6 错误隔离）。
   */
  async function loadDiskCount() {
    if (!deviceId.value) return
    try {
      const res = await fetchDeviceResources(deviceId.value, 'disk')
      diskCount.value = (res.list ?? []).length
    } catch {
      diskCount.value = null
    }
  }

  /** F-6：三个请求各自独立，互不牵连。 */
  // ── Agent 升级（W2）───────────────────────────────────────────────────
  const canUpgrade = computed(() => hasAuth(PermDeviceUpgrade))
  const upgradeRecords = ref<Api.Device.DeviceUpgradeRecord[]>([])
  const upgradeVisible = ref(false)
  const submitting = ref(false)
  const chosenVersion = ref('')
  const releases = ref<Api.Device.AgentReleaseItem[]>([])
  const publishedVersions = ref<string[]>([])

  /** 生效目标（后端已按「设备级 ?? 全站」算好，前端不再拼第二份口径）。 */
  const upgradeTarget = computed(() => device.value?.targetVersion || '')
  /** 一键回滚目标（服务端从升级记录推导；空 = 没有成功历史）。 */
  const rollbackVersion = computed(() => device.value?.rollbackVersion || '')
  const upgradeBadgeInfo = computed(() => (device.value ? upgradeBadge(device.value) : null))
  /** 版本下拉：已发布 ∩ 本设备平台有程序包。 */
  const versionOptions = computed(() =>
    availableVersions(publishedVersions.value, releases.value, device.value ? [device.value] : [])
  )

  function isBadState(state?: string): boolean {
    return state === 'failed' || state === 'rolled_back' || state === 'timeout'
  }

  /** 升级记录的一行状态文案（与任务明细同源，但这里只有状态没有进度）。 */
  function resultLabelOf(state?: string): string {
    switch (state) {
      case 'succeeded':
        return '已达成'
      case 'failed':
        return '失败'
      case 'rolled_back':
        return '已回滚'
      case 'timeout':
        return '超时'
      case 'superseded':
        return '已取代'
      case 'pending':
      case 'downloading':
      case 'verifying':
      case 'installing':
      case 'restarting':
        return '进行中'
      default:
        return resultText(undefined) || '—'
    }
  }

  async function loadUpgradeRecords() {
    if (!deviceId.value) return
    try {
      upgradeRecords.value = await fetchDeviceUpgradeRecords(deviceId.value)
    } catch {
      // 记录加载失败不影响详情主体：置空即可（下方显示「还没有升级记录」）。
      upgradeRecords.value = []
    }
  }

  async function openUpgrade() {
    chosenVersion.value = ''
    upgradeVisible.value = true
    if (releases.value.length === 0) {
      try {
        const res = await fetchAgentReleases()
        releases.value = res.list || []
        publishedVersions.value = res.publishedVersions || []
      } catch (e) {
        ElMessage.error(errMsg(e, '版本列表加载失败'))
      }
    }
  }

  async function submitUpgrade() {
    if (!deviceId.value || !chosenVersion.value) return
    submitting.value = true
    try {
      const res = await upgradeDevice(deviceId.value, { version: chosenVersion.value })
      ElMessage.success(
        res.dispatched > 0 ? `已下发升级到 ${chosenVersion.value}` : '该设备无需升级'
      )
      upgradeVisible.value = false
      await refreshAll()
    } catch (e) {
      ElMessage.error(errMsg(e, '下发失败'))
    } finally {
      submitting.value = false
    }
  }

  /** 一键回滚：目标版本由服务端推导，前端只负责确认。 */
  async function rollback() {
    if (!deviceId.value || !rollbackVersion.value) return
    try {
      await ElMessageBox.confirm(
        `确认把「${device.value?.hostname}」回滚到 ${rollbackVersion.value} 吗？`,
        '提示',
        { type: 'warning' }
      )
    } catch {
      return
    }
    try {
      await dispatchDeviceUpgrade({ version: rollbackVersion.value, ids: [deviceId.value] })
      ElMessage.success(`已下回滚到 ${rollbackVersion.value}`)
      await refreshAll()
    } catch (e) {
      ElMessage.error(errMsg(e, '回滚失败'))
    }
  }

  /**
   * 固定在当前版本 = 把设备级目标设成它当前的版本。
   *
   * 这就是「不跟随全站」的全部语义（设计 §2）：版本与目标一致 → 对账视为无需动作。
   * 用 pin 标志显式表达意图 —— 普通升级不能顺手写目标，否则一次全站下发会把每台
   * 设备都钉死。
   */
  async function pinCurrent() {
    if (!deviceId.value || !device.value?.agentVersion) return
    try {
      await upgradeDevice(deviceId.value, {
        version: device.value.agentVersion,
        pin: true
      })
      ElMessage.success(`已固定在 ${device.value.agentVersion}（不再跟随全站）`)
      await refreshAll()
    } catch (e) {
      ElMessage.error(errMsg(e, '操作失败'))
    }
  }

  /** 恢复跟随全站 = 清空设备级目标。 */
  async function resumeFollow() {
    if (!deviceId.value) return
    try {
      await clearDeviceUpgradeTarget(deviceId.value)
      ElMessage.success('已恢复跟随全站目标')
      await refreshAll()
    } catch (e) {
      ElMessage.error(errMsg(e, '操作失败'))
    }
  }

  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  async function refreshAll() {
    await Promise.all([loadDevice(), loadDiskCount()])
  }

  function back() {
    router.push({ name: 'DeviceList' })
  }

  async function copyText(text: string) {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      ElMessage.success('已复制')
    } catch {
      ElMessage.warning('复制失败，请手动选择文本')
    }
  }

  function copyId() {
    void copyText(deviceId.value)
  }

  function copyIp() {
    void copyText(device.value?.primaryIp ?? '')
  }

  /** 启停（接口早已存在）：成功后**只更新本地状态** + 重取详情，不整页重载。 */
  async function onToggleStatus() {
    const d = device.value
    if (!d) return
    const next = d.status !== 1
    const act = next ? '启用' : '停用'
    try {
      await ElMessageBox.confirm(`确认要「${act}」设备「${d.hostname}」吗？`, '启停确认', {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      })
    } catch {
      return
    }
    toggling.value = true
    try {
      if (next) await enableDevice(d.id)
      else await disableDevice(d.id)
      ElMessage.success(`已${act}`)
      await loadDevice()
    } finally {
      toggling.value = false
    }
  }

  async function onRemove() {
    const d = device.value
    if (!d) return
    try {
      await ElMessageBox.confirm(`确认删除设备「${d.hostname}」吗？删除后不可恢复。`, '删除确认', {
        type: 'warning',
        confirmButtonText: '确定',
        cancelButtonText: '取消'
      })
    } catch {
      return
    }
    // 与启停同样的模式：请求期间按钮转圈，防止重复提交。
    // 这里尤其重要 —— 删除不可逆，重复点两次会多发一个注定 404 的请求。
    deleting.value = true
    try {
      await removeDevice(d.id)
      ElMessage.success('已删除')
      back()
    } finally {
      deleting.value = false
    }
  }

  function setAutoRefresh(sec: number | string | object) {
    const n = Number(sec)
    autoRefreshSec.value = Number.isFinite(n) && n > 0 ? n : 0
  }

  function stopAutoRefresh() {
    if (autoTimer !== null) {
      clearInterval(autoTimer)
      autoTimer = null
    }
  }

  function startAutoRefresh() {
    stopAutoRefresh()
    if (!autoRefreshSec.value) return
    autoTimer = setInterval(() => {
      // 页面不可见时不刷新：后台标签页轮询纯属浪费（也避免用户回来时
      // 看到一堆并发请求的结果错位）。
      if (typeof document !== 'undefined' && document.hidden) return
      // 静默刷新：失败不弹 toast（自动刷新弹错会变成骚扰），
      // 失败后 UI 的角标/异常块会自然变色。
      void loadDevice()
    }, autoRefreshSec.value * 1000)
  }

  watch(autoRefreshSec, () => startAutoRefresh())

  /** F-15：URL 状态同步（tab），刷新后可复原。 */
  function onTabChange(name: string | number) {
    const t = name === 'drill' ? 'drill' : 'machine'
    activeTab.value = t
    const query = { ...route.query }
    if (t === 'drill') query.tab = 'drill'
    else delete query.tab
    void router.replace({ query })
  }

  function syncFromQuery() {
    activeTab.value = route.query.tab === 'drill' ? 'drill' : 'machine'
  }

  watch(
    () => route.query.tab,
    () => syncFromQuery()
  )

  updateColumns()
  if (typeof window !== 'undefined') window.addEventListener('resize', updateColumns)
  onUnmounted(() => {
    if (typeof window !== 'undefined') window.removeEventListener('resize', updateColumns)
    stopAutoRefresh()
  })

  syncFromQuery()
  void refreshAll()
</script>

<style lang="scss" scoped>
  /* 设备模块共享设计令牌（数值与服务监控 monitor-tokens 一致） */
  @use './device-tokens' as t;

  @include t.rise-keyframes;
  @include t.pulse-keyframes;

  .device-detail__inner {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  /* 详情页的基础卡片：与服务监控 .sv-card 同款外观 */
  .dd-surface {
    @include t.card;
    @include t.rise;
  }

  .dark .dd-surface {
    @include t.card-dark;
  }

  /* ══════════ L0 页头 ══════════ */
  .dd-hero {
    position: sticky;
    top: 0;
    z-index: 2;
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 8px 0;
    background: var(--default-box-color);
    border-bottom: 1px solid var(--art-card-border);

    &__identity {
      min-width: 0;
      flex: 1;
    }

    &__title-row {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
    }

    /* 页头图标：与服务监控 .sv-hero__icon 同款渐变方块 —— 
       此前是一个 18px 的行内扁平图标，与「服务监控」的 44px 渐变方块
       并排看是两个不同的产品。 */
    &__iconbox {
      @include t.hero-icon;
      width: 40px;
      height: 40px;
      font-size: 20px;
      border-radius: 12px;
    }

    &__title {
      font-size: 18px;
      font-weight: 600;
      color: var(--el-text-color-primary);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      max-width: 40vw;
    }

    &__sub {
      display: flex;
      flex-wrap: wrap;
      gap: 2px;
      align-items: center;
      margin-top: 4px;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__id,
    &__ip {
      font-family: var(--el-font-family-monospace, monospace);
      padding: 0 3px;
      background: var(--el-fill-color-light);
      border-radius: 3px;
    }

    &__copy {
      display: inline-flex;
      align-items: center;
      padding: 0 3px;
      font-size: 12px;
      color: var(--el-text-color-placeholder);
      cursor: pointer;
      background: none;
      border: 0;

      &:hover {
        color: var(--el-color-primary);
      }
    }

    &__actions {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      align-items: center;
      margin-left: auto;
    }

    &__autotext {
      margin-left: 2px;
      font-size: 12px;
    }
  }

  .live-dot {
    @include t.live-dot;
    width: 6px;
    height: 6px;
    flex: none;
    border-radius: 999px;
    background: var(--el-color-success);
  }

  .live-dot.is-loading {
    background: var(--el-color-warning);
    animation-duration: 0.9s;
  }

  /* ══════════ 异常块（F-5）═════════ */
  .dd-error-box {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 16px;
    background: var(--el-color-danger-light-9);
    border: 1px solid var(--el-color-danger-light-7);
    /* 圆角与服务监控卡片语言统一（8px → 14px），保留 danger 语义色 */
    border-radius: 14px;

    &__icon {
      flex-shrink: 0;
      font-size: 20px;
      color: var(--el-color-danger);
    }

    &__body {
      flex: 1;
      min-width: 0;
    }

    &__title {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-color-danger-dark-2);
    }

    &__hint {
      margin-top: 4px;
      font-size: 12px;
      line-height: 1.6;
      color: var(--el-text-color-secondary);
    }

    &__raw {
      margin-top: 4px;
      overflow: hidden;
      font-size: 12px;
      color: var(--el-text-color-placeholder);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__actions {
      display: flex;
      flex-shrink: 0;
      gap: 6px;
    }
  }

  .dd-stale-alert {
    margin-bottom: 4px;
  }

  /* ══════════ L1 健康卡 ══════════ */
  .dd-cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 16px;
  }

  /* 健康卡：外观收敛到共享令牌（原为 --art-card-border + 8px 圆角 + 无阴影，
     与「服务监控」的 --default-border + 14px 圆角 + 阴影是两套语言）。 */
  .dd-card {
    @include t.card;
    display: flex;
    flex-direction: column;
    transition:
      transform 0.2s ease,
      box-shadow 0.2s ease,
      border-color 0.2s ease;

    &:hover {
      transform: translateY(-2px);
      box-shadow: 0 8px 18px rgba(16, 24, 40, 0.09);
      border-color: var(--el-color-primary-light-5);
    }

    &--skeleton {
      min-height: 116px;
    }

    /* 水位过期：整卡降饱和 + 琥珀描边，与「新鲜」在视觉上不可混淆 */
    &.is-stale {
      border-color: var(--el-color-warning-light-5);
      background: var(--el-color-warning-light-9);
    }

    &__head {
      display: flex;
      gap: 6px;
      align-items: center;
      font-size: 13px;
      color: var(--el-text-color-secondary);
    }

    &__info {
      font-size: 12px;
      color: var(--el-text-color-placeholder);
    }

    &__pulse {
      width: 8px;
      height: 8px;
      margin-left: auto;
      background: var(--el-color-danger);
      border-radius: 50%;
      animation: dd-pulse 2s infinite;
    }

    &__value {
      margin: 8px 0;
      font-size: 28px;
      font-weight: 600;
      /* F-23：等宽数字，自动刷新时数字不跳动 */
      font-variant-numeric: tabular-nums;
      line-height: 1.2;
    }

    &__novalue {
      display: flex;
      align-items: center;
      height: 14px;
      font-size: 12px;
      color: var(--el-text-color-placeholder);
    }

    &__spacer {
      height: 14px;
    }

    &__foot {
      display: flex;
      gap: 8px;
      align-items: center;
      justify-content: space-between;
      margin-top: 8px;
      font-size: 12px;
      color: var(--el-text-color-placeholder);
    }

    &__foot-right {
      font-variant-numeric: tabular-nums;
      color: var(--el-text-color-secondary);
    }
  }

  @include t.reduced-motion('.dd-surface', '.dd-card', '.dd-card__pulse');

  @keyframes dd-pulse {
    0%,
    100% {
      opacity: 1;
      transform: scale(1);
    }

    50% {
      opacity: 0.35;
      transform: scale(1.3);
    }
  }

  /* 尊重系统的「减少动态效果」偏好 */
  @media (prefers-reduced-motion: reduce) {
    .dd-card {
      transition: none;

      &:hover {
        transform: none;
      }
    }

    .dd-card__pulse {
      animation: none;
    }
  }

  /* ══════════ L2 Tab ══════════ */
  .dd-panel {
    :deep(.el-tabs__header) {
      margin-bottom: 12px;
    }
  }

  /* ══════════ L3 信息表 ══════════ */
  .dd-info {
    &__header {
      display: flex;
      align-items: center;
      justify-content: space-between;
    }

    &__title {
      font-size: 15px;
      font-weight: 600;
    }

    &__tools {
      display: flex;
      gap: 6px;
      align-items: center;
    }

    &__switch-label {
      font-size: 12px;
      font-weight: 400;
      color: var(--el-text-color-secondary);
    }

    &__group + &__group {
      margin-top: 12px;
    }

    &__group-name {
      margin-bottom: 6px;
      font-size: 12px;
      font-weight: 600;
      color: var(--el-text-color-secondary);
    }

    &__value-with-copy {
      display: inline-flex;
      gap: 4px;
      align-items: center;

      code {
        font-family: var(--el-font-family-monospace, monospace);
        word-break: break-all;
      }
    }

    &__missing {
      margin-top: 12px;
      font-size: 12px;
      color: var(--el-text-color-placeholder);
    }
  }

  /* ══════════ Agent 升级卡片（W2）══════════
     信息密度优先：版本、目标、状态、原因、时间与四个动作在一行内可扫读，
     失败/回滚的原因是运维最需要的一眼（故就地显示，不藏进 hover）。 */
  .dd-upgrade {
    display: flex;
    flex-direction: column;
    gap: 10px;

    &__head {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
    }

    &__title {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__ver {
      font-size: 13px;
      color: var(--el-text-color-regular);
      font-variant-numeric: tabular-nums;
    }

    &__reason {
      font-size: 12px;
      color: var(--el-color-danger);
    }

    &__time {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__actions {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin-left: auto;
    }

    &__hint {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__records {
      display: flex;
      flex-direction: column;
      gap: 4px;
      padding-top: 8px;
      border-top: 1px solid var(--art-card-border);
    }

    &__records-title {
      font-size: 12px;
      font-weight: 600;
      color: var(--el-text-color-secondary);
    }

    &__record {
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
      align-items: baseline;
      font-size: 12px;
      color: var(--el-text-color-regular);
    }

    &__record-time {
      color: var(--el-text-color-secondary);
      font-variant-numeric: tabular-nums;
    }

    &__record-ver {
      font-variant-numeric: tabular-nums;
    }

    &__record-state {
      &.is-bad {
        color: var(--el-color-danger);
      }
    }

    &__record-reason {
      color: var(--el-color-danger);
    }

    &__empty {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__dialog {
      display: flex;
      flex-direction: column;
      gap: 10px;
    }

    &__dialog-row {
      font-size: 13px;
      color: var(--el-text-color-primary);
    }

    &__dialog-empty {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }
</style>
