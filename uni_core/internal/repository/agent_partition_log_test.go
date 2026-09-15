package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
)

// newPartitionLogTestDB 建一张真的 sqlite 审计表，并注册**生产同款**雪花回调。
//
// 为什么必须注册真回调（device_resource_test 用的是 nil 节点）：本表的核心结构约束
// 就是「必须有名为 ID 的字段」—— 只有真节点在场，「ID 被正确生成」才是可观测的，
// 用 nil 节点则每个 ID 都是调用方自己填的，那条约束在测试里等于不存在。
func newPartitionLogTestDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&entity.AgentPartitionLog{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func mustInsertPartitionLog(t *testing.T, repo *AgentPartitionLogRepo, row *entity.AgentPartitionLog) {
	t.Helper()
	if err := repo.Insert(context.Background(), row); err != nil {
		t.Fatalf("Insert(%s/%s): %v", row.Table, row.PartitionName, err)
	}
}

// 仓储的主路径：写进去能读回来，且 ID 由雪花回调生成（不是 0）。
func TestPartitionLogInsertGeneratesSnowflakeIDAndRoundTrips(t *testing.T) {
	db := newPartitionLogTestDB(t)
	repo := NewAgentPartitionLogRepository(db)
	ctx := context.Background()

	at := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	dropped := at.Add(30 * time.Second)
	days := int32(180)
	row := &entity.AgentPartitionLog{
		Table:         entity.TableNameMetric1h,
		PartitionName: "p_2025_01",
		Action:        entity.PartitionActionDrop,
		LowerBound:    1735689600,
		UpperBound:    1738368000,
		// RowCount 刻意不填：口径是「一律 NULL」（见实体字段注释）。
		RetentionDays: &days,
		CreatedAt:     at,
		DroppedAt:     &dropped,
	}
	mustInsertPartitionLog(t, repo, row)

	if row.ID == 0 {
		t.Fatal("ID = 0：雪花回调没取到 ID 字段（本类型必须有一个叫 ID 的字段）")
	}

	got, err := repo.ListByTable(ctx, entity.TableNameMetric1h, 10)
	if err != nil {
		t.Fatalf("ListByTable: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("读回 %d 行, want 1", len(got))
	}
	r := got[0]
	if r.ID != row.ID {
		t.Fatalf("id = %d, want %d（写入时回写的那一个）", r.ID, row.ID)
	}
	if r.Table != entity.TableNameMetric1h || r.PartitionName != "p_2025_01" {
		t.Fatalf("表/分区名 = %q/%q, want %q/p_2025_01",
			r.Table, r.PartitionName, entity.TableNameMetric1h)
	}
	if r.Action != entity.PartitionActionDrop {
		t.Fatalf("action = %q, want %q", r.Action, entity.PartitionActionDrop)
	}
	if r.LowerBound != row.LowerBound || r.UpperBound != row.UpperBound {
		t.Fatalf("区间 = [%d,%d), want [%d,%d)", r.LowerBound, r.UpperBound, row.LowerBound, row.UpperBound)
	}
	if r.RetentionDays == nil || *r.RetentionDays != days {
		t.Fatalf("retention_days = %v, want %d（当时的保留期快照必须原样落库）", r.RetentionDays, days)
	}
	if !r.CreatedAt.UTC().Equal(at) {
		t.Fatalf("created_at = %v, want %v", r.CreatedAt.UTC(), at)
	}
	if r.DroppedAt == nil || !r.DroppedAt.UTC().Equal(dropped) {
		t.Fatalf("dropped_at = %v, want %v", r.DroppedAt, dropped)
	}
	// RowCount 必须**原样是 NULL**，不是 0：0 会被读成「当时这个分区确实空了」。
	if r.RowCount != nil {
		t.Fatalf("row_count = %d, want NULL（未采集与「确实是 0 行」是两件事）", *r.RowCount)
	}
}

// NULL 与 0 在列上必须可区分：审计表口径靠「不写」表达「不知道」，
// 若列定义把 NULL 读成 0，那条口径在生产上就失去了表达能力。
func TestPartitionLogRowCountDistinguishesNullFromZero(t *testing.T) {
	db := newPartitionLogTestDB(t)
	repo := NewAgentPartitionLogRepository(db)
	ctx := context.Background()
	at := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	zero := int64(0)

	mustInsertPartitionLog(t, repo, &entity.AgentPartitionLog{
		Table: entity.TableNameMetric5m, PartitionName: "p_2027_w09", Action: entity.PartitionActionAdd,
		CreatedAt: at,
	})
	mustInsertPartitionLog(t, repo, &entity.AgentPartitionLog{
		Table: entity.TableNameMetric5m, PartitionName: "p_2027_w10", Action: entity.PartitionActionAdd,
		RowCount: &zero, CreatedAt: at.Add(time.Second),
	})

	got, err := repo.ListByTable(ctx, entity.TableNameMetric5m, 10)
	if err != nil {
		t.Fatalf("ListByTable: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("读回 %d 行, want 2", len(got))
	}
	if got[0].RowCount != nil {
		t.Fatalf("第 1 行 row_count = %d, want NULL", *got[0].RowCount)
	}
	if got[1].RowCount == nil || *got[1].RowCount != 0 {
		t.Fatalf("第 2 行 row_count = %v, want 0（0 与 NULL 必须能分开）", got[1].RowCount)
	}
}

// 读回的顺序与过滤：审计是**流水**，读回顺序即发生顺序（created_at 升序、同刻按 id）；
// table 为空 = 不过滤；limit<=0 用默认上限（绝不做无界读）。
func TestPartitionLogListByTableFiltersOrdersAndCapsLimit(t *testing.T) {
	db := newPartitionLogTestDB(t)
	repo := NewAgentPartitionLogRepository(db)
	ctx := context.Background()
	at := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)

	var want5m, want1h []string
	for i := 0; i < 3; i++ {
		name5m := fmt.Sprintf("p_2027_w%02d", 10+i)
		name1h := fmt.Sprintf("p_2027_%02d", 3+i)
		mustInsertPartitionLog(t, repo, &entity.AgentPartitionLog{
			Table: entity.TableNameMetric5m, PartitionName: name5m, Action: entity.PartitionActionAdd,
			CreatedAt: at.Add(time.Duration(i) * time.Second),
		})
		mustInsertPartitionLog(t, repo, &entity.AgentPartitionLog{
			Table: entity.TableNameMetric1h, PartitionName: name1h, Action: entity.PartitionActionDrop,
			CreatedAt: at.Add(time.Duration(i) * time.Second),
		})
		want5m = append(want5m, name5m)
		want1h = append(want1h, name1h)
	}

	got5m, err := repo.ListByTable(ctx, entity.TableNameMetric5m, 10)
	if err != nil {
		t.Fatalf("ListByTable(5m): %v", err)
	}
	if len(got5m) != len(want5m) {
		t.Fatalf("5m 读回 %d 行, want %d（必须按表过滤）", len(got5m), len(want5m))
	}
	for i, want := range want5m {
		if got5m[i].PartitionName != want {
			t.Fatalf("第 %d 行 = %q, want %q（必须按写入顺序读回）", i+1, got5m[i].PartitionName, want)
		}
	}
	got1h, err := repo.ListByTable(ctx, entity.TableNameMetric1h, 10)
	if err != nil {
		t.Fatalf("ListByTable(1h): %v", err)
	}
	if len(got1h) != len(want1h) {
		t.Fatalf("1h 读回 %d 行, want %d", len(got1h), len(want1h))
	}

	all, err := repo.ListByTable(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListByTable(空表名): %v", err)
	}
	if len(all) != len(want5m)+len(want1h) {
		t.Fatalf("空表名读回 %d 行, want %d（空 = 不过滤）", len(all), len(want5m)+len(want1h))
	}

	limited, err := repo.ListByTable(ctx, "", 2)
	if err != nil {
		t.Fatalf("ListByTable(limit=2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit=2 读回 %d 行, want 2", len(limited))
	}

	// limit<=0 → 默认上限。审计表**不清理**，无界读会随运行年限线性变慢。
	bulk := make([]entity.AgentPartitionLog, 0, defaultPartitionLogLimit+5)
	for i := 0; i < defaultPartitionLogLimit+5; i++ {
		bulk = append(bulk, entity.AgentPartitionLog{
			Table: entity.TableNameMetricDisk, PartitionName: fmt.Sprintf("p_bulk_w%03d", i),
			Action: entity.PartitionActionAdd, CreatedAt: at.Add(time.Duration(i) * time.Second),
		})
	}
	if err := db.CreateInBatches(bulk, 100).Error; err != nil {
		t.Fatalf("批量写入: %v", err)
	}
	for i := range bulk {
		if bulk[i].ID == 0 {
			t.Fatalf("批量写入的第 %d 行 id = 0：雪花回调在切片路径上没生成 ID", i+1)
		}
	}
	capped, err := repo.ListByTable(ctx, entity.TableNameMetricDisk, 0)
	if err != nil {
		t.Fatalf("ListByTable(limit=0): %v", err)
	}
	if len(capped) != defaultPartitionLogLimit {
		t.Fatalf("limit=0 读回 %d 行, want %d（默认上限，不是无界）", len(capped), defaultPartitionLogLimit)
	}
}
