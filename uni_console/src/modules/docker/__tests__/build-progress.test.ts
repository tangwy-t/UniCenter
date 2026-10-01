/**
 * 构建进度面（P2）纯逻辑测试：构建行的解析校验、播报表折叠（文本行按序 +
 * 步骤行按 id 折叠 + 上限保尾）、终态捕获，以及构建表单的协议镜像校验
 * （context 文件名 / dockerfile 子路径 / build-arg 键）。
 *
 * 组件把哪条数据接到哪个控件（表单校验挡提交 / 双通道收尾 / 取消路径）在
 * build-dialog.test.ts（jsdom）；推送侧的层折叠语义（flavor 换词）在
 * pull-progress.test.ts 的推送组 —— 折叠器本体是 utils/pull.ts 的。
 */
import { describe, expect, it } from 'vitest'
import {
  createBuildFeed,
  isValidBuildArgKey,
  isValidBuildContextFilename,
  isValidBuildSubpath,
  parseBuildStreamLine
} from '../utils/build-push'

/** 一条构建行（t 给合法默认；行尾带 \n —— NDJSON 的行都以换行收尾，编码器如此）。 */
function frame(o: Record<string, unknown>): string {
  return `${JSON.stringify({ t: 1700000000000, ...o })}\n`
}

describe('行解析（parseBuildStreamLine）', () => {
  it('文本行（stream）：字段全解析', () => {
    const p = parseBuildStreamLine(frame({ stream: 'Step 1/4 : FROM node:20' }))
    expect(p).toEqual({
      item: {
        t: 1700000000000,
        id: '',
        status: '',
        stream: 'Step 1/4 : FROM node:20',
        done: false,
        error: ''
      },
      eof: false
    })
  })

  it('步骤行（id + status）：buildkit 句柄与状态文案', () => {
    const p = parseBuildStreamLine(frame({ id: 'Step 1/2', status: 'FROM node:20' }))
    expect(p?.item).toMatchObject({ id: 'Step 1/2', status: 'FROM node:20', stream: '' })
  })

  it('成功终态行：done=true（eof 同行挂靠是 core 的活，解析侧只认字段）', () => {
    const p = parseBuildStreamLine(frame({ done: true, eof: true }))
    expect(p?.item?.done).toBe(true)
    expect(p?.eof).toBe(true)
  })

  it('失败终态行：error 原文透传（daemon 的 failed to solve 串）', () => {
    const p = parseBuildStreamLine(frame({ error: 'failed to solve: exit code 1' }))
    expect(p?.item?.error).toBe('failed to solve: exit code 1')
    expect(p?.item?.done).toBe(false)
  })

  it('单独的 eof 行（t 是零值序列化）：只当 eof 信号，不当坏行', () => {
    const p = parseBuildStreamLine(JSON.stringify({ seq: 9, t: 0, eof: true }))
    expect(p).toEqual({ item: null, eof: true })
  })

  it('非 JSON / 非对象 / eof 非布尔 → null（坏行跳过，不打断流）', () => {
    expect(parseBuildStreamLine('not json')).toBeNull()
    expect(parseBuildStreamLine('[1,2]')).toBeNull()
    expect(parseBuildStreamLine('null')).toBeNull()
    expect(
      parseBuildStreamLine(frame({ stream: 'x' }).replace('"t":1700000000000,', '"t":"x",'))
    ).toBeNull()
    expect(parseBuildStreamLine(JSON.stringify({ t: 1, eof: 'yes' }))).toBeNull()
  })

  it('协议校验的镜像：t 非正 / 双终态 → null；构建帧的字段类型也要对', () => {
    expect(parseBuildStreamLine(frame({ stream: 'x', t: 0 }))).toBeNull()
    expect(parseBuildStreamLine(frame({ stream: 'x', done: true, error: '双终态' }))).toBeNull()
    expect(parseBuildStreamLine(frame({ stream: 42 }))).toBeNull()
    expect(parseBuildStreamLine(frame({ id: 7, status: 'x' }))).toBeNull()
    expect(parseBuildStreamLine(frame({ status: ['x'] }))).toBeNull()
    expect(parseBuildStreamLine(frame({ done: 'yes' }))).toBeNull()
  })
})

