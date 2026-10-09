<template>
  <div class="device-release art-full-height overflow-y-auto">
    <div class="dr__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头 ============ -->
      <div class="do-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="do-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">Agent 版本</h2>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="reload"
          />
        </div>
      </div>

      <!-- ============ 全站目标版本（内联拉杆：版本下拉 + 开关，确认时带影响面）============ -->
      <div class="do-card dr-global" :class="{ 'dr-global--on': globalEnabled }">
        <div class="dr-global__row">
          <div class="min-w-0">
            <div class="dr-global__label">全站目标版本</div>
            <div class="dr-global__hint">
              <template v-if="summary">
                待对齐 {{ willAlign }} 台 · 已指定版本（不跟随）{{ summary.pinnedDevices ?? 0 }} 台
              </template>
              <template v-else>—</template>
            </div>
          </div>
          <div class="dr-global__actions">
            <template v-if="canUpgradeGlobal">
              <ElSelect
                v-model="globalVersion"
                class="dr-global__select"
                size="small"
                placeholder="选择已发布版本"
                :disabled="!globalEnabled"
                @change="onGlobalVersionChange"
              >
                <ElOption v-for="v in releaseOptions" :key="v" :label="v" :value="v" />
              </ElSelect>
              <ElSwitch
                v-model="globalEnabled"
                active-text="开启"
                inactive-text="关闭"
                @change="onGlobalToggle"
              />
            </template>
            <span v-else class="dr-muted">无全站升级权限</span>
          </div>
        </div>
      </div>

      <!-- ============ 版本分布 ============ -->
      <div class="do-card dr-dist">
        <div class="dr-dist__head">
          <span class="dr-title">版本分布</span>
          <span class="dr-hint">按设备当前上报的版本统计</span>
        </div>
        <div v-if="distribution.length > 0" class="dr-dist__list">
          <div v-for="d in distribution" :key="d.version" class="dr-dist__item">
            <span class="dr-mono dr-dist__ver">{{ d.version || '—' }}</span>
            <span class="dr-dist__bar">
              <i :style="{ width: barWidth(d.count) }" />
            </span>
            <span class="dr-dist__count">{{ d.count }} 台</span>
          </div>
        </div>
        <div v-else class="dr-empty">暂无设备上报版本</div>
      </div>

      <!-- ============ 程序包 ============ -->
      <div class="do-card dr-list">
        <div class="dr-list__head">
          <div>
            <span class="dr-title">程序包</span>
          </div>
          <ElButton v-if="canUpload" size="small" type="primary" @click="uploadVisible = true">
            上传新版本
          </ElButton>
        </div>
        <ElTable v-loading="loading" :data="releases" size="small" max-height="26rem">
          <ElTableColumn label="版本" width="110">
            <template #default="{ row }"
              ><span class="dr-mono">{{ row.version }}</span></template
            >
          </ElTableColumn>
          <ElTableColumn label="平台" width="150">
            <template #default="{ row }">
              <span class="dr-mono">{{ row.os }}/{{ row.arch }}</span>
            </template>
          </ElTableColumn>
          <ElTableColumn label="大小" width="96">
            <template #default="{ row }"
              ><span class="whitespace-nowrap">{{ formatSize(row.sizeBytes) }}</span></template
            >
          </ElTableColumn>
          <ElTableColumn label="状态" width="92">
            <template #default="{ row }">
              <ElTag
                :type="row.status === RELEASE_PUBLISHED ? 'success' : 'info'"
                size="small"
                effect="light"
              >
                {{ releaseStatusText(row.status) }}
              </ElTag>
            </template>
          </ElTableColumn>
          <ElTableColumn label="上传时间" width="140">
            <template #default="{ row }"
              ><span class="whitespace-nowrap">{{ formatUnixTime(row.createdAt) }}</span></template
            >
          </ElTableColumn>
          <ElTableColumn label="备注" min-width="140">
            <template #default="{ row }">{{ row.notes || '—' }}</template>
          </ElTableColumn>
          <ElTableColumn label="操作" width="188" fixed="right">
            <template #default="{ row }">
              <ElButton
                v-if="canPublish && row.status !== RELEASE_PUBLISHED"
                size="small"
                link
                type="primary"
                @click="publish(row)"
              >
                发布
              </ElButton>
              <ElButton
                v-if="canPublish && row.status === RELEASE_PUBLISHED"
                size="small"
                link
                @click="unpublish(row)"
              >
                撤回
              </ElButton>
              <ElTooltip
                :disabled="row.deletable"
                content="该版本已被升级记录使用，需保留以便回滚"
                placement="top"
              >
                <span>
                  <ElButton
                    v-if="canDelete"
                    size="small"
                    link
                    type="danger"
                    :disabled="!row.deletable"
                    @click="remove(row)"
                  >
                    删除
                  </ElButton>
                </span>
              </ElTooltip>
            </template>
          </ElTableColumn>
        </ElTable>
        <div v-if="!loading && releases.length === 0" class="dr-empty">
          还没有程序包 —— 上传一个 agent 二进制，发布后即可下发
        </div>
      </div>
    </div>

    <!-- ============ 上传对话框 ============ -->
    <ElDialog v-model="uploadVisible" title="上传 Agent 程序包" width="520px" @closed="resetUpload">
      <ElForm ref="formRef" :model="form" :rules="formRules" label-width="80px">
        <ElFormItem label="版本号" prop="version" required>
          <ElInput v-model="form.version" placeholder="如 0.2.0" />
        </ElFormItem>
        <ElFormItem label="目标系统" required>
          <ElSelect v-model="form.os" class="w-full">
            <ElOption label="linux" value="linux" />
          </ElSelect>
        </ElFormItem>
        <ElFormItem label="目标架构" required>
          <ElSelect v-model="form.arch" class="w-full">
            <ElOption label="amd64" value="amd64" />
            <ElOption label="arm64" value="arm64" />
          </ElSelect>
        </ElFormItem>
        <ElFormItem label="备注">
          <ElInput v-model="form.notes" placeholder="可选" />
        </ElFormItem>
        <ElFormItem label="程序包" required>
          <ElUpload
            drag
            class="w-full"
            :auto-upload="false"
            :limit="1"
            :on-change="onFileChange"
            :on-exceed="onExceed"
          >
            <div class="dr-upload__text">
              拖入或选择 agent 二进制（uni_agent 可执行文件，无扩展名）
            </div>
          </ElUpload>
        </ElFormItem>
        <ElFormItem v-if="uploading" label="进度">
          <ElProgress :percentage="uploadPercent" />
        </ElFormItem>
      </ElForm>
      <template #footer>
        <ElButton :disabled="uploading" @click="cancelUpload">取消</ElButton>
        <ElButton
          type="primary"
          :loading="uploading"
          :disabled="!form.file || !form.version"
          @click="submitUpload"
        >
          上传
        </ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  import { computed, onMounted, ref } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElMessage, ElMessageBox, ElProgress, ElTag, ElTooltip } from 'element-plus'
  import type { FormInstance, FormRules } from 'element-plus'

  import {
    PermDeviceReleaseDelete,
    PermDeviceReleasePublish,
    PermDeviceReleaseUpload,
    PermDeviceUpgradeGlobal
  } from '@/enums/permission'
  import { useAuth } from '@/hooks/core/useAuth'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import {
    fetchAgentReleases,
    fetchAgentUpgradeSummary,
    publishAgentRelease,
    removeAgentRelease,
    setAgentGlobalTarget,
    unpublishAgentRelease,
    uploadAgentRelease
  } from '../api'
  import {
    RELEASE_PUBLISHED,
    formatSize,
    formatUnixTime,
    releaseStatusText
  } from '../utils/upgrade'

  defineOptions({ name: 'DeviceRelease' })

  // 页头图标与侧边栏/页签同源（取菜单图标，改「菜单管理」即同步；见 usePageIcon）
  const pageIcon = usePageIcon('ri:upload-cloud-2-line')

  const router = useRouter()

  const { hasAuth } = useAuth()
  const canUpload = computed(() => hasAuth(PermDeviceReleaseUpload))
  const canPublish = computed(() => hasAuth(PermDeviceReleasePublish))
  const canDelete = computed(() => hasAuth(PermDeviceReleaseDelete))
  const canUpgradeGlobal = computed(() => hasAuth(PermDeviceUpgradeGlobal))

  type ReleaseItem = Api.Device.AgentReleaseItem

  const releases = ref<ReleaseItem[]>([])
  const published = ref<string[]>([])
  const summary = ref<Api.Device.DeviceUpgradeSummaryResp | null>(null)
  const loading = ref(false)

  const globalTarget = computed(() => summary.value?.globalTargetVersion ?? '')
  const distribution = computed(() => summary.value?.versionDistribution ?? [])
  const willAlign = computed(() => {
    // 「待对齐」= 生效目标存在但版本还不一致的台数（后端分桶里的 running + pending）。
    if (!summary.value) return 0
    const bucket = summary.value.buckets?.find((b) => b.targetVersion === globalTarget.value)
    if (!bucket) return 0
    return (bucket.running ?? 0) + (bucket.pending ?? 0)
  })
  const maxCount = computed(() => Math.max(1, ...distribution.value.map((d) => d.count ?? 0)))

  function barWidth(count?: number): string {
    return `${Math.round(((count ?? 0) / maxCount.value) * 100)}%`
  }

  async function reload() {
    loading.value = true
    try {
      const [rel, sum] = await Promise.all([fetchAgentReleases(), fetchAgentUpgradeSummary()])
      releases.value = rel.list ?? []
      published.value = rel.publishedVersions ?? []
      summary.value = sum
      // 开关态与当前版本随汇总同步：globalEnabled = 是否存在全站目标。
      globalVersion.value = sum.globalTargetVersion ?? ''
      globalEnabled.value = !!sum.globalTargetVersion
    } catch (e) {
      ElMessage.error(errText(e, '加载失败'))
    } finally {
      loading.value = false
    }
  }

  async function publish(row: ReleaseItem) {
    try {
      await publishAgentRelease(row.id)
      ElMessage.success(`已发布 ${row.version}`)
      await reload()
    } catch (e) {
      ElMessage.error(errText(e, '发布失败'))
    }
  }

  async function unpublish(row: ReleaseItem) {
    try {
      await unpublishAgentRelease(row.id)
      // 撤回**只影响新下发**：已指向该版本的设备仍可继续下载（不打断进行中的升级）。
      ElMessage.success(`已撤回 ${row.version}（已下发的设备不受影响）`)
      await reload()
    } catch (e) {
      ElMessage.error(errText(e, '撤回失败'))
    }
  }

  async function remove(row: ReleaseItem) {
    try {
      await ElMessageBox.confirm(
        `确认删除 ${row.version}（${row.os}/${row.arch}）的程序包吗？`,
        '提示',
        { type: 'warning' }
      )
    } catch {
      return
    }
    try {
      await removeAgentRelease(row.id)
      ElMessage.success('已删除')
      await reload()
    } catch (e) {
      ElMessage.error(errText(e, '删除失败'))
    }
  }

  // ── 上传 ──────────────────────────────────────────────────────────────
  const uploadVisible = ref(false)
  const uploading = ref(false)
  const uploadPercent = ref(0)
  const form = ref<{ version: string; os: string; arch: string; notes: string; file: File | null }>(
    {
      version: '',
      os: 'linux',
      arch: 'amd64',
      notes: '',
      file: null
    }
  )
  const formRef = ref<FormInstance>()
  // 版本号格式：x.y.z（与后端 IsSemver 一致）——required 之外再校验格式，
  // 免得一个「v0.2」在上传后才被后端驳回。
  const formRules: FormRules = {
    version: [
      { required: true, message: '请输入版本号', trigger: 'blur' },
      { pattern: /^\d+\.\d+\.\d+$/, message: '格式应为 x.y.z（如 0.2.0）', trigger: 'blur' }
    ]
  }
  let controller: AbortController | null = null

  function onFileChange(file: { raw?: File }) {
    form.value.file = file.raw ?? null
  }

  function onExceed() {
    ElMessage.warning('一次只能上传一个程序包')
  }

  function resetUpload() {
    uploadVisible.value = false
    uploading.value = false
    uploadPercent.value = 0
    form.value = { version: '', os: 'linux', arch: 'amd64', notes: '', file: null }
    formRef.value?.clearValidate()
    controller = null
  }

  function cancelUpload() {
    // 取消上传：中断请求（后端会清掉半截文件），而不是留在进度条上等超时。
    if (controller) controller.abort()
    resetUpload()
  }

  async function submitUpload() {
    const f = form.value.file
    if (!f) return
    // 版本号先过格式校验（内联错误提示由 ElForm 渲染在输入框下方）。
    if (formRef.value) {
      const valid = await formRef.value
        .validate()
        .then(() => true)
        .catch(() => false)
      if (!valid) return
    }
    const fd = new FormData()
    fd.append('file', f)
    fd.append('version', form.value.version.trim())
    fd.append('os', form.value.os)
    fd.append('arch', form.value.arch)
    if (form.value.notes) fd.append('notes', form.value.notes)
    uploading.value = true
    uploadPercent.value = 0
    controller = new AbortController()
    try {
      await uploadAgentRelease(fd, {
        signal: controller.signal,
        onUploadProgress: (p) => (uploadPercent.value = p)
      })
      ElMessage.success('上传成功，记得在列表里点「发布」')
      resetUpload()
      await reload()
    } catch (e) {
      uploading.value = false
      ElMessage.error(errText(e, '上传失败'))
    }
  }

  // ── 全站目标 ──────────────────────────────────────────────────────────
  // 内联拉杆：开关 = 是否存在全站目标；下拉 = 当前生效目标版本。
  const globalVersion = ref('')
  const globalEnabled = ref(false)
  const releaseOptions = computed(() => published.value)
  // 影响面 = 总台数 - 已指定版本的台数（跟随者里再减掉已在该版本的）。
  const globalPreview = computed(() => {
    if (!globalVersion.value || !summary.value) return 0
    const followers = (summary.value.total ?? 0) - (summary.value.pinnedDevices ?? 0)
    const achieved = summary.value.versionDistribution?.find(
      (d) => d.version === globalVersion.value
    )?.count
    return Math.max(0, followers - (achieved ?? 0))
  })

  /** 影响面结论（确认文案共用，只给台数与波及范围）。 */
  function globalImpactText(): string {
    return `预计下发 ${globalPreview.value} 台（只影响跟随全站的设备）`
  }

  /** 下发全站目标；有任务跳升级任务页跟进，无任务留本页刷新汇总。 */
  async function applyGlobalTarget(): Promise<boolean> {
    try {
      const res = await setAgentGlobalTarget(globalVersion.value)
      ElMessage.success(
        res.affected > 0 ? `已设置全站目标，${res.affected} 台将开始升级` : '已设置全站目标'
      )
      if (res.taskId) {
        router.push({ path: '/device/upgrade-task', query: { id: res.taskId } })
      } else {
        await reload()
      }
      return true
    } catch (e) {
      ElMessage.error(errText(e, '设置失败'))
      return false
    }
  }

  async function onGlobalToggle(val: string | number | boolean) {
    if (val) {
      // 开：无生效版本时取第一个已发布版本；一个都没有则回退并警示。
      if (!globalVersion.value) globalVersion.value = releaseOptions.value[0] ?? ''
      if (!globalVersion.value) {
        ElMessage.warning('没有可用的已发布版本，请先上传并发布程序包')
        globalEnabled.value = false
        return
      }
      // 影响面必须最新：确认前重取汇总（挂载时的 summary 可能已陈旧）。
      try {
        summary.value = await fetchAgentUpgradeSummary()
      } catch {
        /* 取不到则沿用旧汇总，不阻断开关 */
      }
      try {
        await ElMessageBox.confirm(`确认将全站目标设为 ${globalVersion.value}？${globalImpactText()}。`, '开启全站升级', {
          type: 'warning'
        })
      } catch {
        globalEnabled.value = false
        return
      }
      if (!(await applyGlobalTarget())) globalEnabled.value = false
      return
    }
    // 关：保留危险拉杆的确认。
    try {
      await ElMessageBox.confirm('关闭后不再自动对齐全站版本，设备将停在当前版本。', '提示', {
        type: 'warning'
      })
    } catch {
      globalEnabled.value = true
      return
    }
    try {
      await setAgentGlobalTarget('')
      ElMessage.success('已关闭全站升级')
      await reload()
    } catch (e) {
      ElMessage.error(errText(e, '操作失败'))
      globalEnabled.value = true
    }
  }

  async function onGlobalVersionChange() {
    // 下拉变更仅在开启态发生；取消则回退到当前生效版本。
    if (!globalEnabled.value) return
    const next = globalVersion.value
    // 影响面必须最新（同 onGlobalToggle）。
    try {
      summary.value = await fetchAgentUpgradeSummary()
    } catch {
      /* 沿用旧汇总 */
    }
    try {
      await ElMessageBox.confirm(`确认将全站目标改为 ${next}？${globalImpactText()}。`, '更改全站目标', {
        type: 'warning'
      })
    } catch {
      globalVersion.value = globalTarget.value
      return
    }
    if (!(await applyGlobalTarget())) globalVersion.value = globalTarget.value
  }

  function errText(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  onMounted(reload)
</script>

<style lang="scss" scoped>
  @use './device-tokens' as t;

  @include t.rise-keyframes;

  .do-hero {
    &__icon {
      @include t.hero-icon;
      width: 40px;
      height: 40px;
      font-size: 20px;
      border-radius: 12px;
    }
  }

  .do-card {
    @include t.card;
    @include t.rise;
    margin-bottom: 16px;
  }

  .dark .do-card {
    @include t.card-dark;
  }

  .dr-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .dr-hint {
    margin-left: 8px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .dr-muted {
    color: var(--el-text-color-secondary);
    font-size: 12px;
  }

  .dr-mono {
    font-variant-numeric: tabular-nums;
    font-family: var(--art-font-family-mono, monospace);
    font-size: 12px;
  }

  // 左侧描边常驻 3px（仅改色，避免开关切换时内容位移）；开启态转主色强调。
  .dr-global {
    border-left-width: 3px;
    transition: border-color 0.2s ease;
  }

  .dr-global--on {
    border-left-color: var(--el-color-primary);

    .dr-global__label {
      color: var(--el-color-primary);
    }

    .dr-global__select :deep(.el-select__selected-item) {
      color: var(--el-color-primary);
      font-weight: 600;
    }
  }

  .dr-global__row {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: center;
    justify-content: space-between;
  }

  .dr-global__label {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .dr-global__hint {
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-regular);
  }

  .dr-global__actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .dr-global__select {
    width: 132px;
  }

  .dr-dist__head,
  .dr-list__head {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 10px;
  }

  .dr-dist__list {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .dr-dist__item {
    display: grid;
    grid-template-columns: 5rem minmax(0, 1fr) 4.5rem;
    gap: 10px;
    align-items: center;
  }

  .dr-dist__bar {
    display: block;
    height: 8px;
    border-radius: 999px;
    background: var(--el-fill-color-light);
    overflow: hidden;

    i {
      display: block;
      height: 100%;
      border-radius: 999px;
      background: var(--el-color-primary);
      transition: width 0.3s ease;
    }
  }

  .dr-dist__count {
    font-size: 12px;
    color: var(--el-text-color-regular);
    text-align: right;
    font-variant-numeric: tabular-nums;
    /* 「N 台」是计数原子值，不折行。 */
    white-space: nowrap;
  }

  .dr-empty {
    padding: 16px 0;
    text-align: center;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .dr-upload__text {
    font-size: 13px;
    color: var(--el-text-color-regular);
  }

  @media (prefers-reduced-motion: reduce) {
    .dr-dist__bar i {
      transition: none;
    }
  }
</style>
