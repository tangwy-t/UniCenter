package dockerops

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建上下文上传的 agent 侧接收器（v1.3）────────────────────────────────
//
// 拓扑前提：agent 只有一根出站 WSS，浏览器上传的构建上下文字节由 core 中转
// 成二进制分片帧落这里 —— 本接收器就是「代理仓到机器」这条链路的终点：
// 分片组装进 transferDir 的临时产物 + sha256 终验 + gzip 魔数初验 + 中止清理。
//
// 三条与「写执行器」共享、但**不能共用实现**的纪律：
//  1. **路径纪律**：产物名不是任何一方自述的 —— 会话号是 core 生成、顺着帧头
//     到达的唯一事实源，文件名由它唯一推导（agentproto.DockerBuildCtxTransferName）。
//     客户端命名（含路径）在本层**没有表达面**：名字根本不由输入决定。
//     落点 dir 仍走 transferDir（写执行器的同一事实源），文件不存在目录回退
//     —— /tmp 回退是 cmd 结果的附注机制，上传没有结果通道，硬落在
//     「产物目录不可用」的错误上（诚实失败，见 write.go 的附注机制说明）。
//  2. **双段校验的分界**：上传段只验「这像个 tar 归档」（gzip 魔数或未压缩 tar 的
//     ustar 魔数）+ 尺寸；tar 条目/穿越/Dockerfile 存在性是 **image:build 执行时
//     scanBuildContext 的职责** —— 单一校验事实源，这里抢做纵深校验就是把同一份
//     判断拆成两处漂移的复制品（depth 校验若在上传段拦了，build 段的修复不会同步到
//     这里 —— 反之亦然）。明文 tar 放行的直接收益：docker build/load 对未压缩 tar
//     本就合法，上传的地基（transferDir 是 image:load 的读取目录）因此同时惠及
//     构建上下文与从浏览器上传的镜像导出物。
//  3. **失败取向 = 弃会话，不断连**：分片流是搭在连接上的旁路载荷，序号乱了、
//     魔数错了、哈希对不上，作废的是**这个会话**（删除半成品文件）——
//     指标/心跳/指令的主链路绝不陪葬。与 docker 的其它载荷（state/frame）
//     同一取向（见 agenthub 侧 docker_channel.go 的说明）。
//
// 并发模型：一把 Mutex 守住会话表。分片/完成/中止/断连清理四个入口并发到达
//（读循环与 runOnce 收尾不在一个 goroutine），但每帧的工作量有界（≤256KB 的
// 页缓存写 + 一行递增哈希），不需要把文件写移出锁外 —— 锁内还保证
// 「seq 校验 → 写入」的原子序，否则读循环里两个乱序帧会把账目打穿。

// buildCtxSweepAge 是 build-ctx-* 产物的清扫年龄（24h）。选型：
//
//   - **会话级覆盖**：每个上传会话一个唯一推导名（会话号在名字里）——
//     同一用户连续上传各得各的文件，旧文件等 24h 自然回收；不需要「覆盖」
//     的原因：image:build 引用的是**确切文件名**（core 在上传响应里给的），
//     用户下一次上传拿到新名字，即等于覆盖了旧的可用性；
//   - **24h**：覆盖「上传后立刻构建」的工作窗口，也给被中断/被遗忘的半成品
//     一个回收期限；
//   - **驱动力是定时任务表里的 docker-transfer-cleanup**：core 逐台派发
//     system:transfer-cleanup，agent 侧的执行体就是本文件的 sweep。
//
// 它此前挂在**每次上传的开头**，理由自述为「上传是产物唯一产生源，清扫跟着产生源
// 走，不必起第二套调度」。那条前提在载入镜像复用同一条通道后**已被打破**：产物可到
// GB 级（实测 520MB / 1.6GB / 2.87GB），而「传完就不再传」的机器再也没有任何东西
// 触发清扫 —— 产物会永久占着盘。回收因此改成一件**有自己时刻表的事**（core 的
// 定时任务表中一行显式任务），不再寄生在别人的路径上。
const buildCtxSweepAge = 24 * time.Hour

