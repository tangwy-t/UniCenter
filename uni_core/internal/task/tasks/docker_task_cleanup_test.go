package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tangwy-t/UniCenter/uni_core/internal/task"
)

// fakeDeleteBeforeRepo 记录清理调用点（before 时刻），并按需报错。
type fakeDeleteBeforeRepo struct {
	before  time.Time
	calls   int
	deleted int64
	err     error
}

func (f *fakeDeleteBeforeRepo) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	f.calls++
	f.before = before
	return f.deleted, f.err
}

// fakeConfigProvider 是 ConfigProvider 的最小替身（只有本任务读的那个键有值）。
type fakeConfigProvider struct {
	values map[string]int
}

func (f fakeConfigProvider) GetInt(_ context.Context, key string, defaultVal int) int {
	if v, ok := f.values[key]; ok {
		return v
	}
	return defaultVal
}

// TestDockerTaskCleanupRegisteredAndExecutes 钉住 8d 清理任务的三件事：
// 注册名（调度器按 invoke_target 精确查找）、保留天数走配置、按「现在 − N 天」计算
// 截止线交给仓储。
func TestDockerTaskCleanupRegisteredAndExecutes(t *testing.T) {
	if _, ok := task.NewRegistry(All(Deps{})...).Get("docker-task-cleanup"); !ok {
		t.Fatal("docker-task-cleanup 必须在 tasks.All() 里（否则 v018 的 invoke_target 查不到，任务静默不执行）")
	}

	repo := &fakeDeleteBeforeRepo{deleted: 3}
	tk := NewDockerTaskCleanupTask(repo, fakeConfigProvider{values: map[string]int{"sys.docker.taskRetentionDays": 7}})
	if tk.Name() != "docker-task-cleanup" || tk.DisplayName() == "" {
		t.Fatalf("任务名与展示名: %q / %q", tk.Name(), tk.DisplayName())
	}

	start := time.Now()
	if err := tk.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if repo.calls != 1 {
		t.Fatalf("DeleteBefore 调用次数 = %d, want 1", repo.calls)
	}
	// 期望 = 现在 − 7 天（容差 1 分钟：测试跑在真实时钟上）。
	want := start.AddDate(0, 0, -7)
	if diff := repo.before.Sub(want); diff > time.Minute || diff < -time.Minute {
		t.Fatalf("截止线 = %v, want ≈ %v（现在 − 7 天）", repo.before, want)
	}
}

// TestDockerTaskCleanupConfigGuard 钉住误配护栏：0/负数不能被当成「保留 0 天」
// （那会一轮清空整张历史表且不可逆），必须回落到缺省 30 天。
func TestDockerTaskCleanupConfigGuard(t *testing.T) {
	for _, bad := range []int{0, -1, -30} {
		repo := &fakeDeleteBeforeRepo{}
		tk := NewDockerTaskCleanupTask(repo, fakeConfigProvider{values: map[string]int{"sys.docker.taskRetentionDays": bad}})
		if err := tk.Execute(context.Background(), nil); err != nil {
			t.Fatalf("Execute(%d): %v", bad, err)
		}
		want := time.Now().AddDate(0, 0, -defaultDockerTaskRetentionDays)
		if diff := repo.before.Sub(want); diff > time.Minute || diff < -time.Minute {
			t.Fatalf("配置 %d 必须回落到缺省 %d 天, 截止线 = %v (want ≈ %v)",
				bad, defaultDockerTaskRetentionDays, repo.before, want)
		}
	}

	// 没有配置项（未装配/键缺失）同样走缺省。
	repo := &fakeDeleteBeforeRepo{}
	tk := NewDockerTaskCleanupTask(repo, fakeConfigProvider{})
	if err := tk.Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	want := time.Now().AddDate(0, 0, -defaultDockerTaskRetentionDays)
	if diff := repo.before.Sub(want); diff > time.Minute || diff < -time.Minute {
		t.Fatalf("缺省截止线 = %v, want ≈ %v", repo.before, want)
	}
}

// TestDockerTaskCleanupUsesDepsRepo 钉住 All() 里的装配接线：给 Deps 一个可识别的
// 仓储替身，取回的清理任务必须把截止线发给**同一个**替身（接错字段是非常容易发生、
// 又只能在生产里发现的错误 —— 例如复制三类日志清理那三行时抄错变量）。
func TestDockerTaskCleanupUsesDepsRepo(t *testing.T) {
	repo := &fakeDeleteBeforeRepo{}
	var got *DockerTaskCleanupTask
	for _, tk := range All(Deps{DockerTaskRepo: repo, ConfigSvc: fakeConfigProvider{}}) {
		if c, ok := tk.(*DockerTaskCleanupTask); ok {
			got = c
		}
	}
	if got == nil {
		t.Fatal("All() 里没有 DockerTaskCleanupTask")
	}
	if got.repo == nil {
		t.Fatal("All() 没有把 Deps.DockerTaskRepo 接进清理任务")
	}
	if err := got.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if repo.calls != 1 {
		t.Fatalf("截止线没有发到 Deps 里的那个仓储: calls=%d", repo.calls)
	}
}
