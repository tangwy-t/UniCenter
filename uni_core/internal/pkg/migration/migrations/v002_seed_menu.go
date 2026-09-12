package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	"github.com/tangwy-t/webmanager-server/internal/pkg/permission"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     2,
		Description: "初始化系统菜单",
		Up:          seedMenu,
	})
}

// menuDef 描述一个菜单节点。Key 是逻辑名，用于建立父子引用（避免硬编码
// snowflake ID）；Parent 为空表示顶级目录。父节点必须早于子节点书写。
type menuDef struct {
	Key       string
	Parent    string
	Name      string
	Type      string // dir / menu / btn
	Perms     string
	Path      string
	Component string
	Sort      int
	Icon      string
}

// menuDefinitions 是完整系统菜单定义的唯一来源，按“先父后子”书写，保证
// seedMenu 逐条解析父引用时父节点的 snowflake ID 已经生成。
// 层级与 sort/icon 对齐当前生产数据库（服务监控、日志管理为根级目录）。
var menuDefinitions = []menuDef{

	// ── 服务监控 (dir, 根级) ───────────────────────────────────
	{Key: "monitor", Name: "服务监控", Type: "dir", Sort: 0, Icon: "ri:dashboard-2-line"},
	{Key: "monitor:server", Parent: "monitor", Name: "服务器监控", Type: "menu", Perms: permission.PermServerList, Path: "/monitor/server", Component: "monitor/server/index", Sort: 1, Icon: "material-symbols:browse-activity-outline-rounded"},
	{Key: "monitor:pprof", Parent: "monitor", Name: "pprof", Type: "menu", Perms: permission.PermPprofList, Path: "/monitor/pprof", Component: "monitor/pprof/index", Sort: 2, Icon: "boxicons:hot"},
	{Key: "monitor:pprof:enable", Parent: "monitor:pprof", Name: "启用pprof", Type: "btn", Perms: permission.PermPprofEnable, Sort: 1},
	{Key: "monitor:pprof:disable", Parent: "monitor:pprof", Name: "停用pprof", Type: "btn", Perms: permission.PermPprofDisable, Sort: 2},
	{Key: "monitor:cache", Parent: "monitor", Name: "缓存管理", Type: "menu", Perms: permission.PermCacheList, Path: "/monitor/cache", Component: "monitor/cache/index", Sort: 3, Icon: "devicon-plain:redis-wordmark"},
	{Key: "monitor:cache:query", Parent: "monitor:cache", Name: "缓存查询", Type: "btn", Perms: permission.PermCacheQuery, Sort: 1},
	{Key: "monitor:cache:delete", Parent: "monitor:cache", Name: "缓存删除", Type: "btn", Perms: permission.PermCacheDelete, Sort: 2},
	{Key: "monitor:sql", Parent: "monitor", Name: "SQL监控", Type: "menu", Perms: permission.PermSqlList, Path: "/monitor/sql", Component: "monitor/sql/index", Sort: 4, Icon: "hugeicons:sql"},
	{Key: "monitor:online", Parent: "monitor", Name: "在线用户", Type: "menu", Perms: permission.PermOnlineList, Path: "/monitor/online", Component: "monitor/online/index", Sort: 5, Icon: "streamline-flex:user-sync-online-in-person-solid"},
	{Key: "monitor:online:kick", Parent: "monitor:online", Name: "强制下线", Type: "btn", Perms: permission.PermOnlineKick, Sort: 1},

	// ── 系统管理 (dir, 根级) ───────────────────────────────────
	{Key: "system", Name: "系统管理", Type: "dir", Sort: 1, Icon: "ri:settings-3-line"},

	// ── 用户管理 ───────────────────────────────────────────────
	{Key: "user", Parent: "system", Name: "用户管理", Type: "menu", Perms: permission.PermUserList, Path: "/system/user", Component: "system/user/index", Sort: 1, Icon: "ri:group-line"},
	{Key: "user:query", Parent: "user", Name: "用户查询", Type: "btn", Perms: permission.PermUserQuery, Sort: 1},
	{Key: "user:add", Parent: "user", Name: "用户新增", Type: "btn", Perms: permission.PermUserAdd, Sort: 2},
	{Key: "user:edit", Parent: "user", Name: "用户修改", Type: "btn", Perms: permission.PermUserEdit, Sort: 3},
	{Key: "user:delete", Parent: "user", Name: "用户删除", Type: "btn", Perms: permission.PermUserDelete, Sort: 4},
	{Key: "user:enable", Parent: "user", Name: "启用用户", Type: "btn", Perms: permission.PermUserEnable, Sort: 5},
	{Key: "user:disable", Parent: "user", Name: "停用用户", Type: "btn", Perms: permission.PermUserDisable, Sort: 6},
	{Key: "user:reset", Parent: "user", Name: "重置密码", Type: "btn", Perms: permission.PermUserReset, Sort: 7},
	{Key: "user:unlock", Parent: "user", Name: "解锁用户", Type: "btn", Perms: permission.PermUserUnlock, Sort: 8},
	{Key: "user:assign", Parent: "user", Name: "分配角色", Type: "btn", Perms: permission.PermUserAssign, Sort: 9},

	// ── 角色管理 ───────────────────────────────────────────────
	{Key: "role", Parent: "system", Name: "角色管理", Type: "menu", Perms: permission.PermRoleList, Path: "/system/role", Component: "system/role/index", Sort: 2, Icon: "ri:shield-user-line"},
	{Key: "role:query", Parent: "role", Name: "角色查询", Type: "btn", Perms: permission.PermRoleQuery, Sort: 1},
	{Key: "role:add", Parent: "role", Name: "角色新增", Type: "btn", Perms: permission.PermRoleAdd, Sort: 2},
	{Key: "role:edit", Parent: "role", Name: "角色修改", Type: "btn", Perms: permission.PermRoleEdit, Sort: 3},
	{Key: "role:delete", Parent: "role", Name: "角色删除", Type: "btn", Perms: permission.PermRoleDelete, Sort: 4},
	{Key: "role:assign", Parent: "role", Name: "分配用户", Type: "btn", Perms: permission.PermRoleAssign, Sort: 5},
	{Key: "role:status", Parent: "role", Name: "角色状态", Type: "btn", Perms: permission.PermRoleStatus, Sort: 6},
	{Key: "role:sort", Parent: "role", Name: "角色排序", Type: "btn", Perms: permission.PermRoleSort, Sort: 7},

	// ── 菜单管理 ───────────────────────────────────────────────
	{Key: "menu", Parent: "system", Name: "菜单管理", Type: "menu", Perms: permission.PermMenuList, Path: "/system/menu", Component: "system/menu/index", Sort: 3, Icon: "ri:apps-2-line"},
	{Key: "menu:query", Parent: "menu", Name: "菜单查询", Type: "btn", Perms: permission.PermMenuQuery, Sort: 1},
	{Key: "menu:add", Parent: "menu", Name: "菜单新增", Type: "btn", Perms: permission.PermMenuAdd, Sort: 2},
	{Key: "menu:edit", Parent: "menu", Name: "菜单修改", Type: "btn", Perms: permission.PermMenuEdit, Sort: 3},
	{Key: "menu:delete", Parent: "menu", Name: "菜单删除", Type: "btn", Perms: permission.PermMenuDelete, Sort: 4},
	{Key: "menu:sort", Parent: "menu", Name: "菜单排序", Type: "btn", Perms: permission.PermMenuSort, Sort: 5},

	// ── 部门管理 ───────────────────────────────────────────────
	{Key: "dept", Parent: "system", Name: "部门管理", Type: "menu", Perms: permission.PermDeptList, Path: "/system/dept", Component: "system/dept/index", Sort: 4, Icon: "ri:organization-chart"},
	{Key: "dept:query", Parent: "dept", Name: "部门查询", Type: "btn", Perms: permission.PermDeptQuery, Sort: 1},
	{Key: "dept:add", Parent: "dept", Name: "部门新增", Type: "btn", Perms: permission.PermDeptAdd, Sort: 2},
	{Key: "dept:edit", Parent: "dept", Name: "部门修改", Type: "btn", Perms: permission.PermDeptEdit, Sort: 3},
	{Key: "dept:delete", Parent: "dept", Name: "部门删除", Type: "btn", Perms: permission.PermDeptDelete, Sort: 4},
	{Key: "dept:sort", Parent: "dept", Name: "部门排序", Type: "btn", Perms: permission.PermDeptSort, Sort: 5},

	// ── 字典管理 ───────────────────────────────────────────────
	{Key: "dict", Parent: "system", Name: "字典管理", Type: "menu", Perms: permission.PermDictList, Path: "/system/dict", Component: "system/dict/index", Sort: 5, Icon: "material-symbols:book-3-outline"},
	{Key: "dict:type:list", Parent: "dict", Name: "字典类型列表", Type: "btn", Perms: permission.PermDictTypeList, Sort: 0},
	{Key: "dict:type:query", Parent: "dict", Name: "查询字典类型", Type: "btn", Perms: permission.PermDictTypeQuery, Sort: 1},
	{Key: "dict:type:add", Parent: "dict", Name: "新增字典类型", Type: "btn", Perms: permission.PermDictTypeAdd, Sort: 2},
	{Key: "dict:type:edit", Parent: "dict", Name: "修改字典类型", Type: "btn", Perms: permission.PermDictTypeEdit, Sort: 3},
	{Key: "dict:type:delete", Parent: "dict", Name: "删除字典类型", Type: "btn", Perms: permission.PermDictTypeDelete, Sort: 4},
	{Key: "dict:data:query", Parent: "dict", Name: "查询字典数据", Type: "btn", Perms: permission.PermDictDataQuery, Sort: 5},
	{Key: "dict:data:add", Parent: "dict", Name: "新增字典数据", Type: "btn", Perms: permission.PermDictDataAdd, Sort: 6},
	{Key: "dict:data:edit", Parent: "dict", Name: "修改字典数据", Type: "btn", Perms: permission.PermDictDataEdit, Sort: 7},
	{Key: "dict:data:delete", Parent: "dict", Name: "删除字典数据", Type: "btn", Perms: permission.PermDictDataDelete, Sort: 8},
	{Key: "dict:data:list", Parent: "dict", Name: "字典数据列表", Type: "btn", Perms: permission.PermDictDataList, Sort: 9},

	// ── 参数配置 ───────────────────────────────────────────────
	{Key: "config", Parent: "system", Name: "参数配置", Type: "menu", Perms: permission.PermConfigList, Path: "/system/config", Component: "system/config/index", Sort: 6, Icon: "material-symbols:build-outline"},
	{Key: "config:query", Parent: "config", Name: "查询参数", Type: "btn", Perms: permission.PermConfigQuery, Sort: 0},
	{Key: "config:add", Parent: "config", Name: "新增参数", Type: "btn", Perms: permission.PermConfigAdd, Sort: 1},
	{Key: "config:edit", Parent: "config", Name: "修改参数", Type: "btn", Perms: permission.PermConfigEdit, Sort: 2},
	{Key: "config:delete", Parent: "config", Name: "删除参数", Type: "btn", Perms: permission.PermConfigDelete, Sort: 3},

	// ── 通知公告 ───────────────────────────────────────────────
	{Key: "notice", Parent: "system", Name: "通知公告", Type: "menu", Perms: permission.PermNoticeList, Path: "/system/notice", Component: "system/notice/index", Sort: 7, Icon: "ri:bell-line"},
	{Key: "notice:query", Parent: "notice", Name: "查询通知", Type: "btn", Perms: permission.PermNoticeQuery, Sort: 0},
	{Key: "notice:add", Parent: "notice", Name: "新增通知", Type: "btn", Perms: permission.PermNoticeAdd, Sort: 1},
	{Key: "notice:edit", Parent: "notice", Name: "修改通知", Type: "btn", Perms: permission.PermNoticeEdit, Sort: 2},
	{Key: "notice:delete", Parent: "notice", Name: "删除通知", Type: "btn", Perms: permission.PermNoticeDelete, Sort: 3},
	{Key: "notice:publish", Parent: "notice", Name: "发布通知", Type: "btn", Perms: permission.PermNoticePublish, Sort: 4},

	// ── 文件管理 ───────────────────────────────────────────────
	{Key: "file", Parent: "system", Name: "文件管理", Type: "menu", Perms: permission.PermFileList, Path: "/system/file", Component: "system/file/index", Sort: 8, Icon: "ri:folder-settings-line"},
	{Key: "file:upload", Parent: "file", Name: "上传文件", Type: "btn", Perms: permission.PermFileUpload, Sort: 0},
	{Key: "file:delete", Parent: "file", Name: "删除文件", Type: "btn", Perms: permission.PermFileDelete, Sort: 1},
	{Key: "file:download", Parent: "file", Name: "下载文件", Type: "btn", Perms: permission.PermFileDownload, Sort: 3},
	{Key: "file:edit", Parent: "file", Name: "编辑文件", Type: "btn", Perms: permission.PermFileEdit, Sort: 4},

	// ── 定时任务管理 ──────────────────────────────────────────
	{Key: "job", Parent: "system", Name: "定时任务", Type: "menu", Perms: permission.PermJobList, Path: "/system/job", Component: "system/job/index", Sort: 9, Icon: "ri:alarm-line"},
	{Key: "job:query", Parent: "job", Name: "查询任务", Type: "btn", Perms: permission.PermJobQuery, Sort: 1},
	{Key: "job:add", Parent: "job", Name: "新增任务", Type: "btn", Perms: permission.PermJobAdd, Sort: 2},
	{Key: "job:edit", Parent: "job", Name: "修改任务", Type: "btn", Perms: permission.PermJobEdit, Sort: 3},
	{Key: "job:delete", Parent: "job", Name: "删除任务", Type: "btn", Perms: permission.PermJobDelete, Sort: 4},
	{Key: "job:execute", Parent: "job", Name: "执行任务", Type: "btn", Perms: permission.PermJobExecute, Sort: 5},
	{Key: "job:pause", Parent: "job", Name: "暂停任务", Type: "btn", Perms: permission.PermJobPause, Sort: 6},
	{Key: "job:once", Parent: "job", Name: "执行一次", Type: "btn", Perms: permission.PermJobOnce, Sort: 7},
	{Key: "job:log:list", Parent: "job", Name: "任务日志", Type: "btn", Perms: permission.PermJobLogList, Sort: 8},
	{Key: "job:log:delete", Parent: "job", Name: "清空日志", Type: "btn", Perms: permission.PermJobLogDelete, Sort: 9},

	// ── 日志管理 (dir, 根级) ───────────────────────────────────
	{Key: "log", Name: "日志管理", Type: "dir", Sort: 3, Icon: "streamline-plump:log"},

	// ── 操作日志 ───────────────────────────────────────────────
	{Key: "operlog", Parent: "log", Name: "操作日志", Type: "menu", Perms: permission.PermLogOperationList, Path: "/monitor/operlog", Component: "monitor/operlog/index", Sort: 1, Icon: "streamline-ultimate:common-file-text-clock"},
	{Key: "operlog:query", Parent: "operlog", Name: "操作日志查询", Type: "btn", Perms: permission.PermLogOperationList, Sort: 1},
	{Key: "operlog:delete", Parent: "operlog", Name: "操作日志删除", Type: "btn", Perms: permission.PermLogOperationDelete, Sort: 2},

	// ── 登录日志 ───────────────────────────────────────────────
	{Key: "loginlog", Parent: "log", Name: "登录日志", Type: "menu", Perms: permission.PermLogLoginList, Path: "/monitor/logininfor", Component: "monitor/logininfor/index", Sort: 2, Icon: "tdesign:user-time"},
	{Key: "loginlog:query", Parent: "loginlog", Name: "登录日志查询", Type: "btn", Perms: permission.PermLogLoginList, Sort: 1},
}

