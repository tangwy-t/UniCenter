<template>
  <div class="imd art-full-height overflow-y-auto">
    <div class="imd__inner p-4 pb-8 md:p-5">
      <!-- ══════════ 实体头：返回 + 镜像名 + 短 id（与容器详情同一骨架）══════════ -->
      <div class="imd-hero">
        <div class="imd-hero__identity">
          <div class="imd-hero__title-row">
            <ArtButtonTable
              icon="ri:arrow-left-line"
              icon-class="bg-g-300/55 text-g-700"
              title="返回镜像列表"
              @click="back"
            />
            <h2 class="imd-hero__title">{{ title }}</h2>
          </div>
          <p v-if="shortId" class="imd-hero__sub">
            <span class="imd-hero__id" :title="imageId">{{ shortId }}</span>
          </p>
        </div>
        <div class="imd-hero__actions">
          <!-- 二期写操作（spec §11.4）：打标签/导出 tar 无确认档，删除是标准档（经弹窗）。
               使用中的镜像不能删除：按钮禁用并把结论句放在旁边（服务端也会拒绝，不该发出去）。
               指令在途时一并禁用。 -->
          <!-- 用此镜像创建（4a 创建面 → 8b 整页路由）：消灭「拉了镜像跑不起来」的镜像侧
               入口 —— 本镜像与本页主机预填进创建页的 query（创建页里主机仍可换），
               权限与启停同级。 -->
          <ElButton
            v-if="canManage"
            size="small"
            type="primary"
            :disabled="busy"
            @click="openCreate"
          >
            用此镜像创建…
          </ElButton>
          <ElButton v-if="canManage" size="small" :disabled="busy" @click="onTag">
            打标签…
          </ElButton>
          <ElButton v-if="canManage" size="small" :disabled="busy" @click="onSave">
            导出 tar…
          </ElButton>
          <!-- 推送到仓库（P2 分发闭环）：预填本镜像引用，对话框内可选同主机其它
               镜像/凭据，逐层进度与取消。与创建/导出同档（docker:manage）。 -->
          <ElButton v-if="canManage" size="small" :disabled="busy" @click="openPush">
            推送到仓库…
          </ElButton>
          <ElButton
            v-if="canDelete"
            size="small"
            type="danger"
            plain
            :disabled="removeDisabled"
            @click="openRemove"
          >
            删除…
          </ElButton>
          <span v-if="removeBlockedConclusion" class="imd-hero__blocked">
            {{ removeBlockedConclusion }}
          </span>
        </div>
      </div>

      <!-- 摘要行：大小 / 创建于 / 使用 —— 第一屏只回答「这是什么」。 -->
      <div class="imd-facts">
        <div class="imd-fact">
          <span class="imd-fact__label">大小</span>
          <span class="imd-fact__value">{{ sizeText }}</span>
        </div>
        <div class="imd-fact">
          <span class="imd-fact__label">创建于</span>
          <span class="imd-fact__value">{{ createdText }}</span>
        </div>
        <div class="imd-fact">
          <span class="imd-fact__label">使用</span>
          <span class="imd-fact__value" :class="{ 'is-warn': dangling }">{{ useText }}</span>
        </div>
      </div>

      <ElCard class="art-table-card" shadow="never">
        <ElTabs v-model="activeTab" class="imd-tabs">
          <!-- 1. 分层历史（默认屏）。
               顺序**自下而上**（基础层在前）：这是 agent 给的顺序（docker history 是自上而下，
               agent 已翻转），页面不重排 —— 两份顺序只会在某一天悄悄分叉。 -->
          <ElTabPane label="分层历史" name="layers">
            <ElSkeleton v-if="loading && !view" :rows="6" animated />
            <ElEmpty v-else-if="errorText" :description="errorText">
              <ElButton size="small" @click="loadInspect">重试</ElButton>
            </ElEmpty>
            <template v-else-if="view">
              <ArtTable v-if="layerRows.length" :data="layerRows" :columns="layerColumns" />
              <ElEmpty v-else description="这个镜像没有分层信息" />
              <!-- 合计只列、不与镜像大小混为一谈：两者**不是同一口径** —— 各层大小是解压后的
                   差值，共享层会被多个镜像共用，镜像的存储体积还经过压缩。
                   实测（本机 mysql:8.0）：分层合计 812MB，镜像 223MB，合计**偏大**。
                   这句话必须写出来，否则「层加起来 ≠ 镜像大小」会被当成数据错。 -->
              <div v-if="layerRows.length" class="imd-total">
                <span>分层合计 {{ layersTotalText }}（镜像 {{ sizeText }}）</span>
                <span class="imd-total__sub"
                  >各层大小是解压后的差值，合计与镜像大小不是同一口径</span
                >
              </div>
            </template>
          </ElTabPane>

          <!-- 2. 元数据：镜像自身的静态事实（架构/系统/端口/入口点/命令/标签）。 -->
          <ElTabPane label="元数据" name="meta">
            <ElSkeleton v-if="loading && !view" :rows="6" animated />
            <ElEmpty v-else-if="errorText" :description="errorText">
              <ElButton size="small" @click="loadInspect">重试</ElButton>
            </ElEmpty>
            <template v-else-if="view">
              <ElDescriptions :column="descriptionColumns" border size="small">
                <ElDescriptionsItem label="架构">{{ view.architecture || '—' }}</ElDescriptionsItem>
                <ElDescriptionsItem label="系统">{{ view.os || '—' }}</ElDescriptionsItem>
                <ElDescriptionsItem label="暴露端口" :span="2">{{
                  exposedPortsText
                }}</ElDescriptionsItem>
              </ElDescriptions>

              <div class="imd-block">
                <div class="imd-block__title">入口点与命令</div>
                <div class="imd-code">
                  <span class="imd-code__label">入口点</span>
                  <code class="imd-code__value">{{ joinArgs(view.entrypoint) }}</code>
                </div>
                <div class="imd-code">
                  <span class="imd-code__label">命令</span>
                  <code class="imd-code__value">{{ joinArgs(view.cmd) }}</code>
                </div>
              </div>

              <div class="imd-block">
                <div class="imd-block__title">标签（{{ labelRows.length }} 项）</div>
                <ArtTable v-if="labelRows.length" :data="labelRows" :columns="labelColumns" />
                <ElEmpty v-else description="这个镜像没有标签" />
              </div>
            </template>
          </ElTabPane>

          <!-- 3. 关联容器：从当前主机快照按镜像引用反查（不另开端点 —— 快照里已有这些事实）。 -->
          <ElTabPane label="关联容器" name="containers">
            <ElSkeleton v-if="loading && !view" :rows="4" animated />
            <ElEmpty v-else-if="errorText" :description="errorText">
              <ElButton size="small" @click="loadInspect">重试</ElButton>
            </ElEmpty>
            <template v-else-if="view">
              <ElSkeleton v-if="snapshotLoading && !state" :rows="3" animated />
              <!-- 「没读到快照」与「没有容器在用」是两件事，说成一句会让人以为镜像没被引用。 -->
              <ElEmpty v-else-if="snapshotError" description="读取主机快照失败，请稍后重试">
                <ElButton size="small" @click="loadSnapshot">重试</ElButton>
              </ElEmpty>
              <template v-else>
                <ArtTable v-if="related.length" :data="related" :columns="containerColumns" />
                <ElEmpty v-else :description="relatedEmptyText" />
              </template>
            </template>
          </ElTabPane>

          <!-- 4. 安全（P3·安全面）：漏洞扫描报告（触发 + 呈现都在面板内）。
               lazy：没点开不挂载；挂载后切走再切回不丢已读到的报告 —— ElTabPane 的
               默认（非 lazy）会把内容常驻，这里按需挂载与其余三个 Tab 的「共用一次
               inspect 读取」不同源（面板走自己的指令通道），挂载时机由用户的第一
               次兴趣决定。 -->
          <ElTabPane label="安全" name="scan" lazy>
            <ImageScanPanel :host-id="ctx.hostId" :target="actionTarget" />
          </ElTabPane>
        </ElTabs>
      </ElCard>
    </div>

    <!-- 删除（标准档）与导出覆盖（文件名档）的确认弹窗：形态、逐字期望值与保护提示
         全部由组件按动作注册表推导，页面只传事实与 options。删除成功后回列表
         （列表挂载时自己拉最新快照）。 -->
    <DockerActionConfirm
      v-model="confirmState.visible"
      :action="confirmState.action"
      :target="confirmState.target"
      :options="confirmState.options"
      target-kind="镜像"
      :loading="confirmLoading"
      @confirm="onConfirmSubmit"
    />

    <!-- 推送进度对话框（P2 分发闭环）：预填 actionTarget、镜像清单来自本页快照
        （选择项的数据源已在手，对话框不另拉一份）。**不传 refresh**：推送不改变
         本地任何事实（镜像/使用/关联容器都不动 —— 推的是副本），重拉无物可读；
         与拉取/构建（成功后世界变了）的差别就在这一点。 -->
    <PushProgressDialog
      v-model="pushVisible"
      :host-id="ctx.hostId"
      :images="state?.images ?? []"
      :initial-target="actionTarget"
    />
  </div>
