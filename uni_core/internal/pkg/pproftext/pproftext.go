package pproftext

// 本文件把 runtime/pprof 的 text 输出（p.WriteTo(w, 1)）解析为火焰图树与热点函数表。
//
// 为什么不逐行解析 "#\t0x…\tfunc+0x…\tfile:line" 帧行：该输出对不同长度函数名
// 使用了变长的制表符填充，字段边界不稳定；且打印时会隐藏开头的 runtime.* 帧，
// pc 与帧行无法一一对应。而解析 "@" 采样行拿到 pc 列表后，可直接用
// runtime.FuncForPC 在本进程内解析出完整、无裁剪的调用栈（含 runtime 帧），
// 稳定且信息更全。
//
// 各 profile 文本格式（以实际输出为准）：
//   - goroutine/threadcreate 头："goroutine profile: total N"，采样行 "<count> @ 0x…"
//   - heap/allocs 头：    "heap profile: a: b [c: d] @ heap/rate"，
//     采样行 "<alloc_objs>: <alloc_bytes> [<inuse_objs>: <inuse_bytes>] @ 0x…"
//   - block/mutex 头：    "--- contention:"/"--- mutex:" + "cycles/second=N"，
//     采样行 "<count> <cycles> @ 0x…"
//   - 采样行 pc 顺序均为叶在前；0x0 为无效帧需剔除。

import (
	"bufio"
	"bytes"
	"fmt"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
)

// ── profile 元信息 ────────────────────────────────────────────

type profileFamily int

const (
	familyCount profileFamily = iota // "<count> @ 0x…"
	familyHeap                       // "<a>: <b> [<c>: <d>] @ 0x…"
	familyBlock                      // "<count> <cycles> @ 0x…"
)

type ProfileDef struct {
	name       string // 路由/展示名
	Lookup     string // runtime/pprof.Lookup 名
	desc       string
	Category   string // snapshot | capture
	sampleType string
	unit       string
	valueIdx   int // 主采样值在采样值数组中的下标
	family     profileFamily
}

var pprofProfileDefs = []ProfileDef{
	{name: "goroutine", Lookup: "goroutine", desc: "当前全部 goroutine 的调用栈快照", Category: "snapshot", sampleType: "goroutine", unit: "个", valueIdx: 0, family: familyCount},
	{name: "heap", Lookup: "heap", desc: "当前仍存活对象的内存分配", Category: "snapshot", sampleType: "inuse_space", unit: "B", valueIdx: 3, family: familyHeap},
	{name: "allocs", Lookup: "allocs", desc: "进程启动以来的全部内存分配", Category: "snapshot", sampleType: "alloc_space", unit: "B", valueIdx: 1, family: familyHeap},
	{name: "block", Lookup: "block", desc: "阻塞在锁 / 通道同步上的等待统计", Category: "snapshot", sampleType: "contentions", unit: "次", valueIdx: 0, family: familyBlock},
	{name: "mutex", Lookup: "mutex", desc: "互斥锁竞争等待统计", Category: "snapshot", sampleType: "contentions", unit: "次", valueIdx: 0, family: familyBlock},
	{name: "threadcreate", Lookup: "threadcreate", desc: "线程创建事件堆栈", Category: "snapshot", sampleType: "threadcreate", unit: "个", valueIdx: 0, family: familyCount},
	{name: "profile", desc: "CPU 使用采样，按设定时长采集后下载", Category: "capture"},
	{name: "trace", desc: "Go 执行跟踪，按设定时长采集后下载", Category: "capture"},
}

var pprofDefByName = func() map[string]ProfileDef {
	m := make(map[string]ProfileDef, len(pprofProfileDefs))
	for _, d := range pprofProfileDefs {
		m[d.name] = d
	}
	return m
}()

func DefFor(name string) (ProfileDef, bool) {
	d, ok := pprofDefByName[name]
	return d, ok
}

// Lookup 供 HandleProfileFlame 使用;pprofProfileCount 供 status
// 概览使用(包级变量以便测试注入)。
var Lookup = func(name string) *pprof.Profile { return pprof.Lookup(name) }

var pprofProfileCount = func(name string) (int, bool) {
	p := pprof.Lookup(name)
	if p == nil {
		return 0, false
	}
	return p.Count(), true
}

