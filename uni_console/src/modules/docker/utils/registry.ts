/**
 * 仓库凭据（4c）的纯逻辑层：地址形态校验。
 *
 * ── 为什么前端要再挡一道 ─────────────────────────────────────────────
 * 服务端（uni_protocol 的 IsDockerRegistryAddr）才是权威闸；但凭据表单是
 * 「用户手输一个键」的地方 —— 地址写错（带了 https://、带了镜像路径、端口越界）
 * 时在表单里就地给出结论，比提交后收回一句 400 更诚实（与镜像引用校验
 * isValidImageRef 同一条纪律）。
 *
 * ── 单一事实源 ──────────────────────────────────────────────────────
 * 正则与上限**逐字照抄**协议 `dockerRegistryAddrRe` 与 `maxDockerRegistryAddrBytes`
 *（uni_protocol/docker.go）：主机名/IP + 可选端口（1-65535），不带协议头与镜像
 * 路径 —— 「凭据键」与拉取时 RegistryAuth 的 ServerAddress 是同一个值，两处
 * 口径漂移就会出现「存的时候合法、拉的时候标不中」。
 */
/** 协议 dockerRegistryAddrRe 的同形镜像（RE2 语法子集，JS 原生兼容）。 */
const REGISTRY_ADDR_RE =
  /^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?::(\d{1,5}))?$/

/** 协议 maxDockerRegistryAddrBytes 的同值镜像（255 字节）。 */
const MAX_REGISTRY_ADDR_BYTES = 255

/**
 * 是否为合法的仓库地址：主机名/IP + 可选端口（1-65535）。
 *
 * 端口是捕获组 —— 形态对还要数值合法（「host:99999」形态过、数值不过，协议
 * 同样判非法）；空串与超长直接判非法。
 */
export function isValidRegistryAddr(s: string): boolean {
  if (s === '' || s.length > MAX_REGISTRY_ADDR_BYTES) return false
  const m = REGISTRY_ADDR_RE.exec(s)
  if (!m) return false
  if (m[1] !== undefined) {
    const port = Number(m[1])
    return Number.isInteger(port) && port >= 1 && port <= 65535
  }
  return true
}