</template>

<script setup lang="ts">
  import { computed, h, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import {
    ElButton,
    ElCard,
    ElDescriptions,
    ElDescriptionsItem,
    ElEmpty,
    ElMessage,
    ElMessageBox,
    ElSkeleton,
    ElTabPane,
    ElTabs
  } from 'element-plus'
  import { useAppBreakpoints } from '@/hooks/core/useAppBreakpoints'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerDelete, PermDockerManage } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import { formatByUnit, formatUnixSeconds } from '@/modules/device/utils/display'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import ImageScanPanel from '../components/image-scan-panel.vue'
  import PushProgressDialog from '../components/push-progress-dialog.vue'
  import {
    fetchDockerCmdResult,
    fetchDockerState,
    sendDockerCmd,
    type DockerCmdResultResp,
    type DockerContainerItem,
    type DockerImageItem,
    type DockerStateResp
  } from '../api'
  import {
    runErrorMessage,
    useDockerCmds,
    type DockerCmdRunResult
  } from '../composables/useDockerCmds'
  import {
    parseImageInspectPayload,
    pollDelay,
    type ImageInspectView,
    type Phase1Action
  } from '../utils/cmd'
  import {
    containerStateText,
    formatRelativeTime,
    imageRefText,
    inUseText,
    layersTotalMB
  } from '../utils/display'
  import { provideDockerHost } from '../utils/host-context'

  defineOptions({ name: 'DockerImageDetail' })

  type TabName = 'layers' | 'meta' | 'containers' | 'scan'

  interface LayerRow {
    key: number
    sizeText: string
    timeText: string
    createdBy: string
  }

  const route = useRoute()
  const router = useRouter()
  // 主机上下文是**页面级** provide/inject（与列表页、容器详情同一约定）：直接打开详情链接时
  // 主机要从 query 还原，返回列表与跳容器详情时又要把它带回去。不要解构：上下文字段是
  // getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()

  // 元数据信息表（ElDescriptions）的列数：平板竖屏以下（<768）改单列 —— 两列在手机横屏里
  // 会把「标签 + 值」挤成一条缝，长架构/端口串会被截断。
  const { smaller } = useAppBreakpoints()
  const descriptionColumns = computed(() => (smaller('tablet').value ? 1 : 2))

  // 分层历史是默认屏；Tab 是本页的本地状态（列表页没有带 tab 的入口，故不必写进 URL）。
  const activeTab = ref<TabName>('layers')

  /**
   * 路由参数里的镜像 id。
   *
   * 列表页传的是 `encodeURIComponent(row.id)`（镜像 id 含 `sha256:` 冒号，作为路由参数必须
   * 编码），故这里解码回同一份口径 —— 指令的 target、快照匹配、展示都靠它。
   * 手敲的链接可能带不完整转义（单个 `%`），decode 会抛；那时退回原文而不是白屏。
   */
  const imageId = computed(() => {
    const text = String(route.params.id ?? '')
    try {
      return decodeURIComponent(text)
    } catch {
      return text
    }
  })

  /** 确认句里只回显短 id（与 imageRefText 的「无标签」写法同一口径：去算法前缀、取前 12 位）。 */
  const shortId = computed(() => imageId.value.replace('sha256:', '').slice(0, 12))

  // ── 镜像详情（三个 Tab 共用同一次读取）──
  const view = ref<ImageInspectView | null>(null)
  const loading = ref(false)
  const errorText = ref('')

  // ── 主机快照（头部「使用」与关联容器；inspect 里没有这两个事实）──
  const state = ref<DockerStateResp | null>(null)
  const snapshotLoading = ref(false)
  const snapshotError = ref(false)

  // 请求序号（照容器详情的既有模式）：主机切换会并发两份请求，旧的那份可能**晚于**新的返回 ——
  // 直接落盘会把页面换回上一台机器的事实。
  let inspectSeq = 0
  let snapshotSeq = 0

  /** 快照里同一镜像的那一行：按 id 命中（列表页传的就是它），退一步按仓库标签命中。 */
  const snapshotImage = computed<DockerImageItem | null>(() => {
    const images = state.value?.images ?? []
    const byId = images.find((i) => i.id === imageId.value)
    if (byId) return byId
    const tag = view.value?.repoTags?.[0]
    if (!tag) return null
    return images.find((i) => (i.repoTags ?? []).includes(tag)) ?? null
  })

  /** 悬空（无标签且无容器引用）是这一屏唯一需要被「看见」的事实：琥珀色即结论。 */
  const dangling = computed(() => snapshotImage.value?.dangling === true)

  const title = computed(() => {
    const v = view.value
    // 读取失败时也不空着：至少把路由里带过来的短 id 显示出来。
    if (!v) return shortId.value || '镜像详情'
    return imageRefText({ id: v.id || imageId.value, repoTags: v.repoTags })
  })

  /** 大小：快照与列表逐字同源，故优先它；没有快照时用 inspect 的字节数换算。 */
  const imageSizeMB = computed<number | undefined>(() => {
    if (snapshotImage.value) return snapshotImage.value.sizeMb
    return view.value ? view.value.sizeBytes / (1024 * 1024) : undefined
  })

  const sizeText = computed(() => formatByUnit('MB', imageSizeMB.value))
  const createdText = computed(() =>
    formatUnixSeconds(view.value?.created ?? snapshotImage.value?.created)
  )

  /** 使用状态：快照未到时是「—」—— 那时我们并不知道有没有容器在用它，猜一个是不诚实的。 */
  const useText = computed(() => (snapshotImage.value ? inUseText(snapshotImage.value) : '—'))

  /** 分层合计（MB）：空元数据层不计入（它们不占空间，见 layersTotalMB）。 */
  const layersTotalText = computed(() => formatByUnit('MB', layersTotalMB(view.value?.history)))

  /**
   * 分层行：**顺序照抄 agent 给的**（自下而上，基础层在前），页面只做展示换算。
   *
   * 空元数据层没有大小（`ENV`/`CMD` 这类指令不产生文件系统层），对用户显示「—」
   * 而不是「0B」：0B 会被读成「这一层是空的但占了一个位置」，而「—」说的是「没有大小这项事实」。
   */
  const layerRows = computed<LayerRow[]>(() =>
    (view.value?.history ?? []).map((l, i) => ({
      key: i,
      sizeText: l.emptyLayer ? '—' : formatByUnit('MB', (l.sizeBytes ?? 0) / (1024 * 1024)),
      timeText: l.created ? formatRelativeTime(l.created) : '—',
      createdBy: l.createdBy || '—'
    }))
  )

  /** 指令列等宽显示（Dockerfile 指令是代码，等宽才读得出参数边界）。 */
  // 数据列用 minWidth（列宽口径见 responsive-columns.ts：只有结构性列用固定 width）；
  // 「时间」列在手机横屏（<768）隐藏，保留「层大小（标识）+ 指令（内容）」。
  const layerColumns = [
    { prop: 'sizeText', label: '层大小', minWidth: 130 },
    { prop: 'timeText', label: '时间', minWidth: 130, hideBelow: 'tablet' as const },
    {
      prop: 'createdBy',
      label: '指令',
      minWidth: 320,
      showOverflowTooltip: true,
      formatter: (row: LayerRow) => h('code', { class: 'imd-mono' }, row.createdBy)
    }
  ]

  const exposedPortsText = computed(() => {
    const list = view.value?.exposedPorts ?? []
    return list.length ? list.join('、') : '—'
  })

  const labelRows = computed(() =>
    Object.entries(view.value?.labels ?? {}).map(([name, value]) => ({ name, value }))
  )
  const labelColumns = [
    { prop: 'name', label: '标签', minWidth: 260, showOverflowTooltip: true },
    { prop: 'value', label: '值', minWidth: 300, showOverflowTooltip: true }
  ]

  /**
   * 关联容器：用快照里名字匹配（快照的 image 字段与镜像的 repo tag 是同一个引用口径）。
   * 不新开端点 —— 这个事实快照里已经有了。
   */
  const related = computed(() => {
    const tags = new Set(view.value?.repoTags ?? [])
    return (state.value?.containers ?? []).filter((c) => tags.has(c.image))
  })

  /** 快照里根本没有这个镜像时不说「没有容器在用」：那是「没读到」，不是「没有」。 */
  const relatedEmptyText = computed(() =>
    snapshotImage.value ? '没有容器在用这个镜像' : '主机快照里没有这个镜像，暂时无法判断关联容器'
  )

  const containerColumns = [
    { prop: 'name', label: '名称', minWidth: 220, showOverflowTooltip: true },
    {
      prop: 'state',
      label: '状态',
      minWidth: 180,
      showOverflowTooltip: true,
      // 原生状态句（"Up 16 hours"）与容器列表逐字一致，不在这里另造措辞。
      formatter: (row: DockerContainerItem) => containerStateText(row)
    },
    {
      // 操作性列（内容宽度恒定）是唯一保留固定 width 的一列。
      prop: 'operation',
      label: '操作',
      width: 90,
      fixed: 'right' as const,
      formatter: (row: DockerContainerItem) =>
        h('div', { class: 'flex items-center' }, [
          h(ArtButtonTable, {
            type: 'view',
            title: '详情',
            onClick: () => openContainer(row.id)
          })
        ])
    }
  ]

  /** 入口点/命令数组拼成一行（等宽展示），空数组给「—」。 */
  function joinArgs(args: string[] | undefined): string {
    return args && args.length ? args.join(' ') : '—'
  }

  /**
   * 发一条只读指令并轮询到终态。
   *
   * 受理（202 + ref）与结果查询是两步：agent 是异步执行的，结果由轮询取回。节奏由
   * pollDelay 给（1 秒起指数退避、5 秒封顶）—— 轻量读取通常 1 秒内就有结果。超时（约 20 次）
   * 给一句结论句而不是继续等。
   */
  async function runRead(
    action: Phase1Action,
    options: Record<string, unknown> = {}
  ): Promise<DockerCmdResultResp> {
    const accepted = await sendDockerCmd(ctx.hostId, {
      action,
      target: imageId.value,
      options
    })
    let attempt = 0
    for (;;) {
      const res = await fetchDockerCmdResult(ctx.hostId, accepted.ref)
      if (res.status !== 'pending') return res
      await new Promise((r) => setTimeout(r, pollDelay(attempt++)))
      if (attempt > 20) {
        return { status: 'timeout', error: '读取超时，请稍后重试' } as DockerCmdResultResp
      }
    }
  }

  /** 受理期失败（无权限/设备离线/参数不合法）的文案：服务端给的结论句优先。 */
  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /**
   * 读镜像详情（三个 Tab 共用这一次读取）。
   *
   * 失败文案用结果里的 error：那是 agent 给出的结论句（「没有找到这个镜像」这类），
   * 比前端编一句「读取失败」有用得多。静默不弹 toast —— 结论就在页面里。
   */
  async function loadInspect() {
    if (!ctx.hostId || !imageId.value) return
    const seq = ++inspectSeq
    loading.value = true
    try {
      const res = await runRead('image:inspect')
      if (seq !== inspectSeq) return
      if (res.status !== 'succeeded') {
        view.value = null
        errorText.value = res.error || '读取镜像信息失败，请稍后重试'
        return
      }
      errorText.value = ''
      view.value = parseImageInspectPayload(res.payload)
    } catch (e) {
      if (seq !== inspectSeq) return
      view.value = null
      errorText.value = errMsg(e, '读取镜像信息失败，请稍后重试')
    } finally {
      if (seq === inspectSeq) loading.value = false
    }
  }

  /**
   * 读主机快照（头部「使用」与关联容器 Tab 的数据源）。
   *
   * 失败与「快照里没有这个镜像」分开记：前者说「没读到」，后者说「主机上没有」。
   */
  async function loadSnapshot() {
    if (!ctx.hostId) return
    const seq = ++snapshotSeq
    snapshotLoading.value = true
    try {
      const res = await fetchDockerState(ctx.hostId)
      if (seq !== snapshotSeq) return
      state.value = res
      snapshotError.value = false
    } catch {
      if (seq !== snapshotSeq) return
      state.value = null
      snapshotError.value = true
    } finally {
      if (seq === snapshotSeq) snapshotLoading.value = false
    }
  }

  /** 返回镜像表（7a：三个旧列表页收敛为 /docker/resources 的 tab）：带上当前
   *  主机与镜像 tab，资源页据此还原到「同一台机器的那张表」。 */
  function back() {
    router.push({ name: 'DockerResources', query: { host: ctx.hostId, tab: 'images' } })
  }

  /** 跳关联容器的详情页（8a：深链指 /docker/containers/:id，host 随行；容器 id 与
   *  列表同一口径 —— 详情页自己有 inspect，不需要本页替它兜底任何事实）。 */
  function openContainer(id: string) {
    void router.push({
      path: `/docker/containers/${id}`,
      query: { host: ctx.hostId }
    })
  }

  // ── 二期写操作（头部按钮区）──────────────────────────────────────────
  // 确认档与列表页同一来源（utils/actions + components/action-confirm）：删除是标准档，
  // 导出覆盖要逐字输入文件名（两段），打标签无需确认；镜像没有保护粒度。
  // 使用中的镜像不能删除（快照里的 inUse 是 agent 算好的结论）：按钮禁用 + 结论句。
  const { hasAuth } = useAuth()
  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))

  // 写指令通道：受理 + 轮询 + 成功后重读（inspect 与快照都重读 —— 打标签会改引用、
  // 删除会让详情失效，两者都要反映在页面上）。
  const { run, busy } = useDockerCmds({
    refresh: () => {
      void loadInspect()
      void loadSnapshot()
    }
  })

  /** 写操作的 target：优先快照/详情的仓库标签，无标签的镜像退回路由里的镜像 id。 */
  const actionTarget = computed(
    () => snapshotImage.value?.repoTags?.[0] || view.value?.repoTags?.[0] || imageId.value
  )

  // ── 创建容器（4a 创建面 → 8b 整页路由）──────────────────────────────
  // 预填走 query（主机 + 本镜像引用）：创建页据此落定主机、预填镜像并拉该主机的
  // 镜像清单；创建成功后由创建页自己接管（跳新建容器的详情页），本页不再有
  // 「创建成功后重读本页两份事实」这件事 —— 页面间的交接靠路由，不靠回调。
  function openCreate() {
    void router.push({
      path: '/docker/containers/create',
      query: { host: ctx.hostId, image: actionTarget.value }
    })
  }

  // ── 推送到仓库（P2 分发闭环）──────────────────────────────────────────
  // 入口在头部按钮区（canManage 门控）；对话框自带表单/进度/收尾生命周期，
  // 页面只出入口与两份事实（主机 + 镜像清单 + 预填引用）。
  const pushVisible = ref(false)

  function openPush() {
    pushVisible.value = true
  }

  const inUseBlocked = computed(() => snapshotImage.value?.inUse === true)

  /** 使用中的删除结论句（快照未到时不拦：那时并不知道有没有容器在用，猜一个是不诚实的）。 */
  const removeBlockedConclusion = computed(() => {
    if (!inUseBlocked.value) return ''
    const names = snapshotImage.value?.inUseBy ?? []
    return names.length
      ? `镜像正被容器 ${names.join('、')} 使用，需先删除相关容器后再删除`
      : '镜像正被容器使用，需先删除相关容器后再删除'
  })
  const removeDisabled = computed(() => busy.value || inUseBlocked.value)

  interface ConfirmState {
    visible: boolean
    /** 两个入口的提交分支不同：删除（标准档）与导出覆盖（文件名档）。 */
    kind: 'remove' | 'save'
    action: string
    target: string
    options: Record<string, unknown>
  }

  const confirmState = ref<ConfirmState>({
    visible: false,
    kind: 'remove',
    action: '',
    target: '',
    options: {}
  })
  const confirmLoading = ref(false)

  /** 结果 → 界面回执：成功优先用结果的 detail（agent 的结论句），失败用统一的结论句。 */
  function reportResult(res: DockerCmdRunResult): void {
    if (res.ok) ElMessage.success(res.detail || '操作已完成')
    else ElMessage.error(runErrorMessage(res, '操作未完成'))
  }

  /** 收集一段输入的弹窗；取消/关闭返回空串（调用方据此中止，不发送指令）。 */
  async function promptText(title: string, message: string, placeholder: string): Promise<string> {
    try {
      const { value } = await ElMessageBox.prompt(message, title, {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        inputPlaceholder: placeholder,
        inputPattern: /\S/,
        inputErrorMessage: '内容不能为空'
      })
      return (value ?? '').trim()
    } catch {
      return ''
    }
  }

  /** 打标签：src 取当前镜像引用，dst 由用户输入。 */
  async function onTag() {
    const src = actionTarget.value
    const dst = await promptText(
      '打标签',
      `为镜像「${title.value}」输入新的引用。`,
      '例如 仓库/名称:标签'
    )
    if (!dst) return
    reportResult(await run({ action: 'image:tag', options: { src, dst } }))
  }

  /** 导出 tar（两段时序）：先不带覆盖标记发一次；产物已存在时确认文件名后带覆盖标记重发。 */
  async function onSave() {
    const target = actionTarget.value
    const filename = await promptText(
      '导出 tar',
      '只填文件名，产物落在该主机的 agent 下载目录。',
      '例如 镜像名.tar'
    )
    if (!filename) return
    const res = await run({ action: 'image:save', target, options: { filename } })
    if (res.ok) {
      reportResult(res)
      return
    }
    if (res.alreadyExists === true) {
      confirmState.value = {
        visible: true,
        kind: 'save',
        action: 'image:save',
        target,
        options: { filename, overwrite: true }
      }
      return
    }
    reportResult(res)
  }

  /** 删除镜像：标准档确认；使用中的镜像不发指令（服务端也会拒绝），只给结论。 */
  function openRemove() {
    if (inUseBlocked.value) {
      ElMessage.warning('镜像正被容器使用，需先删除相关容器后再删除镜像')
      return
    }
    confirmState.value = {
      visible: true,
      kind: 'remove',
      action: 'image:remove',
      target: actionTarget.value,
      options: {}
    }
  }

  async function onConfirmSubmit(payload: { confirm: string; force: boolean }) {
    const st = confirmState.value
    confirmLoading.value = true
    try {
      if (st.kind === 'remove') {
        const res = await run({
          action: 'image:remove',
          target: st.target,
          confirm: payload.confirm
        })
        reportResult(res)
        // 删除成功 → 回列表页：列表挂载时会自己拉最新快照（删掉的镜像不会再出现）。
        if (res.ok) back()
      } else {
        // 第二段：覆盖标记 + 文件名逐字确认一起带上，服务端逐字校验后才会覆盖。
        const filename = String(st.options.filename ?? '')
        reportResult(
          await run({
            action: 'image:save',
            target: st.target,
            options: { filename, overwrite: true },
            confirm: filename
          })
        )
      }
    } finally {
      confirmLoading.value = false
      confirmState.value = { ...st, visible: false }
    }
  }

  // 主机到达/切换：重新读详情与快照。首次进入时 hostId 从 '' 变成首台 id，也是这里兜住。
  watch(
    () => ctx.hostId,
    () => {
      if (!ctx.hostId) return
      void loadInspect()
      void loadSnapshot()
    },
    { immediate: true }
  )

  // 首次进入：拉主机清单（query 里的主机写回；query 里没有就落到第一台并写回）。
  // hostId 从 '' 变成首台 id 时会走上面的 watch，故这里不直接发读取指令。
  void ctx.reload()
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use './overview-tokens' as t;

  // hero 的「删除…」是 plain danger：对比度 AA 的病灶与处方见 overview-tokens
  // 的 danger-plain-aa（浅色 QA 实测 2.87:1）。
  @include t.danger-plain-aa;

  /* 次要文字对比度 AA（终审 QA D2 同源盘点 · 六页批漏网页补齐）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1，低于 AA 正文线 —— 本页
     hero 短 id、元信息标签、镜像摘要等 12–14px 次要文字全吃它。页面范围内升到
     regular 档（浅色 6.1:1、暗色随主题同样达标；与其余 docker 页同款处置）。
     只重定义变量值，不碰元素样式与布局。 */
  .imd {
    --el-text-color-secondary: var(--el-text-color-regular);
  }

  // 实体头（与容器详情的 hero 同一骨架：左身份、右动作）。
  .imd-hero {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: flex-start;
    justify-content: space-between;
    padding: 16px;
    background: var(--default-box-color);
    border-radius: 8px;

    &__identity {
      min-width: 0;
    }

    &__title-row {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
    }

    &__title {
      margin: 0;
      font-size: 18px;
      font-weight: 600;
      word-break: break-all;
    }

    &__sub {
      margin: 6px 0 0;
      font-size: 12px;
    }

    &__id {
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
    }

    &__actions {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
    }

    // 「不能删除」的结论句：与陈旧标注同一套颜色语言（琥珀色 = 需要注意的结论）。
    // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token。
    &__blocked {
      color: var(--aa-warning-text);
      font-size: 12px;
    }
  }

  // 摘要行：宽度不够就换行（窄屏下不挤压）。
  .imd-facts {
    display: flex;
    flex-wrap: wrap;
    gap: 12px 32px;
    margin-top: 12px;
    padding: 12px 16px;
    background: var(--default-box-color);
    border-radius: 8px;
  }

  .imd-fact {
    display: flex;
    gap: 8px;
    align-items: baseline;
    min-width: 0;

    &__label {
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__value {
      font-size: 13px;
      word-break: break-all;
    }

    // 悬空是「可以回收」的信号，需要被看见（与容器详情的保护标记同一套颜色语言）。
    // 文字对比度 AA（收尾批）：原 el-color-warning 改走 token。
    &__value.is-warn {
      color: var(--aa-warning-text);
    }
  }

  .imd-tabs {
    :deep(.el-tabs__header) {
      margin-bottom: 16px;
    }
  }

  .imd-block {
    margin-top: 16px;

    &__title {
      margin-bottom: 8px;
      font-size: 14px;
      font-weight: 600;
    }
  }

  // 指令列与入口点/命令：Dockerfile 指令是代码，等宽才分得清参数边界。
  // h() 造出的节点拿不到 scoped 属性，故从有作用域的外层用 :deep 穿进去。
  :deep(.imd-mono) {
    font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
  }

  .imd-code {
    display: flex;
    gap: 12px;
    align-items: baseline;
    padding: 6px 0;
    font-size: 13px;

    &__label {
      flex: none;
      min-width: 56px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__value {
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      word-break: break-all;
    }
  }

  // 分层合计：主数字与结论说明分成两行，结论弱化（它是口径说明，不是数字）。
  .imd-total {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
    align-items: baseline;
    padding: 12px 0 0;
    font-size: 14px;

    &__sub {
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }

  /* ── 响应式 ─────────────────────────────────────── */

  // 窄屏（<1024，覆盖平板竖屏与手机横屏）：身份与动作分成上下两段 ——
  // 并排时右侧按钮组会把镜像名/短 id 挤成窄条。
  @include respond-below('desktop') {
    .imd-hero__identity,
    .imd-hero__actions {
      width: 100%;
    }
  }

  // 手机横屏（<768）：摘要行每条占满一行，避免两条挤在一行里互相截断。
  @include respond-below('tablet') {
    .imd-hero {
      padding: 12px;
    }

    .imd-facts {
      gap: 8px 16px;
      padding: 12px;
    }

    .imd-fact {
      flex: 1 1 100%;
    }
  }
</style>