// StatusEntries 生成 status 接口的 profile 概览条目。
func StatusEntries() []response.PprofProfileEntry {
	entries := make([]response.PprofProfileEntry, 0, len(pprofProfileDefs))
	for _, d := range pprofProfileDefs {
		e := response.PprofProfileEntry{
			Name:        d.name,
			Description: d.desc,
			Category:    d.Category,
			Unit:        d.unit,
		}
		if d.Category != "capture" && d.Lookup != "" {
			// Lookup 内部是原子读，成本极低，可按秒级轮询。
			if count, ok := pprofProfileCount(d.Lookup); ok {
				e.Count = count
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// ── text 解析 ─────────────────────────────────────────────────

type pprofSample struct {
	values []int64  // 与 sample type 对齐；主值取 def.valueIdx
	pcs    []uint64 // 叶在前
}

// parseProfileText 解析 profile text 输出中的采样行，返回采样列表。
func parseProfileText(fam profileFamily, data []byte) []pprofSample {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	samples := make([]pprofSample, 0, 256)

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		at := strings.Index(line, " @ ")
		if at < 0 {
			// 头部行 / 空行 / "#" 帧行（帧行不解析，见文件头注释）
			continue
		}
		left := strings.TrimSpace(line[:at])
		pcText := line[at+3:]

		var vals []int64
		switch fam {
		case familyHeap:
			var a, b, c, d int64
			if _, err := fmt.Sscanf(left, "%d: %d [%d: %d]", &a, &b, &c, &d); err != nil {
				continue
			}
			vals = []int64{a, b, c, d}
		case familyBlock:
			var count, cycles int64
			if _, err := fmt.Sscanf(left, "%d %d", &count, &cycles); err != nil {
				continue
			}
			vals = []int64{count, cycles}
		default:
			v, err := strconv.ParseInt(left, 10, 64)
			if err != nil {
				continue
			}
			vals = []int64{v}
		}

		pcs := make([]uint64, 0, 16)
		for _, t := range strings.Fields(pcText) {
			pc, err := strconv.ParseUint(t, 0, 64)
			if err != nil || pc == 0 {
				continue // 0x0 为填充位 / 无效帧
			}
			pcs = append(pcs, pc)
		}
		if len(pcs) == 0 {
			continue
		}
		samples = append(samples, pprofSample{values: vals, pcs: pcs})
	}
	return samples
}

// ── 帧解析与火焰树 ────────────────────────────────────────────

// resolveFunc 解析单个 pc 对应的函数信息。包级变量以便测试注入。
var resolveFunc = func(pc uintptr) (name, file string, line int, ok bool) {
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "", "", 0, false
	}
	file, line = f.FileLine(pc)
	return f.Name(), file, line, true
}

type funcFrame struct {
	name string
	file string
	line int
}

// resolveFrames 把叶在前的 pc 列表解析为根在前的帧列表；
// 深度超上限时丢弃靠近叶子的尾部（保留入口链，flat 归属最深保留帧），
// 并合并相邻重复帧（递归调用的常见产物，防爆栈）。
func resolveFrames(pcs []uint64) []funcFrame {
	frames := make([]funcFrame, 0, len(pcs))
	for i := len(pcs) - 1; i >= 0; i-- {
		name, file, line, ok := resolveFunc(uintptr(pcs[i]))
		if !ok || name == "" {
			continue
		}
		last := len(frames) - 1
		if last >= 0 && frames[last].name == name {
			continue
		}
		frames = append(frames, funcFrame{name: name, file: sanitizeFile(file), line: line})
		if len(frames) >= maxFlameDepth {
			break
		}
	}
	return frames
}

// sanitizeFile 摘掉模块缓存目录 / 源码根等长前缀，仅保留可读路径。
func sanitizeFile(file string) string {
	for _, r := range []struct{ prefix, to string }{
		{"/go/pkg/mod/", "mod/"},
		{"/usr/local/go/src/", "go/"},
		{"/src/", ""},
	} {
		file = strings.ReplaceAll(file, r.prefix, r.to)
	}
	return file
}

const (
	maxFlameDepth    = 48   // 单条采样最大保留帧数(过深的 runtime 链会淹没业务帧,48 层足够呈现热点主干)
	maxFlameChildren = 240  // 每层最多保留的子节点数
	maxFlameNodes    = 6000 // 火焰树总节点数上限
)

// buildNode 火焰树构建期节点：按完整函数名索引子节点。
type buildNode struct {
	fn       string // 完整函数名
	short    string
	value    int64
	children map[string]*buildNode
}

func newBuildNode(fn, short string) *buildNode {
	return &buildNode{fn: fn, short: short, children: make(map[string]*buildNode, 4)}
}

type topAcc struct {
	name string
	file string
	line int
	flat int64
	cum  int64
}

// BuildProfile 解析 profile text 并构建火焰树 + 热点函数表。
// topN 为热点函数行数（由上层 clamp）。
func BuildProfile(def ProfileDef, data []byte, topN int) *response.PprofProfileResponse {
	samples := parseProfileText(def.family, data)

	root := newBuildNode("", "root")
	accs := make(map[string]*topAcc, 64)
	nodeCount := 1

	addSample := func(v int64, frames []funcFrame) {
		// cum 归属每一帧；flat 仅归属叶帧
		for i, fr := range frames {
			acc := accs[fr.name]
			if acc == nil {
				acc = &topAcc{name: fr.name, file: fr.file, line: fr.line}
				accs[fr.name] = acc
			}
			acc.cum += v
			if i == len(frames)-1 {
				acc.flat += v
			}
		}

		// 火焰树插入（根在前）；节点数超上限时放弃挂树，热点表仍保留
		node := root
		node.value += v
		for _, fr := range frames {
			child, ok := node.children[fr.name]
			if !ok {
				if nodeCount >= maxFlameNodes {
					return
				}
				child = newBuildNode(fr.name, shortenFn(fr.name))
				node.children[fr.name] = child
				nodeCount++
			}
			child.value += v
			node = child
		}
	}

	for _, s := range samples {
		v := s.values[def.valueIdx]
		if v == 0 {
			continue // 大量 0 值采样（heap 静态区常见）不产生有效信息
		}
		frames := resolveFrames(s.pcs)
		if len(frames) == 0 {
			continue
		}
		addSample(v, frames)
	}

	// 热点函数表：按 flat 降序取 top N
	accList := make([]*topAcc, 0, len(accs))
	for _, acc := range accs {
		accList = append(accList, acc)
	}
	sort.Slice(accList, func(i, j int) bool {
		if accList[i].flat != accList[j].flat {
			return accList[i].flat > accList[j].flat
		}
		if accList[i].cum != accList[j].cum {
			return accList[i].cum > accList[j].cum
		}
		return accList[i].name < accList[j].name
	})
	top := make([]response.PprofTopFunc, 0, topN)
	for i, acc := range accList {
		if i >= topN {
			break
		}
		top = append(top, response.PprofTopFunc{
			Fn:   acc.name,
			Name: shortenFn(acc.name),
			File: acc.file,
			Line: acc.line,
			Flat: acc.flat,
			Cum:  acc.cum,
		})
	}

	// 构建期树 → 响应树（子节点按 value 排序 + 裁剪）
	resp := &response.PprofProfileResponse{
		Name:        def.name,
		Unit:        def.unit,
		SampleType:  def.sampleType,
		SampleCount: len(samples),
		TotalValue:  root.value,
		Top:         top, // 始终为数组(无采样时为 [])
	}
	if root.value == 0 || len(samples) == 0 {
		return resp // Flame 保持 nil，前端展示空态
	}
	flame, truncated := toResponseNode(root)
	resp.Flame = flame
	resp.Truncated = truncated
	return resp
}

// toResponseNode 递归转换构建期节点为响应节点，并做子节点裁剪。
func toResponseNode(n *buildNode) (*response.FlameNode, bool) {
	truncated := false
	out := &response.FlameNode{Name: n.short, Value: n.value}
	if len(n.children) == 0 {
		return out, false
	}

	children := make([]*buildNode, 0, len(n.children))
	for _, c := range n.children {
		children = append(children, c)
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].value != children[j].value {
			return children[i].value > children[j].value
		}
		return children[i].fn < children[j].fn
	})

	keep := children
	var merged *response.FlameNode
	if len(children) > maxFlameChildren {
		keep = children[:maxFlameChildren]
		var restSum int64
		for _, c := range children[maxFlameChildren:] {
			restSum += c.value
		}
		merged = &response.FlameNode{
			Name:  "其它…",
			Value: restSum,
		}
		truncated = true
	}
	for _, c := range keep {
		cn, t := toResponseNode(c)
		if t {
			truncated = true
		}
		out.Children = append(out.Children, cn)
	}
	if merged != nil {
		out.Children = append(out.Children, merged)
	}
	return out, truncated
}

// shortenFn 把完整函数名压缩为适合火焰图帧标签的短名（最多保留最后两段路径）：
//
//	internal/pkg/ws.(*Client).WritePump → pkg/ws.(*Client).WritePump
//	strings.(*genericReplacer).Replace   → strings.(*genericReplacer).Replace
func shortenFn(full string) string {
	name := full
	for _, p := range []string{
		"github.com/tangwy-t/webmanager-server/",
		"github.com/",
		"gorm.io/",
		"go.uber.org/",
		"golang.org/x/",
		"google.golang.org/",
		"gopkg.in/",
	} {
		if strings.HasPrefix(name, p) {
			name = strings.TrimPrefix(name, p)
			break
		}
	}
	if segs := strings.Split(name, "/"); len(segs) > 2 {
		return strings.Join(segs[len(segs)-2:], "/")
	}
	return name
}
