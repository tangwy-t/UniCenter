package repository

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/contextkeys"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/database"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/snowflake"
)

// newDockerImageTargetTestDB 建一张真的 sqlite 目标表，并注册**生产同款**雪花回调
// （Upsert 依赖 ID 生成回调；审计回调填 created_by/updated_by —— 与
// docker_task_history_test 同款选择）。
func newDockerImageTargetTestDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&entity.DockerImageTarget{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestDockerImageTargetUpsertOverwritesValueOnly 钉住声明语义：
//   - 同 repoTag 重设收敛到同一行（唯一键 + OnConflict），不产生第二行；
//   - 只刷**值列**（target_digest/updated_at/updated_by）—— created_at/created_by
//     是首次声明的事实，重设不改写（updated_at 才是「这次改动」的时刻）。
func TestDockerImageTargetUpsertOverwritesValueOnly(t *testing.T) {
	db := newDockerImageTargetTestDB(t)
	repo := NewDockerImageTargetRepo(db)
	ctxA := contextkeys.WithUserID(context.Background(), 1)
	ctxB := contextkeys.WithUserID(context.Background(), 2)

	first := &entity.DockerImageTarget{RepoTag: "app:1", TargetDigest: "sha256:aaa"}
	if err := repo.Upsert(ctxA, first); err != nil {
		t.Fatalf("Upsert #1: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("行 ID 必须由雪花回调生成")
	}
	createdID := first.ID

	second := &entity.DockerImageTarget{RepoTag: "app:1", TargetDigest: "sha256:bbb"}
	if err := repo.Upsert(ctxB, second); err != nil {
		t.Fatalf("Upsert #2: %v", err)
	}

	rows, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("同 repoTag 覆盖后应恰有一行: %d (%+v)", len(rows), rows)
	}
	got := rows[0]
	if got.ID != createdID {
		t.Fatalf("覆盖必须命中同一行（ID 不变）: got=%d want=%d", got.ID, createdID)
	}
	if got.TargetDigest != "sha256:bbb" {
		t.Fatalf("target_digest 未被覆盖: %q", got.TargetDigest)
	}
	if got.CreatedBy == nil || *got.CreatedBy != 1 {
		t.Fatalf("created_by 是首次声明的事实（应为 1），不该被覆盖: %v", got.CreatedBy)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != 2 {
		t.Fatalf("updated_by 应随覆盖刷成 2（审计回调）: %v", got.UpdatedBy)
	}
}

// TestDockerImageTargetDeleteIdempotent 钉住删除幂等：删不存在的目标**不报错**
//（与「取消一个没设过的目标」语义一致），且不误伤其它 repoTag。
func TestDockerImageTargetDeleteIdempotent(t *testing.T) {
	db := newDockerImageTargetTestDB(t)
	repo := NewDockerImageTargetRepo(db)
	ctx := context.Background()

	if err := repo.Upsert(ctx, &entity.DockerImageTarget{RepoTag: "app:1", TargetDigest: "sha256:aaa"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := repo.Upsert(ctx, &entity.DockerImageTarget{RepoTag: "web:1", TargetDigest: "sha256:ccc"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// 删除不存在的 → 幂等成功。
	if err := repo.Delete(ctx, "nope:1"); err != nil {
		t.Fatalf("删除不存在的目标必须幂等成功（无 404）: %v", err)
	}
	// 删除存在的 → 成功；再删一次仍成功（幂等）。
	if err := repo.Delete(ctx, "app:1"); err != nil {
		t.Fatalf("Delete 存在行: %v", err)
	}
	if err := repo.Delete(ctx, "app:1"); err != nil {
		t.Fatalf("重复删除必须幂等成功: %v", err)
	}

	rows, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].RepoTag != "web:1" {
		t.Fatalf("只应删掉 app:1、留下 web:1: %+v", rows)
	}
}

// TestDockerImageTargetFindByRepoTagsBatch 钉住**批量取**：一次取多 tag 的目标
//（漂移聚合的 N+1 闸），返回 repoTag→行 映射、未声明的 tag 不出现、空入参不报错。
func TestDockerImageTargetFindByRepoTagsBatch(t *testing.T) {
	db := newDockerImageTargetTestDB(t)
	repo := NewDockerImageTargetRepo(db)
	ctx := context.Background()

	for _, r := range []struct{ tag, dig string }{
		{"app:1", "sha256:aaa"}, {"web:1", "sha256:bbb"}, {"db:5", "sha256:ccc"},
	} {
		if err := repo.Upsert(ctx, &entity.DockerImageTarget{RepoTag: r.tag, TargetDigest: r.dig}); err != nil {
			t.Fatalf("Upsert %s: %v", r.tag, err)
		}
	}

	got, err := repo.FindByRepoTags(ctx, []string{"app:1", "web:1", "absent:9"})
	if err != nil {
		t.Fatalf("FindByRepoTags: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("批量取应恰命中 2 个已声明目标: %+v", got)
	}
	if got["app:1"].TargetDigest != "sha256:aaa" || got["web:1"].TargetDigest != "sha256:bbb" {
		t.Fatalf("批量取的值不符: %+v", got)
	}
	if _, ok := got["absent:9"]; ok {
		t.Fatalf("未声明的 tag 不该出现: %+v", got)
	}

	// 空入参：返回空映射、不报错（不生成 IN ()）。
	empty, err := repo.FindByRepoTags(ctx, nil)
	if err != nil {
		t.Fatalf("空入参不该报错: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("空入参应回空映射: %+v", empty)
	}
}