// maxBuildCtxUploadBytes 是组装账目的尺寸闸 = **两档上传产物的较大者**
// （协议 MaxDockerImageLoadBytes，8GB）。
//
// 为什么 agent 不做用途分流（v1.2.7 的取舍）：上传的发送方是**已认证的 core**，
// 不是浏览器直连 —— 用途分档在 core 侧执法（它按 query 参数 kind 选档），agent
// 侧再分一次只会多一条「两端对同一次上传的用途判断不一致」的失败路径，还要么给
// 二进制帧加一个 kind 字段（动线上帧结构），要么让 core 另发一条设置帧。取**较大
// 者**的代价是「构建上下文在 agent 侧少了一道更紧的闸」，收益是这条闸的职责退化成
// 它真正该管的那件事：**不让一次上传吃爆自己的盘**（那是与用途无关的）。
//
// var 而非 const：测试调小来驱动账目触线而不必真的搬运 GB 级字节
// （与 adapter 的 maxBuildContextEntries 同一纪律）。
var maxBuildCtxUploadBytes = int64(agentproto.MaxDockerImageLoadBytes)

// buildCtxDiskReserveBytes 是写盘预留：判「盘够不够」时要在本片之外多留出的余量。
//
// 为什么不留 0：本闸的职责是「不让一次上传吃爆自己的盘」—— 写到刚好 0 会让同一台
// 主机上的 docker daemon 与容器一起停摆（上传失败是局部故障，整机停摆是全局故障）。
// 64MB 是「写出收尾与 daemon 侧一点周转」的量级，对 4.9T 的现场盘不值一提。
const buildCtxDiskReserveBytes = 64 << 20

// buildCtxFreeBytes 探测目录所在文件系统的可用字节数。var：测试注入**假 statfs**
// 驱动「盘量不足早失败」，不必真去把一块盘写满（与 maxBuildCtxUploadBytes 同一纪律）。
var buildCtxFreeBytes = fsFreeBytes

// fsFreeBytes 用 syscall.Statfs 取可用字节（不引第三方依赖）。
//
// 取 Bfree 而不是 Bavail：agent 以 root 运行（产物目录 0700、文件 0600），
// Bavail 会把「root 本来就能用的保留块」排除在外 —— 那是**别人的**口径，不是本
// 进程真能写的空间（本闸要判的正是后者）；「别把盘写满」的政策由
// buildCtxDiskReserveBytes 显式承担。
//
// 只在 Linux 上编译：agent 的发布产物就是 linux/amd64（见 Makefile 的
// uni_agent-release），没有第二平台需要这份探测。
func fsFreeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bsize) * int64(st.Bfree), nil
}

// diskShortText 是盘量不足的**结论句**（可执行：说得清差多少）。
// need = 本片字节 + 预留（见 buildCtxDiskReserveBytes）。
func diskShortText(need, free int64) string {
	return fmt.Sprintf("可用空间不足：需 %s，剩 %s", humanBytes(need), humanBytes(free))
}

// humanBytes 把字节折成结论句里的可读形态（"8GB" / "64MB" / "512B"）。
// 只为上面这一句服务（agent 侧没有第二处需要它），故不引 humanize、
// 也不做单位表：按 2 的幂逐级除，整数档给整数值，非整数档保留一位小数。
func humanBytes(n int64) string {
	type unit struct {
		size int64
		name string
	}
	units := []unit{{1 << 30, "GB"}, {1 << 20, "MB"}, {1 << 10, "KB"}}
	for _, u := range units {
		if n >= u.size {
			if n%u.size == 0 {
				return strconv.FormatInt(n/u.size, 10) + u.name
			}
			return strconv.FormatFloat(float64(n)/float64(u.size), 'f', 1, 64) + u.name
		}
	}
	return strconv.FormatInt(n, 10) + "B"
}

// buildCtxFileMode 是临时产物的权限（0600）：agent 以 root 运行，产物目录
// 0700、文件 0600 —— 与 transferDir 的「世界不可写」纪律同一条（§7.5）。
const buildCtxFileMode = 0o600

