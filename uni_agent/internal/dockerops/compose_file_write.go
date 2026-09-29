package dockerops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
	"gopkg.in/yaml.v3"
)

// ── 四期：配置编辑的三条写路径（compose.file:validate/write/patch）────────────
//
// 三条 action 共用同一个收尾（spec §8 的四步闭环与 §9 的「两条保存路径，一个收尾」）：
//
//	乐观锁（base_hash 对当前文件 sha256）→ 预检（config -q）→ 备份（保留 10 份）
//	→ 原子写（同目录临时文件 + fsync + rename）→ 回读校验（sha256 与期望一致）
//
// 四条防护在这条链上各自落在哪（§8）：
//   - 路径白名单：路径**只**由 composeConfigFileOf 解析（协议上从不出现路径）；
//   - 乐观锁：base_hash 不一致 →「文件已被他人修改，请刷新」；
//   - 备份+回滚：写前落一份 <file>.bak-<令牌>，回滚只是换一个正文来源；
//   - 尺寸限制：正文/备份/合并结果都 ≤ MaxDockerComposeFileBytes（1MB）。
//
// 预检与写都走**自有临时文件**：临时文件必须与配置文件**同目录**（compose 的相对路径
// 与 .env 都按 -f 文件所在目录解析），放到 /tmp 会让预检结论与实际保存不是同一个环境。

const (
	// maxComposeBackups 是备份保留份数（§8：保留最近 10 份，多余的按令牌倒序删）。
	maxComposeBackups = 10
	// composeBackupStampLayout 是备份令牌的挂钟格式（YYYYMMDD-HHMMSS，见协议校验）。
	composeBackupStampLayout = "20060102-150405"
	// composeBackupInfix 是备份文件名里区隔令牌的中缀：<file>.bak-<令牌>。
	composeBackupInfix = ".bak-"
	// composeTempPrefix 是手术临时文件的前缀（隐藏文件，任何一条失败路径都会删掉它）。
	composeTempPrefix = ".uni_agent-compose-"
	// composeConclusionMaxRunes 是**结论句**的长度上限。
	//
	// 只约束结论句（页面直接显示）：compose 的一行错误通常 <200 字，但把一句可能很长
	// 的原文不加限制地塞进 result.error 会让页面排版失控。超限**显式**加省略号
	// （不是静默截断）；完整原文照旧进 detail（2KB 归一化由 dispatcher 做）。
	composeConclusionMaxRunes = 300
)

// doComposeFile 执行一条 compose 配置文件操作（write.go 的 Do 在这三条 action 上调用）。
func (e *WriteExecutor) doComposeFile(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	switch cmd.Action {
	case agentproto.DockerActionComposeFileValidate:
		return e.composeFileValidate(ctx, cmd)
	case agentproto.DockerActionComposeFileWrite:
		return e.composeFileWriteCmd(ctx, cmd)
	case agentproto.DockerActionComposeFilePatch:
		return e.composeFilePatchCmd(ctx, cmd)
	default:
		return nil, &ExecError{Msg: "该操作尚未开放"}
	}
}

// composeConfigPath 解析项目配置文件的绝对路径：**只能**由 composeConfigFileOf 产出
// （容器标签 → 项目索引），协议上从不接受路径入参（§8 路径白名单）。
func (e *WriteExecutor) composeConfigPath(ctx context.Context, project string) (string, error) {
	if !agentproto.IsDockerProjectName(project) {
		// 协议层已校验；这里是纵深防御（路径这条链上每个可变片段都再过一遍）。
		return "", &ExecError{Msg: "项目名不合法"}
	}
	return composeConfigFileOf(ctx, e.api, project)
}

// composeFileValidate 只做预检：把 content 写到与配置文件同目录的临时文件里跑
// `docker compose -f <临时文件> config -q`，随后删掉临时文件。
//
// 为什么用临时文件而不是把 content 喂给 stdin：compose 的 config 从 stdin 读时
// 项目目录与 .env 的解析基准会变（相对路径按 stdin 就无从谈起），而临时文件放在
// 配置文件同目录时，两者与真实保存后的运行环境一致。
func (e *WriteExecutor) composeFileValidate(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	path, err := e.composeConfigPath(ctx, cmd.Options.Target)
	if err != nil {
		return nil, err
	}
	content := []byte(cmd.Options.Content)
	if len(content) == 0 {
		return nil, &ExecError{Msg: "配置内容为空，没有可校验的文件"}
	}
	if len(content) > agentproto.MaxDockerComposeFileBytes {
		return nil, &ExecError{Msg: "配置内容超过 1MB 上限"}
	}
	if err := e.composePrecheck(ctx, path, content); err != nil {
		return nil, err
	}
	// 校验通过不产生数据面：预检的结论就是「通过」本身。
	return nil, nil
}

