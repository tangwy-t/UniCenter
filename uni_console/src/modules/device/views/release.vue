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
          <p class="mt-0.5 text-xs text-g-600">程序包发布与全站目标版本</p>
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

      <!-- ============ 全站目标版本（危险拉杆：只在这一页、且带影响面）============ -->
      <div class="do-card dr-global">
        <div class="dr-global__row">
          <div class="min-w-0">
            <div class="dr-global__label">全站目标版本</div>
            <div class="dr-global__value">
              <template v-if="globalTarget">{{ globalTarget }}</template>
              <template v-else><span class="dr-muted">未开启</span></template>
            </div>
            <div class="dr-global__hint">
              <template v-if="summary">
                待对齐 {{ willAlign }} 台 · 已指定版本（不跟随）{{ summary.pinnedDevices ?? 0 }} 台
              </template>
              <template v-else>—</template>
            </div>
          </div>
          <div class="dr-global__actions">
            <ElButton v-if="canUpgradeGlobal" size="small" type="primary" @click="openGlobalDialog">
              {{ globalTarget ? '更改全站目标' : '设置全站目标' }}
            </ElButton>
            <ElButton v-if="canUpgradeGlobal && globalTarget" size="small" @click="clearGlobal">
              关闭全站升级
            </ElButton>
            <span v-if="!canUpgradeGlobal" class="dr-muted">无全站升级权限</span>
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
            <span class="dr-hint">上传后需「发布」才能被选为目标版本</span>
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
            <template #default="{ row }">{{ formatSize(row.sizeBytes) }}</template>
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
            <template #default="{ row }">{{ formatUnixTime(row.createdAt) }}</template>
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
      <ElForm label-width="80px">
        <ElFormItem label="版本号" required>
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
            accept="*"
            :on-change="onFileChange"
            :on-exceed="onExceed"
          >
            <div class="dr-upload__text">拖入或用下方按钮选择 agent 二进制</div>
            <template #tip>
              <div class="dr-upload__tip">上传后为草稿态，需在列表里点「发布」</div>
            </template>
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

    <!-- ============ 全站目标对话框（写明影响面）============ -->
    <ElDialog v-model="globalVisible" title="设置全站目标版本" width="440px">
      <div class="dr-gdialog">
        <div class="dr-gdialog__hint">
          只影响**跟随全站**的设备；已单独指定版本的设备不会被动。
        </div>
        <ElSelect v-model="globalVersion" class="w-full" placeholder="选择已发布版本">
          <ElOption v-for="v in releaseOptions" :key="v" :label="v" :value="v" />
        </ElSelect>
        <div v-if="globalPreview" class="dr-gdialog__preview">
          预计下发 <b>{{ globalPreview }}</b> 台
        </div>
      </div>
      <template #footer>
        <ElButton @click="globalVisible = false">取消</ElButton>
        <ElButton type="primary" :disabled="!globalVersion" @click="applyGlobal">确认设置</ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  import { computed, onMounted, ref } from 'vue'
  import { ElMessage, ElMessageBox, ElProgress, ElTag, ElTooltip } from 'element-plus'

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
  const globalVisible = ref(false)
  const globalVersion = ref('')
  const releaseOptions = computed(() => published.value)
  const globalPreview = computed(() => {
    if (!globalVersion.value || !summary.value) return 0
    // 影响面 = 总台数 - 已指定版本的台数（跟随者里再减掉已在该版本的）。
    const followers = (summary.value.total ?? 0) - (summary.value.pinnedDevices ?? 0)
    const achieved = summary.value.versionDistribution?.find(
      (d) => d.version === globalVersion.value
    )?.count
    return Math.max(0, followers - (achieved ?? 0))
  })

  function openGlobalDialog() {
    globalVersion.value = globalTarget.value || releaseOptions.value[0] || ''
    globalVisible.value = true
  }

  async function applyGlobal() {
    try {
      const res = await setAgentGlobalTarget(globalVersion.value)
      ElMessage.success(
        res.affected > 0 ? `已设置全站目标，${res.affected} 台将开始升级` : '已设置全站目标'
      )
      globalVisible.value = false
      await reload()
    } catch (e) {
      ElMessage.error(errText(e, '设置失败'))
    }
  }

  async function clearGlobal() {
    try {
      await ElMessageBox.confirm('关闭后不再自动对齐全站版本，设备将停在当前版本。', '提示', {
        type: 'warning'
      })
    } catch {
      return
    }
    try {
      await setAgentGlobalTarget('')
      ElMessage.success('已关闭全站升级')
      await reload()
    } catch (e) {
      ElMessage.error(errText(e, '操作失败'))
    }
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

  .dr-global__value {
    margin-top: 2px;
    font-size: 22px;
    font-weight: 650;
    line-height: 1.2;
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
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

  .dr-upload__tip {
    margin-top: 4px;
    font-size: 11px;
    color: var(--el-text-color-secondary);
  }

  .dr-gdialog__hint {
    margin-bottom: 10px;
    font-size: 12px;
    color: var(--el-text-color-regular);
  }

  .dr-gdialog__preview {
    margin-top: 10px;
    font-size: 13px;
    color: var(--el-text-color-primary);
  }

  @media (prefers-reduced-motion: reduce) {
    .dr-dist__bar i {
      transition: none;
    }
  }
</style>
