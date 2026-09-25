import { describe, expect, it } from 'vitest'
import {
  isPhase1Action,
  parseImageInspectPayload,
  parseLogsPayload,
  PHASE1_ACTIONS,
  pollDelay
} from '../utils/cmd'

describe('一期可用的动作', () => {
  it('只有四个只读动作属于一期', () => {
    expect([...PHASE1_ACTIONS].sort()).toEqual([
      'compose.file:read',
      'container:inspect',
      'container:logs',
      'image:inspect'
    ])
  })
  it('操作类动作不属于一期（页面据此不渲染按钮）', () => {
    for (const a of [
      'container:start',
      'container:stop',
      'container:restart',
      'container:remove',
      'image:prune',
      'image:pull',
      'volume:remove',
      'network:remove',
      'compose:down'
    ]) {
      expect(isPhase1Action(a)).toBe(false)
    }
  })
})

describe('轮询节奏', () => {
  it('1 秒起指数退避到 5 秒封顶', () => {
    expect(pollDelay(0)).toBe(1000)
    expect(pollDelay(1)).toBe(2000)
    expect(pollDelay(2)).toBe(4000)
    expect(pollDelay(3)).toBe(5000)
    expect(pollDelay(50)).toBe(5000)
  })
})

describe('结果载荷解析', () => {
  it('日志载荷：文本 + 是否截断', () => {
    expect(parseLogsPayload({ lines: 'a\nb\n', truncated: true })).toEqual({
      lines: 'a\nb\n',
      truncated: true
    })
    // 形状不符时给空结果而不是抛错（结果来自 agent，页面不该因此白屏）
    expect(parseLogsPayload(null)).toEqual({ lines: '', truncated: false })
    expect(parseLogsPayload({})).toEqual({ lines: '', truncated: false })
  })

  it('镜像详情载荷：分层列表与元数据', () => {
    const p = parseImageInspectPayload({
      id: 'sha256:x',
      sizeBytes: 100,
      history: [{ sizeBytes: 10, createdBy: 'CMD' }]
    })
    expect(p.id).toBe('sha256:x')
    expect(p.history).toHaveLength(1)
    expect(parseImageInspectPayload(undefined).history).toEqual([])
  })
})