// composeFileWriteCmd 全文写（content）或回滚（backup 读取 <file>.bak-<令牌>）。
func (e *WriteExecutor) composeFileWriteCmd(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	o := &cmd.Options
	path, err := e.composeConfigPath(ctx, o.Target)
	if err != nil {
		return nil, err
	}
	cur, err := readComposeFileAt(path)
	if err != nil {
		return nil, err
	}
	if err := e.checkComposeBaseHash(path, cur, o.BaseHash); err != nil {
		return nil, err
	}
	var next []byte
	if o.Backup != "" {
		next, err = readComposeBackup(path, o.Backup)
		if err != nil {
			return nil, err
		}
	} else {
		next = []byte(o.Content)
		if len(next) == 0 {
			return nil, &ExecError{Msg: "配置内容为空，没有可写的内容"}
		}
		if len(next) > agentproto.MaxDockerComposeFileBytes {
			return nil, &ExecError{Msg: "配置内容超过 1MB 上限"}
		}
	}
	return e.commitComposeFile(ctx, path, next)
}

// composeFilePatchCmd 只按键合并被触碰的内容（yaml.v3 Node API 定位 → 原文手术），
// 未触碰的服务/段落（含注释）逐字节保留；合并后走与 write 完全相同的收尾。
func (e *WriteExecutor) composeFilePatchCmd(ctx context.Context, cmd *agentproto.DockerCmd) ([]byte, error) {
	o := &cmd.Options
	path, err := e.composeConfigPath(ctx, o.Target)
	if err != nil {
		return nil, err
	}
	cur, err := readComposeFileAt(path)
	if err != nil {
		return nil, err
	}
	if err := e.checkComposeBaseHash(path, cur, o.BaseHash); err != nil {
		return nil, err
	}
	next, err := applyComposePatch(cur, o.Patch)
	if err != nil {
		return nil, err
	}
	if len(next) > agentproto.MaxDockerComposeFileBytes {
		return nil, &ExecError{Msg: "改后的配置超过 1MB 上限"}
	}
	return e.commitComposeFile(ctx, path, next)
}

// ── 收尾：预检 → 备份 → 原子写 → 回读 ────────────────────────────────────

// commitComposeFile 是所有写路径的唯一收尾（§9）。
//
// 成功返回**回读后的**完整载荷：保存以 agent 回读的内容为准（唯一事实源），前端据此
// 刷新乐观锁基线 hash 与备份历史。
func (e *WriteExecutor) commitComposeFile(ctx context.Context, path string, next []byte) ([]byte, error) {
	if err := e.composePrecheck(ctx, path, next); err != nil {
		return nil, err
	}
	if _, err := e.backupComposeFile(path); err != nil {
		return nil, err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		// 保留原文件的权限位：目标是宿主机的部署文件，改权限是另一回事。
		mode = fi.Mode().Perm()
	}
	if err := writeFileAtomic(path, next, mode); err != nil {
		return nil, err
	}
	// 回读校验：rename 之后再读一遍算 sha256 与期望比对。rename 是原子的，故这里
	// 失败时文件已经是完整的新内容（不是半成品）——不一致只可能来自外部并发改写，
	// 如实报错让用户重新载入。
	back, err := os.ReadFile(path)
	if err != nil {
		return nil, &ExecError{Msg: "写入后回读失败", Detail: err.Error()}
	}
	want := sha256Hex(next)
	if got := sha256Hex(back); got != want {
		return nil, &ExecError{Msg: "写入后校验失败：文件内容与预期不一致，请刷新后重试",
			Detail: fmt.Sprintf("want %s got %s", want, got)}
	}
	backups, err := listComposeBackups(path)
	if err != nil {
		// 备份清单是展示数据：保存**已经成功**，不能因为列表读不到把它翻成失败。
		e.setNote("备份列表读取失败：" + err.Error())
		backups = nil
	}
	return composeFilePayload(back, path, backups)
}

