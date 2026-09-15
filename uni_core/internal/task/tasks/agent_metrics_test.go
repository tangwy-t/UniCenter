package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	"github.com/tangwy-t/UniCenter/uni_core/internal/task"
)

// agentMetricTaskNames 是 4 个设备指标任务的**契约名字**。
//
// 这组字符串必须与 v009 种子的 `invoke_target` 逐字一致：调度器按
// `invoke_target` 去 task.Registry 里找目标（scheduler/executor.go:100），
// 找不到就只写一条 job_log 错误而后**静默不执行** —— 指标链路整段断掉，
// 却没有任何一处会编译失败或报错。双向守卫见
// migrations/v009_seed_agent_jobs_test.go 的 TestV009InvokeTargetsMatchTaskNames。
var agentMetricTaskNames = []string{
	"agent-metrics-flush",
	"agent-metrics-rollup",
	"agent-metrics-backfill",
	"agent-metrics-partition",
}

// ── Step 1.1：4 个任务都在 All() 里 ─────────────────────

func TestAgentMetricTasksAreRegisteredInAll(t *testing.T) {
	got := map[string]bool{}
	for _, tk := range All(Deps{}) {
		got[tk.Name()] = true
	}
	for _, want := range agentMetricTaskNames {
		if !got[want] {
			t.Fatalf("任务 %q 没在 tasks.All() 里 —— 漏注册 = 调度器按 invoke_target 找不到目标、任务静默不执行（All() 现有：%v）",
				want, allTaskNames())
		}
	}
}

// All() 里的任务名必须两两不同：task.NewRegistry 遇到重名直接 panic
// （registry.go 的「开发期安全」），即重名会让整个服务起不来。
func TestTaskNamesAreUniqueInAll(t *testing.T) {
	seen := map[string]bool{}
	for _, tk := range All(Deps{}) {
		if seen[tk.Name()] {
			t.Fatalf("任务名 %q 在 All() 里重复 —— task.NewRegistry 会 panic，服务启动即崩", tk.Name())
		}
		seen[tk.Name()] = true
	}
}

func allTaskNames() []string {
	out := make([]string, 0, 16)
	for _, tk := range All(Deps{}) {
		out = append(out, tk.Name())
	}
	return out
}

// ── Step 1.2（本包的一半）：Name() 逐字契约 ──────────────

func TestAgentMetricTaskNameContract(t *testing.T) {
	cases := []struct {
		want string
		tk   task.Task
	}{
		{"agent-metrics-flush", NewAgentMetricsFlushTask(nil, nil)},
		{"agent-metrics-rollup", NewAgentMetricsRollupTask(nil, nil)},
		{"agent-metrics-backfill", NewAgentMetricsBackfillTask(nil, nil)},
		{"agent-metrics-partition", NewAgentMetricsPartitionTask(nil, nil)},
	}
	display := map[string]bool{}
	for _, c := range cases {
		if got := c.tk.Name(); got != c.want {
			t.Fatalf("Name() = %q, want %q（改一个字就与 v009 的 invoke_target 脱节）", got, c.want)
		}
		dn := c.tk.DisplayName()
		if dn == "" {
			t.Fatalf("任务 %q 的 DisplayName 不能为空（jobs 列表与前端下拉都读它）", c.want)
		}
		if display[dn] {
			t.Fatalf("展示名 %q 重复：运维在 jobs 列表里无法区分两个任务", dn)
		}
		display[dn] = true
	}
}

// ── Step 1.3：Execute 的 params 语义 ────────────────────

