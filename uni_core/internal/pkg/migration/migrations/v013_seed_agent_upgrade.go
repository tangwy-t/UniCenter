package migrations

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     13,
		Description: "新增 agent 升级菜单、按钮权限与配置键",
		Up:          seedAgentUpgrade,
	})
}

// ── 为什么是 v013（而不是改 v008 / v012）────────────────────────────────
//
// 迁移框架只跑「版本 > 当前最大版本」的迁移，已在真实库执行过的迁移对其
// **完全无效**（v012 的注释把这条讲透了）。新菜单必须走新版本号。
//
// ── 与前端插件路由的双向钉住 ─────────────────────────────────────────
//
// `Path` 必须与 uni_console/src/modules/device/index.ts 注册的路由**逐字一致**：
// MenuProcessor 用菜单 path 去前端路由表取组件，取不到就**整条菜单静默消失**
// （无报错、无日志）。该约束由 v013 的前端侧守卫测试钉住（与 v012 同款，随前端
// 路由落地同批提交）；本文件的守卫覆盖它当时能验证的部分。
//
// ── 权限码必须全部被菜单引用 ────────────────────────────────────────
//
// v002_seed_menu_perm_test 做**双向**检查：菜单引用的码必须在 All() 注册，
// All() 里每个码也必须被某个菜单引用（否则判为死常量）。故下面每个新码都有一条
// 菜单项（menu 或 btn）承载 —— 漏一个，守卫就红。
var agentUpgradeMenuDefinitions = []menuDef{
	{Key: "device:release", Parent: "device", Name: "Agent 版本", Type: "menu",
		Perms: permission.PermDeviceReleaseList, Path: "/device/release",
		Component: "device/release", Sort: 2, Icon: "iconoir:git"},
	{Key: "device:release:upload", Parent: "device:release", Name: "上传发布物", Type: "btn",
		Perms: permission.PermDeviceReleaseUpload, Sort: 1},
	{Key: "device:release:publish", Parent: "device:release", Name: "发布与撤回", Type: "btn",
		Perms: permission.PermDeviceReleasePublish, Sort: 2},
	{Key: "device:release:delete", Parent: "device:release", Name: "删除发布物", Type: "btn",
		Perms: permission.PermDeviceReleaseDelete, Sort: 3},
	// 全站目标版本是**危险拉杆**（一次影响所有设备，含之后新注册的），
	// 故它是独立按钮权限，挂在「Agent 版本」页下 —— 页面上它也是独立区域。
	{Key: "device:release:global", Parent: "device:release", Name: "全站目标版本", Type: "btn",
		Perms: permission.PermDeviceUpgradeGlobal, Sort: 4},

	{Key: "device:upgrade-task", Parent: "device", Name: "升级任务", Type: "menu",
		Perms: permission.PermDeviceQuery, Path: "/device/upgrade-task",
		Component: "device/upgrade-task", Sort: 3, Icon: "material-symbols:work-update-outline"},

	// 升级是**设备行上的操作**（列表操作列与详情页），故挂在设备列表页下。
	{Key: "device:index:upgrade", Parent: "device:index", Name: "升级 Agent", Type: "btn",
		Perms: permission.PermDeviceUpgrade, Sort: 5},
}

// agentUpgradeConfigDefinitions 是本迁移新增的配置键（单一来源）。
//
//   - `sys.agent.release.maxSize`：发布物上传上限。**必须独立于
//     sys.file.upload.maxSize**（后者默认 10MB）—— 静态 Go 二进制通常 15–25MB，
//     复用文件模块的上限会让「上传 agent 产物」直接 400，而且报错说的是文件模块
//     的限制，与 agent 升级毫无字面关系，极难排查。
//   - `sys.agent.targetVersion`：全站目标版本。**默认空 = 关闭**：这个键一旦有值，
//     所有设备（含之后新注册的）都会自动对齐到它 —— 默认必须是「不开启」，
//     否则一次迁移就把全站设备点了自动升级。
var agentUpgradeConfigDefinitions = []configDef{
	{Key: "sys.agent.release.maxSize", Value: "209715200", Type: "N", Name: "Agent 发布物上传上限",
		Remark: "单个 agent 程序包最大字节数(默认 200MB)", Enabled: true},
	{Key: "sys.agent.targetVersion", Value: "", Type: "S", Name: "Agent 全站目标版本",
		Remark: "为空则不启用全站升级；建议从「设备管理 → Agent 版本」页设置", Enabled: true},
}