// composeFilePayload 把回读内容折成协议载荷。正文大到会撑爆 result.payload 上限时
// 只回 hash（保存已成功，基线 hash 才是下一次保存的关键；正文可由 read 再取）。
//
// 为什么会有这个分支：compose 文件上限 1MB（§13），而 result.payload 上限 256KB
// （协议 MaxDockerPayloadBytes）——不设这道闸的话，大文件的写入**成功**会在 core
// 解码结果时被整帧拒掉，用户看到的是「超时」而不是「已保存」。
func composeFilePayload(content []byte, path string, backups []agentproto.DockerComposeBackup) ([]byte, error) {
	p := &agentproto.DockerComposeFilePayload{
		Content: string(content),
		Hash:    sha256Hex(content),
		Path:    path,
		Backups: backups,
	}
	out, err := marshalWritePayload(p)
	if err != nil {
		return nil, err
	}
	if len(out) > agentproto.MaxDockerPayloadBytes {
		p.Content = ""
		if out, err = marshalWritePayload(p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkComposeBaseHash 是乐观锁：base_hash 必须等于当前文件内容的 sha256。
//
// 结论句与 spec §8 一字不差（用户可照做：刷新）；当前 hash 进 detail 便于排障
// （是否真的被别人改过、还是客户端发错了基线）。
func (e *WriteExecutor) checkComposeBaseHash(path string, cur []byte, base string) error {
	got := sha256Hex(cur)
	if base == got {
		return nil
	}
	return &ExecError{Msg: "文件已被他人修改，请刷新", Detail: "current hash: " + got}
}

// composePrecheck 把 content 落到配置文件同目录的临时文件里跑 `config -q`。
//
// argv 固定、零 shell（照 composeBinary 的 flavor 纪律）：plugin 形态是
// `docker compose -f <tmp> config -q`，独立二进制/v1 形态是 `docker-compose -f <tmp> config -q`。
// 校验失败时**透传 stderr 首行**作为结论句（用户要的就是那一行），完整输出进 detail。
func (e *WriteExecutor) composePrecheck(ctx context.Context, path string, content []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, composeTempPrefix+filepath.Base(path)+"-*")
	if err != nil {
		return &ExecError{Msg: "配置文件所在目录不可写，无法校验", Detail: err.Error()}
	}
	tmp := f.Name()
	// 所有返回路径都删临时文件：校验的临时正文既不是产物，也不该在宿主上留痕。
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(content); err != nil {
		f.Close()
		return &ExecError{Msg: "准备校验文件失败", Detail: err.Error()}
	}
	if err := f.Close(); err != nil {
		return &ExecError{Msg: "准备校验文件失败", Detail: err.Error()}
	}
	bin, err := composeBinary(e.flavorValue())
	if err != nil {
		return err
	}
	argv := []string{"-f", tmp, "config", "-q"}
	if e.flavorValue() == agentproto.DockerComposeFlavorPlugin {
		argv = append([]string{"compose"}, argv...)
	}
	out, err := e.exec(ctx, bin, argv...).CombinedOutput()
	if err != nil {
		line := firstNonEmptyLine(out)
		if line == "" {
			line = err.Error()
		}
		return &ExecError{Msg: clipConclusion(line), Detail: strings.TrimSpace(string(out))}
	}
	return nil
}

// ── 备份：落盘、列表、保留 10 份、回滚读取 ────────────────────────────────

// composeBackupPath 由配置文件路径 + 令牌拼备份路径。令牌经协议校验与本层复核，
// 只含数字与一个短横 —— 路径拼接不可能逃出配置文件所在目录。
func composeBackupPath(path, token string) string {
	return path + composeBackupInfix + token
}

// readComposeFileAt 读当前配置文件（先看大小再读：超限不进内存，§13）。
func readComposeFileAt(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
	}
	if fi.Size() > agentproto.MaxDockerComposeFileBytes {
		return nil, &ExecError{Msg: "配置文件过大（超过 1MB）"}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &ExecError{Msg: "读取配置文件失败", Detail: err.Error()}
	}
	return b, nil
}

// readComposeBackup 读回滚来源：<file>.bak-<令牌>。
//
// 令牌再校验一次（协议层已拦）：agent 是最后一道，而这条路径直接拼文件名。
func readComposeBackup(path, token string) ([]byte, error) {
	if !agentproto.IsDockerBackupToken(token) {
		return nil, &ExecError{Msg: "备份令牌不合法"}
	}
	bp := composeBackupPath(path, token)
	fi, err := os.Stat(bp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &ExecError{Msg: "找不到这份备份（它可能已被清理），请刷新备份列表"}
		}
		return nil, &ExecError{Msg: "读取备份失败", Detail: err.Error()}
	}
	if fi.Size() > agentproto.MaxDockerComposeFileBytes {
		return nil, &ExecError{Msg: "备份文件过大（超过 1MB）"}
	}
	b, err := os.ReadFile(bp)
	if err != nil {
		return nil, &ExecError{Msg: "读取备份失败", Detail: err.Error()}
	}
	return b, nil
}

// backupComposeFile 把当前文件落一份备份，并按令牌倒序保留最近 maxComposeBackups 份。
//
// 令牌取 agent 挂钟（秒级），但**不早于最新已有令牌**：同秒内连续保存向后顺延一秒，
// 而「最早那份已被清理」留下的空洞不会被复用 —— 复用会让新备份的令牌小于旧备份，
// 时间倒序的列表因此骗人（新备份排到旧备份后面），也会覆盖清理后的同名文件。
func (e *WriteExecutor) backupComposeFile(path string) (string, error) {
	cur, err := os.ReadFile(path)
	if err != nil {
		return "", &ExecError{Msg: "备份配置文件失败", Detail: err.Error()}
	}
	t := e.nowValue().Truncate(time.Second)
	if tokens, err := composeBackupNames(path); err == nil && len(tokens) > 0 {
		// 倒序第一份是最新令牌；它不早于当前挂钟时，从它的下一秒起算。
		if newest, err := time.ParseInLocation(composeBackupStampLayout, tokens[0], t.Location()); err == nil &&
			!newest.Before(t) {
			t = newest.Add(time.Second)
		}
	}
	token := t.Format(composeBackupStampLayout)
	for fileExists(composeBackupPath(path, token)) {
		// 兜底（令牌解析失败等异常形态）：顺延到第一个空闲秒，绝不覆盖同名备份。
		t = t.Add(time.Second)
		token = t.Format(composeBackupStampLayout)
	}
	// 备份可能是「配置里带着口令」的敏感文件：0600（严格于原文的常见 0644）。
	if err := writeFileAtomic(composeBackupPath(path, token), cur, 0o600); err != nil {
		return "", err
	}
	if err := pruneComposeBackups(path); err != nil {
		// 旧备份清理失败不阻断保存（数据没丢，只是多留了几份）；留一条附注。
		e.setNote("旧备份清理失败：" + err.Error())
	}
	return token, nil
}

