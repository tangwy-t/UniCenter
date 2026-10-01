package dockerops

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── image:pull 的进度透出（4b）────────────────────────────────────────────
//
// 形态选型（为什么 pull **不改会话制**、也不改 result 形状）：
//   - pull 的既有语义 = 一条 15 分钟时限的写指令：受理 → 轮询 → 终态结论句
//     （「拉取镜像失败/成功」），todo 页面/在飞去重/确认档全部挂在「指令直到拉完
//     才是终态」上。改成 stats/exec 那种「受理即回会话句柄」会让老前端的轮询在
//     拉取还在跑时就看到终态 —— 结论句、409 在飞、结果载荷三处语义同时被破坏；
//   - 于是保持写指令形态，进度走**既有帧通道**：执行期间开一条 streamPull 会话
//     （不计用户槽位、豁免空闲超时），daemon 的进度行逐条折进会话；core 在受理
//     指令时就按「会话 id = pull_ + ref」预登记（两端同一派生函数，见协议
//     DockerPullSessionID），帧通道因此在指令**未终态**时就可消费 —— 这就是本切片
//     与时序有关的最微妙处，也是 id 不走随机、走派生的原因。
//   - eof 纪律：eof 帧**先于** result 发出（本文件的 waitStreamEnded 钉住顺序），
//     消费端的长连接收口与轮询终态对齐；终端结果句（done/error）作为**恰一条、
//     恰在最后**的记录进帧流。
//
// 帧纪律照 sessions.go 的包注释：数据的搬运是「daemon 行 → 折叠器 → 会话缓冲」，
// 帧的发送与限速全在会话泵里 —— 本文件不碰协议帧。

// pullProgressFramer 把 daemon 的进度行折叠成批（一帧一批 JSON 行，一行一条协议
// DockerPullProgressItem），自带一条冲刷循环：emit 只投数据，窗口到点/终态请求由
// 循环自己节拍（daemon 的读循环是阻塞的，不能靠「下一条到达」触发上一批的冲刷）。
//
// 折叠规则（节流的两个半闸）：
//   - **200ms 窗口合并**（pullFrameInterval）：窗口内的行按层 id 取**最后一条**
//     （同层的 current 抖动只留最新），消息行（没有层的行）按到达顺序保留；
//     窗口到点才写会话 —— 每秒至多 5 批，与 800 帧/分的全局预算互不侵犯；
//   - **跨窗内容去重**：某批与上一批逐字节相同就不发 —— 停滞的拉取（一直
//     "Waiting"）不产帧。两个半闸合起来就是「状态变化才发声，发声也压着节奏」；
//     不加字节级去重的话，一个 0 进展的层能按 5 帧/s 刷满 15 分钟。
//
// 顺手冲刷（pullFrameBytes）：折叠批字节数到阈值立即发，不等窗口 —— 保证单帧
// 远低于协议上限，一行 JSON 不会被 pump 的防御性切帧拦腰截断。
type pullProgressFramer struct {
	sess  *streamSession
	now   func() time.Time
	after func(time.Duration) <-chan time.Time

	mu        sync.Mutex
	layers    []layerFold
	msgs      []agentproto.DockerPullProgressItem
	bytes     int
	finished  bool
	finishReq *finishReq
	// last 是上一批已发出的原文字节（跨窗去重；只被冲刷循环读写，无需锁）。
	last []byte
	// wake 是 emit/finish → 冲刷循环的信号（容量 1，合并）。
	wake chan struct{}
}

// finishReq 是终态请求（done=true 或 errMsg 非空，恰其一）。
type finishReq struct {
	done   bool
	errMsg string
}

// layerFold 是窗口内一层的折叠态。
type layerFold struct {
	ID   string
	Item agentproto.DockerPullProgressItem
}

// newPullProgressFramer 构造折叠器并起冲刷循环（now/after 取会话管理器的注入时钟，
// 测试可驱动）。循环随会话 ctx 收口（取消/发送失败收摊时退出，不碰已关的会话）。
func newPullProgressFramer(sess *streamSession) *pullProgressFramer {
	fr := &pullProgressFramer{
		sess: sess, now: sess.mgr.cfg.now, after: sess.mgr.cfg.after,
		wake: make(chan struct{}, 1),
	}
	go fr.run()
	return fr
}