// ── 早期形态闸（上传段对内容的全部形态要求）──────────────────────────────

// buildCtxUstarOffset 是 tar 头里 ustar 魔数的偏移（POSIX 1003.1 与 GNU 两种
// 变体都以 "ustar" 开头：`ustar\0` 与 `ustar  `）。
const buildCtxUstarOffset = 257

// buildCtxUstarMagic 是未压缩 tar 的形态指纹。
const buildCtxUstarMagic = "ustar"

// buildCtxMagicWindow 是形态闸的校验窗口（字节数）：按两种形态里最长的取 ——
// gzip 只看偏移 0 的两字节，未压缩 tar 要看到偏移 257 的 ustar 魔数（257 + 5）。
// 窗口只**拷贝**前若干字节做判读，分片本身仍原样全量入文件（校验窗口不截断数据流）。
const buildCtxMagicWindow = buildCtxUstarOffset + len(buildCtxUstarMagic)

// buildCtxForm 判读形态窗口覆盖的字节是否属于两种合法形态之一，并报告窗口是否
// 已足够下结论：
//   - gzip：偏移 0 的 1f 8b（两字节即可定型）；
//   - 未压缩 tar：偏移 257 的 ustar 魔数（需攒够一整窗）。
//
// 「还不知道」（decided=false）与「不是」（ok=false）必须分开：短流在攒够窗口前
// 被误判成非法，就会把合法的明文 tar 挡在门外。两者都不是 = 早期形态闸拒绝
// （**深度校验仍在 scanBuildContext**，本条只是「趁早认出不是我们认的形态」）。
func buildCtxForm(head []byte) (ok, decided bool) {
	if len(head) >= 2 && head[0] == gzipMagic[0] && head[1] == gzipMagic[1] {
		return true, true
	}
	if len(head) >= buildCtxMagicWindow {
		return string(head[buildCtxUstarOffset:buildCtxMagicWindow]) == buildCtxUstarMagic, true
	}
	return false, false
}

// buildCtxReceiver 是上传分片流的组装器（Runtime 持有，连接层经 hook 投喂）。
type buildCtxReceiver struct {
	// write 是 transferDir 的事实源（transferDirValue 取活值：配置会热更新）。
	write *WriteExecutor
	log   Logger
	now   func() time.Time

	mu       sync.Mutex
	sessions map[uint64]*buildCtxUploadSession
}

// buildCtxUploadSession 是一个上传会话的组装状态。
type buildCtxUploadSession struct {
	id   uint64
	name string // 推导产物名（记账/终验比对用）
	path string

	nextSeq uint64 // 期望的下一个分片序号（从 1 起）
	final   bool   // 已收到 FINAL 帧
	formOK  bool   // 形态已定型（gzip 或未压缩 tar 之一）
	head    []byte // 形态校验窗口（攒够 buildCtxMagicWindow 前的暂存）

	h    hash.Hash // 递增 sha256（终验在 finish 时与期望比对，不必二次读文件）
	f    *os.File
	size int64
}

func newBuildCtxReceiver(w *WriteExecutor, log Logger, now func() time.Time) *buildCtxReceiver {
	if now == nil {
		now = time.Now
	}
	return &buildCtxReceiver{
		write:    w,
		log:      log,
		now:      now,
		sessions: make(map[uint64]*buildCtxUploadSession),
	}
}