describe('播报表折叠（createBuildFeed）', () => {
  it('文本行按到达顺序追加；步骤行按 id 折叠（后帧是更新不是新行）', () => {
    const feed = createBuildFeed()
    feed.pushRaw(
      [
        frame({ stream: '#1 [internal] load build definition from Dockerfile' }),
        frame({ id: 'Step 1/2', status: 'FROM node:20' }),
        frame({ id: 'Step 1/2', status: 'FROM node:21' }), // 同步骤抖动只留最新
        frame({ stream: '#3 [2/2] RUN npm install' })
      ].join('\n')
    )
    expect(feed.lines.map((l) => l.text)).toEqual([
      '#1 [internal] load build definition from Dockerfile',
      'FROM node:21',
      '#3 [2/2] RUN npm install'
    ])
    // 步骤行的位置钉在首次到达处（不随后帧挪位），id 与 step 标记如实携带。
    expect(feed.lines[1]).toMatchObject({ id: 'Step 1/2', step: true })
    expect(feed.lines[0]!.step).toBe(false)
    expect(feed.lines[2]!.id).toBe('')
  })

  it('无步骤 id 的状态行（消息行）也进播报：按到达顺序，不当层表折叠', () => {
    const feed = createBuildFeed()
    feed.pushRaw([frame({ status: 'loading namer' }), frame({ stream: '#1 DONE' })].join('\n'))
    expect(feed.lines.map((l) => l.text)).toEqual(['loading namer', '#1 DONE'])
    expect(feed.lines.every((l) => !l.step)).toBe(true)
  })

  it('行 key 稳定：步骤行 = 步骤 id，文本行 = 全局行号（不因折叠挪位）', () => {
    const feed = createBuildFeed()
    feed.pushRaw(frame({ stream: 'a' }))
    feed.pushRaw(frame({ id: 's1', status: 'v1' }))
    feed.pushRaw(frame({ id: 's1', status: 'v2' }))
    expect(feed.lines.map((l) => l.key)).toEqual(['l:0', 's:s1'])
  })

  it('跨 chunk 的半行能接上（NDJSON 拆包纪律在构建流同样成立）', () => {
    const feed = createBuildFeed()
    const line = frame({ stream: '#4 RUN ./ci.sh' })
    const cut = Math.floor(line.length / 2)
    feed.pushRaw(line.slice(0, cut))
    expect(feed.lines.length).toBe(0) // 半行不成帧
    feed.pushRaw(line.slice(cut)) // 后半自带行尾换行，补齐即成帧
    expect(feed.lines.length).toBe(1)
    expect(feed.lines[0]!.text).toBe('#4 RUN ./ci.sh')
  })

  it('上限保尾：超出 400 行丢弃最旧的，丢弃量记进 droppedLines（说在明处）', () => {
    const feed = createBuildFeed()
    const lines: string[] = []
    for (let i = 0; i < 410; i++) lines.push(frame({ stream: `line ${i}` }))
    feed.pushRaw(lines.join('\n'))
    expect(feed.lines.length).toBe(400)
    expect(feed.droppedLines).toBe(10)
    // 保留的是尾部（排障价值在「最后发生了什么」）。
    expect(feed.lines[0]!.text).toBe('line 10')
    expect(feed.lines[399]!.text).toBe('line 409')
    // 后续追加继续保尾。
    feed.pushRaw(frame({ stream: 'line 410' }))
    expect(feed.droppedLines).toBe(11)
    expect(feed.lines[399]!.text).toBe('line 410')
  })

  it('终态项恰一条被捕获（成功 / 失败两种），eof 同帧置位', () => {
    const ok = createBuildFeed()
    ok.pushRaw([frame({ stream: '#9 writing image' }), frame({ done: true, eof: true })].join('\n'))
    expect(ok.terminal).toEqual({ ok: true, error: '' })
    expect(ok.eof).toBe(true)

    const fail = createBuildFeed()
    fail.pushRaw(frame({ error: 'failed to solve: exit code 1', eof: true }))
    expect(fail.terminal).toEqual({ ok: false, error: 'failed to solve: exit code 1' })
  })

  it('eof 之后残余字节不再入账（与 stats/pull 流同纪律）', () => {
    const feed = createBuildFeed()
    feed.pushRaw(frame({ stream: 'done' }))
    feed.pushRaw(frame({ done: true, eof: true }))
    const before = feed.lines.length
    feed.pushRaw(frame({ stream: 'stale' }))
    expect(feed.lines.length).toBe(before)
  })

  it('坏行被跳过：播报表不进脏数据，流不中断', () => {
    const feed = createBuildFeed()
    feed.pushRaw(
      ['{bad json', frame({ stream: 'x', done: true, error: '双终态' }), frame({ id: 's1' })].join(
        '\n'
      )
    )
    // 只有合法的步骤行进表（空 status 的步骤行照进 —— 文本面渲染 id）。
    expect(feed.lines.map((l) => l.id)).toEqual(['s1'])
  })
})

