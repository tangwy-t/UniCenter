// @vitest-environment jsdom
/**
 * 声明式目标版本三个端点的封装契约（Wave 4c）：
 *   - setImageTarget → POST /docker/image-targets，body = {repoTag, targetDigest}；
 *   - clearImageTarget → DELETE /docker/image-targets**?repoTag=…（显式 urlencode）**
 *     —— repoTag 含 `:` 与 `/`，必须走 query 且编码；
 *   - fetchImageTargets → GET /docker/image-targets。
 * 端点前缀（VITE_API_PREFIX）不在此断言（用 stringContaining 只看路径段）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn()
}))
vi.mock('@/utils/http', () => ({ default: http }))
vi.mock('@/store/modules/user', () => ({
  useUserStore: () => ({ accessToken: '' })
}))

import { clearImageTarget, fetchImageTargets, setImageTarget } from '../api'

beforeEach(() => {
  http.get.mockReset().mockResolvedValue({ items: [], total: 0 })
  http.post.mockReset().mockResolvedValue({})
  http.del.mockReset().mockResolvedValue(undefined)
})

describe('api · 镜像目标版本（Wave 4c）', () => {
  it('setImageTarget：POST，body = { repoTag, targetDigest }', () => {
    const digest = 'sha256:cafe'
    void setImageTarget({ repoTag: 'mysql:8.0.22', targetDigest: digest })
    expect(http.post).toHaveBeenCalledWith({
      url: expect.stringContaining('/docker/image-targets'),
      data: { repoTag: 'mysql:8.0.22', targetDigest: digest },
      showErrorMessage: false
    })
  })

  it('clearImageTarget：DELETE，repoTag 在 query 且 urlencode（`:`→%3A、`/`→%2F）', () => {
    void clearImageTarget('harbor.io/app:v1')
    expect(http.del).toHaveBeenCalledWith({
      url: expect.stringContaining('/docker/image-targets?repoTag=harbor.io%2Fapp%3Av1'),
      showErrorMessage: false
    })
    const url = (http.del.mock.calls[0]![0] as { url: string }).url
    // 未编码的原始字符不得出现在 URL 里。
    expect(url).not.toContain('app:v1')
  })

  it('fetchImageTargets：GET /docker/image-targets', () => {
    void fetchImageTargets()
    expect(http.get).toHaveBeenCalledWith({
      url: expect.stringContaining('/docker/image-targets'),
      showErrorMessage: false
    })
  })
})