// Chunk 组装一个分片。返回 error 表示该会话已被作废（半成品已删除）——
// 由 Runtime 记日志；连接本身不受影响（弃会话不断连，见文件头说明）。
func (r *buildCtxReceiver) Chunk(sessionID, seq uint64, final bool, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sess, ok := r.sessions[sessionID]
	if !ok {
		if seq != 1 {
			// 已知会话之外的乱序帧：没有可作废的状态，也没有可删的文件 ——
			// 记录即可（core 的正常中止流不会出现；出现 = 对端实现偏差）。
			return fmt.Errorf("未知上传会话 %d 的分片（seq=%d）", sessionID, seq)
		}
		s, err := r.open(sessionID)
		if err != nil {
			return err
		}
		sess = s
	}

	if seq != sess.nextSeq {
		// 序号纪律：首要=1，之后严格 +1。乱序/重复/跳号 → 弃会话。
		// 为什么是**弃**而不是「缓冲等缺口补上」：core 的发送队列保序（单队列
		// 串行写 socket），乱序出现只可能是诡异丢失 —— 等不来缺口，而哈希终验
		// 必然失败；不弃则会把该会话的临时文件与账目挂到断连为止。
		r.drop(sess, fmt.Sprintf("分片乱序（seq=%d, want %d）", seq, sess.nextSeq))
		return fmt.Errorf("上传会话 %d 分片乱序：seq=%d, want %d", sessionID, seq, sess.nextSeq)
	}
	if sess.final {
		r.drop(sess, "FINAL 之后仍有分片")
		return fmt.Errorf("上传会话 %d 在 FINAL 之后收到分片", sessionID)
	}
	if !sess.formOK {
		// 早期形态闸：gzip 魔数（偏移 0）或未压缩 tar 的 ustar 魔数（偏移 257）
		// 之一即通过，两者都不是才弃会话（深度校验在 scanBuildContext）。
		// 判读用的字节是**拷贝**，分片本身仍原样入文件（校验窗口不截断数据流）。
		if need := buildCtxMagicWindow - len(sess.head); need > 0 {
			take := need
			if take > len(payload) {
				take = len(payload)
			}
			sess.head = append(sess.head, payload[:take]...)
		}
		if ok, decided := buildCtxForm(sess.head); decided {
			if !ok {
				r.drop(sess, "内容既不是 tar 归档也不是 gzip 压缩的 tar 归档")
				return fmt.Errorf("上传会话 %d 内容既不是 tar 归档也不是 gzip 压缩的 tar 归档", sessionID)
			}
			sess.formOK = true
			sess.head = nil // 形态已定型，窗口不再需要
		}
	}
	// 尺寸账目（defensive：core 已在它那一档上拦过，这里防的是「core 被攻陷/
	// 协议漂移」时把 agent 的盘写穿 —— 一行账目的成本，守住上传段与 build 段
	// 共用同一把尺）。上限是**两档的较大者**（不做用途分流，见
	// maxBuildCtxUploadBytes 的取舍说明）。
	sess.size += int64(len(payload))
	if sess.size > maxBuildCtxUploadBytes {
		r.drop(sess, fmt.Sprintf("上传产物超过 %s 上限", humanBytes(maxBuildCtxUploadBytes)))
		return fmt.Errorf("上传会话 %d 超过 %s 上限",
			sessionID, humanBytes(maxBuildCtxUploadBytes))
	}
	// 盘量预检（v1.2.7）：按「本片 + 预留」比对可用空间，不够就**早失败** ——
	// 而不是写到一半撞 ENOSPC（那时错误句只会说 "no space left on device"，
	// 差多少、该清什么全无从判断，半成品还占着盘）。
	if err := r.diskRoom(sess.path, int64(len(payload))); err != nil {
		r.drop(sess, err.Error())
		return fmt.Errorf("上传会话 %d：%w", sessionID, err)
	}
	if _, err := sess.f.Write(payload); err != nil {
		r.drop(sess, "写产物文件失败")
		return fmt.Errorf("上传会话 %d 写产物失败: %w", sessionID, err)
	}
	_, _ = sess.h.Write(payload)
	sess.nextSeq++
	if final {
		sess.final = true
	}
	return nil
}