// seedAgentUpgrade 追加两个菜单（及其按钮权限）与两个配置键。
func seedAgentUpgrade(tx *gorm.DB) error {
	dirID, err := findMenuID(tx, "type = ? AND name = ?", "dir", "设备管理")
	if err != nil {
		return fmt.Errorf("v013 找不到「设备管理」目录菜单（v008 应已创建）: %w", err)
	}
	indexID, err := findMenuID(tx, "path = ?", "/device/index")
	if err != nil {
		return fmt.Errorf("v013 找不到设备列表菜单 /device/index（v008 应已创建）: %w", err)
	}

	// 父引用按「键 → ID」解析：本切片**按先父后子书写**（device:* 与
	// device:index:* 是既有菜单，device:release:* 由本轮创建），
	// 故循环中不必回查数据库。父键不在 map 里也不在既有菜单里 = 定义写错了。
	idByKey := map[string]uint64{"device": dirID, "device:index": indexID}

	for _, d := range agentUpgradeMenuDefinitions {
		parentID, ok := idByKey[d.Parent]
		if !ok {
			return fmt.Errorf("v013 菜单 %s 的父 %q 未解析（父必须先于子书写）", d.Key, d.Parent)
		}
		// 幂等查重：menu 按 (parent_id, path)，btn 按 (parent_id, perms)。
		// 迁移框架保证同版本只执行一次，但「从备份恢复 + 重启」会让同一 Up
		// 在语义上重放（v012 注释记录过这个场景）；重复插入的症状是侧边栏出现
		// 两条同名菜单或两个同名按钮。
		dup, err := menuExists(tx, parentID, d)
		if err != nil {
			return err
		}
		if dup {
			continue
		}
		menu := entity.SysMenu{
			ParentID: util.Ptr(parentID),
			Name:     d.Name,
			Type:     d.Type,
			Perms:    strPtr(d.Perms),
			Visible:  util.Ptr[int8](entity.MenuVisible),
			Status:   util.Ptr[int8](entity.MenuStatusEnabled),
			Sort:     util.Ptr(d.Sort),
		}
		if d.Path != "" {
			menu.Path = strPtr(d.Path)
		}
		if d.Component != "" {
			menu.Component = strPtr(d.Component)
		}
		if d.Icon != "" {
			menu.Icon = strPtr(d.Icon)
		}
		if err := tx.Create(&menu).Error; err != nil {
			return fmt.Errorf("v013 创建菜单 %s: %w", d.Key, err)
		}
		idByKey[d.Key] = menu.ID
	}

	return seedAgentUpgradeConfig(tx)
}

// seedAgentUpgradeConfig 按 config_key 去重地补齐配置键。
//
// 与 v011 同款取向：真实库里这一行可能已被运维改过（例如把上传上限调大），
// 种子的意义只是「别让它缺失」，不是「让它回到默认值」——
// 覆写会把运维调过的值打回原值，且不可观测。
func seedAgentUpgradeConfig(tx *gorm.DB) error {
	for _, def := range agentUpgradeConfigDefinitions {
		var n int64
		if err := tx.Model(&entity.SysConfig{}).
			Where("config_key = ?", def.Key).Count(&n).Error; err != nil {
			return fmt.Errorf("v013 查询配置 %s: %w", def.Key, err)
		}
		if n > 0 {
			continue
		}
		row := entity.SysConfig{
			Name:        def.Name,
			ConfigKey:   def.Key,
			ConfigValue: def.Value,
			ConfigType:  def.Type,
			Remark:      util.Ptr(def.Remark),
			Status:      util.Ptr[int8](boolToInt8(def.Enabled)),
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("v013 写入配置 %s: %w", def.Key, err)
		}
	}
	return nil
}

// findMenuID 按条件取一条菜单的 ID（父菜单必须已存在，查不到即报错）。
func findMenuID(tx *gorm.DB, where string, args ...any) (uint64, error) {
	var m entity.SysMenu
	if err := tx.Where(where, args...).First(&m).Error; err != nil {
		return 0, err
	}
	return m.ID, nil
}

// menuExists 按定义类型做去重查询。
func menuExists(tx *gorm.DB, parentID uint64, d menuDef) (bool, error) {
	q := tx.Model(&entity.SysMenu{}).Where("parent_id = ?", parentID)
	if d.Path != "" {
		q = q.Where("path = ?", d.Path)
	} else {
		q = q.Where("perms = ?", d.Perms)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, fmt.Errorf("v013 查询菜单 %s: %w", d.Key, err)
	}
	return n > 0, nil
}
