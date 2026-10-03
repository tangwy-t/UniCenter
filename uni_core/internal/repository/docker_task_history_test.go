package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
)

// newDockerTaskHistoryTestDB 建一张真的 sqlite 历史表，并注册**生产同款**雪花回调
// （Upsert 的写入路径依赖 ID 生成回调，nil 节点会让这一段的真实性归零 ——
// 与 agent_partition_log_test 同款选择）。
func newDockerTaskHistoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	node, err := snowflake.New(1, logger.NewNop())
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	database.NewCallbacks(node).Register(db)
	if err := db.AutoMigrate(&entity.DockerTaskHistory{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func taskHistoryRow(ref string, deviceID, userID uint64, action string, accepted int64) *entity.DockerTaskHistory {
	return &entity.DockerTaskHistory{
		Ref: ref, DeviceID: deviceID, Action: action, Target: "alpine",
		UserID: userID, Status: "succeeded", Summary: "执行成功",
		AcceptedAt: accepted, FinishedAt: accepted + 500,
	}
}

// TestDockerTaskHistoryUpsertRefreshesTerminalFactsOnly 钉住写入契约：
//   - 同 ref 重放收敛到同一行（唯一键 + DoUpdates），不产生第二行；
//   - 只刷新**终态事实列**（status/summary/finished_at）—— 归属与受理时刻是受理时
//     的事实，不得被后续写入改写（LateResultWins 覆盖 timeout 时同理）。
func TestDockerTaskHistoryUpsertRefreshesTerminalFactsOnly(t *testing.T) {
	db := newDockerTaskHistoryTestDB(t)
	repo := NewDockerTaskHistoryRepo(db)
	ctx := context.Background()

	row := taskHistoryRow("r1", 7, 1, "image:pull", 1000)
	row.Status = "timeout"
	row.Summary = "指令超时未完成"
	row.FinishedAt = 1500
	if err := repo.Upsert(ctx, row); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if row.ID == 0 {
		t.Fatal("行 ID 必须由雪花回调生成")
	}

	// 迟到的真实结果覆盖推断（事实优先）：同 ref 再写一次，只改终态事实列。
	late := taskHistoryRow("r1", 7, 1, "image:pull", 9999) // 受理时刻/目标都故意不同
	late.Status = "succeeded"
	late.Summary = "执行成功"
	late.FinishedAt = 2000
	if err := repo.Upsert(ctx, late); err != nil {
		t.Fatalf("Upsert(重放): %v", err)
	}

	var n int64
	if err := db.Model(&entity.DockerTaskHistory{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("同 ref 必须收敛到一行, got %d", n)
	}
	rows, err := repo.FindTop(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := rows[0]
	if got.Status != "succeeded" || got.Summary != "执行成功" || got.FinishedAt != 2000 {
		t.Fatalf("终态事实列必须刷新: %+v", got)
	}
	if got.AcceptedAt != 1000 || got.Target != "alpine" || got.DeviceID != 7 || got.UserID != 1 {
		t.Fatalf("受理时的事实不得被重放改写: %+v", got)
	}
}

// TestDockerTaskHistoryFindTopOrderAndFilters 钉住读面：受理时刻降序（同毫秒 ref
// 降序）、三档过滤与实时面逐条对齐、pending 恒空。
func TestDockerTaskHistoryFindTopOrderAndFilters(t *testing.T) {
	db := newDockerTaskHistoryTestDB(t)
	repo := NewDockerTaskHistoryRepo(db)
	ctx := context.Background()

	seed := []*entity.DockerTaskHistory{
		taskHistoryRow("r1", 7, 1, "image:pull", 1000),
		taskHistoryRow("r2", 9, 2, "image:pull", 2000),
		taskHistoryRow("r3", 7, 1, "container:restart", 3000),
		taskHistoryRow("r4", 7, 2, "container:restart", 3000),
	}
	for _, row := range seed {
		if err := repo.Upsert(ctx, row); err != nil {
			t.Fatalf("Upsert(%s): %v", row.Ref, err)
		}
	}

	rows, err := repo.FindTop(ctx, &request.DockerTasksQuery{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"r4", "r3", "r2", "r1"} // 时刻降序；3000 同毫秒按 ref 降序
	if len(rows) != len(want) {
		t.Fatalf("全量 = %d 条, want %d", len(rows), len(want))
	}
	for i, ref := range want {
		if rows[i].Ref != ref {
			t.Fatalf("顺序不符: [%d] = %s, want %s", i, rows[i].Ref, ref)
		}
	}
	// limit 是取数上界（合并读面的 need），必须生效
	if rows, err := repo.FindTop(ctx, &request.DockerTasksQuery{}, 2); err != nil || len(rows) != 2 {
		t.Fatalf("limit 未生效: %d rows, err=%v", len(rows), err)
	}

	total, err := repo.Count(ctx, &request.DockerTasksQuery{})
	if err != nil || total != 4 {
		t.Fatalf("Count 全量 = %d, want 4 (err=%v)", total, err)
	}
	if total, _ := repo.Count(ctx, &request.DockerTasksQuery{HostID: 7}); total != 3 {
		t.Fatalf("hostId 过滤: %d, want 3", total)
	}
	if total, _ := repo.Count(ctx, &request.DockerTasksQuery{Action: "image:pull"}); total != 2 {
		t.Fatalf("action 过滤: %d, want 2", total)
	}
	if total, _ := repo.Count(ctx, &request.DockerTasksQuery{Status: "done"}); total != 4 {
		t.Fatalf("done = 一切终态 = 全表: %d, want 4", total)
	}
	// pending 在本表恒空：显式落成恒假条件，将来多一处读路径也不会查出「历史里的
	// 进行中任务」这种不存在的东西。
	if total, _ := repo.Count(ctx, &request.DockerTasksQuery{Status: "pending"}); total != 0 {
		t.Fatalf("pending 档历史恒空: %d, want 0", total)
	}
	if rows, _ := repo.FindTop(ctx, &request.DockerTasksQuery{Status: "pending"}, 10); len(rows) != 0 {
		t.Fatalf("pending 档 FindTop 必须为空: %+v", rows)
	}
	if total, _ := repo.Count(ctx, &request.DockerTasksQuery{HostID: 7, Action: "container:restart"}); total != 2 {
		t.Fatalf("host+action 组合: %d, want 2", total)
	}
}

// TestDockerTaskHistoryCountByRefsAndDeleteBefore 钉住合并口径的两个辅助查询与
// 保留清理：重叠数按 ref 精确计数；清理按**受理时刻**删且软删（默认作用域之外
// 不再可见）。
func TestDockerTaskHistoryCountByRefsAndDeleteBefore(t *testing.T) {
	db := newDockerTaskHistoryTestDB(t)
	repo := NewDockerTaskHistoryRepo(db)
	ctx := context.Background()

	for _, row := range []*entity.DockerTaskHistory{
		taskHistoryRow("r1", 7, 1, "image:pull", 1000),
		taskHistoryRow("r2", 7, 1, "image:pull", 2000),
		taskHistoryRow("r3", 7, 1, "image:pull", 3000),
	} {
		if err := repo.Upsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	overlap, err := repo.CountByRefs(ctx, []string{"r1", "r3", "live-only"})
	if err != nil || overlap != 2 {
		t.Fatalf("重叠数 = %d, want 2 (err=%v)", overlap, err)
	}
	if overlap, err := repo.CountByRefs(ctx, nil); err != nil || overlap != 0 {
		t.Fatalf("空 ref 集重叠数 = %d, want 0 (err=%v)", overlap, err)
	}

	// 受理超过 2500ms 的删掉（r1/r2）；r3 必须留下。
	n, err := repo.DeleteBefore(ctx, time.UnixMilli(2500))
	if err != nil || n != 2 {
		t.Fatalf("清理应删 2 行, got %d (err=%v)", n, err)
	}
	rows, err := repo.FindTop(ctx, nil, 10)
	if err != nil || len(rows) != 1 || rows[0].Ref != "r3" {
		t.Fatalf("清理后只剩 r3: %+v (err=%v)", rows, err)
	}
	if total, _ := repo.Count(ctx, nil); total != 1 {
		t.Fatalf("Count 必须只看活跃行: %d, want 1", total)
	}
	// 软删（与操作日志清理同款）：物理行还在，只是出了默认作用域。
	var phys int64
	if err := db.Unscoped().Model(&entity.DockerTaskHistory{}).Count(&phys).Error; err != nil {
		t.Fatal(err)
	}
	if phys != 3 {
		t.Fatalf("软删后物理行数 = %d, want 3", phys)
	}
}