// Finish 终验一个完整的组装结果：会话指纹（名字/大小/哈希）三条全对才落盘
// 承认（文件已在 transferDir 里，实现是「删除会话状态」而不是「移动文件」）。
//
// v1 无回执通道：终验失败**删产物 + 记日志**（core 的 HTTP 响应在转完时就
// 发完了，错不在自己的浏览器侧是「下一次 image:build 找不到产物文件」——
// 一秒钟可查的诚实失败，好过悄悄留下一份坏 tar 等构建时报出费解的 daemon 错）。
func (r *buildCtxReceiver) Finish(sessionID uint64, sha256Hex string, size int64, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sess, ok := r.sessions[sessionID]
	if !ok {
		return fmt.Errorf("上传会话 %d 不存在（完成帧早到或已作废）", sessionID)
	}
	if want := agentproto.DockerBuildCtxTransferName(sessionID); name != want {
		// 完成帧自述的名字必须等于推导名（协议 Validate 已拒，这里拒的是
		//「直接挂在 transport 上的手工测试/被改过的中间件」这类旁路形态）。
		r.drop(sess, "产物名与推导名不符")
		return fmt.Errorf("上传会话 %d 产物名 %q 与推导名 %q 不符", sessionID, name, want)
	}
	if !sess.final {
		r.drop(sess, "未收到 FINAL 分片")
		return fmt.Errorf("上传会话 %d 分片未收全", sessionID)
	}
	if !sess.formOK {
		// 形态窗口从未定型：短于一个校验窗口的流（既无 gzip 魔数，也看不到
		// ustar 魔数）。两种合法形态的最短长度都远超窗口，到不了的是坏输入。
		r.drop(sess, "内容既不是 tar 归档也不是 gzip 压缩的 tar 归档")
		return fmt.Errorf("上传会话 %d 内容既不是 tar 归档也不是 gzip 压缩的 tar 归档", sessionID)
	}
	if sess.size != size {
		r.drop(sess, fmt.Sprintf("尺寸不符（期望 %d, 实收 %d）", size, sess.size))
		return fmt.Errorf("上传会话 %d 尺寸不符：期望 %d, 实收 %d", sessionID, size, sess.size)
	}
	got := hex.EncodeToString(sess.h.Sum(nil))
	if got != sha256Hex {
		// 哈希终验：传送完整性由这一行背书。不匹配 = 分片路上丢字节/被改写，
		// 产物必须删 —— 「失败即重传整份」的语义从这里开始（v1 无续传）。
		r.drop(sess, fmt.Sprintf("sha256 不符（期望 %s, 实得 %s）", sha256Hex, got))
		return fmt.Errorf("上传会话 %d sha256 不符：期望 %s, 实得 %s", sessionID, sha256Hex, got)
	}
	if err := sess.f.Close(); err != nil {
		// 关不上也删：不能把「可能没 flush 完」的文件当产物留下。
		r.drop(sess, "收尾关闭失败")
		return fmt.Errorf("上传会话 %d 收尾关闭失败: %w", sessionID, err)
	}
	delete(r.sessions, sessionID)
	return nil
}

// Abort 丢弃一个会话的半成品（中止控制帧的落点）。未知会话是正常时序
// （core 的中止帧与 agent 的自弃竞态），静默返回。
func (r *buildCtxReceiver) Abort(sessionID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sess, ok := r.sessions[sessionID]; ok {
		r.drop(sess, "收到中止控制帧")
	}
}

// AbortAll 丢弃全部进行中的半成品（断连清理的落点：通道没了，完成/中止帧
// 都不会再来 —— 挂着等只会把半成品留到 24h 清扫）。
func (r *buildCtxReceiver) AbortAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sess := range r.sessions {
		r.drop(sess, "连接断开")
	}
}

