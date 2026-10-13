<template>
  <!-- 单根（single-root 守卫扫描全模块的 .vue）：ElDialog 是唯一根（与
       build/pull/push 进度对话框同一挂法，原地渲染不搬 body）。
       **非模态**（modal=false）：载入是「长耗时写操作」，进度只是「观看」——
       在途时用户可以拖开本窗（draggable）或直接关掉（关掉 = 收起观看，指令在
       服务端照常跑完）。与其余三个进度对话框同一套机制（.docker-progress-nonblocking）
       是为了不挡住页面，不是可选项。 -->
  <ElDialog
    :model-value="modelValue"
    title="载入镜像"
    width="560px"
    :modal="false"
    modal-class="docker-progress-nonblocking"
    :close-on-click-modal="false"
    :lock-scroll="false"
    draggable
    @update:model-value="onDialogVisible"
  >
    <!-- ── 输入态：来源二选一（主机本地文件 / 从浏览器上传）── -->
    <div v-if="phase === 'input'" class="il-input">
      <div class="il-field">
        <div class="il-field__head">
          <label class="il-field__label">镜像文件来源</label>
          <!-- 两种来源（与构建对话框的上下文形态同型）：主机已有文件的手填路径
               （老路径保留）vs 本机文件直传（② 端点，进度可见）。 -->
          <ElRadioGroup v-model="mode" size="small">
            <ElRadioButton value="local">主机本地文件</ElRadioButton>
            <ElRadioButton value="upload">从浏览器上传</ElRadioButton>
          </ElRadioGroup>
        </div>

        <!-- 本机形态：文件已在该主机 agent 下载目录，只填文件名（只拦空值，
             格式由服务端兜 —— 与 image:load 的注册表口径一致）。 -->
        <template v-if="mode === 'local'">
          <ElInput
            v-model="filenameInput"
            :placeholder="inputSpec.placeholder"
            clearable
            @keyup.enter="startLoad"
          />
          <p class="il-field__hint">{{ inputSpec.hint }}</p>
        </template>

        <!-- 上传形态：选文件 → 预检（后缀/8GB 档，选中的瞬间就拒，不白传一场）
             → 提交后流式上传（进度条）→ 拿到服务端产物名后自动派发载入。 -->
        <template v-else>
          <!-- 藏起来的原生 file input（与构建对话框同因）：ElUpload 自带一套
               上传语义，与「一个文件、一条流、自管进度」的诉求不合。 -->
          <input
            ref="fileInputEl"
            type="file"
            class="il-file-hidden"
            :accept="LOAD_ACCEPT"
            @change="onFilePicked"
          />
          <div class="il-upload-entry">
            <ElButton size="small" @click="pickFile">
              {{ uploadFile ? '重新选择' : '选择文件…' }}
            </ElButton>
            <span class="il-upload-entry__name">{{ uploadFile?.name ?? '' }}</span>
          </div>
          <p v-if="uploadError" class="il-field__error">{{ uploadError }}</p>
          <p v-else class="il-field__hint"
            >选择本机的 tar 压缩包（≤{{ MAX_LOAD_GB }}GB），提交后先上传再载入该主机。</p
          >
        </template>

        <!-- 提交钮禁用的**可见理由**（要求：不藏在 hover title 里）——空输入时
             就地一行说明为什么还不能提交。 -->
        <p v-if="blockReason" class="il-field__reason">{{ blockReason }}</p>
        <!-- 受理失败的就地结论句（403/409/400…，服务端 msg 优先）：留在输入态
             可改可重试，不上 toast——对话框还开着，结论就地给。 -->
        <p v-if="acceptError" class="il-field__error">{{ acceptError }}</p>
      </div>
    </div>

    <!-- ── 上传态：文件名 + 真实传输进度（XHR 的 upload 进度经 http 层透传）── -->
    <div v-else-if="phase === 'uploading'" class="il-uploading">
      <p class="il-uploading__file">{{ uploadFile?.name }}</p>
      <ElProgress :percentage="uploadPercent" :stroke-width="8" class="il-uploading__bar" />
    </div>

    <!-- ── 载入进行中：状态（排队/执行中）+ 已用时秒表 ──
         docker load **没有逐字节进度**（不像 pull 逐层报），故这里**如实**只给
         真实可得的两件事：指令状态与已用时；不造假的百分比/进度条。 -->
    <div v-else-if="phase === 'loading'" class="il-loading">
      <p class="il-loading__file">{{ loadTarget }}</p>
      <div class="il-loading__status">
        <ArtSvgIcon icon="ri:loader-4-line" class="il-loading__spin" />
        <span>{{ loadStatusText }}</span>
      </div>
      <p class="il-loading__elapsed">已用时 {{ elapsedText }}</p>
    </div>

    <!-- ── 终态：结论句（成功 = 文件名 + 耗时；失败 = 原因原文）── -->
    <div v-else class="il-result" :class="resultOk ? 'is-ok' : 'is-fail'">
      <ArtSvgIcon :icon="resultIcon" class="il-result__icon" />
      <span class="il-result__text">{{ resultText }}</span>
    </div>

    <template #footer>
      <template v-if="phase === 'input'">
        <ElButton @click="requestClose">关闭</ElButton>
        <ElButton type="primary" :disabled="!canStart" :loading="accepting" @click="startLoad">
          载入镜像
        </ElButton>
      </template>
      <!-- 上传/载入在途只有一条出口：关闭 = 收起这场观看（服务端指令照常跑完）；
           上传在途关闭会中止这次上传（还没派发载入）。 -->
      <ElButton v-else-if="phase !== 'result'" @click="requestClose">关闭</ElButton>
      <ElButton v-else type="primary" @click="requestClose">关闭</ElButton>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  /**
   * 载入镜像对话框：镜像页工具条「载入镜像…」的业务弹窗（两段式的第二步）。
   *
   * 取代此前「DockerActionConfirm 输入档收集文件名 → runWrite → toast」的黑盒等待：
   *   - 来源二选一：主机本地文件（填文件名，文件须已在 agent 下载目录）或
   *     从浏览器上传（本机选文件 → 上传 → 自动载入）；
   *   - **两种来源都有实时反馈**：输入态收在对话框里，提交后对话框**不关**，
   *     就地给出「载入进行中」（状态 + 已用时秒表）与终态结论句。
   *
   * 上传通道复用构建上下文的 ② 端点（POST build-context **带 kind=load**，落点就是
   * transferDir —— 正是 image:load 读取的目录，服务端按 kind=load 选 8GB 档尺寸闸）：
   * 选文件 → 预检（后缀/8GB 档）→ 流式上传（XHR 的
   * 上传进度，与构建对话框同款）→ 拿服务端产物名自动派发 image:load，用户不必
   * 再手填一次文件名。
   *
   * **无载入进度条**（协议硬事实）：`docker load` 不提供逐字节进度（不像 pull 逐层
   * 报），故载入段**不造**假的百分比/进度条，只如实给状态与已用时；-状态来源就是
   * 既有轮询（GET cmds/:ref，与 useDockerCmds/构建对话框同一口径），不新造协议。
   *
   * 生命周期纪律照 build-progress-dialog：序号 + AbortController，关对话框/重开让
   * 在飞的「上传 + 轮询」双双失效，hostId 在受理时钉死一道（一次指令只属于受理它
   * 的那台主机）。
   */
  import { computed, onBeforeUnmount, ref, watch } from 'vue'
  import {
    ElButton,
    ElDialog,
    ElInput,
    ElProgress,
    ElRadioButton,
    ElRadioGroup
  } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import { fetchDockerCmdResult, sendDockerCmd, uploadDockerBuildContext } from '../api'
  import { classifyAcceptError } from '../composables/useDockerCmds'
  import { pollDelay } from '../utils/cmd'
  import { lookupDockerAction } from '../utils/actions'

  defineOptions({ name: 'DockerImageLoadDialog' })

  const props = defineProps<{
    modelValue: boolean
    /** 目标主机（受理时钉死：一次指令只属于受理它的那台主机）。 */
    hostId: string
    /** 成功关闭后的重拉（页面的 refresh；双次——立即 + 1.5s 落定，新镜像要进列表）。 */
    refresh?: () => void | Promise<void>
  }>()

  const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

  type Phase = 'input' | 'uploading' | 'loading' | 'result'
  type Mode = 'local' | 'upload'

  interface ResultState {
    ok: boolean
    text: string
  }

  /**
   * image:load 的注册表条目仍是唯一真相（label/placeholder/hint）——本对话框
   * 消费它而不是另抄一份文案（抄一份只会漂移）。输入档的校验口径也照旧：
   * **只拦空值**，文件名格式由服务端兜。
   */
  const inputSpec = lookupDockerAction('image:load')?.input ?? {
    label: '文件名',
    placeholder: '例如 镜像名.tar',
    hint: ''
  }

  /** 上传文件选择器的后缀白名单（载入读侧 / 上传侧都收 tar / tar.gz / tgz）。 */
  const LOAD_ACCEPT = '.tar,.tar.gz,.tgz'
  const LOAD_ARCHIVE_SUFFIX_RE = /\.(tar|tar\.gz|tgz)$/i
  /**
   * image:load 上传的字节上限（8GB）—— **与 `uni_protocol.MaxDockerImageLoadBytes`
   * 同源，服务端才是权威**：本地这把尺只是「选文件的瞬间就拒」的前移便利（上传以
   * 分钟计，别让用户白传一场），服务端因超限打回的结论照原样显示，不被本地猜测覆盖。
   */
  const MAX_LOAD_BYTES = 8 * 1024 * 1024 * 1024
  /** 展示数（GB，由上面那个数派生，不另写死）。 */
  const MAX_LOAD_GB = MAX_LOAD_BYTES / 1024 ** 3

  /** 轮询兜底上限：载入的等待可到分钟级（大 tar），比轻量指令的 20 次宽松。 */
  const MAX_POLL_ATTEMPTS = 60

  const phase = ref<Phase>('input')
  const mode = ref<Mode>('local')
  const filenameInput = ref('')
  const acceptError = ref('')
  const accepting = ref(false)

  // ── 上传段 ──
  const uploadFile = ref<File | null>(null)
  /** 预检/上传失败的就地结论句（对话框还开着，结论就地给）。 */
  const uploadError = ref('')
  const uploadPercent = ref(0)
  const fileInputEl = ref<HTMLInputElement | null>(null)
  /** 上传段生命周期序号：关对话框/重开让在飞的「上传」失效。 */
  let uploadAbort: AbortController | null = null

  // ── 载入段 ──
  const loadStatus = ref<'queued' | 'running'>('queued')
  /** 本次载入用的文件名（受理时钉下；模板要显示，故是 ref）。 */
  const loadTarget = ref('')
  const resultState = ref<ResultState | null>(null)

  // ── 秒表 ──
  const elapsedSec = ref(0)
  let tickTimer: ReturnType<typeof setInterval> | null = null
  let startedAt = 0
  let endedAt = 0

  /** 生命周期序号：关闭/重开让在飞的「上传 + 轮询」双双失效。 */
  let seq = 0
  /** 双次重拉只补一次的闸（关闭路径可能被走两遍：页脚按钮 + prop 回写）。 */
  let refreshedAfterSuccess = false
  /** 「落定重拉」的定时器句柄集（卸载时全部撤销，照 build-progress-dialog）。 */
  const settleTimers = new Set<ReturnType<typeof setTimeout>>()

  const trimmedFilename = computed(() => filenameInput.value.trim())

  const busy = computed(
    () => accepting.value || phase.value === 'uploading' || phase.value === 'loading'
  )

  const canStart = computed(() => {
    if (busy.value) return false
    if (mode.value === 'local') return trimmedFilename.value !== ''
    return uploadFile.value !== null && uploadError.value === ''
  })

  /** 提交钮禁用的可见理由（空输入时的硬事实，不藏进 title）。 */
  const blockReason = computed(() => {
    if (busy.value) return ''
    if (mode.value === 'local') return trimmedFilename.value === '' ? '请填写文件名' : ''
    return uploadFile.value === null ? '请选择要上传的文件' : ''
  })

  // ── 来源形态切换：两份来源互不残留 ──

  watch(mode, (m) => {
    // 切进上传：手填的文件名（本地来源的事实）清零，免成「看不见却还在」的暗值。
    // 切回本地：已选文件与上传段状态一并清零。两种来源各自独立，不互相追尾。
    if (m === 'upload') {
      filenameInput.value = ''
      acceptError.value = ''
    } else {
      uploadFile.value = null
      uploadError.value = ''
      uploadPercent.value = 0
    }
  })

  function pickFile(): void {
    fileInputEl.value?.click()
  }

  /**
   * 选定文件：预检（后缀 / 8GB 档，毫秒级）——失败结论就地给，重选即重试。
   * 预检是服务端同一把尺的前移：别让用户白传上 GB 才收到 400（上传以分钟计）。
   */
  function onFilePicked(e: Event): void {
    const input = e.target as HTMLInputElement
    const file = input.files?.[0]
    // value 先清：否则第二次选同一个文件不触发 change。
    input.value = ''
    if (!file || phase.value !== 'input' || busy.value) return
    uploadFile.value = file
    uploadError.value = ''
    uploadPercent.value = 0
    if (!LOAD_ARCHIVE_SUFFIX_RE.test(file.name.trim())) {
      uploadError.value = '只支持 tar / tar.gz / tgz 文件'
      return
    }
    if (file.size > MAX_LOAD_BYTES) {
      uploadError.value = `文件超过 ${MAX_LOAD_GB}GB 上限`
      return
    }
  }

  // ── 提交：本地来源直接派发；上传来源先传后载 ──

  async function startLoad(): Promise<void> {
    if (!canStart.value) return
    // hostId 在入口钉死（切机时页面会关掉本对话框，这里钉死兜那条缝）。
    const hostId = props.hostId
    acceptError.value = ''
    if (mode.value === 'local') {
      await dispatchLoad(hostId, trimmedFilename.value)
      return
    }
    await uploadThenLoad(hostId)
  }

  /** 上传来源：先流式上传拿产物名，成功后**自动**以它派发载入（用户不必再填）。 */
  async function uploadThenLoad(hostId: string): Promise<void> {
    const file = uploadFile.value
    if (!file) return
    const mySeq = seq
    phase.value = 'uploading'
    uploadPercent.value = 0
    const controller = new AbortController()
    uploadAbort = controller
    let filename = ''
    try {
      const resp = await uploadDockerBuildContext(
        hostId,
        file,
        (percent) => {
          if (mySeq === seq) uploadPercent.value = percent
        },
        controller.signal,
        // 用途 = load：服务端据此选 8GB 档尺寸闸（构建上下文那条是 512MB，不共用）。
        'load'
      )
      if (mySeq !== seq) return
      filename = resp.filename
    } catch (e) {
      // 收口路径上的 abort 不是故障（请求是被自己掐断的）。
      if (mySeq !== seq) return
      // 上传失败回输入态：结论就地给，可重选可重试（服务端 msg 优先）。
      phase.value = 'input'
      uploadError.value = classifyAcceptError(e).message
      return
    } finally {
      if (uploadAbort === controller) uploadAbort = null
    }
    if (mySeq !== seq) return
    await dispatchLoad(hostId, filename)
  }

  /**
   * 派发 image:load 并进入「载入进行中」。
   *
   * 载荷形态与既有口径**逐字一致**（runWrite('image:load', undefined, { filename })）：
   * 无 target、无 confirm，主参数是文件名。受理失败留在输入态（可改可重试），
   * 结论句由 classifyAcceptError 分类（服务端 msg 优先）。
   */
  async function dispatchLoad(hostId: string, filename: string): Promise<void> {
    const mySeq = seq
    loadTarget.value = filename
    loadStatus.value = 'queued'
    phase.value = 'loading'
    startTicking()
    accepting.value = true
    let cmdRef = ''
    try {
      const accepted = await sendDockerCmd(hostId, {
        action: 'image:load',
        target: undefined,
        options: { filename }
      })
      cmdRef = accepted.ref
    } catch (e) {
      if (mySeq !== seq) return
      stopTicking()
      phase.value = 'input'
      acceptError.value = classifyAcceptError(e).message
      return
    } finally {
      accepting.value = false
    }
    if (mySeq !== seq) return
    loadStatus.value = 'running'
    void runPollLoop(mySeq, hostId, cmdRef)
  }

  // ── 通道：result 轮询（终态收尾，既有口径） ──

  const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

  async function runPollLoop(mySeq: number, hostId: string, cmdRef: string): Promise<void> {
    let attempt = 0
    for (;;) {
      if (mySeq !== seq) return
      let res: Awaited<ReturnType<typeof fetchDockerCmdResult>> | undefined
      try {
        res = await fetchDockerCmdResult(hostId, cmdRef)
      } catch {
        res = undefined // 单次网络失败不当终态：下一轮自愈
      }
      if (mySeq !== seq) return
      if (res && res.status !== 'pending') {
        finalize(mySeq, res.status === 'succeeded', res.error ?? '')
        return
      }
      attempt++
      if (attempt > MAX_POLL_ATTEMPTS) {
        finalizeUnconfirmed(mySeq)
        return
      }
      await sleep(pollDelay(attempt - 1))
    }
  }

  /** result 终态收尾（权威通道）：成功给文件名 + 耗时，失败给原因原文。 */
  function finalize(mySeq: number, ok: boolean, error: string): void {
    if (mySeq !== seq) return
    stopTicking()
    if (ok) {
      setResult(true, `已载入 ${loadTarget.value}（耗时 ${durationText()}）`)
    } else {
      setResult(false, error || '载入镜像失败')
    }
  }

  /** result 等不到（记录异常/网络断）的退路：不代答结果，让用户稍后刷新。 */
  function finalizeUnconfirmed(mySeq: number): void {
    if (mySeq !== seq) return
    stopTicking()
    setResult(false, '载入结果未能确认，请稍后刷新镜像列表')
  }

  function setResult(ok: boolean, text: string): void {
    resultState.value = { ok, text }
    phase.value = 'result'
  }

  // ── 秒表（载入段的「已用时」，每 1 秒走一格） ──

  function startTicking(): void {
    endedAt = 0
    startedAt = Date.now()
    elapsedSec.value = 0
    tickTimer = setInterval(() => {
      elapsedSec.value = Math.max(0, Math.floor((Date.now() - startedAt) / 1000))
    }, 1000)
  }

  function stopTicking(): void {
    if (tickTimer) {
      clearInterval(tickTimer)
      tickTimer = null
    }
    if (startedAt > 0) {
      endedAt = Date.now()
      elapsedSec.value = Math.max(0, Math.round((endedAt - startedAt) / 1000))
    }
  }

  const elapsedText = computed(() => formatByUnit('s', elapsedSec.value))
  const loadStatusText = computed(() => (loadStatus.value === 'queued' ? '排队中' : '执行中'))

  function durationText(): string {
    const sec = Math.max(0, Math.round((endedAt - startedAt) / 1000))
    return formatByUnit('s', sec)
  }

  // ── 终态的派生展示 ──

  const resultOk = computed(() => resultState.value?.ok ?? false)
  const resultText = computed(() => resultState.value?.text ?? '')
  const resultIcon = computed(() =>
    resultOk.value ? 'ri:checkbox-circle-line' : 'ri:close-circle-line'
  )

  // ── 收口 / 关闭 / 重开 ──

  function abortUpload(): void {
    uploadAbort?.abort()
    uploadAbort = null
  }

  /** 收口：序号让在飞的上传与轮询失效 + 停表 + 断开在途上传。幂等。 */
  function cleanup(): void {
    seq++
    stopTicking()
    abortUpload()
  }

  /** 关对话框的完整收口（幂等）：清理 + 成功后的双次重拉。 */
  function cleanupAndFinish(): void {
    cleanup()
    maybeDoubleRefresh()
  }

  /** 成功关闭后的双次重拉（载入成功会新增一枚镜像，列表必须重拉才看得到）。 */
  function maybeDoubleRefresh(): void {
    if (!resultState.value?.ok || refreshedAfterSuccess) return
    refreshedAfterSuccess = true
    safeRefresh()
    const t = setTimeout(() => {
      settleTimers.delete(t)
      safeRefresh()
    }, 1500)
    settleTimers.add(t)
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

  /** ElDialog 自己的关闭路径（X 按钮）：除转发给父组件外当场收口。 */
  function onDialogVisible(v: boolean): void {
    if (!v) cleanupAndFinish()
    emit('update:modelValue', v)
  }

  // 父组件置 true（打开）时整表重置；置 false 走收口。immediate 让「带着打开态
  // 挂载」与「先挂载再打开」走同一条路。关闭一律只收口观看：指令在服务端照常跑完。
  watch(
    () => props.modelValue,
    (v, old) => {
      if (v) {
        resetAll()
        return
      }
      if (old) cleanupAndFinish()
    },
    { immediate: true }
  )

  function resetAll(): void {
    cleanup()
    phase.value = 'input'
    mode.value = 'local'
    filenameInput.value = ''
    acceptError.value = ''
    accepting.value = false
    uploadFile.value = null
    uploadError.value = ''
    uploadPercent.value = 0
    loadStatus.value = 'queued'
    loadTarget.value = ''
    resultState.value = null
    elapsedSec.value = 0
    refreshedAfterSuccess = false
  }

  // 卸载只收口不重拉（页面没了，重拉的落点不存在），并撤销还没到点的落定重拉。
  onBeforeUnmount(() => {
    cleanup()
    for (const t of settleTimers) clearTimeout(t)
    settleTimers.clear()
  })
</script>

<style lang="scss" scoped>
  @use '../views/overview-tokens' as t;

  // 「关闭」等默认档按钮的主色文字对比度 AA（病灶与处方见 overview-tokens）。
  @include t.primary-text-aa;

  // 主按钮禁用态视觉档（与 pull/build/push 对话框同款收口）：EP 对实心 primary 的
  // 禁用态落 --el-color-primary-light-5，读起来像「第二种主按钮」；归到中性禁用槽。
  :deep(.el-dialog__footer .el-button--primary.is-disabled) {
    --el-button-disabled-bg-color: var(--el-fill-color-light);
    --el-button-disabled-border-color: var(--el-border-color-light);
    --el-button-disabled-text-color: var(--el-text-color-placeholder);
  }

  // 非模态的指针穿透机制规则已收敛到 assets/styles/core/el-ui.scss（全局唯一一份）。

  .il-input {
    padding-bottom: 4px;
  }

  .il-field {
    &__head {
      display: flex;
      flex-wrap: wrap;
      gap: 4px 12px;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 8px;
    }

    &__label {
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    &__error,
    &__hint,
    &__reason {
      margin: 6px 0 0;
      font-size: 12px;
      line-height: 1.6;
    }

    &__error {
      // 文字对比度 AA：走 token（白底 3.27 不够），与其它对话框同款。
      color: var(--aa-danger-text);
    }

    &__hint {
      color: var(--el-text-color-secondary);
    }

    // 提交钮禁用的可见理由：次级但**可见**（不藏 title），故用正文色而非占位色。
    &__reason {
      color: var(--el-text-color-regular);
    }
  }

  // 原生文件选择器常驻 DOM（要它可被触发/断言），视觉上完全让位给自管按钮。
  .il-file-hidden {
    display: none;
  }

  // 上传入口：按钮 + 已选文件名（文件名是「选了什么」的回执，弱化展示）。
  .il-upload-entry {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;

    &__name {
      font-size: 12px;
      color: var(--el-text-color-secondary);
      word-break: break-all;
    }
  }

  // 上传在途：文件名（正文色）+ 进度条（传输进度是这段唯一的事实）。
  .il-uploading {
    &__file {
      margin: 0;
      font-size: 13px;
      color: var(--el-text-color-regular);
      word-break: break-all;
    }

    &__bar {
      margin-top: 8px;
      margin-bottom: 2px;
    }
  }

  // 载入进行中：文件名（主信息）+ 状态行（转圈 + 排队中/执行中）+ 已用时。
  .il-loading {
    padding: 4px 0 8px;

    &__file {
      margin: 0;
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
      word-break: break-all;
    }

    &__status {
      display: flex;
      gap: 6px;
      align-items: center;
      margin-top: 8px;
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    &__spin {
      flex: none;
      font-size: 15px;
      color: var(--el-color-primary);
      animation: il-rotate 1.2s linear infinite;
    }

    &__elapsed {
      margin: 6px 0 0;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }

  @keyframes il-rotate {
    to {
      transform: rotate(360deg);
    }
  }

  // 终态：结论句是这一屏唯一的信息（与其余进度对话框同款形态）。
  .il-result {
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
</style>
