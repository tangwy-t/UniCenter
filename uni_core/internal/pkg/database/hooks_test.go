package database

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"gorm.io/gorm"
)

type auditRow struct {
	ID        uint64  `gorm:"primaryKey;autoIncrement:false"`
	Name      string  `gorm:"column:name"`
	CreatedBy *uint64 `gorm:"column:created_by"`
	UpdatedBy *uint64 `gorm:"column:updated_by"`
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&auditRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Register the production callbacks (audit:set_create / audit:set_update /
	// id:generate). A nil snowflake node is fine for these tests.
	NewCallbacks(nil).Register(db)
	return db
}

// TestSetCreateAuditStruct covers the normal single-record Create: the audit
// callbacks fill CreatedBy/UpdatedBy from the authenticated user in context.
func TestSetCreateAuditStruct(t *testing.T) {
	db := newTestDB(t)
	ctx := contextkeys.WithUserID(context.Background(), 42)

	row := auditRow{ID: 1, Name: "single"}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var got auditRow
	if err := db.First(&got, 1).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.CreatedBy == nil || *got.CreatedBy != 42 {
		t.Fatalf("CreatedBy = %v, want 42", got.CreatedBy)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != 42 {
		t.Fatalf("UpdatedBy = %v, want 42", got.UpdatedBy)
	}
}

// TestSetCreateAuditSlice is a regression test for the association-save flow:
// GORM internally issues Create with a slice Dest (Association.Append of a
// many2many relation). The audit callbacks must iterate the slice instead of
// panicking with "reflect: call of reflect.Value.Field on slice Value".
func TestSetCreateAuditSlice(t *testing.T) {
	db := newTestDB(t)
	ctx := contextkeys.WithUserID(context.Background(), 7)

	rows := []auditRow{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	if err := db.WithContext(ctx).Create(&rows).Error; err != nil {
		t.Fatalf("create slice: %v", err)
	}

	var got []auditRow
	if err := db.Find(&got).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for i := range got {
		if got[i].CreatedBy == nil || *got[i].CreatedBy != 7 {
			t.Fatalf("row %d CreatedBy = %v, want 7", got[i].ID, got[i].CreatedBy)
		}
		if got[i].UpdatedBy == nil || *got[i].UpdatedBy != 7 {
			t.Fatalf("row %d UpdatedBy = %v, want 7", got[i].ID, got[i].UpdatedBy)
		}
	}
}

// TestSetCreateAuditNoContext ensures a Create without an authenticated user
// simply skips audit fields (and, in the slice case, stays panic-free).
func TestSetCreateAuditNoContext(t *testing.T) {
	db := newTestDB(t)

	rows := []auditRow{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("create slice: %v", err)
	}

	var got []auditRow
	if err := db.Find(&got).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	for i := range got {
		if got[i].CreatedBy != nil || got[i].UpdatedBy != nil {
			t.Fatalf("row %d audit fields must stay nil without user context", got[i].ID)
		}
	}
}
