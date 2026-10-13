// @vitest-environment jsdom
/**
 * 构建上下文上传端点（POST /docker/hosts/:id/build-context）的封装契约：
 * 尺寸上限**按用途分档**，前端把用途如实告诉服务端 —— `kind=load` 走 query，
 * 构建上传**不带**该参数（缺省 build、512MB 档，与从前逐字节一致）。
 * 端点前缀（VITE_API_PREFIX）不在此断言（用 stringContaining 只看路径段）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@/utils/http', () => ({ default: http }))
vi.mock('@/store/modules/user', () => ({ useUserStore: () => ({ accessToken: '' }) }))

import { uploadDockerBuildContext } from '../api'

beforeEach(() => {
  http.post.mockReset().mockResolvedValue({ filename: 'build-ctx-1.tar.gz' })
})

/** 最近一次 post 的 url 与选项。 */
function lastOpts(): { url: string; headers: Record<string, string>; timeout: number } {
  return http.post.mock.calls.at(-1)![0] as {
    url: string
    headers: Record<string, string>
    timeout: number
  }
}

describe('api · 构建上下文上传（用途分档：build 512MB / load 8GB）', () => {
  it('缺省用途（构建）：URL **不带** kind 参数，与从前逐字节一致', () => {
    void uploadDockerBuildContext('h1', new Blob([new Uint8Array([1])]))
    const o = lastOpts()
    expect(o.url).toContain('/docker/hosts/h1/build-context')
    expect(o.url).not.toContain('kind')
    // 分档不动传输口径：octet-stream 流式 + 显式超时（分钟级上传不被 15s 掐断）。
    expect(o.headers['Content-Type']).toBe('application/octet-stream')
    expect(o.timeout).toBe(0)
  })

  it('显式 build：同样不带 kind（与缺省同一条路，构建对话框零变化）', () => {
    void uploadDockerBuildContext(
      'h1',
      new Blob([new Uint8Array([1])]),
      undefined,
      undefined,
      'build'
    )
    expect(lastOpts().url).not.toContain('kind')
  })

  it('用途 load（image:load 镜像归档）：URL 带 ?kind=load（服务端据此选 8GB 档）', () => {
    void uploadDockerBuildContext(
      'h1',
      new Blob([new Uint8Array([1])]),
      undefined,
      undefined,
      'load'
    )
    expect(lastOpts().url).toContain('/docker/hosts/h1/build-context?kind=load')
  })
})