// composeBackupNames 返回同目录下全部合法备份的令牌（**倒序**：新的在前）。
func composeBackupNames(path string) ([]string, error) {
	dir := filepath.Dir(path)
	prefix := filepath.Base(path) + composeBackupInfix
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	tokens := make([]string, 0, maxComposeBackups)
	for _, ent := range entries {
		if ent.IsDir() || ent.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := ent.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		token := strings.TrimPrefix(name, prefix)
		if !agentproto.IsDockerBackupToken(token) {
			// 不像令牌的同前缀文件不是我们的备份：不列、不删（不碰不认识的文件）。
			continue
		}
		tokens = append(tokens, token)
	}
	// 令牌是定宽挂钟格式，字典序即时间序；倒序 = 新的在前。
	sort.Sort(sort.Reverse(sort.StringSlice(tokens)))
	return tokens, nil
}

// listComposeBackups 扫描备份并补齐 hash/size/at（最多 maxComposeBackups 份）。
//
// 单份读不出来时**跳过该份**（它也无法用于回滚），不让整张列表失败；目录读不到
// 才返回错误（那是环境问题，调用方决定怎么呈现）。
func listComposeBackups(path string) ([]agentproto.DockerComposeBackup, error) {
	tokens, err := composeBackupNames(path)
	if err != nil {
		return nil, err
	}
	if len(tokens) > maxComposeBackups {
		tokens = tokens[:maxComposeBackups]
	}
	out := make([]agentproto.DockerComposeBackup, 0, len(tokens))
	for _, token := range tokens {
		bp := composeBackupPath(path, token)
		fi, err := os.Stat(bp)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(bp)
		if err != nil {
			continue
		}
		out = append(out, agentproto.DockerComposeBackup{
			Token: token, Hash: sha256Hex(b), SizeBytes: fi.Size(), At: fi.ModTime().Unix(),
		})
	}
	return out, nil
}