// run 是折叠器的冲刷循环：窗口到点/批超阈值冲刷，终态请求冲刷并收尾。
func (fr *pullProgressFramer) run() {
	var timer <-chan time.Time
	for {
		fr.mu.Lock()
		pending := len(fr.layers)+len(fr.msgs) > 0
		over := fr.bytes >= pullFrameBytes
		fr.mu.Unlock()
		if pending && timer == nil {
			timer = fr.after(pullFrameInterval)
		} else if !pending {
			timer = nil
		}
		if over {
			fr.flush()
			continue
		}
		select {
		case <-fr.sess.ctx.Done():
			return // 会话收摊（cancel/发送失败）：残留数据随会话一起作废
		case <-timer:
			timer = nil
			fr.flush()
		case <-fr.wake:
			if req, ok := fr.takeFinish(); ok {
				fr.flush()
				fr.sess.write(encodePullItem(agentproto.DockerPullProgressItem{
					T: fr.now().UnixMilli(), Done: req.done, Error: req.errMsg,
				}))
				fr.sess.markEOF()
				return
			}
		}
	}
}

// emit 收一条 daemon 进度行（adapter 的同步回调，绝不阻塞）。会话已收摊时是无操作
// （cancel 后的迟到行）—— 写会话本来就是幂等的，这里提前退出省一次锁。
func (fr *pullProgressFramer) emit(p PullProgress) {
	if fr.sess.isClosing() {
		return
	}
	item := agentproto.DockerPullProgressItem{
		T: fr.now().UnixMilli(), ID: p.ID, Status: p.Status,
		Current: p.Current, Total: p.Total,
	}
	fr.mu.Lock()
	if fr.finished {
		// 终态已请求：随后 cold 冲刷会把残余批与终态项一起发走，这里就不收新行了
		//（迟到行属于下一个事实，进不了已完成的事实流）。
		fr.mu.Unlock()
		return
	}
	if p.ID == "" {
		fr.msgs = append(fr.msgs, item)
	} else {
		replaced := false
		for i := range fr.layers {
			if fr.layers[i].ID == p.ID {
				fr.layers[i].Item = item // 同层只留最新
				replaced = true
				break
			}
		}
		if !replaced {
			fr.layers = append(fr.layers, layerFold{ID: p.ID, Item: item})
		}
	}
	fr.bytes += len(p.ID) + len(p.Status) + 64
	fr.mu.Unlock()
	fr.signal()
}

// takeFinish 取终态请求（已取走或无请求时 ok=false）。
func (fr *pullProgressFramer) takeFinish() (*finishReq, bool) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.finishReq == nil {
		return nil, false
	}
	req := fr.finishReq
	fr.finishReq = nil
	return req, true
}

func (fr *pullProgressFramer) signal() {
	select {
	case fr.wake <- struct{}{}:
	default:
	}
}

// flush 把当前窗口折成一批写进会话（读走即清）。跨窗内容相同的批被去重掉。
// 只被冲刷循环调用（与 emit 之间有锁，last 无争用）。
func (fr *pullProgressFramer) flush() {
	fr.mu.Lock()
	if len(fr.layers) == 0 && len(fr.msgs) == 0 {
		fr.mu.Unlock()
		return
	}
	var buf bytes.Buffer
	for _, m := range fr.msgs {
		buf.Write(encodePullItem(m))
	}
	for _, l := range fr.layers {
		buf.Write(encodePullItem(l.Item))
	}
	payload := buf.Bytes()
	fr.layers = fr.layers[:0]
	fr.msgs = fr.msgs[:0]
	fr.bytes = 0
	fr.mu.Unlock()
	if bytes.Equal(payload, fr.last) {
		return
	}
	fr.last = payload
	fr.sess.write(payload)
}

// finish 请求终态（done/errMsg 恰其一）：冲刷循环会把残余批与终态项一并发走再
// mark eof —— 终态项**恰一条、恰在最后**（协议契约），错误文本是 daemon 原文
// （错误行由 adapter 折成返回错误，到这里原样入帧；页面看结论句，帧里留原文）。
func (fr *pullProgressFramer) finish(done bool, errMsg string) {
	if fr.sess.isClosing() {
		return
	}
	fr.mu.Lock()
	if fr.finished {
		fr.mu.Unlock()
		return
	}
	fr.finished = true
	req := &finishReq{done: true}
	if errMsg != "" {
		req = &finishReq{errMsg: errMsg}
	}
	fr.finishReq = req
	fr.mu.Unlock()
	fr.signal()
}

