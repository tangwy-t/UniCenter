import { describe, expect, it } from 'vitest'
import {
  COMPOSE_LOGS_ACTIONS,
  isPhase1Action,
  parseContainerInspectPayload,
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

describe('5a 聚合日志动作（compose:logs）', () => {
  it('恰好一条，且不进一期 runRead 清单（会话制流，不走轮询闭环）', () => {
    expect(COMPOSE_LOGS_ACTIONS).toEqual(['compose:logs'])
    expect(isPhase1Action('compose:logs')).toBe(false)
    // 与五份清单（一期/二期/三/四/五期）都没有交集 —— 互补条数断言靠它单列。
    expect([...PHASE1_ACTIONS]).not.toContain('compose:logs')
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

  // 载荷的键名是**协议侧的 snake_case**：core 对结果载荷原样透传（不重命名），
  // 故这里必须用真的线上形态断言 —— 用 camelCase 写测试会让「解析器读错键名」
  // 这件事永远看不见（字段静默为空，页面显示空白）。
  it('镜像详情载荷：按线上 snake_case 解析成分层列表与元数据', () => {
    const p = parseImageInspectPayload({
      id: 'sha256:x',
      repo_tags: ['mysql:8.0.22'],
      size_bytes: 100,
      exposed_ports: ['3306/tcp'],
      history: [
        { size_bytes: 10, created_by: 'CMD ["mysqld"]' },
        { size_bytes: 0, created_by: 'ENV x=1', empty_layer: true }
      ]
    })
    expect(p.id).toBe('sha256:x')
    expect(p.repoTags).toEqual(['mysql:8.0.22'])
    expect(p.sizeBytes).toBe(100)
    expect(p.exposedPorts).toEqual(['3306/tcp'])
    expect(p.history).toHaveLength(2)
    expect(p.history[0]!.createdBy).toBe('CMD ["mysqld"]')
    expect(p.history[1]!.emptyLayer).toBe(true)
    expect(parseImageInspectPayload(undefined).history).toEqual([])
  })

  it('容器详情载荷：按线上 snake_case 解析时刻/退出码/重启策略', () => {
    const v = parseContainerInspectPayload({
      id: 'c1',
      name: 'mysql',
      state: 'exited',
      created: 1789000000,
      started_at: 1789000100,
      finished_at: 1789000200,
      exit_code: 0,
      restart_policy: 'unless-stopped',
      env: ['TZ=Asia/Shanghai']
    })
    expect(v.name).toBe('mysql')
    expect(v.createdAt).toBe(1789000000)
    expect(v.startedAt).toBe(1789000100)
    expect(v.finishedAt).toBe(1789000200)
    expect(v.exitCode).toBe(0)
    expect(v.restartPolicy).toBe('unless-stopped')
    expect(v.env).toEqual(['TZ=Asia/Shanghai'])
  })
})