// pruneComposeBackups 按令牌倒序保留最近 maxComposeBackups 份，多余的删除。
func pruneComposeBackups(path string) error {
	tokens, err := composeBackupNames(path)
	if err != nil {
		return err
	}
	if len(tokens) <= maxComposeBackups {
		return nil
	}
	for _, token := range tokens[maxComposeBackups:] {
		if err := os.Remove(composeBackupPath(path, token)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ── 原子写 ──────────────────────────────────────────────────────────────

// writeFileAtomic 同目录临时文件 → 写 → fsync → rename。
//
// rename 之前任何失败都走 defer 删临时文件，原文件**一个字节都没动**（不留半成品）。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, composeTempPrefix+filepath.Base(path)+"-*")
	if err != nil {
		return &ExecError{Msg: "配置文件所在目录不可写", Detail: err.Error()}
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if mode == 0 {
		mode = 0o644
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return &ExecError{Msg: "写入配置文件失败", Detail: err.Error()}
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return &ExecError{Msg: "写入配置文件失败", Detail: err.Error()}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return &ExecError{Msg: "写入配置文件失败", Detail: err.Error()}
	}
	if err := f.Close(); err != nil {
		return &ExecError{Msg: "写入配置文件失败", Detail: err.Error()}
	}
	if err := os.Rename(tmp, path); err != nil {
		return &ExecError{Msg: "替换配置文件失败", Detail: err.Error()}
	}
	cleanup = false
	return nil
}

// ── 小工具 ──────────────────────────────────────────────────────────────

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// firstNonEmptyLine 取合并输出的第一条非空行（trim 后）。
//
// 为什么是「首行」：compose 把结论放在第一行，其后常跟着提示与上下文 —— 结论句只要
// 那一行，其余进 detail（2KB 归一化）。
func firstNonEmptyLine(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// clipConclusion 给结论句封顶（显式省略号：不是静默截断）。
func clipConclusion(s string) string {
	r := []rune(s)
	if len(r) <= composeConclusionMaxRunes {
		return s
	}
	return string(r[:composeConclusionMaxRunes]) + "…"
}

// ── 补丁引擎：yaml.v3 Node 定位 + 原文手术 ───────────────────────────────
//
// 为什么不是「解析成 Node → 改 Node → 重新编码」：yaml.v3 的编码器会把整份文档
// 重新排版（空行丢失、缩进归一、流式序列被改写），而未触碰段落必须**逐字节保留**
//（§9：patch 的语义价值就在于「只动被改过的键」）。故这里用 Node 只做**定位**
//（每个键/值在原文里的行列），真正的修改是在原字节上做区间手术：
//
//   - 改一个键的值：只替换那个值的字节区间（同行的行尾注释因此原样留下）；
//   - 删一个键/服务（值为 null）：删掉该条目的整段（含紧贴的注释行）；
//   - 新增：在容器的最后一个子条目之后插入新行。
//
// 任何一处没把握的形态（流式映射、空容器）走整段重建的兜底：只重排被触碰的那一段，
// 其余字节仍然不动。

// composePatchEdit 是一处原文手术：替换 [start,end) 为 text（空串 = 删除）。
type composePatchEdit struct {
	start, end int
	text       string
}

// composeText 是原文的行索引（手术全部基于它，避免每处都自己数行号）。
type composeText struct {
	src   []byte
	lines []composeLine
}

// composeLine 记一行的字节区间；next = 下一行起点（含换行，故切片区间恰好吃掉换行）。
type composeLine struct {
	start, end, next int
}

func newComposeText(src []byte) *composeText {
	t := &composeText{src: src}
	start := 0
	for i := 0; i < len(src); i++ {
		if src[i] != '\n' {
			continue
		}
		end := i
		if end > start && src[end-1] == '\r' {
			end--
		}
		t.lines = append(t.lines, composeLine{start: start, end: end, next: i + 1})
		start = i + 1
	}
	if start < len(src) {
		// 最后一行没有换行符：next 指向文件末尾。
		t.lines = append(t.lines, composeLine{start: start, end: len(src), next: len(src)})
	}
	return t
}

// line 取第 ln 行（1-based，与 yaml.Node 的行号口径一致）；越界时钳到边界。
func (t *composeText) line(ln int) composeLine {
	if ln < 1 {
		ln = 1
	}
	if ln > len(t.lines) {
		ln = len(t.lines)
	}
	if len(t.lines) == 0 {
		return composeLine{start: len(t.src), end: len(t.src), next: len(t.src)}
	}
	return t.lines[ln-1]
}

func (t *composeText) lineBytes(ln int) []byte {
	l := t.line(ln)
	return t.src[l.start:l.end]
}

// indentOf 数行首空格（YAML 的缩进只可能是空格：制表符会让解析失败）。
func indentOf(b []byte) int {
	n := 0
	for n < len(b) && b[n] == ' ' {
		n++
	}
	return n
}

func isBlankLine(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}

func isCommentLine(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\t' {
			continue
		}
		return c == '#'
	}
	return false
}

// blockEndLine 求从 fromLine 起、缩进深于 keyIndent 的最后一行（空行不结束块，
// 但只有后面的行确实更深时才被吸收 —— 条目之间的空行不属于任何一侧）。
func (t *composeText) blockEndLine(fromLine, keyIndent int) int {
	last := fromLine
	for ln := fromLine + 1; ln <= len(t.lines); ln++ {
		b := t.lineBytes(ln)
		if isBlankLine(b) {
			continue
		}
		if indentOf(b) > keyIndent {
			last = ln
			continue
		}
		break
	}
	return last
}

// headStartLine 求条目起点行：yaml 把紧贴上方的注释行挂在 key 的 HeadComment 上，
// 按它给出的行数上溯（并逐行复核确实是注释，防 Node 信息与实际文本错位）。
func (t *composeText) headStartLine(key *yaml.Node) int {
	n := commentLineCount(key.HeadComment)
	start := key.Line
	for i := 0; i < n && start > 1; i++ {
		if !isCommentLine(t.lineBytes(start - 1)) {
			break
		}
		start--
	}
	return start
}

// entryEndLine 求条目的最后一行：值块的最后一行 + yaml 明确挂在条目子树上的脚注注释行。
func (t *composeText) entryEndLine(key, val *yaml.Node) int {
	end := t.blockEndLine(key.Line, key.Column-1)
	foot := map[string]bool{}
	collectFootComments(key, foot)
	collectFootComments(val, foot)
	if len(foot) == 0 {
		return end
	}
	for ln := end + 1; ln <= len(t.lines); ln++ {
		b := t.lineBytes(ln)
		if !isCommentLine(b) || !foot[strings.TrimSpace(string(b))] {
			break
		}
		end = ln
	}
	return end
}

// deleteEntryEdit 生成「删掉这个条目」的手术：从注释头到条目末行（含换行）。
func (t *composeText) deleteEntryEdit(key, val *yaml.Node) composePatchEdit {
	start := t.line(t.headStartLine(key)).start
	end := t.line(t.entryEndLine(key, val)).next
	return composePatchEdit{start: start, end: end}
}

// valueSpan 求一个键的值的字节区间。返回 blockMode=true 时区间从键行的冒号之后
// 开始（替换整块值，键 token 与键行上的注释保留）；false 时只替换同行上的值 token
// （行尾注释也因此原样留下）。
func (t *composeText) valueSpan(key, val *yaml.Node) (start, end int, blockMode bool) {
	if subtreeMaxLine(val) > key.Line || isBlockScalarNode(val) {
		// 值跨行（块映射/块序列/多行流式/字面量标量）：整块替换。
		return t.colonEnd(key, val), t.line(t.blockEndLine(key.Line, key.Column-1)).next, true
	}
	return t.valueTokenStart(key, val), t.valueTokenEnd(key.Line, val), false
}

// subtreeMaxLine 求子树里节点的最大行号：单行值与「值后面跟着更深缩进的注释」都满足
// == key.Line，从而不会把纯注释行误判成值的一部分（不然替换整块会把注释一并吃掉）。
func subtreeMaxLine(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	max := n.Line
	for _, c := range n.Content {
		if m := subtreeMaxLine(c); m > max {
			max = m
		}
	}
	return max
}

// valueTokenStart 是同行值的起点（列号直接换算成字节偏移）。
func (t *composeText) valueTokenStart(key, val *yaml.Node) int {
	return t.line(key.Line).start + (val.Column - 1)
}

// colonEnd 是键行上「值之前」的字节偏移。
//
//   - 值在同一行：从值 token 往前跳过空白，落在冒号之后；这样替换整块值时能把旧的
//     ` 值`（含前置空格）一起吃掉，而键 token 不动；
//   - 值在下一行（块值）：落在键行末尾；键行上的注释（`db: # 注释`）因此原样保留。
func (t *composeText) colonEnd(key, val *yaml.Node) int {
	l := t.line(key.Line)
	if val != nil && val.Line == key.Line {
		i := t.valueTokenStart(key, val)
		for i > l.start && (t.src[i-1] == ' ' || t.src[i-1] == '\t') {
			i--
		}
		if i > l.start && t.src[i-1] == ':' {
			return i
		}
		return t.valueTokenStart(key, val)
	}
	return l.end
}

// valueTokenEnd 求同行值的 token 末尾：引号/流式括号成对匹配，普通标量止于
// 「空白 + #」（行尾注释）或行尾。
func (t *composeText) valueTokenEnd(ln int, val *yaml.Node) int {
	l := t.line(ln)
	start := l.start + (val.Column - 1)
	if start < l.start || start > l.end {
		return l.end
	}
	switch {
	case val.Style&yaml.DoubleQuotedStyle != 0 || val.Style&yaml.SingleQuotedStyle != 0:
		return t.quotedEnd(start, val.Style&yaml.DoubleQuotedStyle != 0)
	case val.Kind == yaml.SequenceNode || val.Kind == yaml.MappingNode:
		return t.flowEnd(start)
	default:
		return plainScalarEnd(t.src, start, l.end)
	}
}

// quotedEnd 找闭合引号（双引号里的 \" 转义；单引号里的 ” 是转义的单引号）。
func (t *composeText) quotedEnd(start int, double bool) int {
	src := t.src
	q := src[start]
	i := start + 1
	for i < len(src) {
		c := src[i]
		if double && c == '\\' {
			i += 2
			continue
		}
		if !double && c == '\'' && i+1 < len(src) && src[i+1] == '\'' {
			i += 2
			continue
		}
		if c == q {
			return i + 1
		}
		i++
	}
	return len(src)
}

// flowEnd 从流式集合的起始括号扫到配对括号（跳过内部的引号串）。
func (t *composeText) flowEnd(start int) int {
	src := t.src
	if start >= len(src) {
		return len(src)
	}
	open := src[start]
	closeCh := byte(']')
	if open == '{' {
		closeCh = '}'
	}
	depth := 0
	for i := start; i < len(src); i++ {
		c := src[i]
		switch c {
		case '\'', '"':
			i = t.quotedEnd(i, c == '"') - 1
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(src)
}

// plainScalarEnd 求普通标量的末尾：行内第一个「空白 + #」是注释起点，其余到行尾
// （尾随空白不属于 token）。
func plainScalarEnd(src []byte, start, lineEnd int) int {
	end := lineEnd
	for i := start; i < lineEnd; i++ {
		if src[i] != ' ' && src[i] != '\t' {
			continue
		}
		j := i
		for j < lineEnd && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		if j < lineEnd && src[j] == '#' {
			end = i
			break
		}
	}
	for end > start && (src[end-1] == ' ' || src[end-1] == '\t') {
		end--
	}
	return end
}

func isBlockScalarNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0
}

// collectFootComments 收集一棵子树里所有节点自带的脚注注释行（trim 后的原文形态）。
func collectFootComments(n *yaml.Node, out map[string]bool) {
	if n == nil {
		return
	}
	for _, line := range strings.Split(n.FootComment, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out[s] = true
		}
	}
	for _, c := range n.Content {
		collectFootComments(c, out)
	}
}

func commentLineCount(comment string) int {
	if strings.TrimSpace(comment) == "" {
		return 0
	}
	return len(strings.Split(comment, "\n"))
}

// isBlockMappingNode 报告节点是不是**块状**映射（流式与空值都不走逐键手术，
// 由 rebuildContainerEdit 整段重建）。
func isBlockMappingNode(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.MappingNode && n.Style&yaml.FlowStyle == 0
}

// mappingChildIndent 取块映射子键的缩进（第一个子键的列号；空映射给 -1）。
func mappingChildIndent(m *yaml.Node) int {
	if m == nil || len(m.Content) == 0 {
		return -1
	}
	return m.Content[0].Column - 1
}

// findMapEntry 在映射节点里按键名找键/值节点对。
func findMapEntry(m *yaml.Node, name string) (key, val *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// mappingAppendPoint 是块映射「追加一个子条目」的字节位置：最后一个子条目之后。
func (t *composeText) mappingAppendPoint(m *yaml.Node) int {
	if m == nil || len(m.Content) < 2 {
		return -1
	}
	key := m.Content[len(m.Content)-2]
	val := m.Content[len(m.Content)-1]
	return t.line(t.entryEndLine(key, val)).next
}

// applyComposePatch 把补丁合并进原文，返回新文本（未触碰段落逐字节保留）。
func applyComposePatch(src []byte, patch map[string]any) ([]byte, error) {
	if len(patch) == 0 {
		return src, nil
	}
	for section := range patch {
		if section != "services" && section != "networks" && section != "volumes" {
			return nil, &ExecError{Msg: "补丁包含不支持的段落：" + section}
		}
	}
	if len(src) == 0 {
		// 空文件没有可合并的骨架（新增段落需要挂在顶层映射上）。
		return nil, &ExecError{Msg: "配置文件是空的，没有可合并的内容"}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, &ExecError{Msg: "配置文件不是合法的 YAML，请切到 YML 模式修复", Detail: err.Error()}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, &ExecError{Msg: "配置文件的最外层不是映射，无法按键合并"}
	}
	root := doc.Content[0]
	t := newComposeText(src)
	edits := make([]composePatchEdit, 0, 8)
	// insertions 按插入点聚合：同一位置的多个新条目必须一次插入（否则后一个的偏移失效）。
	insertions := map[int]*strings.Builder{}
	addInsert := func(point int, text string) {
		if point < 0 {
			return
		}
		if insertions[point] == nil {
			insertions[point] = &strings.Builder{}
		}
		insertions[point].WriteString(text)
		insertions[point].WriteString("\n")
	}

	for _, section := range []string{"services", "networks", "volumes"} {
		raw, ok := patch[section]
		if !ok {
			continue
		}
		entries, ok := raw.(map[string]any)
		if !ok {
			return nil, &ExecError{Msg: "补丁段落 " + section + " 的形态不正确"}
		}
		skey, sval := findMapEntry(root, section)
		if skey == nil {
			// 整段新增：追加到文件末尾（顶层映射的一个新键）。
			text := section + ":\n" + renderMappingBlock(entries, 2)
			if len(src) > 0 && src[len(src)-1] != '\n' {
				text = "\n" + text
			}
			addInsert(len(src), strings.TrimSuffix(text, "\n"))
			continue
		}
		if !isBlockMappingNode(sval) {
			edit, err := rebuildContainerEdit(t, skey, sval, entries)
			if err != nil {
				return nil, err
			}
			edits = append(edits, edit)
			continue
		}
		childIndent := mappingChildIndent(sval)
		if childIndent < 0 {
			childIndent = skey.Column - 1 + 2
		}
		for _, name := range sortedAnyKeys(entries) {
			pv := entries[name]
			ckey, cval := findMapEntry(sval, name)
			if pv == nil {
				if ckey != nil {
					edits = append(edits, t.deleteEntryEdit(ckey, cval))
				}
				continue
			}
			fields, ok := pv.(map[string]any)
			if !ok {
				return nil, &ExecError{Msg: "补丁条目 " + section + "/" + name + " 的形态不正确"}
			}
			if ckey == nil {
				// 整条新增（如新服务）：按该映射现有子键的缩进插入。
				addInsert(t.mappingAppendPoint(sval), renderEntry(name, fields, childIndent))
				continue
			}
			if !isBlockMappingNode(cval) {
				edit, err := rebuildContainerEdit(t, ckey, cval, fields)
				if err != nil {
					return nil, err
				}
				edits = append(edits, edit)
				continue
			}
			fieldIndent := mappingChildIndent(cval)
			if fieldIndent < 0 {
				fieldIndent = ckey.Column - 1 + 2
			}
			for _, field := range sortedAnyKeys(fields) {
				fv := fields[field]
				fkey, fval := findMapEntry(cval, field)
				if fv == nil {
					if fkey != nil {
						edits = append(edits, t.deleteEntryEdit(fkey, fval))
					}
					continue
				}
				if fkey == nil {
					addInsert(t.mappingAppendPoint(cval), renderEntry(field, fv, fieldIndent))
					continue
				}
				edit, err := t.replaceValueEdit(fkey, fval, fv)
				if err != nil {
					return nil, err
				}
				edits = append(edits, edit)
			}
		}
	}
	for point, b := range insertions {
		edits = append(edits, composePatchEdit{start: point, end: point, text: b.String()})
	}
	return applyComposeEdits(t, edits)
}

// replaceValueEdit 生成「改一个键的值」的手术。
//
// 三条分支对应三类形态迁移：
//   - 新值是集合：从冒号后整块替换（键 token 留下），旧值/行尾注释随旧值让位 ——
//     整块重排是不可避免的（YAML 的集合必须是块或流式，不能再挂回同行标量的位置）；
//   - 旧值跨行、新值是标量：也从冒号后整块替换，新标量另起一行（键行上的注释保留）；
//   - 两边都是同行标量：只替换值 token（缩进、行尾注释原样留下）。
func (t *composeText) replaceValueEdit(key, val *yaml.Node, next any) (composePatchEdit, error) {
	if isCollectionValue(next) {
		start := t.colonEnd(key, val)
		end := t.line(t.blockEndLine(key.Line, key.Column-1)).next
		rendered, err := renderValueBlock(next, key.Column-1+2)
		if err != nil {
			return composePatchEdit{}, err
		}
		// 首尾各一个换行：被替换区间以「键行换行」开头、以「块尾换行」结尾，替换
		// 前后行结构一致（条目之间的空行不会被无声吃掉）。
		return composePatchEdit{start: start, end: end, text: "\n" + rendered + "\n"}, nil
	}
	start, end, blockMode := t.valueSpan(key, val)
	if blockMode {
		// 跨行值换成标量：新标量另起一行（缩进与块值同级），键行上的注释保留。
		rendered, err := renderValueBlock(next, key.Column-1+2)
		if err != nil {
			return composePatchEdit{}, err
		}
		return composePatchEdit{start: start, end: end, text: "\n" + rendered + "\n"}, nil
	}
	text, err := marshalInlineValue(next)
	if err != nil {
		return composePatchEdit{}, err
	}
	return composePatchEdit{start: start, end: end, text: text}, nil
}

// rebuildContainerEdit 是流式/空容器的兜底：把现有内容解码成 Go 值、叠加补丁、
// 整段重渲染被触碰的这一个容器（其余字节不动）。
func rebuildContainerEdit(t *composeText, key, val *yaml.Node, entries map[string]any) (composePatchEdit, error) {
	merged := map[string]any{}
	if val != nil && val.Kind == yaml.MappingNode {
		if err := val.Decode(&merged); err != nil {
			return composePatchEdit{}, &ExecError{Msg: "这段配置无法解析，请切到 YML 模式编辑", Detail: err.Error()}
		}
	} else if val == nil || val.Tag != "!!null" {
		return composePatchEdit{}, &ExecError{Msg: "这段配置的形态无法按键合并，请切到 YML 模式编辑"}
	}
	for _, name := range sortedAnyKeys(entries) {
		if entries[name] == nil {
			delete(merged, name)
			continue
		}
		merged[name] = entries[name]
	}
	start := t.colonEnd(key, val)
	end := t.line(t.blockEndLine(key.Line, key.Column-1)).next
	rendered := renderMappingBlock(merged, key.Column-1+2)
	if rendered == "" {
		return composePatchEdit{start: start, end: end, text: " {}\n"}, nil
	}
	return composePatchEdit{start: start, end: end, text: "\n" + rendered + "\n"}, nil
}

// applyComposeEdits 应用手术：按起点降序（后面的编辑不影响前面的偏移），且断言
// 区间互不重叠 —— 重叠说明定位逻辑出错，宁可报错也不产出一份可疑的文件。
func applyComposeEdits(t *composeText, edits []composePatchEdit) ([]byte, error) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	lastStart := len(t.src) + 1
	out := append([]byte(nil), t.src...)
	for _, e := range edits {
		if e.start < 0 || e.end < e.start || e.end > len(t.src) {
			return nil, &ExecError{Msg: "合并补丁失败（内部定位越界）"}
		}
		if e.end > lastStart {
			return nil, &ExecError{Msg: "合并补丁失败（内部区间重叠）"}
		}
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
		lastStart = e.start
	}
	return out, nil
}

// ── 值渲染（yaml 编码，键序稳定）─────────────────────────────────────────

// marshalYAMLValue 把 JSON 解出的 Go 值编码成 YAML 文本（去掉结尾换行）。
// 顶层用 2 空格缩进：新增的行与绝大多数 compose 文件的写法一致。
func marshalYAMLValue(v any) (string, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(sb.String(), "\n"), nil
}

// marshalInlineValue 渲染同行标量（单行，无换行）。
func marshalInlineValue(v any) (string, error) {
	text, err := marshalYAMLValue(v)
	if err != nil {
		return "", err
	}
	if strings.Contains(text, "\n") {
		// 集合被当成标量替换是上游判断错误：包成流式形态也不该发生，退回 YAML 流式。
		return strings.ReplaceAll(text, "\n", " "), nil
	}
	return text, nil
}

// isCollectionValue 报告值需不需要跨行（JSON 的数组/对象）。
func isCollectionValue(v any) bool {
	switch v.(type) {
	case []any, map[string]any:
		return true
	default:
		return false
	}
}

// renderEntry 渲染一个「键: 值」条目（缩进 indent）；集合值落到下一层。
//
// 判定用**值类型**而不是「编码后有没有换行」：单键映射（`{driver: bridge}`）编码出来
// 也只有一行，若按行数判断就会被写成 `app-net: driver: bridge`（无效 YAML）。
func renderEntry(key string, v any, indent int) string {
	text, err := marshalYAMLValue(v)
	if err != nil {
		// 值来自 JSON，yaml 编码不会失败；真失败时退化为空对象并继续（有 precheck 兜底）。
		text = "{}"
	}
	if !isCollectionValue(v) || text == "{}" || text == "[]" {
		return spaces(indent) + key + ": " + text
	}
	return spaces(indent) + key + ":\n" + indentBlock(text, indent+2)
}

// renderValueBlock 渲染一个「跟在 key: 后面」的块值（首行缩进 indent）。
func renderValueBlock(v any, indent int) (string, error) {
	text, err := marshalYAMLValue(v)
	if err != nil {
		return "", &ExecError{Msg: "补丁里的值无法编码成 YAML"}
	}
	return indentBlock(text, indent), nil
}

// renderMappingBlock 渲染一批「名字 → 值」，按名字排序（输出稳定，便于 diff）。
func renderMappingBlock(entries map[string]any, indent int) string {
	lines := make([]string, 0, len(entries))
	for _, name := range sortedAnyKeys(entries) {
		if entries[name] == nil {
			continue
		}
		lines = append(lines, renderEntry(name, entries[name], indent))
	}
	return strings.Join(lines, "\n")
}

// indentBlock 给多行文本的每一行加 indent 个空格前缀。
func indentBlock(text string, indent int) string {
	pad := spaces(indent)
	lines := strings.Split(text, "\n")
	for i := range lines {
		if lines[i] == "" {
			continue
		}
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

func sortedAnyKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