// encodePullItem 编码一条进度记录（JSON 行 + 换行；字段固定，编码失败实际不存在，
// 真失败时给空串 —— 丢一行进度比发非法载荷好，与 copyStats 的取向一致）。
func encodePullItem(p agentproto.DockerPullProgressItem) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// waitStreamEnded 等会话泵把 eof 帧**真正发出去**（eof ≤ result 的时序纪律）。
//
// 为什么必须等：result 与 eof 帧走同一条连接（FIFO），但 eof 是交给泵**异步**发的 ——
// 不等一下，dispatcher 的 result 可能抢在泵拿到调度之前进发送队列，消费端就会先看到
// 终态、后收到 eof；对旧轮询无碍，对长连接的收口径则是一次「流还没关，指令已终态」
// 的错位。等待上界 3 秒是「绝不扣住 worker」的兜底（泵的限速等待毫秒级，正常远用不到），
// 真等到超时也照发 result —— 消费端有轮询终态兜底。
func waitStreamEnded(sess *streamSession) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !sess.isClosing() {
		time.Sleep(20 * time.Millisecond)
	}
}

// pullAuthOf 把指令里的仓库认证折成 adapter 的本地形态（4c）：auth 缺席
//（公共仓库/主机侧 docker login）即 nil —— 与 4b 之前逐字一致。协议已经校验过
// auth 只能出现在 image:pull 上、且 registry 与 options.registry 同键、三字段
// 非空并合尺寸闸（见协议 DockerCmd.Validate），这里只做翻译不重复校验。
func pullAuthOf(cmd *agentproto.DockerCmd) *PullAuth {
	if cmd.Auth == nil {
		return nil
	}
	return &PullAuth{Registry: cmd.Auth.Registry, Username: cmd.Auth.Username, Password: cmd.Auth.Password}
}

// pullImage 是 image:pull 的写执行路径（4b）：进度透出 + 既有语义逐字保留。
// auth（4c）是 core 随指令注入的仓库认证（nil = 无凭据，与 4b 逐字一致）——
// 透传给 adapter，本层不做任何映射（SDK 类型不出 adapter）。
//
// 三个结局：
//   - 成功：终态项 Done → eof → result{ok}（结论句按老流程由前端说法，detail 无附注）；
//   - daemon 失败：终态项 Error（daemon 原文）→ eof → 结论句「拉取镜像失败」；
//   - 取消（core 下发 cancel → 会话收摊 → 拉取用的 ctx 被中断）：**不发**终态项与
//     eof（会话已消失，帧也发不出去），结论句「拉取已取消」—— 与「失败」分开措辞：
//     用户主动放弃不是故障，不该按故障句式弹红。
func (e *WriteExecutor) pullImage(ctx context.Context, cmdRef, target string, auth *PullAuth) error {
	sessions := e.sessionsValue()
	if sessions == nil {
		// 流通道未装配（测试替身/老装配形态）：一期黑盒语义原样保留 —— emit=nil
		// 走 io.Discard 快路径，结果只回结论。这正是「旧流程必须照旧工作」的守卫处。
		if err := e.api.ImagePull(ctx, target, auth, nil); err != nil {
			return wrapDocker("拉取镜像失败", err)
		}
		return nil
	}
	// 会话句柄由 ref **派生**（协议 DockerPullSessionID）而不是随机：core 在受理
	// 指令时就用同一个派生函数预登记了会话 —— 两端不在一把句柄上会话，帧就找不到
	// 主人。派生形态的保密度论证见 sessions.go 的 openNamed 与协议注释。
	sess, err := sessions.openNamed(streamPull, agentproto.DockerPullSessionID(cmdRef))
	if err != nil {
		// 拉取进度不占用户槽位，本分支只可能因句柄非法 —— 折叠成结论句而不是
		// 静默退黑盒（进度是本期承诺的可见性，静默丢了等于没做）。
		return &ExecError{Msg: err.Error()}
	}
	// 拉取跑在**派发器的 ctx** 上（15 分钟时限照旧），cancel 的中断桥挂在会话的
	// closeUpstream：teardown 一关上游，这个 cancel 就把 SDK 的读打断 —— 会话取消
	// 与指令时限两条路都在，谁也不欠谁的上下文。
	pullCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess.attachUpstream(func() error { cancel(); return nil })

	fr := newPullProgressFramer(sess)
	err = e.api.ImagePull(pullCtx, target, auth, fr.emit)
	if err != nil {
		if sess.isClosing() {
			// 会话已收摊 = 用户取消（或断连后的 cancel）：终态项与 eof 无从发出，
			// 结论句按「取消」措辞。
			return &ExecError{Msg: "拉取已取消"}
		}
		fr.finish(false, err.Error())
		waitStreamEnded(sess)
		return wrapDocker("拉取镜像失败", err)
	}
	fr.finish(true, "")
	waitStreamEnded(sess)
	return nil
}
