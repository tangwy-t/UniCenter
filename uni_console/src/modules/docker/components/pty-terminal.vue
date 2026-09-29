<template>
  <div class="pty-terminal">
    <!-- xterm 的宿主：尺寸由 CSS 给（fit addon 按这个盒子算行列）。 -->
    <div ref="screenRef" class="pty-terminal__screen"></div>
    <!-- 连接/失败期的浮层：只给一句结论（失败时带「重试」）。 -->
    <div v-if="overlayText" class="pty-terminal__overlay">
      <p class="pty-terminal__message">{{ overlayText }}</p>
      <ElButton v-if="retryable" size="small" @click="retry">重试</ElButton>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 容器终端（三期）：xterm.js + WebSocket 流。
   *
   * 建立次序（与 core/agent 的冻结契约一一对应）：
   *   1) 发 `container:exec`（options.command 缺省 = 容器里的 /bin/sh）；
   *   2) 轮询 `cmds/:ref` 到 succeeded，取 `streamTicket`（TTL 30s、单次使用）；
   *   3) 用票升级 WebSocket：`hosts/:id/stream/exec?ticket=...`。
   * WS 建立失败时票已被消费 —— 重新轮询 `cmds/:ref` 拿新票再试（会话本身还在，
   * 服务端在升级失败时只释放接入占用、不取消会话）。
   *
   * 组件生命周期 = 会话生命周期：挂载（首次切到「终端」Tab）建会话，卸载（切走/离开
   * 页面）发 cancel 并 dispose —— 不为看不见的终端留一个 shell。
   */
  import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
  import { ElButton } from 'element-plus'
  import { FitAddon } from '@xterm/addon-fit'
  import { Terminal } from '@xterm/xterm'
  import '@xterm/xterm/css/xterm.css'
  import { dockerExecStreamUrl, fetchDockerCmdResult, sendDockerCmd } from '../api'
  import { pollDelay } from '../utils/cmd'
  import { createFrameTextDecoder } from '../utils/stream'

  const props = defineProps<{ hostId: string; containerId: string }>()

  /** 连接生命周期：idle(未开始) → connecting → ready → ended/closed/failed。 */
  type PtyState = 'idle' | 'connecting' | 'ready' | 'ended' | 'closed' | 'failed'

  const state = ref<PtyState>('idle')
  const errorText = ref('')
  const screenRef = ref<HTMLElement | null>(null)

  let term: Terminal | null = null
  let fitAddon: FitAddon | null = null
  let socket: WebSocket | null = null
  let socketTimer: ReturnType<typeof setTimeout> | null = null
  /** 正在握手的 socket 的 reject：卸载时用它把悬挂的 Promise 收掉。 */
  let pendingReject: ((e: Error) => void) | null = null
  let resizeRaf = 0
  let disposed = false
  /** 当前会话的指令号（重连时用它轮询最新票据；换会话后更新）。 */
  let sessionRef = ''
  /** 帧文本解码器：跨帧的 UTF-8 序列与 PTY 控制序列在它里面续接。 */
  const decoder = createFrameTextDecoder()

  const overlayText = computed(() => {
    if (state.value === 'connecting' || state.value === 'idle') return '正在连接容器终端…'
    if (state.value === 'failed') return errorText.value || '无法连接容器终端，请稍后重试'
    if (state.value === 'closed') return '终端连接已断开。'
    return ''
  })
  const retryable = computed(() => state.value === 'failed' || state.value === 'closed')

  onMounted(() => {
    const el = screenRef.value
    if (!el) return
    term = new Terminal({
      cursorBlink: true,
      fontSize: 12,
      fontFamily: "var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace)",
      scrollback: 2000,
      theme: { background: '#101418', foreground: '#e6e6e6', cursor: '#e6e6e6' }
    })
    fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(el)
    term.onData((data) => sendInput(data))
    refit()
    window.addEventListener('resize', onWindowResize)
    void connect()
  })

  onBeforeUnmount(() => {
    disposed = true
    window.removeEventListener('resize', onWindowResize)
    if (resizeRaf) cancelAnimationFrame(resizeRaf)
    if (socketTimer) clearTimeout(socketTimer)
    if (pendingReject) {
      const reject = pendingReject
      pendingReject = null
      reject(new Error('已离开终端'))
    }
    // 显式 cancel：让 agent 立刻终止进程并释放会话槽位（断开连接也会触发取消，显式更直接）。
    if (socket && socket.readyState === WebSocket.OPEN) {
      try {
        socket.send(JSON.stringify({ type: 'cancel' }))
      } catch {
        /* 连接已坏：close 兜底 */
      }
    }
    closeSocket()
    term?.dispose()
    term = null
    fitAddon = null
  })

  const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

  /** 建立会话 + 升级 WS；失败按退避重试（优先复用仍活着的会话，用新票重接）。 */
  async function connect(): Promise<void> {
    state.value = 'connecting'
    errorText.value = ''
    const maxAttempts = 3
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      if (disposed) return
      try {
        const ticket = await ensureSession()
        await openSocket(ticket)
        return
      } catch (e) {
        closeSocket()
        if (disposed) return
        if (attempt === maxAttempts - 1) {
          state.value = 'failed'
          errorText.value =
            e instanceof Error && e.message ? e.message : '无法连接容器终端，请稍后重试'
          return
        }
        await sleep(400 * (attempt + 1))
      }
    }
  }

  /**
   * 保证有一条可接入的会话并取一张**最新**票据。
   *
   * 会话还活着（上次只是 WS 升级失败）时只需重新轮询拿新票 —— 不必重建进程；
   * 轮询不到（记录过期）就发一条新的 `container:exec`。
   */
  async function ensureSession(): Promise<string> {
    if (sessionRef !== '') {
      try {
        const res = await fetchDockerCmdResult(props.hostId, sessionRef)
        if (res.streamTicket) return res.streamTicket
      } catch {
        /* 记录已过期：落回重建 */
      }
      sessionRef = ''
    }
    const accepted = await sendDockerCmd(props.hostId, {
      action: 'container:exec',
      target: props.containerId
    })
    let attempt = 0
    for (;;) {
      if (disposed) throw new Error('已离开终端')
      const res = await fetchDockerCmdResult(props.hostId, accepted.ref)
      if (res.status !== 'pending') {
        if (res.status !== 'succeeded') {
          throw new Error(res.error || '无法建立终端会话，请稍后重试')
        }
        if (!res.streamTicket) throw new Error('无法获取终端连接信息，请稍后重试')
        sessionRef = accepted.ref
        return res.streamTicket
      }
      await sleep(pollDelay(attempt++))
      if (attempt > 20) throw new Error('建立终端会话超时，请重试')
    }
  }

  /** 用一次性票升级 WS；resolve 于**打开**，失败（含超时）reject。 */
  function openSocket(ticket: string): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      if (disposed) {
        reject(new Error('已离开终端'))
        return
      }
      const ws = new WebSocket(dockerExecStreamUrl(props.hostId, ticket))
      socket = ws
      pendingReject = (e: Error) => reject(e)
      let opened = false
      let settled = false
      const clearTimer = () => {
        if (socketTimer) {
          clearTimeout(socketTimer)
          socketTimer = null
        }
      }
      const fail = (message: string) => {
        if (settled) return
        settled = true
        clearTimer()
        pendingReject = null
        reject(new Error(message))
      }
      socketTimer = setTimeout(() => {
        try {
          ws.close()
        } catch {
          /* 已关则忽略 */
        }
        fail('连接容器终端超时，请重试')
      }, 10000)
      ws.onopen = () => {
        if (settled || disposed) {
          ws.close()
          return
        }
        opened = true
        settled = true
        clearTimer()
        pendingReject = null
        state.value = 'ready'
        // 打开时按 fit 后的尺寸发一次 resize：容器里的 shell 要立刻知道窗口多大。
        refit()
        term?.focus()
        resolve()
      }
      ws.onmessage = (ev) => onSocketMessage(ev)
      ws.onerror = () => fail('无法连接容器终端，请稍后重试')
      ws.onclose = () => {
        clearTimer()
        socket = null
        if (disposed) return
        if (!opened) {
          fail('无法连接容器终端，请稍后重试')
          return
        }
        // 收到 eof 后的服务端关闭是**正常收尾**，不覆盖成「已断开」。
        if (state.value === 'ended') return
        // 没收到 eof 的断开：会话已被服务端收尾，给出结论与「重试」。
        state.value = 'closed'
      }
    })
  }

  /** 关掉当前 WS 并摘掉回调（避免旧连接的事件落在新会话上）。 */
  function closeSocket(): void {
    const ws = socket
    socket = null
    if (!ws) return
    ws.onopen = ws.onmessage = ws.onerror = ws.onclose = null
    try {
      ws.close()
    } catch {
      /* 已关则忽略 */
    }
  }

  function onSocketMessage(ev: MessageEvent): void {
    if (disposed || !term) return
    let msg: { type?: string; data?: unknown }
    try {
      msg = JSON.parse(String(ev.data)) as { type?: string; data?: unknown }
    } catch {
      return // 形状意外的消息：跳过
    }
    if (msg.type === 'data' && typeof msg.data === 'string') {
      term.write(decoder.push(msg.data))
      return
    }
    if (msg.type === 'eof') {
      term.write(decoder.flush())
      writeConclusion('会话已结束。')
      state.value = 'ended' // input 只在 ready 时发送 → 到此输入自然禁用
    }
  }

  /** 结论句写进终端（暗色），与 PTY 输出同屏可见。 */
  function writeConclusion(text: string): void {
    term?.write(`\r\n\x1b[90m${text}\x1b[0m\r\n`)
  }

  function sendInput(data: string): void {
    if (state.value !== 'ready') return
    send({ type: 'input', data })
  }

  function send(msg: unknown): boolean {
    if (!socket || socket.readyState !== WebSocket.OPEN) return false
    socket.send(JSON.stringify(msg))
    return true
  }

  /** fit 到宿主尺寸，并把新的 cols/rows 告诉容器（未连上时 send 静默丢弃）。 */
  function refit(): void {
    if (!term || !fitAddon) return
    try {
      fitAddon.fit()
    } catch {
      /* 宿主尚未布局（隐藏时）：下次 resize 再算 */
    }
    send({ type: 'resize', cols: term.cols, rows: term.rows })
  }

  /** 窗口 resize 合并到一帧里再 fit（拖拽窗口会连续触发，逐个 fit 没有必要）。 */
  function onWindowResize(): void {
    if (resizeRaf) cancelAnimationFrame(resizeRaf)
    resizeRaf = requestAnimationFrame(() => {
      resizeRaf = 0
      refit()
    })
  }

  /** 重试：重开一条新会话（旧连接若已断开，服务端已把会话取消）。 */
  function retry(): void {
    closeSocket()
    sessionRef = ''
    void connect()
  }
</script>

<style lang="scss" scoped>
  .pty-terminal {
    position: relative;
    border-radius: 6px;
    overflow: hidden;

    &__screen {
      box-sizing: border-box;
      height: 420px;
      padding: 8px;
      background: #101418;
    }

    &__overlay {
      position: absolute;
      inset: 0;
      display: flex;
      flex-direction: column;
      gap: 12px;
      align-items: center;
      justify-content: center;
      // 半透明：失败时上一屏的输出仍隐约可见（判因线索不该被浮层完全盖掉）。
      background: rgb(16 20 24 / 78%);
    }

    &__message {
      margin: 0;
      color: #e6e6e6;
      font-size: 13px;
    }
  }
</style>
