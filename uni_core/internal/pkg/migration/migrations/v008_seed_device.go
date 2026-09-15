package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/permission"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     8,
		Description: "初始化设备管理菜单与 agent 配置",
		Up:          seedDevice,
	})
}

// 本文件**只**做种子；6 张分区指标表的建表钩子单独注册在 hook_agent_metrics.go
// —— 那个钩子不是「一次性版本化迁移」，而是每次启动都跑的幂等 reconcile，
// 且依赖 repository 包（本文件在迁移体系里保持零业务依赖）。

// deviceMenuDefinitions 是设备管理域的菜单定义（父必须先于子书写）。
var deviceMenuDefinitions = []menuDef{
	{Key: "device", Name: "设备管理", Type: "dir", Sort: 2, Icon: "ri:server-line"},
	{Key: "device:index", Parent: "device", Name: "设备列表", Type: "menu",
		Perms: permission.PermDeviceList, Path: "/device/index", Component: "device/index", Sort: 1, Icon: "ri:hard-drive-2-line"},
	{Key: "device:index:query", Parent: "device:index", Name: "设备查询", Type: "btn", Perms: permission.PermDeviceQuery, Sort: 1},
	{Key: "device:index:enable", Parent: "device:index", Name: "启用设备", Type: "btn", Perms: permission.PermDeviceEnable, Sort: 2},
	{Key: "device:index:disable", Parent: "device:index", Name: "停用设备", Type: "btn", Perms: permission.PermDeviceDisable, Sort: 3},
	{Key: "device:index:delete", Parent: "device:index", Name: "删除设备", Type: "btn", Perms: permission.PermDeviceDelete, Sort: 4},
}

// deviceConfigDefinitions 是设备监控域的配置项（单一来源）。
var deviceConfigDefinitions = []configDef{
	{Key: "sys.agent.enrollToken", Value: "", Type: "S", Name: "Agent 注册令牌",
		Remark: "agent 首次注册所需令牌，需人工设置", Enabled: true},
	{Key: "sys.agent.offlineThreshold", Value: "30", Type: "N", Name: "离线判定阈值",
		Remark: "最后上报超过该秒数即视为离线", Enabled: true},
	{Key: "sys.agent.reportInterval", Value: "10", Type: "N", Name: "指标上报间隔",
		Remark: "agent 上报间隔(秒)，最小 2", Enabled: true},
	{Key: "sys.agent.heartbeatInterval", Value: "30", Type: "N", Name: "心跳间隔",
		Remark: "agent 心跳间隔(秒)", Enabled: true},
	{Key: "sys.agent.historyRetentionDays", Value: "30", Type: "N", Name: "5min 档保留天数",
		Remark: "5min 明细指标保留天数", Enabled: true},
	{Key: "sys.agent.metrics1hRetentionDays", Value: "180", Type: "N", Name: "1h 档保留天数",
		Remark: "1h 趋势指标保留天数", Enabled: true},
}

// seedDevice 写入设备菜单与 agent 配置。
// 复用 v002 的 strPtr 与 v005 的 boolToInt8（同包辅助函数）。
// 菜单按 deviceMenuDefinitions 的顺序创建：父先于子，故每条的父 ID 必已在
// idByKey 里（由 TestV008MenuParentsResolve 守卫该前提）。
func seedDevice(tx *gorm.DB) error {
	idByKey := make(map[string]uint64, len(deviceMenuDefinitions))
	for _, d := range deviceMenuDefinitions {
		parentID := uint64(0)
		if d.Parent != "" {
			pid, ok := idByKey[d.Parent]
			if !ok {
				return errMenuParentMissing(d.Key, d.Parent)
			}
			parentID = pid
		}
		menu := entity.SysMenu{
			ParentID:  util.Ptr(parentID),
			Name:      d.Name,
			Type:      d.Type,
			Perms:     strPtr(d.Perms),
			Path:      strPtr(d.Path),
			Component: strPtr(d.Component),
			Sort:      util.Ptr(d.Sort),
			Icon:      strPtr(d.Icon),
			Visible:   util.Ptr[int8](entity.MenuVisible),
			Status:    util.Ptr[int8](entity.MenuStatusEnabled),
		}
		if err := tx.Create(&menu).Error; err != nil {
			return err
		}
		idByKey[d.Key] = menu.ID // 雪花回调在 Create 时就地回写
	}

	configs := make([]entity.SysConfig, 0, len(deviceConfigDefinitions))
	for _, c := range deviceConfigDefinitions {
		configs = append(configs, entity.SysConfig{
			Name:        c.Name,
			ConfigKey:   c.Key,
			ConfigValue: c.Value,
			ConfigType:  c.Type,
			Remark:      util.Ptr(c.Remark),
			Status:      util.Ptr[int8](boolToInt8(c.Enabled)),
		})
	}
	return tx.CreateInBatches(configs, 100).Error
}

func errMenuParentMissing(key, parent string) error {
	return &menuParentError{key: key, parent: parent}
}

type menuParentError struct{ key, parent string }

func (e *menuParentError) Error() string {
	return "v008 菜单 " + e.key + " 的父节点 " + e.parent + " 尚未创建（定义必须父先于子）"
}
