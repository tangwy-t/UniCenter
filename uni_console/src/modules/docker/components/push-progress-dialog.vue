<template>
  <!-- 单根（single-root 守卫扫描全模块的 .vue）：ElDialog 是唯一根（与
       pull-progress-dialog 同一挂法，原地渲染不搬 body）。 -->
  <ElDialog
    :model-value="modelValue"
    title="推送镜像"
    width="560px"
    :close-on-click-modal="false"
    :close-on-press-escape="phase !== 'pushing'"
    @update:model-value="onDialogVisible"
  >
    <!-- ── 输入态：本地镜像选择 + 凭据 ──
         确认档照 image:push 的注册表现状（confirm: 'none'）：点了直接派发 ——
         对话框本身就是一次明确的确认（选镜像 + 开始推送两步），协议也不要求
         confirm 值（与拉取对话框同一条论证）。 -->
    <div v-if="phase === 'input'" class="sp-input">
      <ElSelect
        v-model="targetChoice"
        class="sp-input__select"
        placeholder="搜索或输入镜像引用"
        filterable
        allow-create
        default-first-option
        :disabled="accepting"
      >
        <ElOption v-for="o in targetOptions" :key="o" :label="o" :value="o" />
      </ElSelect>
      <!-- 清单为空（快照未到/读取失败）：不说「没有镜像」，那是「没读到」——
           手输兜底（allow-create），daemon 是存在性的最终事实。判据看清单本身
          （而不是选项数）：预填引用在场时选项非空，但「没清单可选」仍是事实。 -->
      <p v-if="listEmpty" class="sp-input__hint">
        主机镜像清单未就绪，可直接输入要推送的镜像引用。
      </p>
      <p v-if="targetErrorText" class="sp-input__error">{{ targetErrorText }}</p>

      <!-- 私库凭据选择（与拉取对话框同一面，4c）：仅 docker:config 用户渲染。
           选项 = 已存凭据的仓库地址（凭据键本身，不含任何秘密；密码由服务端受理
           时注入）；默认「不使用」= options 不带 registry 字段。 -->
      <div v-if="canConfig" class="sp-creds">
        <ElSelect
          v-if="registryItems.length > 0"
          v-model="registryChoice"
          class="sp-creds__select"
          placeholder="选择仓库"
          :disabled="accepting"
        >
          <ElOption label="不使用（匿名推送）" value="" />
          <ElOption
            v-for="r in registryItems"
            :key="r.registry"
            :label="r.registry"
            :value="r.registry"
          />
        </ElSelect>
        <!-- 空态/失败态各给一句就地说明（不悄悄消失：看得见下拉的人该知道为什么没得选）；
             读取中不出声（credsLoaded 才判「确实没有」，免得闪一句错话）。 -->
        <p v-else-if="credsFailed || credsLoaded" class="sp-creds__hint">
          {{
            credsFailed
              ? '已保存凭据读取失败，可先匿名推送'
              : '还没有已保存的仓库凭据（镜像页底栏可管理）'
          }}
        </p>
      </div>
      <!-- 受理期错误（403/409/400…）：分类句来自 classifyAcceptError（服务端 msg 优先）；
           留在输入态可改可重试，不弹 toast —— 对话框还开着，结论就地给。 -->
      <p v-if="acceptError" class="sp-input__error">{{ acceptError }}</p>
      <p class="sp-input__hint"
        >推送需要镜像带仓库前缀（如 registry.example.com/app:v1），推送到 Docker Hub
        用「用户名/名称:标签」形态。</p
      >
    </div>

    <!-- ── 进度态：汇总 + 逐层列表（层形态与拉取同款，语义词换推送侧）── -->
    <div v-else-if="phase === 'pushing'" class="sp-progress">
      <div class="sp-progress__ref">{{ pushTarget }}</div>
      <p v-if="feedView.note" class="sp-progress__note">{{ feedView.note }}</p>
      <p v-if="summaryText" class="sp-progress__summary">{{ summaryText }}</p>

      <ul v-if="feedView.layers.length" class="sp-layers">
        <li
          v-for="layer in feedView.layers"
          :key="layer.id"
          class="sp-layer"
          :class="{ 'is-done': layer.done }"
        >
          <span class="sp-layer__id" :title="layer.id">{{ layer.shortId }}</span>
          <span class="sp-layer__status" :title="layer.status">{{ layer.status }}</span>
          <!-- 进度条：有 total 走确定值；无 total（"Preparing"/"Waiting" 这类还没有
               progressDetail 的行）走不确定态动画；层终态（Pushed）后不再画条。 -->
          <ElProgress
            v-if="!layer.done"
            class="sp-layer__bar"
            :percentage="layer.total > 0 ? layerPercent(layer.current, layer.total) : 100"
            :indeterminate="layer.total <= 0"
            :stroke-width="6"
            :show-text="false"
          />
          <span v-if="!layer.done && layer.total > 0" class="sp-layer__bytes">
            {{ formatPullBytes(layer.current) }} / {{ formatPullBytes(layer.total) }}
          </span>
          <ArtSvgIcon v-if="layer.done" icon="ri:check-line" class="sp-layer__check" />
        </li>
      </ul>
      <p v-else class="sp-progress__empty">正在等待进度…</p>

      <!-- 收尾提示：三态（用户取消 / 流接入失败 / 流中途断开）+ eof 后等 result，
           各自说清楚「接下来等什么」，不让对话框看起来像卡死（与拉取同款）。 -->
      <p v-if="waitingHint" class="sp-progress__hint">{{ waitingHint }}</p>
    </div>

    <!-- ── 终态：结论句（成功 = 镜像引用 + 耗时；失败 = 结论句原文）── -->
    <div v-else class="sp-result" :class="resultOk ? 'is-ok' : 'is-fail'">
      <ArtSvgIcon :icon="resultIcon" class="sp-result__icon" />
      <span class="sp-result__text">{{ resultText }}</span>
    </div>

    <template #footer>
      <template v-if="phase === 'input'">
        <ElButton :disabled="accepting" @click="requestClose">关闭</ElButton>
        <ElButton type="primary" :disabled="!canStart" :loading="accepting" @click="startPush">
          开始推送
        </ElButton>
      </template>
      <!-- 推送在途只有一条出路：取消（断流 → 服务端终止推送）。ESC 与遮罩点击此时
           都被关掉（action-confirm 的 loading 纪律）：误触不该杀掉一场进行中的推送。 -->
      <ElButton v-else-if="phase === 'pushing'" :disabled="canceled" @click="cancelPush">
        {{ canceled ? '已取消' : '取消推送' }}
      </ElButton>
      <ElButton v-else type="primary" @click="requestClose">关闭</ElButton>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  /**
   * 推送进度对话框（P2 分发闭环前端半边）：镜像详情页「推送到仓库…」的输入面 +
   * 逐层实时进度 + 取消。生命周期与双通道纪律照 4b 的拉取对话框**逐条同源**
   *（受理 → pending 期即开流 + result 轮询 → eof 先于 result → 断开即取消），
   * 差异只有三处：
   *   - 输入面：镜像引用从**该主机的镜像清单**选（可搜索；手输兜底 —— 快照未到或
   *     陈旧时 daemon 仍是存在性的最终事实），而不是拉取的「从仓库拉什么」；
   *   - 凭据面与拉取**同一份**（4c 的凭据键、config 门控、空态/失败态说明）；
   *   - 进度面：层形态与拉取同款（daemon 的 push 与 pull 是同一个 JSON 进度流），
   *     语义词换推送侧（Pushing/Pushed、汇总说「已上传」）—— 折叠器复用
   *     utils/pull.ts 的 createPushFeed（LayerFeedFlavor 换语义字面量）。
   *
   * 为什么是独立组件而不是泛化 pull-progress-dialog：输入面结构性不同（选择 +
   * 预填 vs 单输入）、层语义词不同 —— 能共享的（折叠/解析/收尾时序）都在纯逻辑层
   * 复用，组件骨架照 4b 克隆（与 task-pull-progress 的取舍同源：不为了「一份基类」
   * 去动已验证的组件）。
   */
  import { computed, onBeforeUnmount, ref, watch } from 'vue'
  import { ElButton, ElDialog, ElOption, ElProgress, ElSelect } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerConfig } from '@/enums/permission'
  import {
    fetchDockerCmdResult,
    fetchDockerRegistries,
    openDockerPushStream,
    sendDockerCmd,
    type DockerImageItem,
    type DockerRegistryItem
  } from '../api'
  import { classifyAcceptError } from '../composables/useDockerCmds'
  import { pollDelay } from '../utils/cmd'
  import {
    createPushFeed,
    formatPullBytes,
    isValidImageRef,
    layerPercent,
    type PullFeed,
    type PullLayer,
    type PullTerminal
  } from '../utils/pull'

  defineOptions({ name: 'DockerPushProgressDialog' })

  const props = defineProps<{
    modelValue: boolean
    /** 推送目标主机（受理时钉死：一次指令只属于受理它的那台主机）。 */
    hostId: string
    /** 该主机的镜像清单（选择项的数据源 —— 页面级快照已在手，对话框不另拉一份）；
        未就绪时给空数组，手输兜底。 */
    images?: DockerImageItem[]
    /** 预填的镜像引用（详情页入口带上当前镜像；清单未含它时也并入选项）。 */
    initialTarget?: string
    /** 成功关闭后的重拉（可选）：推送不改本地镜像清单 —— 默认无物可重读，调用方
        只在确有页面事实会变时才传。 */
    refresh?: () => void | Promise<void>
  }>()

  const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

  type Phase = 'input' | 'pushing' | 'result'

  interface FeedView {
    layers: PullLayer[]
    note: string
    uploadedBytes: number
    totalBytes: number
    doneLayers: number
  }

  interface ResultState {
    ok: boolean
    text: string
  }

  const phase = ref<Phase>('input')
  const targetChoice = ref('')
  const acceptError = ref('')
  const accepting = ref(false)

  // ── 私库凭据（与拉取对话框同一面，4c） ──

  /** 凭据下拉只对 docker:config 渲染：能管凭据的人才看得见「用哪条」；
      其他人不受影响（推送照旧匿名，多一个空下拉只会平添困惑）。 */
  const { hasAuth } = useAuth()
  const canConfig = computed(() => hasAuth(PermDockerConfig))
  /** 已存凭据清单（拉列表的时机 = 对话框打开；密码在列表里恒为掩码，用不到）。 */
  const registryItems = ref<DockerRegistryItem[]>([])
  /** 选中的仓库地址（'' = 不使用）。 */
  const registryChoice = ref('')
  /** 清单是否已落定（区分「还在读」与「读完了确实没有」—— 后者要给一句说明）。 */
  const credsLoaded = ref(false)
  const credsFailed = ref(false)

  /**
   * 拉凭据清单（打开对话框时，仅 canConfig）。
   *
   * 失败**不挡推送**：匿名推送是既有能力，凭据只是增强 —— 下拉退回一句
   * 「读取失败，可先匿名推送」的说明，而不是把输入态标红。
   */
  async function loadCreds(): Promise<void> {
    if (!canConfig.value) return // 无 config 权限者不发请求（只会在服务端换回一个 403）
    credsLoaded.value = false
    credsFailed.value = false
    try {
      const resp = await fetchDockerRegistries()
      registryItems.value = resp.list
      credsLoaded.value = true
    } catch {
      registryItems.value = []
      credsFailed.value = true
    }
  }

  const feedView = ref<FeedView>({
    layers: [],
    note: '',
    uploadedBytes: 0,
    totalBytes: 0,
    doneLayers: 0
  })
  const canceled = ref(false)
  const streamEnded = ref(false)
  /** '' = 流还连着；'open' = 接入失败（推送仍在服务端跑）；'broken' = 连上后断开（断开即取消）。 */
  const streamFailed = ref<'' | 'open' | 'broken'>('')
  const streamFailText = ref('')
  const resultState = ref<ResultState | null>(null)
  /** 进度态头部的目标引用（受理时钉下；模板要显示，故是 ref）。 */
  const pushTarget = ref('')

  /** 生命周期序号：关闭/重开让在飞的「流 + 轮询」双双失效（照 pull-progress-dialog）。 */
  let pushSeq = 0
  let streamAbort: AbortController | null = null
  /** 流的终态项（done/error 那一行；取消路径上服务端不发）。 */
  let streamTerminal: PullTerminal | null = null
  let startedAt = 0
  let endedAt = 0
  /** 双次重拉只补一次的闸（关闭路径可能被走两遍：页脚按钮 + prop 回写）。 */
  let refreshedAfterSuccess = false

  /** 流收口后 result 轮询的兜底上限（对齐 useDockerCmds 的 20 次口径）。 */
  const MAX_RESULT_POLLS = 20

  // ── 输入态 ──

  /** 与拉取对话框同一句人话（同一把协议尺 dockerImageRefRe）。 */
  const IMAGE_REF_ERROR = '镜像引用不合法：以字母或数字开头，仅可包含字母、数字与 / . _ : @ -'

  /**
   * 选择项：清单里各镜像的全部仓库标签（一枚镜像可有多个标签，各自是独立的
   * 推送引用）+ 预填引用（清单未含它时并入 —— 详情页的镜像可能恰好不在快照里）。
   * 无标签的悬空镜像没有可推送的引用（推送需要仓库名），不进选项。
   */
  const targetOptions = computed<string[]>(() => {
    const out: string[] = []
    for (const img of props.images ?? []) {
      for (const tag of img.repoTags ?? []) {
        if (tag !== '' && !out.includes(tag)) out.push(tag)
      }
    }
    const pre = (props.initialTarget ?? '').trim()
    if (pre !== '' && !out.includes(pre)) out.push(pre)
    return out
  })

  /** 镜像清单是否空手（未传/未到）：手输提示的判据（见模板注释）。 */
  const listEmpty = computed(() => (props.images ?? []).length === 0)

  /** 校验是实时的（非空才显错）：空串不该挨骂 —— 空值由按钮禁用表达。 */
  const targetErrorText = computed(() => {
    const v = targetChoice.value.trim()
    if (v === '') return ''
    return isValidImageRef(v) ? '' : IMAGE_REF_ERROR
  })

  const canStart = computed(
    () => targetChoice.value.trim() !== '' && targetErrorText.value === '' && !accepting.value
  )

  // ── 进度态的派生展示 ──

  /** 汇总（与拉取同款形态，动词换「上传」：Pushing 段的字节是推上去的量）。 */
  const summaryText = computed(() => {
    const f = feedView.value
    if (f.layers.length === 0) return ''
    const parts: string[] = []
    if (f.totalBytes > 0) {
      parts.push(`已上传 ${formatPullBytes(f.uploadedBytes)} / ${formatPullBytes(f.totalBytes)}`)
    }
    parts.push(`${f.doneLayers}/${f.layers.length} 层完成`)
    return parts.join(' · ')
  })

  /** 收尾提示：把「还在等什么」说清楚（三态收口 + eof 后等 result），与拉取同款。 */
  const waitingHint = computed(() => {
    if (phase.value !== 'pushing') return ''
    if (canceled.value) return '已取消，正在等待指令收尾…'
    if (streamFailed.value === 'open') {
      return `进度流未能建立（${streamFailText.value}），推送仍在进行，等待结果…`
    }
    if (streamFailed.value === 'broken') {
      return streamFailText.value
        ? `进度流已断开（${streamFailText.value}），断开即取消推送，正在等待收尾…`
        : '进度流已断开，断开即取消推送，正在等待收尾…'
    }
    if (streamEnded.value) return '进度已全部到达，正在等待指令结果…'
    return ''
  })

  // ── 终态的派生展示 ──

  const resultOk = computed(() => resultState.value?.ok ?? false)
  const resultText = computed(() => resultState.value?.text ?? '')
  const resultIcon = computed(() =>
    resultOk.value ? 'ri:checkbox-circle-line' : 'ri:close-circle-line'
  )

  // ── 小工具（文案与错误归类，照 pull-progress-dialog 的同名函数口径） ──

  const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 流端点接入失败的结论句（状态码是排障线索，不进页面）。 */
  function streamOpenConclusion(status: number): string {
    if (status === 401) return '登录状态已失效'
    if (status === 403) return '没有接入该进度流的权限'
    if (status === 404) return '该指令已不存在或会话已过期'
    if (status === 409) return '该进度会话已有连接'
    return '进度流未能建立'
  }

  // ── 发起（受理 → 双通道并行） ──

  async function startPush(): Promise<void> {
    const target = targetChoice.value.trim()
    if (!canStart.value) return
    // hostId 在入口钉死（与 useDockerCmds 同理由）：推送在途时切了主机，轮询跟着
    // 新主机走会拿老 ref 查新记录。详情页的主机与镜像受理时钉死同一场。
    const hostId = props.hostId
    accepting.value = true
    acceptError.value = ''
    let cmdRef: string
    try {
      const accepted = await sendDockerCmd(hostId, {
        action: 'image:push',
        target,
        // 选中凭据时带 registry（凭据键，不含秘密 —— 密码由服务端受理时解密注入，
        // 前端任何路径都摸不到）；没选就是字面量 {}（与拉取对话框同一口径 ——
        // 协议对空串与缺省同判非法，宁缺勿滥）。
        options: registryChoice.value ? { registry: registryChoice.value } : {}
      })
      cmdRef = accepted.ref
    } catch (e) {
      // 受理失败留在输入态（选择保留，可改可重试）：403/409/400 的结论句由
      // classifyAcceptError 分类（服务端 msg 优先）。
      acceptError.value = classifyAcceptError(e).message
      return
    } finally {
      accepting.value = false
    }
    pushTarget.value = target
    startedAt = Date.now()
    streamTerminal = null
    phase.value = 'pushing'
    const seq = ++pushSeq
    // 双通道并行：流（pending 期间即可接入）+ 轮询（终态结论句）。
    void runStreamLoop(seq, hostId, cmdRef)
    void runPollLoop(seq, hostId, cmdRef)
  }

  // ── 通道一：进度流 ──

  /** 把折叠器的当前快照换进响应式视图（换数组引用驱动重渲染，与 container-stats 同手法）。 */
  function syncFeed(feed: PullFeed): void {
    feedView.value = {
      layers: feed.layers.slice(),
      note: feed.note,
      // 折叠器的字段名是拉取史的（downloadedBytes）—— 推送形态下它记账的是
      // Pushing 段（视图字段名换 uploadedBytes，页面文案说「已上传」）。
      uploadedBytes: feed.downloadedBytes,
      totalBytes: feed.totalBytes,
      doneLayers: feed.doneLayers
    }
  }

  async function runStreamLoop(seq: number, hostId: string, cmdRef: string): Promise<void> {
    const controller = new AbortController()
    streamAbort = controller
    try {
      const res = await openDockerPushStream(hostId, cmdRef, controller.signal)
      if (seq !== pushSeq) return
      if (!res.ok || !res.body) {
        // 接入失败（401/403/404/409…）：这条连接没建立过，**不会**触发服务端取消 ——
        // 推送仍在跑，降级为「无进度、等结果」，不中止流程。
        streamFailed.value = 'open'
        streamFailText.value = streamOpenConclusion(res.status)
        return
      }
      const feed = createPushFeed()
      const reader = res.body.getReader()
      const dec = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (seq !== pushSeq) return
        if (done) break
        feed.pushRaw(dec.decode(value, { stream: true }))
        syncFeed(feed)
        if (feed.eof) break
      }
      if (seq !== pushSeq) return
      // 收尾：把解码器里的残留字节冲进折叠器（终态行可能正好被切在 chunk 尾）。
      feed.pushRaw(dec.decode())
      syncFeed(feed)
      if (feed.eof) {
        streamTerminal = feed.terminal
        streamEnded.value = true
      } else {
        // 读尽但没见 eof：网络层收口、应用层没收官 —— 视同断流（断开即取消）。
        streamFailed.value = 'broken'
        streamFailText.value = ''
      }
    } catch (e) {
      if (seq !== pushSeq) return
      // 主动断开（取消/收尾/关对话框）不是故障：流是被自己掐断的。
      if ((e as { name?: string })?.name === 'AbortError') return
      streamFailed.value = 'broken'
      streamFailText.value = errMsg(e, '进度流连接中断')
    } finally {
      if (streamAbort === controller) streamAbort = null
    }
  }

  // ── 通道二：result 轮询（终态收尾） ──

  async function runPollLoop(seq: number, hostId: string, cmdRef: string): Promise<void> {
    let attempt = 0
    /** 兜底上限的起算点：流收口之后才起算 —— 流还活着时轮询**不设上限**（15 分钟档
        的推送里，层帧就是「还活着」的证据；useDockerCmds 的固定 20 次上限是给
        黑盒等待的，这里流把「活着」的举证接过去了）。 */
    let deadlineArmedAt: number | null = null
    for (;;) {
      if (seq !== pushSeq) return
      let res: Awaited<ReturnType<typeof fetchDockerCmdResult>> | undefined
      try {
        res = await fetchDockerCmdResult(hostId, cmdRef)
      } catch {
        res = undefined // 单次网络失败不当终态：流通道可能还活着，下一轮自愈
      }
      if (seq !== pushSeq) return
      if (res && res.status !== 'pending') {
        finalize(seq, res.status === 'succeeded', res.error ?? '')
        return
      }
      attempt++
      if (deadlineArmedAt === null && resultDeadlineArmed()) deadlineArmedAt = attempt
      if (deadlineArmedAt !== null && attempt - deadlineArmedAt >= MAX_RESULT_POLLS) {
        finalizeFallback(seq)
        return
      }
      await sleep(pollDelay(attempt - 1))
    }
  }

  /** result 轮询是否该起兜底上限：eof / 断流 / 用户取消之后，result 按时序应很快
      落定（agent 钉了 eof 先于 result），等不到就是记录异常，不该无限等。 */
  function resultDeadlineArmed(): boolean {
    return streamEnded.value || streamFailed.value !== '' || canceled.value
  }

  /** result 终态收尾（权威通道）：结论句原文 + 断掉还挂着的流。 */
  function finalize(seq: number, ok: boolean, error: string): void {
    if (seq !== pushSeq) return
    endedAt = Date.now()
    // result 已终态：流若还挂着（时序兜底下可能没走完）就断掉 —— 会话已收口，
    // 此时的 cancel 无副作用。
    abortStream()
    if (ok) {
      setResult(true, `已推送 ${pushTarget.value}（耗时 ${durationText()}）`)
    } else {
      setResult(false, error || '推送镜像失败')
    }
  }

  /** result 等不到（记录异常/网络断）的退路：结论退回流的终态项；取消路径上服务端
      不发终态项，按「已取消」措辞；两头都没有时只说「未能确认」—— 不编造结论。 */
  function finalizeFallback(seq: number): void {
    if (seq !== pushSeq) return
    endedAt = Date.now()
    if (canceled.value) {
      setResult(false, '推送已取消')
    } else if (streamTerminal) {
      if (streamTerminal.ok) {
        setResult(true, `已推送 ${pushTarget.value}（耗时 ${durationText()}）`)
      } else {
        setResult(false, streamTerminal.error || '推送镜像失败')
      }
    } else {
      setResult(false, '推送结果未能确认，请稍后在任务中心核对')
    }
  }

  function setResult(ok: boolean, text: string): void {
    resultState.value = { ok, text }
    phase.value = 'result'
  }

  function durationText(): string {
    const sec = Math.max(0, Math.round((endedAt - startedAt) / 1000))
    return formatByUnit('s', sec)
  }

  // ── 取消 / 关闭 / 收口 ──

  function cancelPush(): void {
    if (phase.value !== 'pushing' || canceled.value) return
    canceled.value = true
    // 断流即取消（端点契约：客户端断开 → 服务端向 agent 下发 cancel 终止推送）。
    // result 轮询继续：结论句「推送已取消」由服务端落进 result，页面前端不代答。
    abortStream()
  }

  /** 断流（幂等：没有在飞的流就不扰动）。 */
  function abortStream(): void {
    streamAbort?.abort()
    streamAbort = null
  }

  /** 收口：序号让在飞的流与轮询失效 + 断流（= 服务端取消推送）。幂等。 */
  function cleanup(): void {
    pushSeq++
    abortStream()
  }

  /** 关对话框的完整收口（幂等）：清理 + 成功后的双次重拉（调用方给了 refresh 才有）。 */
  function cleanupAndFinish(): void {
    cleanup()
    maybeDoubleRefresh()
  }

  /** 成功关闭后的双次重拉（useDockerCmds 的落定纪律，1.5s；refresh 可选 —— 推送
      不改本地清单，只在调用方确有页面事实会变时才传）。 */
  function maybeDoubleRefresh(): void {
    if (!resultState.value?.ok || refreshedAfterSuccess) return
    refreshedAfterSuccess = true
    safeRefresh()
    setTimeout(safeRefresh, 1500)
  }

  function safeRefresh(): void {
    try {
      Promise.resolve(props.refresh?.()).catch(() => {
        /* 静默：展示层动作不拖垮结论 */
      })
    } catch {
      /* 同上 */
    }
  }

  /** 页脚按钮的关闭路径（与 X/ESC/父组件置 false 同一条收口）。 */
  function requestClose(): void {
    cleanupAndFinish()
    emit('update:modelValue', false)
  }

  /** ElDialog 自己的关闭路径（X 按钮）：除转发给父组件外当场收口 —— 父组件没接
      v-model 时 prop 不会变，watch 不触发，流就漏了。 */
  function onDialogVisible(v: boolean): void {
    if (!v) cleanupAndFinish()
    emit('update:modelValue', v)
  }

  // 父组件置 false（页面级收口）走 watch；置 true（打开）时整表重置 —— 上一次
  // 推送的任何残留（层表/结论/凭据选择）不进新一场；凭据清单随打开重拉（管理
  // 对话框可能在两次推送之间改过它）。immediate：「带着打开态挂载」与「先挂载再
  // 打开」走同一条路。
  watch(
    () => props.modelValue,
    (v, old) => {
      if (v) {
        resetAll()
        void loadCreds()
        return
      }
      if (old) cleanupAndFinish()
    },
    { immediate: true }
  )

  // 预填引用晚到（详情页的快照/inspect 是异步落定的）而对话框已开着：还没选过
  // 时不追着补 —— 只补「一次都没填」的那条缝，别覆盖用户已做的选择。
  watch(
    () => props.initialTarget,
    (t) => {
      if (props.modelValue && phase.value === 'input' && targetChoice.value === '' && t) {
        targetChoice.value = t
      }
    }
  )

  function resetAll(): void {
    cleanup()
    phase.value = 'input'
    targetChoice.value = (props.initialTarget ?? '').trim()
    acceptError.value = ''
    accepting.value = false
    registryChoice.value = ''
    feedView.value = { layers: [], note: '', uploadedBytes: 0, totalBytes: 0, doneLayers: 0 }
    canceled.value = false
    streamEnded.value = false
    streamFailed.value = ''
    streamFailText.value = ''
    resultState.value = null
    pushTarget.value = ''
    streamTerminal = null
    startedAt = 0
    endedAt = 0
    refreshedAfterSuccess = false
  }

  // 卸载只收口不重拉：页面都没了，重拉的落点不存在（页脚关闭路径已补过重拉）。
  onBeforeUnmount(cleanup)
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../views/overview-tokens' as t;

  // 「关闭 / 取消推送」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  // 输入态：错误句与提示句都是对话框内的就地结论（对话框开着，结论不放 toast）。
  .sp-input {
    padding-bottom: 4px;

    &__select {
      width: 100%;
    }

    &__error,
    &__hint {
      margin: 8px 0 0;
      font-size: 12px;
      line-height: 1.6;
    }

    &__error {
      color: var(--el-color-danger);
    }

    &__hint {
      color: var(--el-text-color-secondary);
    }
  }

  // 凭据选择：与镜像选择同一列（对话框是单列表单，下拉不另起一行标签）。
  .sp-creds {
    margin-top: 10px;

    &__select {
      width: 100%;
    }

    &__hint {
      margin: 6px 0 0;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.6;
    }
  }

  // 进度态头部：目标引用是主信息（加粗），消息行与汇总是次级（弱化色）。
  .sp-progress {
    &__ref {
      color: var(--el-text-color-primary);
      font-size: 14px;
      font-weight: 600;
      word-break: break-all;
    }

    &__note,
    &__summary,
    &__empty,
    &__hint {
      margin: 6px 0 0;
      font-size: 12px;
      line-height: 1.6;
    }

    &__note {
      color: var(--el-text-color-secondary);
      word-break: break-all;
    }

    &__summary {
      color: var(--el-text-color-regular);
    }

    &__empty,
    &__hint {
      color: var(--el-text-color-secondary);
    }
  }

  // 层列表：限高滚动 —— 层多的镜像（几十层）不该把对话框顶出屏幕。
  .sp-layers {
    max-height: 264px;
    margin: 10px 0 0;
    padding: 0;
    list-style: none;
    overflow-y: auto;
  }

  .sp-layer {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 5px 0;
    font-size: 12px;

    &__id {
      // 等宽 12 位：ch 单位在等宽字体下就是一位，层 id 之间可纵向对读。
      flex: none;
      width: 12ch;
      overflow: hidden;
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__status {
      flex: none;
      width: 9.5em;
      overflow: hidden;
      color: var(--el-text-color-regular);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    &__bar {
      flex: 1;
      min-width: 48px;
    }

    &__bytes {
      flex: none;
      color: var(--el-text-color-secondary);
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      text-align: right;
      white-space: nowrap;
    }

    &__check {
      flex: none;
      font-size: 14px;
      color: var(--el-color-success);
    }

    // 终态行整体弱化（完成的事不与进行中的事抢注意力）。
    &.is-done &__status {
      color: var(--el-text-color-secondary);
    }
  }

  // 终态：结论句是这一屏唯一的信息 —— 图标定语义色，文字用主文字色。
  .sp-result {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    padding: 4px 0 8px;

    &__icon {
      flex: none;
      margin-top: 2px;
      font-size: 18px;
    }

    &__text {
      color: var(--el-text-color-primary);
      font-size: 14px;
      line-height: 1.6;
      word-break: break-all;
    }

    &.is-ok &__icon {
      color: var(--el-color-success);
    }

    &.is-fail &__icon {
      color: var(--el-color-danger);
    }
  }

  // 平板竖屏以下（对话框占满宽度）：字节列让位给进度条（条是主信息，字节是精确值，
  // 窄屏上两者只能留一个）。
  @include respond-below('tablet') {
    .sp-layer__status {
      width: 7em;
    }

    .sp-layer__bytes {
      display: none;
    }
  }
</style>
