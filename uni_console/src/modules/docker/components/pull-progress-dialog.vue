<template>
  <!-- 单根（single-root 守卫扫描全模块的 .vue）：ElDialog 是唯一根。它默认在原地
       渲染（append-to-body 未开 —— 与 action-confirm 同一挂法；页内没有嵌套弹窗，
       不需要搬到 body）。 -->
  <ElDialog
    :model-value="modelValue"
    title="拉取镜像"
    width="560px"
    :close-on-click-modal="false"
    :close-on-press-escape="phase !== 'pulling'"
    @update:model-value="onDialogVisible"
  >
    <!-- ── 输入态：镜像引用 ──
         确认档照 image:pull 的注册表现状（confirm: 'none'）：点了直接派发，不另造
         一档确认 —— 拉取是「多出来一个东西」的常规写操作，且对话框本身就是一次
         明确的确认（输入 + 开始两步），再叠一档只会训练无脑点确认。 -->
    <div v-if="phase === 'input'" class="pp-input">
      <ElInput
        v-model="refInput"
        placeholder="例如 nginx:latest"
        :disabled="accepting"
        clearable
        @keyup.enter="startPull"
      />
      <!-- 私库凭据选择（4c）：仅 docker:config 用户渲染（无该权限的人连下拉都
           不出现，拉取行为与 4b 逐字一致 —— 匿名拉取）。选项 = 已存凭据的仓库
           地址（凭据键本身，不含任何秘密；密码由服务端受理时注入）；默认
           「不使用」= options 不带 registry 字段，与 4b 的载荷逐字一致。 -->
      <div v-if="canConfig" class="pp-creds">
        <ElSelect
          v-if="registryItems.length > 0"
          v-model="registryChoice"
          class="pp-creds__select"
          placeholder="选择仓库"
          :disabled="accepting"
        >
          <ElOption label="不使用（匿名拉取）" value="" />
          <ElOption
            v-for="r in registryItems"
            :key="r.registry"
            :label="r.registry"
            :value="r.registry"
          />
        </ElSelect>
        <!-- 空态/失败态各给一句就地说明（不悄悄消失：看得见下拉的人该知道为什么没得选）；
             读取中不出声（credsLoaded 才判「确实没有」，免得闪一句错话）。 -->
        <p v-else-if="credsFailed || credsLoaded" class="pp-creds__hint">
          {{
            credsFailed
              ? '已保存凭据读取失败，可先匿名拉取'
              : '还没有已保存的仓库凭据（镜像页底栏可管理）'
          }}
        </p>
      </div>
      <p v-if="refErrorText" class="pp-input__error">{{ refErrorText }}</p>
      <!-- 受理期错误（403/409/400…）：分类句来自 classifyAcceptError（服务端 msg 优先）；
           留在输入态可改可重试，不弹 toast —— 对话框还开着，结论就地给。 -->
      <p v-if="acceptError" class="pp-input__error">{{ acceptError }}</p>
      <p class="pp-input__hint"
        >可填「仓库/名称:标签」的完整形态（如 registry/app:v1）；拉取进度将逐层实时显示。</p
      >
    </div>

    <!-- ── 进度态：汇总 + 逐层列表 ── -->
    <div v-else-if="phase === 'pulling'" class="pp-progress">
      <div class="pp-progress__ref">{{ pullTarget }}</div>
      <!-- 消息行（"Pulling from …" / "Digest: …" / "Status: …"）的最新一条：拉取现在
           走到哪一步的总览提示（全量消息历史属于日志/活动流的职责）。 -->
      <p v-if="feedView.note" class="pp-progress__note">{{ feedView.note }}</p>
      <p v-if="summaryText" class="pp-progress__summary">{{ summaryText }}</p>

      <ul v-if="feedView.layers.length" class="pp-layers">
        <li
          v-for="layer in feedView.layers"
          :key="layer.id"
          class="pp-layer"
          :class="{ 'is-done': layer.done }"
        >
          <span class="pp-layer__id" :title="layer.id">{{ layer.shortId }}</span>
          <span class="pp-layer__status" :title="layer.status">{{ layer.status }}</span>
          <!-- 进度条：有 total 走确定值；无 total（"Pulling fs layer"/"Waiting" 这类
               还没有 progressDetail 的行）走不确定态动画 —— 「在动但不知多少」比
               假装 0% 诚实。层终态后不再画条（画面与 CLI 的「Pull complete」行一致）。 -->
          <ElProgress
            v-if="!layer.done"
            class="pp-layer__bar"
            :percentage="layer.total > 0 ? layerPercent(layer.current, layer.total) : 100"
            :indeterminate="layer.total <= 0"
            :stroke-width="6"
            :show-text="false"
          />
          <span v-if="!layer.done && layer.total > 0" class="pp-layer__bytes">
            {{ formatPullBytes(layer.current) }} / {{ formatPullBytes(layer.total) }}
          </span>
          <ArtSvgIcon v-if="layer.done" icon="ri:check-line" class="pp-layer__check" />
        </li>
      </ul>
      <p v-else class="pp-progress__empty">正在等待进度…</p>

      <!-- 收尾提示：三态（用户取消 / 流接入失败 / 流中途断开）+ eof 后等 result，
           各自说清楚「接下来等什么」，不让对话框看起来像卡死。 -->
      <p v-if="waitingHint" class="pp-progress__hint">{{ waitingHint }}</p>
    </div>

    <!-- ── 终态：结论句（成功 = 镜像名 + 耗时；失败 = 结论句原文）── -->
    <div v-else class="pp-result" :class="resultOk ? 'is-ok' : 'is-fail'">
      <ArtSvgIcon :icon="resultIcon" class="pp-result__icon" />
      <span class="pp-result__text">{{ resultText }}</span>
    </div>

    <template #footer>
      <template v-if="phase === 'input'">
        <ElButton :disabled="accepting" @click="requestClose">关闭</ElButton>
        <ElButton type="primary" :disabled="!canStart" :loading="accepting" @click="startPull">
          开始拉取
        </ElButton>
      </template>
      <!-- 拉取在途只有一条出路：取消（断流 → 服务端终止拉取）。ESC 与遮罩点击此时
           都被关掉（action-confirm 的 loading 纪律）：误触不该杀掉一场进行中的拉取。 -->
      <ElButton v-else-if="phase === 'pulling'" :disabled="canceled" @click="cancelPull">
        {{ canceled ? '已取消' : '取消拉取' }}
      </ElButton>
      <ElButton v-else type="primary" @click="requestClose">关闭</ElButton>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  /**
   * 拉取进度对话框（4b 前端半边）：把 images 页「拉取镜像」的黑盒等待换成逐层实时进度。
   *
   * 双通道并行（后端契约，选型 B）：
   *   - 进度流 GET cmds/:ref/pull —— 指令 **pending 期间即可接入**（会话由 core 受理时
   *     预登记），逐层帧在拉取全程到达；eof 挂在最后一条终态进度行上；
   *   - result 轮询照旧（cmds/:ref）—— 终态结论句（成功/失败/取消）由它收尾，且
   *     eof **先于** result 终态（agent 钉住的时序）。
   * 收尾口径：result 到终态时以它为准（结论句原文）；流 eof 之后 result 迟迟不落时
   * 退回流的终态项 —— 双确认但不互等致死。
   *
   * 断流的语义重量（与日志/stats 流的根本不同）：**客户端断开 = 服务端取消这场拉取**
   * （core 会向 agent 下发 cancel）。取消按钮、关对话框、卸载都走同一条 abort ——
   * 这不是副作用，是本端点给消费端的契约。
   *
   * 生命周期纪律照 container-stats 的 statsSeq：序号 + AbortController，关对话框/
   * 重新打开让在飞的「流 + 轮询」双双失效；主机切换由页面关掉本对话框（images 页
   * onHostSwitch 既有纪律），组件内再把 hostId 在受理时钉死一道（双保险）。
   */
  import { computed, onBeforeUnmount, ref, watch } from 'vue'
  import { ElButton, ElDialog, ElInput, ElOption, ElProgress, ElSelect } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerConfig } from '@/enums/permission'
  import {
    fetchDockerCmdResult,
    fetchDockerRegistries,
    openDockerPullStream,
    sendDockerCmd,
    type DockerRegistryItem
  } from '../api'
  import { classifyAcceptError } from '../composables/useDockerCmds'
  import { pollDelay } from '../utils/cmd'
  import {
    createPullFeed,
    formatPullBytes,
    isValidImageRef,
    layerPercent,
    type PullFeed,
    type PullLayer,
    type PullTerminal
  } from '../utils/pull'

  defineOptions({ name: 'DockerPullProgressDialog' })

  const props = defineProps<{
    modelValue: boolean
    /** 拉取目标主机（受理时钉死：一次指令只属于受理它的那台主机）。 */
    hostId: string
    /** 成功关闭后的重拉（页面的 refresh；双次 —— 立即 + 1.5s 落定，useDockerCmds 纪律）。 */
    refresh?: () => void | Promise<void>
  }>()

  const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

  type Phase = 'input' | 'pulling' | 'result'

  interface FeedView {
    layers: PullLayer[]
    note: string
    downloadedBytes: number
    totalBytes: number
    doneLayers: number
  }

  interface ResultState {
    ok: boolean
    text: string
  }

  const phase = ref<Phase>('input')
  const refInput = ref('')
  const acceptError = ref('')
  const accepting = ref(false)

  // ── 私库凭据（4c） ──

  /** 凭据下拉只对 docker:config 渲染：能管凭据的人才看得见「用哪条」；
      其他人不受影响（拉取照旧匿名，多一个空下拉只会平添困惑）。 */
  const { hasAuth } = useAuth()
  const canConfig = computed(() => hasAuth(PermDockerConfig))
  /** 已存凭据清单（拉列表的时机 = 对话框打开；密码在列表里恒为掩码，用不到）。 */
  const registryItems = ref<DockerRegistryItem[]>([])
  /** 选中的仓库地址（'' = 不使用：载荷与 4b 逐字一致）。 */
  const registryChoice = ref('')
  /** 清单是否已落定（区分「还在读」与「读完了确实没有」—— 后者要给一句说明）。 */
  const credsLoaded = ref(false)
  const credsFailed = ref(false)

  /**
   * 拉凭据清单（打开对话框时，仅 canConfig）。
   *
   * 失败**不挡拉取**：匿名拉取是既有能力，凭据只是增强 —— 下拉退回一句
   * 「读取失败，可先匿名拉取」的说明，而不是把输入态标红。
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
    downloadedBytes: 0,
    totalBytes: 0,
    doneLayers: 0
  })
  const canceled = ref(false)
  const streamEnded = ref(false)
  /** '' = 流还连着；'open' = 接入失败（拉取仍在服务端跑）；'broken' = 连上后断开（断开即取消）。 */
  const streamFailed = ref<'' | 'open' | 'broken'>('')
  const streamFailText = ref('')
  const resultState = ref<ResultState | null>(null)
  /** 进度态头部的目标引用（受理时钉下；模板要显示，故是 ref）。 */
  const pullTarget = ref('')

  /** 生命周期序号：关闭/重开让在飞的「流 + 轮询」双双失效（照 container-stats 的 statsSeq）。 */
  let pullSeq = 0
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

  const IMAGE_REF_ERROR = '镜像引用不合法：以字母或数字开头，仅可包含字母、数字与 / . _ : @ -'

  const trimmedRef = computed(() => refInput.value.trim())

  /** 校验是实时的（非空才显错）：空串不该挨骂，留白首尾在提交时裁掉。 */
  const refErrorText = computed(() => {
    const v = trimmedRef.value
    if (v === '') return ''
    return isValidImageRef(v) ? '' : IMAGE_REF_ERROR
  })

  const canStart = computed(
    () => trimmedRef.value !== '' && refErrorText.value === '' && !accepting.value
  )

  // ── 进度态的派生展示 ──

  const summaryText = computed(() => {
    const f = feedView.value
    if (f.layers.length === 0) return ''
    const parts: string[] = []
    if (f.totalBytes > 0) {
      parts.push(`已下载 ${formatPullBytes(f.downloadedBytes)} / ${formatPullBytes(f.totalBytes)}`)
    }
    parts.push(`${f.doneLayers}/${f.layers.length} 层完成`)
    return parts.join(' · ')
  })

  /** 收尾提示：把「还在等什么」说清楚（三态收口 + eof 后等 result），对话框不该看起来卡死。 */
  const waitingHint = computed(() => {
    if (phase.value !== 'pulling') return ''
    if (canceled.value) return '已取消，正在等待指令收尾…'
    if (streamFailed.value === 'open') {
      return `进度流未能建立（${streamFailText.value}），拉取仍在进行，等待结果…`
    }
    if (streamFailed.value === 'broken') {
      return streamFailText.value
        ? `进度流已断开（${streamFailText.value}），断开即取消拉取，正在等待收尾…`
        : '进度流已断开，断开即取消拉取，正在等待收尾…'
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

  // ── 小工具（文案与错误归类，照 container-stats 的同名函数口径） ──

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

  async function startPull(): Promise<void> {
    const target = trimmedRef.value
    if (!canStart.value) return
    // hostId 在入口钉死（与 useDockerCmds 同理由）：拉取在途时切了主机，轮询跟着
    // 新主机走会拿老 ref 查新记录。切机时页面会关掉本对话框（onHostSwitch），
    // 这里钉死是兜那条缝。
    const hostId = props.hostId
    accepting.value = true
    acceptError.value = ''
    let cmdRef: string
    try {
      const accepted = await sendDockerCmd(hostId, {
        action: 'image:pull',
        target,
        // 选中凭据时带 registry（凭据键，不含秘密 —— 密码由服务端受理时解密注入，
        // 前端任何路径都摸不到）；没选就是字面量 {}（与 4b 的载荷逐字一致，不多
        // 一个空值的 registry 字段 —— 协议对空串与缺省同判非法，宁缺勿滥）。
        options: registryChoice.value ? { registry: registryChoice.value } : {}
      })
      cmdRef = accepted.ref
    } catch (e) {
      // 受理失败留在输入态（输入保留，可改可重试）：403/409/400 的结论句由
      // classifyAcceptError 分类（服务端 msg 优先）。
      acceptError.value = classifyAcceptError(e).message
      return
    } finally {
      accepting.value = false
    }
    pullTarget.value = target
    startedAt = Date.now()
    streamTerminal = null
    phase.value = 'pulling'
    const seq = ++pullSeq
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
      downloadedBytes: feed.downloadedBytes,
      totalBytes: feed.totalBytes,
      doneLayers: feed.doneLayers
    }
  }

  async function runStreamLoop(seq: number, hostId: string, cmdRef: string): Promise<void> {
    const controller = new AbortController()
    streamAbort = controller
    try {
      const res = await openDockerPullStream(hostId, cmdRef, controller.signal)
      if (seq !== pullSeq) return
      if (!res.ok || !res.body) {
        // 接入失败（401/403/404/409…）：这条连接没建立过，**不会**触发服务端取消 ——
        // 拉取仍在跑，降级为「无进度、等结果」，不中止流程。
        streamFailed.value = 'open'
        streamFailText.value = streamOpenConclusion(res.status)
        return
      }
      const feed = createPullFeed()
      const reader = res.body.getReader()
      const dec = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (seq !== pullSeq) return
        if (done) break
        feed.pushRaw(dec.decode(value, { stream: true }))
        syncFeed(feed)
        if (feed.eof) break
      }
      if (seq !== pullSeq) return
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
      if (seq !== pullSeq) return
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
        的长拉取里，流帧就是「还活着」的证据；useDockerCmds 的固定 20 次上限是给
        黑盒等待的，这里流把「活着」的举证接过去了）。 */
    let deadlineArmedAt: number | null = null
    for (;;) {
      if (seq !== pullSeq) return
      let res: Awaited<ReturnType<typeof fetchDockerCmdResult>> | undefined
      try {
        res = await fetchDockerCmdResult(hostId, cmdRef)
      } catch {
        res = undefined // 单次网络失败不当终态：流通道可能还活着，下一轮自愈
      }
      if (seq !== pullSeq) return
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
    if (seq !== pullSeq) return
    endedAt = Date.now()
    // result 已终态：流若还挂着（时序兜底下可能没走完）就断掉 —— 会话已收口，
    // 此时的 cancel 无副作用。
    abortStream()
    if (ok) {
      setResult(true, `已拉取 ${pullTarget.value}（耗时 ${durationText()}）`)
    } else {
      setResult(false, error || '拉取镜像失败')
    }
  }

  /** result 等不到（记录异常/网络断）的退路：结论退回流的终态项；取消路径上服务端
      不发终态项，按「已取消」措辞；两头都没有时只说「未能确认」—— 不编造结论。 */
  function finalizeFallback(seq: number): void {
    if (seq !== pullSeq) return
    endedAt = Date.now()
    if (canceled.value) {
      setResult(false, '拉取已取消')
    } else if (streamTerminal) {
      if (streamTerminal.ok) {
        setResult(true, `已拉取 ${pullTarget.value}（耗时 ${durationText()}）`)
      } else {
        setResult(false, streamTerminal.error || '拉取镜像失败')
      }
    } else {
      setResult(false, '拉取结果未能确认，请稍后刷新镜像列表')
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

  function cancelPull(): void {
    if (phase.value !== 'pulling' || canceled.value) return
    canceled.value = true
    // 断流即取消（端点契约：客户端断开 → 服务端向 agent 下发 cancel 终止拉取）。
    // result 轮询继续：结论句「拉取已取消」由服务端落进 result，页面前端不代答。
    abortStream()
  }

  /** 断流（幂等：没有在飞的流就不扰动）。 */
  function abortStream(): void {
    streamAbort?.abort()
    streamAbort = null
  }

  /** 收口：序号让在飞的流与轮询失效 + 断流（= 服务端取消拉取）。幂等。 */
  function cleanup(): void {
    pullSeq++
    abortStream()
  }

  /** 关对话框的完整收口（幂等）：清理 + 成功后的双次重拉。 */
  function cleanupAndFinish(): void {
    cleanup()
    maybeDoubleRefresh()
  }

  /** 成功关闭后的双次重拉（useDockerCmds 的落定纪律，1.5s）：agent 落定时已 push
      一帧，但「立即重拉」与那一帧会擦肩（缓存要等新帧到达才换）—— 补一次落定
      重拉覆盖两种到达次序。重拉失败不影响结论（陈旧/离线由页面头部标注）。 */
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

  // 父组件置 false（主机切换 onHostSwitch / 页面级收口）走 watch；置 true（打开）
  // 时整表重置 —— 上一次拉取的任何残留（层表/结论/刷新标记/凭据选择）不进新一场；
  // 凭据清单随打开重拉（管理对话框可能在两次拉取之间改过它）。immediate：
  // 「带着打开态挂载」（测试/将来别的入口直接传 true）与「先挂载再打开」走同一条路。
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

  function resetAll(): void {
    cleanup()
    phase.value = 'input'
    refInput.value = ''
    acceptError.value = ''
    accepting.value = false
    registryChoice.value = ''
    feedView.value = { layers: [], note: '', downloadedBytes: 0, totalBytes: 0, doneLayers: 0 }
    canceled.value = false
    streamEnded.value = false
    streamFailed.value = ''
    streamFailText.value = ''
    resultState.value = null
    pullTarget.value = ''
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

  // 输入态：错误句与提示句都是对话框内的就地结论（对话框开着，结论不放 toast）。
  .pp-input {
    padding-bottom: 4px;

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

  // 凭据选择：与镜像引用同一列宽（对话框是单列表单，下拉不另起一行标签）。
  .pp-creds {
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
  .pp-progress {
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
  .pp-layers {
    max-height: 264px;
    margin: 10px 0 0;
    padding: 0;
    list-style: none;
    overflow-y: auto;
  }

  .pp-layer {
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

  // 终态：结论句是这一屏唯一的信息 —— 图标定语义色，文字用主文字色（长结论句
  // 读起来不该比进度行更费眼）。
  .pp-result {
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
    .pp-layer__status {
      width: 7em;
    }

    .pp-layer__bytes {
      display: none;
    }
  }
</style>