// seedMenu 按“层级”批量创建菜单：parent_id 是父节点的 snowflake ID（创建时才
// 生成），因此同一层（父键已就绪）的节点先成批 CREATE，回读 ID 后再进入下一层。
// menuDefinitions 已按“父先于子”书写，BFS 逐层推进即可，全程无硬编码 ID。
func seedMenu(tx *gorm.DB) error {
	idByKey := make(map[string]uint64, len(menuDefinitions))
	used := make([]bool, len(menuDefinitions))

	for {
		batch := make([]entity.SysMenu, 0, 16)
		var keys []string // 与 batch 同序，创建后据此回读 ID

		for i, d := range menuDefinitions {
			if used[i] {
				continue
			}
			if d.Parent != "" {
				if _, ok := idByKey[d.Parent]; !ok {
					continue // 父尚未创建，留到下一轮
				}
			}
			parentID := uint64(0)
			if d.Parent != "" {
				parentID = idByKey[d.Parent]
			}
			batch = append(batch, entity.SysMenu{
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
			})
			keys = append(keys, d.Key)
			used[i] = true
		}

		if len(batch) == 0 {
			break
		}
		if err := tx.CreateInBatches(batch, 100).Error; err != nil {
			return err
		}
		// snowflake 回调在 Create 时就地回写了 batch[i].ID，按序落映射。
		for i, key := range keys {
			idByKey[key] = batch[i].ID
		}
	}

	if len(idByKey) != len(menuDefinitions) {
		return gorm.ErrRecordNotFound
	}
	// 菜单 ID 无需对外共享：admin 默认全量菜单，普通角色授权由运营在
	// 菜单/角色页手动配置，种子间不传递包级变量。
	return nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return util.Ptr(s)
}