describe('构建表单校验（协议镜像的正则，跨语言漂移由这里钉）', () => {
  it('上下文文件名：tar / tar.gz / tgz 三形态、不带路径成分', () => {
    expect(isValidBuildContextFilename('app.tar')).toBe(true)
    expect(isValidBuildContextFilename('app.tar.gz')).toBe(true)
    expect(isValidBuildContextFilename('app.tgz')).toBe(true)
    expect(isValidBuildContextFilename('My-Project.1.tar')).toBe(true)
    // 带路径 / 逃逸形态 / 其它后缀 / 空串 → 拒
    expect(isValidBuildContextFilename('../app.tar')).toBe(false)
    expect(isValidBuildContextFilename('dir/app.tar')).toBe(false)
    expect(isValidBuildContextFilename('app.zip')).toBe(false)
    expect(isValidBuildContextFilename('.tar')).toBe(false)
    expect(isValidBuildContextFilename('')).toBe(false)
  })

  it('dockerfile 子路径：普通文件的相对路径子集（禁 .. / 空段 / 反斜杠）', () => {
    expect(isValidBuildSubpath('Dockerfile')).toBe(true)
    expect(isValidBuildSubpath('docker/Dockerfile')).toBe(true)
    expect(isValidBuildSubpath('build.ctx/Dockerfile.prod')).toBe(true)
    expect(isValidBuildSubpath('../Dockerfile')).toBe(false)
    expect(isValidBuildSubpath('docker/../Dockerfile')).toBe(false)
    expect(isValidBuildSubpath('/Dockerfile')).toBe(false) // 绝对路径不是「上下文内相对路径」
    expect(isValidBuildSubpath('docker//Dockerfile')).toBe(false) // 空段
    expect(isValidBuildSubpath('docker\\Dockerfile')).toBe(false) // 反斜杠
    expect(isValidBuildSubpath('.')).toBe(false)
    expect(isValidBuildSubpath('')).toBe(false)
    // 超长（协议字符串档 512B）→ 拒
    expect(isValidBuildSubpath(`${'a'.repeat(600)}/Dockerfile`)).toBe(false)
  })

  it('build-arg 键：POSIX 标识符（字母或下划线开头，后续字母数字下划线）', () => {
    expect(isValidBuildArgKey('VERSION')).toBe(true)
    expect(isValidBuildArgKey('_private')).toBe(true)
    expect(isValidBuildArgKey('node_env_1')).toBe(true)
    expect(isValidBuildArgKey('1BAD')).toBe(false) // 数字开头（shell 变量也不允许）
    expect(isValidBuildArgKey('bad-key')).toBe(false) // 连字符不在标识符集
    expect(isValidBuildArgKey('bad key')).toBe(false)
    expect(isValidBuildArgKey('')).toBe(false)
  })
})
