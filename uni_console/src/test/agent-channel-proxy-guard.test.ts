/**
 * 守卫：agent 专用 TLS 终端（20443）必须透传来源 IP 头；明文入口必须保持拒绝。
 *
 * 为什么这是红线（身份字段张冠李戴类）：device.primary_ip 由 core 在 WS 升级
 * 握手里取 c.ClientIP() 落库，而 ClientIP 的代理感知解析依赖 nginx 转发
 * X-Forwarded-For / X-Real-IP（生产 trustedProxies=172.16.0.0/12，把 console
 * 容器视为可信代理）。20443 块一旦漏掉这两个头，core 只会取到 TCP 对端 =
 * console 容器 IP —— 全部设备重新注册后落库同一地址，看起来像个真实答案，
 * 排障会被引向错误方向。
 *
 * 另一半：80 块里 `location = /api/v1/agent/ws { return 403; }` 是「agent 通道
 * 只走 TLS」的落点，改这份配置时极易顺手误删 —— 一并钉死。
 */
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const conf = readFileSync(fileURLToPath(new URL('../../nginx.conf', import.meta.url)), 'utf-8')

/** 取包含 needle 的 server 块原文：自其前最近一个 `server {` 起，按大括号配平到块尾。 */
function serverBlockContaining(needle: string): string {
  const at = conf.indexOf(needle)
  if (at < 0) throw new Error(`nginx.conf 中找不到 "${needle}"`)
  const start = conf.lastIndexOf('server {', at)
  if (start < 0) throw new Error(`找不到包含 "${needle}" 的 server { 起点`)
  let depth = 0
  for (let i = start; i < conf.length; i++) {
    if (conf[i] === '{') depth++
    else if (conf[i] === '}' && --depth === 0) return conf.slice(start, i + 1)
  }
  throw new Error(`包含 "${needle}" 的 server 块大括号未配平`)
}

describe('agent 通道反代配置守卫', () => {
  it('20443 TLS 终端透传 X-Real-IP / X-Forwarded-For（primary_ip 观测依赖）', () => {
    const tls = serverBlockContaining('listen 20443 ssl')
    expect(tls).toMatch(/proxy_set_header\s+X-Real-IP\s+\$remote_addr;/)
    expect(tls).toMatch(/proxy_set_header\s+X-Forwarded-For\s+\$proxy_add_x_forwarded_for;/)
  })

  it('80 明文入口保留对 agent ws 的 403 拒绝（通道只走 TLS）', () => {
    const plain = serverBlockContaining('location = /api/v1/agent/ws')
    expect(plain).toMatch(/location = \/api\/v1\/agent\/ws\s*\{/)
    expect(plain).toMatch(/return 403;/)
  })
})