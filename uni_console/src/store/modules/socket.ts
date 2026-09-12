/**
 * WebSocket 连接状态与消息分发
 *
 * - 生命周期：connect（登录后/页面刷新恢复）、disconnect（登出）、
 *   ensureConnected（幂等入口）、断网恢复（online 事件）自动重连
 * - 分发：按服务端消息 type 适配 —— auth_ok / auth_err / new_notice / pong / kicked
 * - 角标：unreadCount 累计当前会话 new_notice 数；auth_ok 时归零
 *   （服务端认证成功后会 catch-up 重推全部未读，避免重连虚增）
 */
import { ref, computed, watch } from 'vue'
import WebSocketClient from '@/utils/socket'
import {
  buildWsUrl,
  buildAuthMessage,
  buildPingMessage,
  parseServerMessage,
  ServerMsgType
} from '@/utils/socket/protocol'
import type { ServerMessage, NoticeData } from '@/utils/socket/protocol'
import { useUserStore } from './user'
import { ElMessageBox } from 'element-plus'

// 模块级 online 监听器引用:保证全局仅注册一次(幂等),避免 HMR/多实例
// 下重复 addEventListener 累积监听器。
let onlineHandler: (() => void) | undefined

export const useSocketStore = defineStore(
  'socketStore',
  () => {
    const userStore = useUserStore()

    const authed = ref(false)
    const unreadCount = ref(0)
    const lastNotice = ref<NoticeData | null>(null)

    const isConnected = computed(() => authed.value)

    /** 停止重连并复位状态（auth_err / kicked / 主动登出共用） */
    const teardown = () => {
      WebSocketClient.destroyInstance()
      authed.value = false
    }

    /** 消息分发核心：按 type 适配 */
    const dispatchMessage = (msg: ServerMessage) => {
      switch (msg.type) {
        case ServerMsgType.AUTH_OK:
          authed.value = true
          unreadCount.value = 0 // 等待服务端 catch-up 重推
          break
        case ServerMsgType.NEW_NOTICE:
          unreadCount.value += 1
          lastNotice.value = (msg.data as NoticeData) ?? null
          break
        case ServerMsgType.PONG:
          break // 心跳应答，静默
        case ServerMsgType.KICKED: {
          const reason = (msg.data as { message?: string })?.message || '您的账号在其他设备登录'
          teardown()
          ElMessageBox.alert(reason, '提示', { type: 'warning' }).catch(() => {})
          // 强制登出：无论用户是否关闭弹窗，短暂延时后登出（与 http 层 401 登出风格一致）
          setTimeout(() => userStore.logOut(), 1000)
          break
        }
        case ServerMsgType.AUTH_ERR:
          // token 无效/吊销：停止重连（重连必然再次失败）并静默登出
          teardown()
          userStore.logOut()
          break
        default:
          console.warn('[socket] 未知消息类型:', msg.type)
      }
    }

    /** 建立连接：无 token 直接返回；重复调用幂等（内部 ensureConnection 兜底） */
    const connect = () => {
      if (!userStore.accessToken) return
      const client = WebSocketClient.getInstance({
        url: buildWsUrl(),
        messageHandler: (event) => {
          const msg = parseServerMessage(event.data)
          if (msg) dispatchMessage(msg)
        },
        onOpen: () => {
          const token = userStore.accessToken
          if (token) client.send(buildAuthMessage(token))
        },
        onClose: () => {
          // 连接关闭同步状态：重连过程中 isConnected 反映真实状态；
          // 已销毁旧实例的延迟 close 事件不会走到这里（client 内部按 target 过滤）
          authed.value = false
        },
        // 应用层心跳用 JSON 消息：服务端按 JSON 解析，裸字符串 'ping' 会被拒绝刷 WARN
        pingMessage: buildPingMessage
      })
      // 幂等建连：首次创建时 getInstance 已自动 init；实例已存在但连接已死
      // （如重连次数耗尽）时这里负责重新拉起。重连成功后的重新认证由 onOpen 钩子负责。
      client.ensureConnection()
    }

    /** 断开连接并复位 */
    const disconnect = () => {
      teardown()
      unreadCount.value = 0
      lastNotice.value = null
    }

    /** 幂等入口：已登录时确保连接存在（App 启动/登录后调用；断线后也可重新拉起） */
    const ensureConnected = () => {
      if (!userStore.accessToken) return
      connect()
    }

    /** 外部显式校准未读数（收件箱拉取/标记已读后），负数钳制为 0、小数向下取整。 */
    const setUnreadCount = (count: number) => {
      unreadCount.value = Math.max(0, Math.trunc(count))
    }

    // token 刷新（http 401 自动 refresh）后重建连接：旧连接仍认证旧 token，
    // 断开重连保证一致性；连接已死时同样借此机会重新拉起。
    watch(
      () => userStore.accessToken,
      (newToken, oldToken) => {
        if (!newToken || newToken === oldToken) return
        if (authed.value) teardown()
        connect()
      }
    )

    // 断网恢复后自动重连，覆盖「重连次数耗尽后连接永久死亡」的场景。
    // 保存 handler 引用并幂等注册:setup store 正常只执行一次,但 HMR/测试
    // 可能多次实例化,重复 addEventListener 会累积监听器泄漏。
    if (typeof window !== 'undefined' && !onlineHandler) {
      onlineHandler = () => {
        if (userStore.accessToken) connect()
      }
      window.addEventListener('online', onlineHandler)
    }

    return {
      authed,
      unreadCount,
      lastNotice,
      isConnected,
      connect,
      disconnect,
      ensureConnected,
      setUnreadCount
    }
  },
  {
    persist: false // 连接状态不需要持久化
  }
)