// open 在 transferDir 里为会话建立产物文件（调用方持锁）。
func (r *buildCtxReceiver) open(sessionID uint64) (*buildCtxUploadSession, error) {
	dir := r.write.transferDirValue()
	if !writableDir(dir) {
		// 迥异于 image:save 的 /tmp 回退（见 write.go）：上传没有结果通道带
		// 「已回退 /tmp」的附注，回退会让 core 以为产物在 transferDir 而 build
		// 读不到 —— 诚实拒绝，让上传者从日志看到这句结论。
		return nil, fmt.Errorf("上传会话 %d：产物目录不可用（%s）", sessionID, dir)
	}
	// 盘量预检（开局那一段）：连预留都占不出来就不必开始 —— 在清扫与建文件之前
	// 就把结论说清（more=0：此时还不知道总量，见 Chunk 里按片的那一段）。
	if err := r.diskRoom(dir, 0); err != nil {
		return nil, fmt.Errorf("上传会话 %d：%w", sessionID, err)
	}
	// 这里**不再**清扫超龄产物：回收已改为定时任务表里的 docker-transfer-cleanup
	// （core 逐台派发 system:transfer-cleanup → 本文件的 sweep）。此前它挂在这里
	//（「上传是唯一产生源」），而「传完就不再传」的机器在那条形态下永远等不到清扫
	// —— 理由与代价见 buildCtxSweepAge 的说明。
	name := agentproto.DockerBuildCtxTransferName(sessionID)
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, buildCtxFileMode)
	if err != nil {
		return nil, fmt.Errorf("上传会话 %d：创建产物文件失败: %w", sessionID, err)
	}
	sess := &buildCtxUploadSession{
		id:      sessionID,
		name:    name,
		path:    path,
		nextSeq: 1,
		h:       sha256.New(),
		f:       f,
	}
	r.sessions[sessionID] = sess
	return sess, nil
}

// diskRoom 预检「path 所在的文件系统还能不能装下 more 字节 + 预留」（more=0 即只判
// 预留本身，开局那一段用）。
//
// path 既可以是产物目录本身、也可以是产物文件：statfs(2) 对两者都给出**所在文件
// 系统**的账，不需要先取父目录（先取父目录反而会对「目录本身」多剥一层，
// 在挂载点边界上算错一块盘）。
//
// 探测失败**放行**（返回 nil）：读不到盘量不是拒绝上传的理由 —— 本闸的职责是
// 「早失败并说清差多少」，不是「没有探测就不许传」；写失败那一条仍是最后兜底，
// 本闸只是把同一件事提前到**写之前**并说清差额。
func (r *buildCtxReceiver) diskRoom(path string, more int64) error {
	need := more + buildCtxDiskReserveBytes
	free, err := buildCtxFreeBytes(path)
	if err != nil {
		return nil
	}
	if free < need {
		return errors.New(diskShortText(need, free))
	}
	return nil
}

// drop 作废一个会话：删文件、关句柄、清表。**只删本会话自己的推导名文件** ——
// 名字由会话号推导保证它不会碰到 image:save/load 的用户自命产物。
func (r *buildCtxReceiver) drop(sess *buildCtxUploadSession, reason string) {
	_ = sess.f.Close()
	if err := os.Remove(sess.path); err != nil && !errors.Is(err, os.ErrNotExist) && r.log != nil {
		r.log.Warn("构建上下文半成品删除失败", "session", sess.id, "path", sess.path, "err", err.Error())
	}
	delete(r.sessions, sess.id)
	if r.log != nil {
		r.log.Warn("构建上下文会话作废", "session", sess.id, "name", sess.name, "reason", reason)
	}
}

// sweep 移除 transferDir 里 ≥24h 的 build-ctx-* 产物（返回移除条数与释放字节）。
//
// 圈定对象只看「推导名形态」：任何别的文件（save 的 .tar、用户文件）都不是
// 上传产物，碰都不碰 —— 清扫面与上传面同一把名字尺（ParseDockerBuildCtxTransferName）。
//
// 谁在驱动它：core 定时任务表里的 docker-transfer-cleanup 逐台派发的
// system:transfer-cleanup（agent 侧执行体在 runtime.go 的 cleanupTransfer）。
// 它**不再**挂在上传开头 —— 那条暗线已删（理由见 buildCtxSweepAge 的说明）：
// 「传完就不再传」的机器也必须被回收到。
//
// 释放字节在这里累加而不是让调用方回头 stat：删除是一次「已经拿在手上的事实」，
// 删完再去看文件大小只会拿到「已经不在」的错，而清理的战果（条数 + 字节）正是
// 这条指令唯一的结果。
func (r *buildCtxReceiver) sweep(dir string) (int, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	horizon := r.now().Add(-buildCtxSweepAge)
	removed := 0
	var freed int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := agentproto.ParseDockerBuildCtxTransferName(e.Name()); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(horizon) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			removed++
			freed += info.Size()
		}
	}
	return removed, freed, nil
}
