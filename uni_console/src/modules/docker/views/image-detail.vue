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
        <!-- 一期没有任何操作按钮：打标签/导出/删除都是二期能力（分期控件矩阵，spec §11.0）。
             这里连它们的位置都不留 —— 「不渲染 ≠ 禁用」，画一个二期按钮就是让用户点了拿 400。
             层历史是一次读取的静态事实，故也没有「刷新」：重新进入页面即重新读取。 -->
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
              <!-- 合计只列不与镜像大小混为一谈：共享层会被多个镜像共用，故合计通常小于镜像大小。
                   这句话必须写出来，否则「12 层加起来 ≠ 545MB」会被当成数据错。 -->
              <div v-if="layerRows.length" class="imd-total">
                <span>分层合计 {{ layersTotalText }}（镜像 {{ sizeText }}）</span>
                <span class="imd-total__sub">镜像大小包含共享层，分层合计通常小于镜像大小</span>
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
              <ElDescriptions :column="2" border size="small">
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
        </ElTabs>
      </ElCard>
    </div>
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
    ElSkeleton,
    ElTabPane,
    ElTabs
  } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import { formatByUnit, formatUnixSeconds } from '@/modules/device/utils/display'
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

  type TabName = 'layers' | 'meta' | 'containers'

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
  const layerColumns = [
    { prop: 'sizeText', label: '层大小', width: 130 },
    { prop: 'timeText', label: '时间', width: 130 },
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
    { prop: 'name', label: '标签', width: 260, showOverflowTooltip: true },
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
      width: 180,
      showOverflowTooltip: true,
      // 原生状态句（"Up 16 hours"）与容器列表逐字一致，不在这里另造措辞。
      formatter: (row: DockerContainerItem) => containerStateText(row)
    },
    {
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

  /** 返回镜像列表：带上当前主机，列表页据此还原到同一台机器。 */
  function back() {
    router.push({ name: 'DockerImages', query: { host: ctx.hostId } })
  }

  /** 跳关联容器的详情：容器 id 与列表同一口径（同样是路由参数，同样带主机）。 */
  function openContainer(id: string) {
    void router.push({
      name: 'DockerContainerDetail',
      params: { id },
      query: { host: ctx.hostId }
    })
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
    &__value.is-warn {
      color: var(--el-color-warning);
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
</style>
