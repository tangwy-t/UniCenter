package dockerops

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 拉取/推送/构建共用的进度折叠器（4b 的折叠纪律抽成公共实现）────────────────
//
// 三个进度族的**折叠纪律是同一套**（4b 定案，P2 复用）：
//   - **200ms 窗口合并**（pullFrameInterval）：窗口内的行按层/步骤 id 取**最后一条**
//     （同 id 的 current 抖动只留最新），消息行（没有 id 的行）按到达顺序保留；
//     窗口到点才写会话 —— 每秒至多 5 批，与 800 帧/分的全局预算互不侵犯；
//   - **跨窗内容去重**：某批与上一批逐字节相同就不发 —— 停滞的操作（一直
//     "Waiting" / 无输出构建）不产帧；
//   - **顺手冲刷**（字节阈值）：折叠批到阈值立即发（一行 JSON 不会被 pump 的
//     防御性切帧拦腰截断）。
//
// 三个族的差异只在**记录形状与终态项的编码**（协议 item 各写各的），由
// progressFramerConfig 注入。4b 的拉取保持行为逐字不变 —— 既有测试就是
// 「抽公共实现没改拉取语义」的回归钉。

// progressLine 是折叠器内部的**中性记录**（协议 item 的超集形状：pull/push 用
// ID/Status/Current/Total，build 多用 Stream）。
type progressLine struct {
	ID      string
	Status  string
	Stream  string
	Current int64
	Total   int64
}

// progressFramerConfig 是折叠器的构造参数（编码器与阈值都由调用族注入）。
type progressFramerConfig struct {
	// encode 把一条中性记录折成 JSON 行（含尾换行）。编码失败返回 nil（丢一行
	// 进度比发非法载荷好 —— 与 4b 的取向一致；真失败在 Json encode 层实际不存在）。
	encode func(progressLine) []byte
	// term 把终态折成**恰一条、恰在最后**的终态项（done/errMsg 恰其一）。
	term func(t int64, done bool, errMsg string) []byte
	// threshold 是顺手冲刷的字节阈值（0 = 不启用顺手冲刷）。
	threshold int
}

// progressFramer 是折叠器本体：emit 只投数据，窗口到点/终态请求由冲刷循环自己
// 节拍（daemon 的读循环是阻塞的，不能靠「下一条到达」触发上一批的冲刷）。
type progressFramer struct {
	sess  *streamSession
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
	cfg   progressFramerConfig

	mu        sync.Mutex
	layers    []progressLayerFold
	msgs      []progressLine
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

// progressLayerFold 是窗口内一条 id 的折叠态。
type progressLayerFold struct {
	ID   string
	Line progressLine
}

// newProgressFramer 构造折叠器并起冲刷循环（now/after 取会话管理器的注入时钟，
// 测试可驱动）。循环随会话 ctx 收口（取消/发送失败收摊时退出，不碰已关的会话）。
func newProgressFramer(sess *streamSession, cfg progressFramerConfig) *progressFramer {
	fr := &progressFramer{
		sess: sess, now: sess.mgr.cfg.now, after: sess.mgr.cfg.after, cfg: cfg,
		wake: make(chan struct{}, 1),
	}
	go fr.run()
	return fr
}

// run 是折叠器的冲刷循环：窗口到点/批超阈值冲刷，终态请求冲刷并收尾。
func (fr *progressFramer) run() {
	var timer <-chan time.Time
	for {
		fr.mu.Lock()
		pending := len(fr.layers)+len(fr.msgs) > 0
		over := fr.cfg.threshold > 0 && fr.bytes >= fr.cfg.threshold
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
				fr.sess.write(fr.cfg.term(fr.now().UnixMilli(), req.done, req.errMsg))
				fr.sess.markEOF()
				return
			}
		}
	}
}

// emit 收一条进度行（adapter 的同步回调，绝不阻塞）。会话已收摊时是无操作
// （cancel 后的迟到行）—— 写会话本来就是幂等的，这里提前退出省一次锁。
func (fr *progressFramer) emit(l progressLine) {
	if fr.sess.isClosing() {
		return
	}
	fr.mu.Lock()
	if fr.finished {
		// 终态已请求：随后冷冲刷会把残余批与终态项一起发走，这里就不收新行了
		//（迟到行属于下一个事实，进不了已完成的事实流）。
		fr.mu.Unlock()
		return
	}
	if l.ID == "" {
		fr.msgs = append(fr.msgs, l)
	} else {
		replaced := false
		for i := range fr.layers {
			if fr.layers[i].ID == l.ID {
				fr.layers[i].Line = l // 同 id 只留最新
				replaced = true
				break
			}
		}
		if !replaced {
			fr.layers = append(fr.layers, progressLayerFold{ID: l.ID, Line: l})
		}
	}
	fr.bytes += len(l.ID) + len(l.Status) + len(l.Stream) + 64
	fr.mu.Unlock()
	fr.signal()
}

// takeFinish 取终态请求（已取走或无请求时 ok=false）。
func (fr *progressFramer) takeFinish() (*finishReq, bool) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.finishReq == nil {
		return nil, false
	}
	req := fr.finishReq
	fr.finishReq = nil
	return req, true
}

func (fr *progressFramer) signal() {
	select {
	case fr.wake <- struct{}{}:
	default:
	}
}

// flush 把当前窗口折成一批写进会话（读走即清）。跨窗内容相同的批被去重掉。
// 只被冲刷循环调用（与 emit 之间有锁，last 无争用）。
func (fr *progressFramer) flush() {
	fr.mu.Lock()
	if len(fr.layers) == 0 && len(fr.msgs) == 0 {
		fr.mu.Unlock()
		return
	}
	var buf bytes.Buffer
	for _, m := range fr.msgs {
		buf.Write(fr.cfg.encode(m))
	}
	for _, l := range fr.layers {
		buf.Write(fr.cfg.encode(l.Line))
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
// mark eof —— 终态项**恰一条、恰在最后**（协议契约）。
func (fr *progressFramer) finish(done bool, errMsg string) {
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

// encodePullItem 编码一条拉取进度记录（JSON 行 + 换行；字段固定，编码失败实际不存在，
// 真失败时给空串 —— 丢一行进度比发非法载荷好，与 copyStats 的取向一致）。
func encodePullItem(p agentproto.DockerPullProgressItem) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// encodePushItem 编码一条推送进度记录（与 encodePullItem 同一形态纪律）。
func encodePushItem(p agentproto.DockerPushProgressItem) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// encodeBuildItem 编码一条构建进度记录（与 encodePullItem 同一形态纪律）。
func encodeBuildItem(p agentproto.DockerBuildProgressItem) []byte {
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