// flush 恒为全量（服务侧 FlushOnce 没有单设备入口）→ params 被**明确忽略**并记警告，
// 而不是静默吞掉（运维给了 device_id 却看到全量结果时，日志是唯一的线索）。
func TestAgentMetricsFlushTaskIgnoresParamsAndLogsStats(t *testing.T) {
	fake := &fakeAgentService{flushStats: service.FlushStats{
		DevicesScanned: 2, BucketsWritten: 3, BucketsSkipped: 1, ResourcesUpserted: 4,
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsFlushTask(fake, lg)
	if err := tk.Execute(context.Background(), json.RawMessage(`{"device_id":1001}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.flushCalls != 1 {
		t.Fatalf("FlushOnce 调用次数 = %d, want 1", fake.flushCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "忽略 params") {
		t.Fatalf("给了非空 params 必须记一条明确的「忽略」日志，实际日志：%s", out)
	}
	// stats 必须以 zap 结构字段落日志（不是拼进 message 的字符串）——
	// 否则采集端按字段名取不到桶数/失败数，指标盘子就只剩人眼可读。
	for _, field := range []string{`"bucketsWritten":3`, `"devicesScanned":2`, `"bucketsSkipped":1`, `"resourcesUpserted":4`} {
		if !strings.Contains(out, field) {
			t.Fatalf("日志缺少 zap 字段 %s，实际日志：%s", field, out)
		}
	}
}

func TestAgentMetricsRollupTaskCallsRollupOnceAndLogsStats(t *testing.T) {
	fake := &fakeAgentService{rollupStats: service.RollupStats{
		HoursScanned: 5, HoursWritten: 4, HoursRepaired: 1, HoursSkipped: 1,
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsRollupTask(fake, lg)
	if err := tk.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.rollupCalls != 1 {
		t.Fatalf("RollupOnce 调用次数 = %d, want 1", fake.rollupCalls)
	}
	out := buf.String()
	for _, field := range []string{`"hoursScanned":5`, `"hoursWritten":4`, `"hoursRepaired":1`, `"hoursSkipped":1`} {
		if !strings.Contains(out, field) {
			t.Fatalf("日志缺少 zap 字段 %s，实际日志：%s", field, out)
		}
	}
}

func TestAgentMetricsPartitionTaskCallsReconcileAndLogsStats(t *testing.T) {
	fake := &fakeAgentService{partitionStats: service.PartitionStats{
		TablesScanned: 6, PartitionsCreated: 2, PartitionsTruncated: 1, PartitionsDropped: 3,
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsPartitionTask(fake, lg)
	if err := tk.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.reconcileCalls != 1 {
		t.Fatalf("Reconcile 调用次数 = %d, want 1", fake.reconcileCalls)
	}
	out := buf.String()
	for _, field := range []string{`"tablesScanned":6`, `"partitionsCreated":2`, `"partitionsTruncated":1`, `"partitionsDropped":3`} {
		if !strings.Contains(out, field) {
			t.Fatalf("日志缺少 zap 字段 %s，实际日志：%s", field, out)
		}
	}
}

// backfill 是 4 个任务里**唯一**支持 params 的：`{"hours":N,"device_ids":[...]}`。
func TestAgentMetricsBackfillTaskForwardsParams(t *testing.T) {
	fake := &fakeAgentService{backfillStats: service.BackfillStats{WindowHours: 6, CursorsRewound: 2}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsBackfillTask(fake, lg)
	err := tk.Execute(context.Background(), json.RawMessage(`{"hours":6,"device_ids":[1001,1002]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.backfillCalls != 1 {
		t.Fatalf("BackfillOnce 调用次数 = %d, want 1", fake.backfillCalls)
	}
	if fake.backfillHours != 6 {
		t.Fatalf("透传 hours = %d, want 6", fake.backfillHours)
	}
	if len(fake.backfillDeviceIDs) != 2 || fake.backfillDeviceIDs[0] != 1001 || fake.backfillDeviceIDs[1] != 1002 {
		t.Fatalf("透传 device_ids = %v, want [1001 1002]", fake.backfillDeviceIDs)
	}
	if fake.bootstrapCalls != 1 {
		t.Fatalf("Bootstrap 调用次数 = %d, want 1（先对齐缺失起点再回溯）", fake.bootstrapCalls)
	}
	if !fake.bootstrapRanBeforeBackfill() {
		t.Fatalf("调用顺序 = %v：Bootstrap 必须先于 BackfillOnce", fake.order)
	}
	// 生效窗口只认服务返回的 WindowHours（任务侧不复制「24h」这个常量）。
	for _, field := range []string{`"windowHours":6`, `"cursorsRewound":2`} {
		if !strings.Contains(buf.String(), field) {
			t.Fatalf("日志缺少 zap 字段 %s，实际日志：%s", field, buf.String())
		}
	}
}

func TestAgentMetricsBackfillTaskDefaultsToRetentionWindow(t *testing.T) {
	// 缺省（无 params）：hours 传 0 = 「用满 raw 保留窗口」，由服务夹取成 24h。
	fake := &fakeAgentService{backfillStats: service.BackfillStats{
		WindowHours: 24, CursorsRewound: 3, DevicesScanned: 3,
		Flush: service.FlushStats{BucketsWritten: 7},
	}}
	lg, buf := newCaptureLogger(t)

	tk := NewAgentMetricsBackfillTask(fake, lg)
	if err := tk.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.backfillHours != 0 {
		t.Fatalf("缺省 hours 应交给服务取保留窗口（0），got %d", fake.backfillHours)
	}
	if len(fake.backfillDeviceIDs) != 0 {
		t.Fatalf("缺省 device_ids 应为空（= 全部活跃设备），got %v", fake.backfillDeviceIDs)
	}
	out := buf.String()
	for _, field := range []string{`"windowHours":24`, `"bucketsWritten":7`} {
		if !strings.Contains(out, field) {
			t.Fatalf("日志缺少 zap 字段 %s，实际日志：%s", field, out)
		}
	}
}

func TestAgentMetricsBackfillTaskRejectsBadParams(t *testing.T) {
	bad := []string{
		`{"hours":0}`,        // 显式 0：窗口无意义 → 大声失败，不静默当缺省
		`{"hours":-3}`,       // 负数
		`{"hours":`,          // 非法 JSON
		`{"hours":"6"}`,      // 类型不符
		`{"device_ids":[0]}`, // 设备号 0 不是合法设备
	}
	for _, raw := range bad {
		fake := &fakeAgentService{}
		tk := NewAgentMetricsBackfillTask(fake, nil)
		if err := tk.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("params %s 必须报错（配置错误要大声，否则一个错参数会静默跑满 24h 重放）", raw)
		}
		if fake.backfillCalls != 0 || fake.bootstrapCalls != 0 {
			t.Fatalf("params %s 非法时不得触碰服务（backfill=%d bootstrap=%d）",
				raw, fake.backfillCalls, fake.bootstrapCalls)
		}
	}
}

// ── Step 1.4：错误必须上抛（调度器要记 job_log）──────────

func TestAgentMetricTasksPropagateServiceErrors(t *testing.T) {
	cases := []struct {
		name string
		tk   task.Task
		want error
	}{
		{"flush", NewAgentMetricsFlushTask(&fakeAgentService{flushErr: errBoomFlush}, nil), errBoomFlush},
		{"rollup", NewAgentMetricsRollupTask(&fakeAgentService{rollupErr: errBoomRollup}, nil), errBoomRollup},
		{"partition", NewAgentMetricsPartitionTask(&fakeAgentService{partitionErr: errBoomReconcile}, nil), errBoomReconcile},
		{"backfill", NewAgentMetricsBackfillTask(&fakeAgentService{backfillErr: errBoomBackfill}, nil), errBoomBackfill},
	}
	for _, c := range cases {
		err := c.tk.Execute(context.Background(), nil)
		if err == nil {
			t.Fatalf("%s 任务把服务错误吞掉了 —— 调度器记 success、告警永不触发", c.name)
		}
		if !errors.Is(err, c.want) {
			t.Fatalf("%s 任务的错误必须可用 errors.Is 识别（调度器/告警按哨兵分派），got %v", c.name, err)
		}
	}
}

// TestAgentMetricTasksLogMissingPartitionAsP1 钉住任务层**识别**缺分区哨兵：
// 它不是「部分失败、下轮重试」，而是「P1 + 重试无用，先跑分区对账」。
//
// 为什么必须在任务层断言：哨兵只在服务层上抛是不够的 —— 任务层若把它当普通失败，
// 运维看到的是一条「本轮部分失败（下轮重试）」的日志，而真实的处置要求完全不同
// （分区没建好之前，每一轮都会以同样的方式失败；spec §7.3 把它定为 P1）。
func TestAgentMetricTasksLogMissingPartitionAsP1(t *testing.T) {
	// 两个任务都用**缺分区哨兵**作为服务返回值：任务在构造时固定 logger，
	// 故构造器以捕获日志为参数（这样日志里的 P1 标注才能被断言）。
	cases := []struct {
		name string
		// build 用给定的 logger 造任务（替身的错误固定为缺分区哨兵）。
		build func(logger.LoggerInterface) task.Task
	}{
		{"flush", func(lg logger.LoggerInterface) task.Task {
			return NewAgentMetricsFlushTask(&fakeAgentService{flushErr: service.ErrMetricPartitionMissing}, lg)
		}},
		{"backfill", func(lg logger.LoggerInterface) task.Task {
			return NewAgentMetricsBackfillTask(&fakeAgentService{backfillErr: service.ErrMetricPartitionMissing}, lg)
		}},
	}
	for _, c := range cases {
		buf := &bytes.Buffer{}
		core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
			zapcore.AddSync(buf), zapcore.DebugLevel)
		lg, err := logger.NewWithCore(core)
		if err != nil {
			t.Fatalf("NewWithCore: %v", err)
		}
		tk := c.build(lg)

		err = tk.Execute(context.Background(), nil)
		if !errors.Is(err, service.ErrMetricPartitionMissing) {
			t.Fatalf("%s 任务把缺分区哨兵换成了别的错误（告警无法按哨兵分派）：%v", c.name, err)
		}
		logs := buf.String()
		if !strings.Contains(logs, "P1") {
			t.Fatalf("%s 任务的日志没有标注 P1（缺分区会被当成可自愈抖动）：\n%s", c.name, logs)
		}
		if !strings.Contains(logs, "分区缺失") {
			t.Fatalf("%s 任务的日志没有说明「分区缺失」（排障入口必须是可读的）：\n%s", c.name, logs)
		}
		// 缺分区不能被写成「下轮重试同一批桶」—— 那句话会让人以为等一轮就好。
		if strings.Contains(logs, "下轮重试同一批桶") {
			t.Fatalf("%s 任务把缺分区当成了「部分失败、下轮重试」：\n%s", c.name, logs)
		}
	}
}

// backfill 的 Bootstrap 失败同样要上抛（Redis 不可用时不能假装补齐成功）。
func TestAgentMetricsBackfillTaskPropagatesBootstrapError(t *testing.T) {
	fake := &fakeAgentService{bootstrapErr: errBoomBootstrap}
	tk := NewAgentMetricsBackfillTask(fake, nil)

	err := tk.Execute(context.Background(), nil)
	if !errors.Is(err, errBoomBootstrap) {
		t.Fatalf("Bootstrap 失败必须上抛，got %v", err)
	}
	if fake.backfillCalls != 0 {
		t.Fatalf("起点都没对齐就跑了回溯（backfill=%d）", fake.backfillCalls)
	}
}

// ── 替身与工具 ──────────────────────────────────────────

var (
	errBoomBootstrap = errors.New("boom: bootstrap")
	errBoomFlush     = errors.New("boom: flush")
	errBoomBackfill  = errors.New("boom: backfill")
	errBoomRollup    = errors.New("boom: rollup")
	errBoomReconcile = errors.New("boom: reconcile")
)

// fakeAgentService 同时实现三个窄接口（消费方定义的接口都很小，一个替身够用），
// 并记录调用顺序 —— 「Bootstrap 先于回溯」这类**顺序**契约只能靠它断言。
type fakeAgentService struct {
	order []string

	bootstrapCalls int
	bootstrapErr   error

	flushCalls int
	flushStats service.FlushStats
	flushErr   error

	backfillCalls     int
	backfillDeviceIDs []uint64
	backfillHours     int
	backfillStats     service.BackfillStats
	backfillErr       error

	rollupCalls int
	rollupStats service.RollupStats
	rollupErr   error

	reconcileCalls int
	partitionStats service.PartitionStats
	partitionErr   error
}

func (f *fakeAgentService) Bootstrap(_ context.Context) error {
	f.bootstrapCalls++
	f.order = append(f.order, "bootstrap")
	return f.bootstrapErr
}

func (f *fakeAgentService) FlushOnce(_ context.Context) (service.FlushStats, error) {
	f.flushCalls++
	f.order = append(f.order, "flush")
	return f.flushStats, f.flushErr
}

func (f *fakeAgentService) BackfillOnce(_ context.Context, deviceIDs []uint64, hours int) (service.BackfillStats, error) {
	f.backfillCalls++
	f.order = append(f.order, "backfill")
	f.backfillDeviceIDs = append([]uint64(nil), deviceIDs...)
	f.backfillHours = hours
	return f.backfillStats, f.backfillErr
}

func (f *fakeAgentService) RollupOnce(_ context.Context) (service.RollupStats, error) {
	f.rollupCalls++
	f.order = append(f.order, "rollup")
	return f.rollupStats, f.rollupErr
}

func (f *fakeAgentService) Reconcile(_ context.Context) (service.PartitionStats, error) {
	f.reconcileCalls++
	f.order = append(f.order, "reconcile")
	return f.partitionStats, f.partitionErr
}

// bootstrapRanBeforeBackfill 检查 order 里最后一次 backfill 之前出现过 bootstrap。
func (f *fakeAgentService) bootstrapRanBeforeBackfill() bool {
	seenBootstrap := false
	for _, step := range f.order {
		switch step {
		case "bootstrap":
			seenBootstrap = true
		case "backfill":
			if !seenBootstrap {
				return false
			}
		}
	}
	return seenBootstrap
}

// newCaptureLogger 返回一个把 JSON 日志写进 buffer 的真实 zap logger。
//
// 用它而不是 logger.NewNop()：本任务的契约是「stats 记进日志（zap 字段）」，
// Nop 日志会让「有没有落字段」这件事**无法断言**。
func newCaptureLogger(t *testing.T) (*logger.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(buf),
		zapcore.DebugLevel,
	)
	lg, err := logger.NewWithCore(core)
	if err != nil {
		t.Fatalf("NewWithCore: %v", err)
	}
	return lg, buf
}
