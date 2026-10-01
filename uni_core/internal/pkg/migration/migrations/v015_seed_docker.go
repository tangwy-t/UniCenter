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
		Description: "新增 Docker 管理菜单（总览/容器/镜像与存储/项目）、按钮权限与配置键",
		Up:          seedDocker,
	})
}

// dockerMenuDefinitions 是 Docker 管理域的菜单定义（父必须先于子书写）。
//
// `Path` 必须与 uni_console/src/modules/docker/index.ts 注册的路由**逐字一致**：
// MenuProcessor 用菜单 path 去前端路由表取组件，取不到就**整条菜单静默消失**
// （无报错、无日志）。该约束由前端侧守卫测试钉住（v015_seed_docker_frontend_test.go）。
//
// 权限码与菜单的**双向**约束（v002_seed_menu_perm_test）：菜单引用的码必须在 All()
// 注册，All() 里每个码也必须被某个菜单引用 —— 故六个码各有一个菜单/按钮承载。
//
// ── 7a 旧页收敛：种子按「初始化即最终态」构造 ────────────────────────
//
// 本列表当前是 Docker 域的**最终菜单面**：总览 /docker、容器 /docker/containers、
// 镜像与存储 /docker/resources、项目 /docker/projects。它是两次历史变更的合并结果：
//
//   - v016 曾把「Docker 总览」（/docker）作为独立批次追加 —— 已并入本列表
//     （v016 文件现为墓碑，说明见 v016_seed_docker_overview.go）；
//   - images / volumes / networks 三个列表菜单曾被本迁移种下 —— 7a 把三个旧列表页
//     收敛为 /docker/resources 的三个 tab 后**整行删除**（不留菜单、不留 redirect，
//     开发阶段零兼容负担：已迁移的开发库按重置口径处理，见 v016 墓碑文件头的说明）。
//
// 「初始化即最终态」的取向：fresh 库只跑一次 v015 就得到正确菜单，不再靠
// v016/v018 的增删链把历史形状搬运到最终形状 —— 迁移链是给**数据**记账的，
// 开发期反复改种子时，链条越长越容易在中途形状上出错。
var dockerMenuDefinitions = []menuDef{
	{Key: "docker", Name: "Docker 管理", Type: "dir", Sort: 3, Icon: "ri:stack-line"},
	// 控制塔总览（原 v016）：Sort=0，点开目录先看到跨主机总览，再进各资源页。
	// Icon 复用舰队隐喻 ri:ship-line（与总览响应的 fleet KPI 同一套语言）。
	{Key: "docker:overview", Parent: "docker", Name: "Docker 总览", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker",
		Component: "docker/overview", Sort: 0, Icon: "ri:ship-line"},
	{Key: "docker:containers", Parent: "docker", Name: "容器", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/containers", Component: "docker/containers",
		Sort: 1, Icon: "ri:ship-line"},
	// 镜像与存储（7a 新增）：镜像/数据卷/网络三个旧列表页收敛为一个 tab 容器页。
	// Sort=2：容器之后、项目之前 —— 与被替换的三条旧菜单（原 Sort 2/3/4）占同一段。
	// Icon 取 ri:database-2-line（存储池隐喻）：页面名以「镜像」开头、以「存储」收尾，
	// 单取 box（镜像）或 hard-drive（磁盘）都会偏到一半；ri: 前缀整套离线内置，
	// 不用担心离线图标集。
	{Key: "docker:resources", Parent: "docker", Name: "镜像与存储", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/resources", Component: "docker/resources",
		Sort: 2, Icon: "ri:database-2-line"},
	{Key: "docker:projects", Parent: "docker", Name: "项目", Type: "menu",
		Perms: permission.PermDockerList, Path: "/docker/projects", Component: "docker/projects",
		Sort: 3, Icon: "ri:stack-line"},

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
// 幂等查重：迁移框架保证同版本只执行一次，但「从备份恢复 + 重启」会让同一 Up
// 在语义上重放（备份里的 sys_migration 落后于实际数据）；重复插入的症状是侧边栏
// 出现两条同名菜单或两个同名按钮。
//
// **重放路径靠 idByKey 回填兜住**：查重命中时不 `continue` 完事，而是用
// findMenuID 把既有行的 ID 回读进 idByKey —— 否则父节点被整体跳过，其子节点在
// 下一轮迭代解析父键失败，整条迁移报错回滚（migration.Run 里 Up 出错即中止）。
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
		dup, err := dockerMenuExists(tx, parentID, d)
		if err != nil {
			return err
		}
		if dup {
			// 回填既有行的 ID：父节点必须仍可解析，否则子节点在下一轮报错。
			existingID, err := findMenuID(tx, "parent_id = ? AND name = ? AND type = ?", parentID, d.Name, d.Type)
			if err != nil {
				// 查重命中却回读不到 = 库里这棵子树与本定义不一致（例如菜单被
				// 改名过）。静默跳过只会把错误推迟到子节点，且报错指向错的地方。
				return fmt.Errorf("v015 菜单 %s 查重命中但按 (parent_id,name,type) 回读失败（库中菜单树与本定义不一致）: %w", d.Key, err)
			}
			idByKey[d.Key] = existingID
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

// dockerMenuExists 是 v015 **专用**的查重口径（不动同包 v013 的 menuExists，
// 别的迁移在用它）。
//
// 为什么 dir 必须单独判：目录节点既无 Path 也无 Perms，若沿用 menuExists 的
// 「Path 为空 → 比 perms」分支，它会拿一个空串 perms 去比 —— 那会匹配到**别的**
// 顶级目录（任何 perms 为空串的根目录），症状是「docker 目录永不创建，紧接着
// 5 个子菜单报父节点缺失」。故：
//
//   - dir  ：按 (parent_id, name, type) —— 目录的身份就是「顶层下的这个分类」；
//   - menu ：按 path（前端路由逐字一致，改 path 即新页面）；
//   - btn  ：按 perms（按钮无 path，权限码即身份）。
//
// 7a 提示：菜单查重键是 path —— 已按旧 v015 执行过的开发库里，images/volumes/
// networks 三行的 path 不在本定义里，若手工让 v015 重放会**再种一遍新面**而不是
// 清掉旧行（重放语义是「补缺」不是「对账」）。这正是开发库走重置口径的原因之一。
func dockerMenuExists(tx *gorm.DB, parentID uint64, d menuDef) (bool, error) {
	q := tx.Model(&entity.SysMenu{}).Where("parent_id = ?", parentID)
	switch {
	case d.Type == "dir":
		q = q.Where("name = ? AND type = ?", d.Name, d.Type)
	case d.Path != "":
		q = q.Where("path = ?", d.Path)
	default:
		q = q.Where("perms = ?", d.Perms)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, fmt.Errorf("v015 查询菜单 %s: %w", d.Key, err)
	}
	return n > 0, nil
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
