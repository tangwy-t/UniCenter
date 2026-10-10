package migrations

import (
	"testing"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
)

func TestV019IsRegistered(t *testing.T) {
	found := false
	for _, m := range migration.All() {
		if m.Version == 19 {
			found = true
			if m.Up == nil {
				t.Fatal("v019 的 Up 不能为空")
			}
		}
	}
	if !found {
		t.Fatal("v019 未注册（新增迁移必须在 init() 中 migration.Register）")
	}
}

func TestV019ButtonPermsExistInPermissionRegistry(t *testing.T) {
	all := map[string]bool{}
	for _, p := range permission.All() {
		all[p] = true
	}
	for _, m := range deviceGroupMenuDefinitions {
		if m.Perms == "" {
			t.Fatalf("按钮 %s 必须承载一个权限码（否则它是死节点）", m.Key)
		}
		if !all[m.Perms] {
			t.Fatalf("按钮 %q 引用了未在 permission.All() 注册的权限码 %q", m.Key, m.Perms)
		}
	}
}

func TestV019ButtonShape(t *testing.T) {
	if len(deviceGroupMenuDefinitions) != 1 {
		t.Fatalf("v019 只该种一个按钮, got %d", len(deviceGroupMenuDefinitions))
	}
	d := deviceGroupMenuDefinitions[0]
	if d.Key != "device:index:group" {
		t.Fatalf("按钮 Key = %q, want device:index:group", d.Key)
	}
	if d.Parent != "device:index" {
		t.Fatalf("按钮 Parent = %q, want device:index（挂在设备列表下）", d.Parent)
	}
	if d.Name != "设置分组" {
		t.Fatalf("按钮 Name = %q, want 设置分组", d.Name)
	}
	if d.Type != "btn" {
		t.Fatalf("按钮 Type = %q, want btn", d.Type)
	}
	if d.Perms != permission.PermDeviceGroup {
		t.Fatalf("按钮 Perms = %q, want %q", d.Perms, permission.PermDeviceGroup)
	}
}

// TestV019ButtonSortFollowsExisting 钉住新按钮排在 device:index 既有四个按钮之后
// （取当前最大 + 1）：任何一边改了排号都能抓住。
//
// 假设用**交叉校验**而非死记字面量：直接扫 v008 的 deviceMenuDefinitions 求既有
// 按钮的最大 Sort，若它不再是 4（本测试写死的前提过期），本测试先红。
func TestV019ButtonSortFollowsExisting(t *testing.T) {
	const existingMaxSort = 4 // v008 的 query=1 / enable=2 / disable=3 / delete=4
	maxSort := 0
	for _, m := range deviceMenuDefinitions {
		if m.Parent == "device:index" && m.Type == "btn" && m.Sort > maxSort {
			maxSort = m.Sort
		}
	}
	if maxSort != existingMaxSort {
		t.Fatalf("v008 device:index 下按钮的最大 Sort = %d, want %d（本测试的假设已过期，需同步）", maxSort, existingMaxSort)
	}
	for _, d := range deviceGroupMenuDefinitions {
		if d.Sort != existingMaxSort+1 {
			t.Fatalf("按钮 %s 的 Sort = %d, want %d（既有最大+1）", d.Key, d.Sort, existingMaxSort+1)
		}
	}
}

// TestV019RunsThroughMigrationRunner 走真实执行路径：runner 跑完后按钮在场，
// 且挂在 v008 建的 device:index 下；再跑一次 Up（模拟「恢复旧备份 + 重启」的重放）
// 不产生第二行 —— 去重按 (parent_id, perms)。
func TestV019RunsThroughMigrationRunner(t *testing.T) {
	db := newAgentSeedTestDB(t)
	if err := migration.Run(db, logger.NewNop()); err != nil {
		t.Fatalf("migration.Run: %v", err)
	}

	parent := findMenuRow(t, db, "type = ? AND path = ?", "menu", "/device/index")
	btn := findMenuRow(t, db, "parent_id = ? AND perms = ?", parent.ID, permission.PermDeviceGroup)
	if btn.Name != "设置分组" || btn.Type != "btn" {
		t.Fatalf("按钮行不符: %+v", btn)
	}
	if btn.Status == nil || *btn.Status != entity.MenuStatusEnabled {
		t.Fatalf("按钮必须是启用态: %+v", btn)
	}
	if btn.Sort == nil || *btn.Sort != 5 {
		t.Fatalf("按钮 Sort = %v, want 5", btn.Sort)
	}

	// 重放：直接重跑 Up，钉住幂等（备份恢复 + 重启的真实场景）。
	if err := seedDeviceGroupPerm(db); err != nil {
		t.Fatalf("重放 seedDeviceGroupPerm: %v", err)
	}
	var n int64
	if err := db.Model(&entity.SysMenu{}).
		Where("parent_id = ? AND perms = ?", parent.ID, permission.PermDeviceGroup).
		Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重放不得翻倍: 按钮行数 = %d, want 1", n)
	}
}

func findMenuRow(t *testing.T, db *gorm.DB, where string, args ...any) entity.SysMenu {
	t.Helper()
	var m entity.SysMenu
	if err := db.Where(where, args...).First(&m).Error; err != nil {
		t.Fatalf("查菜单 (%s %v): %v", where, args, err)
	}
	return m
}