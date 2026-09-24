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
		Version:     15,
		Description: "新增 Docker 管理菜单、按钮权限与配置键",
		Up:          seedDocker,
	})
}

// dockerMenuDefinitions 是 Docker 管理域的菜单定义（父必须先于子书写）。
//
// `Path` 必须与 uni_console/src/modules/docker/index.ts 注册的路由**逐字一致**：
// MenuProcessor 用菜单 path 去前端路由表取组件，取不到就**整条菜单静默消失**
// （无报错、无日志）。该约束由前端侧守卫测试钉住（随前端路由落地同批提交，Plan 1b）。
//
// 权限码与菜单的**双向**约束（v002_seed_menu_perm_test）：菜单引用的码必须在 All()
// 注册，All() 里每个码也必须被某个菜单引用 —— 故六个码各有一个菜单/按钮承载。
var dockerMenuDefinitions = []menuDef{
	{Key: "docker", Name: "Docker 管理", Type: "dir", Sort: 3, Icon: "ri:stack-line"},
	{Key: "docker:containers", Parent: "docker", Name: "容器", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/containers", Component: "docker/containers",
		Sort: 1, Icon: "ri:ship-line"},
	{Key: "docker:images", Parent: "docker", Name: "镜像", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/images", Component: "docker/images",
		Sort: 2, Icon: "ri:box-1-line"},
	{Key: "docker:volumes", Parent: "docker", Name: "数据卷", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/volumes", Component: "docker/volumes",
		Sort: 3, Icon: "ri:hard-drive-3-line"},
	{Key: "docker:networks", Parent: "docker", Name: "网络", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/networks", Component: "docker/networks",
		Sort: 4, Icon: "ri:share-forward-line"},
	{Key: "docker:projects", Parent: "docker", Name: "项目", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/projects", Component: "docker/projects",
		Sort: 5, Icon: "ri:stack-line"},

	// 按钮权限挂在各自的页面下。**一期页面不渲染二期按钮**（§11.0 矩阵），
	// 但权限码必须先存在于菜单树里 —— 授权界面（角色管理）据此分配。
	{Key: "docker:containers:inspect", Parent: "docker:containers", Name: "查看详情", Type: "btn",
		Perms: permission.PermDockerInspect, Sort: 1},
	{Key: "docker:containers:manage", Parent: "docker:containers", Name: "启停重启", Type: "btn",
		Perms: permission.PermDockerManage, Sort: 2},
	{Key: "docker:containers:delete", Parent: "docker:containers", Name: "删除资源", Type: "btn",
		Perms: permission.PermDockerDelete, Sort: 3},
	{Key: "docker:containers:exec", Parent: "docker:containers", Name: "终端", Type: "btn",
		Perms: permission.PermDockerExec, Sort: 4},
	{Key: "docker:projects:config", Parent: "docker:projects", Name: "编辑配置", Type: "btn",
		Perms: permission.PermDockerConfig, Sort: 1},
}

// dockerConfigDefinitions 是本迁移新增的配置键（单一来源）。
//
//   - snapshotInterval：**最小 10 秒**（协议层 DockerConfig.Validate 拒小于 10 的值）——
//     与 stale 阈值 max(3×interval, 90s) 一起决定了「多久算陈旧」。
//   - protected：四粒度清单（容器名 / project:名 / project:名/服务 / volume:名）。
//     **默认值必须含底座**：它一旦为空，uni-center 自己的容器就成了可删对象。
//   - transferDir：image:save/load 的产物根。默认**不用 /tmp** —— 世界可写的目录
//     对 root 进程是符号链接攻击的温床，而 agent 以 root 跑。
var dockerConfigDefinitions = []configDef{
	{Key: "sys.docker.snapshotInterval", Value: "30", Type: "N", Name: "Docker 快照周期",
		Remark: "秒；最小 10。重连时经 hello_ack 下发给 agent", Enabled: true},
	{Key: "sys.docker.protected", Value: "uni-center-core,uni-center-console,mysql,redis,project:uni-center,volume:uni-center-uploads",
		Type: "S", Name: "Docker 受保护目标",
		Remark: "支持 容器名 / project:名 / project:名/服务 / volume:名 四种粒度，逗号分隔", Enabled: true},
	{Key: "sys.docker.transferDir", Value: "/var/lib/uni_agent/transfer", Type: "S", Name: "Docker 镜像产物目录",
		Remark: "image 导入导出的产物根目录（agent 自建，0700）", Enabled: true},
}

// seedDocker 追加 Docker 管理的菜单树与三个配置键。
//
// 结构（循环、幂等查重、parent 解析）**逐字照抄 v008_seed_device.go**：它已正确处理
// 顶层 dir（parent 为空 → parent_id=0）与 menu/btn 两种查重键。
func seedDocker(tx *gorm.DB) error {
	if err := seedDockerMenus(tx); err != nil {
		return err
	}
	return seedDockerConfig(tx)
}

// seedDockerMenus 写入 Docker 菜单（父先于子书写，父 ID 经 idByKey 传递）。
//
// 幂等查重走同包的 menuExists（与 v013 同款）：迁移框架保证同版本只执行一次，
// 但「从备份恢复 + 重启」会让同一 Up 在语义上重放；重复插入的症状是侧边栏
// 出现两条同名菜单或两个同名按钮。
func seedDockerMenus(tx *gorm.DB) error {
	idByKey := make(map[string]uint64, len(dockerMenuDefinitions))
	for _, d := range dockerMenuDefinitions {
		parentID := uint64(0)
		if d.Parent != "" {
			pid, ok := idByKey[d.Parent]
			if !ok {
				return fmt.Errorf("v015 菜单 %s 的父节点 %s 尚未创建（定义必须父先于子）", d.Key, d.Parent)
			}
			parentID = pid
		}
		dup, err := menuExists(tx, parentID, d)
		if err != nil {
			return err
		}
		if dup {
			continue
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
	return nil
}

// seedDockerConfig 按 config_key 去重地补齐配置键（与 v013 同款取向）：
// 真实库里这一行可能已被运维改过（例如把快照周期调大），种子的意义只是
// 「别让它缺失」，不是「让它回到默认值」—— 覆写会把运维调过的值打回原值，且不可观测。
func seedDockerConfig(tx *gorm.DB) error {
	for _, def := range dockerConfigDefinitions {
		var n int64
		if err := tx.Model(&entity.SysConfig{}).
			Where("config_key = ?", def.Key).Count(&n).Error; err != nil {
			return fmt.Errorf("v015 查询配置 %s: %w", def.Key, err)
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
			return fmt.Errorf("v015 写入配置 %s: %w", def.Key, err)
		}
	}
	return nil
}
