// Package permission 定义系统权限标识的单一来源(single source of truth)。
//
// 同一权限码此前三处散落:router.go 的 perm("system:user:add")、
// migration v002 种子菜单里 Perms: "system:user:add"、以及前端 v-perm /
// v-auth 模板里的 'system:user:add' 字符串字面量。任一处改名、笔误或漏同步
// 都会静默导致鉴权失效或 UI 权限错误 —— 三者之间没有编译期关联。
//
// 收敛后:后端种子与路由都引用这里的同名常量(编译器保证一致),前端常量
// 由 apigen 生成自 All()(CI 漂移守卫)。新增权限码必须经过
// "定义常量 → 加入 All() → 在种子内引用"三步,配套测试保证三者始终一致。
package permission

// All 返回注册在案的全部权限码(升序)。它是向后端种子、前端生成器与漂移
// 守卫暴露的唯一枚举;新增常量后若忘记加入本切片,测试会失败。
func All() []string {
	return []string{
		PermCacheDelete,
		PermCacheList,
		PermCacheQuery,
		PermConfigAdd,
		PermConfigDelete,
		PermConfigEdit,
		PermConfigList,
		PermConfigQuery,
		PermDeptAdd,
		PermDeptDelete,
		PermDeptEdit,
		PermDeptList,
		PermDeptQuery,
		PermDeptSort,
		PermDictDataAdd,
		PermDictDataDelete,
		PermDictDataEdit,
		PermDictDataList,
		PermDictDataQuery,
		PermDictList,
		PermDictTypeAdd,
		PermDictTypeDelete,
		PermDictTypeEdit,
		PermDictTypeList,
		PermDictTypeQuery,
		PermFileDelete,
		PermFileDownload,
		PermFileEdit,
		PermFileList,
		PermFileUpload,
		PermJobAdd,
		PermJobDelete,
		PermJobEdit,
		PermJobExecute,
		PermJobList,
		PermJobLogDelete,
		PermJobLogList,
		PermJobOnce,
		PermJobPause,
		PermJobQuery,
		PermLogLoginList,
		PermLogOperationDelete,
		PermLogOperationList,
		PermMenuAdd,
		PermMenuDelete,
		PermMenuEdit,
		PermMenuList,
		PermMenuQuery,
		PermMenuSort,
		PermNoticeAdd,
		PermNoticeDelete,
		PermNoticeEdit,
		PermNoticeList,
		PermNoticePublish,
		PermNoticeQuery,
		PermOnlineKick,
		PermOnlineList,
		PermPprofDisable,
		PermPprofEnable,
		PermPprofList,
		PermRoleAdd,
		PermRoleAssign,
		PermRoleDelete,
		PermRoleEdit,
		PermRoleList,
		PermRoleQuery,
		PermRoleSort,
		PermRoleStatus,
		PermServerList,
		PermSqlList,
		PermUserAdd,
		PermUserAssign,
		PermUserDelete,
		PermUserDisable,
		PermUserEdit,
		PermUserEnable,
		PermUserList,
		PermUserQuery,
		PermUserReset,
		PermUserUnlock,
	}
}

// ── 缓存管理 / system:cache:* ────────────────────────────────────────────
const (
	PermCacheDelete = "system:cache:delete"
	PermCacheList   = "system:cache:list"
	PermCacheQuery  = "system:cache:query"
)

// ── 参数配置 / system:config:* ───────────────────────────────────────────
const (
	PermConfigAdd    = "system:config:add"
	PermConfigDelete = "system:config:delete"
	PermConfigEdit   = "system:config:edit"
	PermConfigList   = "system:config:list"
	PermConfigQuery  = "system:config:query"
)

// ── 部门管理 / system:dept:* ─────────────────────────────────────────────
const (
	PermDeptAdd    = "system:dept:add"
	PermDeptDelete = "system:dept:delete"
	PermDeptEdit   = "system:dept:edit"
	PermDeptList   = "system:dept:list"
	PermDeptQuery  = "system:dept:query"
	PermDeptSort   = "system:dept:sort"
)

// ── 字典管理 / system:dict:* ─────────────────────────────────────────────
const (
	PermDictDataAdd    = "system:dict:data:add"
	PermDictDataDelete = "system:dict:data:delete"
	PermDictDataEdit   = "system:dict:data:edit"
	PermDictDataList   = "system:dict:data:list"
	PermDictDataQuery  = "system:dict:data:query"
	PermDictList       = "system:dict:list"
	PermDictTypeAdd    = "system:dict:type:add"
	PermDictTypeDelete = "system:dict:type:delete"
	PermDictTypeEdit   = "system:dict:type:edit"
	PermDictTypeList   = "system:dict:type:list"
	PermDictTypeQuery  = "system:dict:type:query"
)

// ── 文件管理 / system:file:* ─────────────────────────────────────────────
const (
	PermFileDelete   = "system:file:delete"
	PermFileDownload = "system:file:download"
	PermFileEdit     = "system:file:edit"
	PermFileList     = "system:file:list"
	PermFileUpload   = "system:file:upload"
)

// ── 定时任务 / system:job:* ──────────────────────────────────────────────
const (
	PermJobAdd       = "system:job:add"
	PermJobDelete    = "system:job:delete"
	PermJobEdit      = "system:job:edit"
	PermJobExecute   = "system:job:execute"
	PermJobList      = "system:job:list"
	PermJobLogDelete = "system:job:log:delete"
	PermJobLogList   = "system:job:log:list"
	PermJobOnce      = "system:job:once"
	PermJobPause     = "system:job:pause"
	PermJobQuery     = "system:job:query"
)

// ── 日志管理 / system:log:* ──────────────────────────────────────────────
const (
	PermLogLoginList       = "system:log:login:list"
	PermLogOperationDelete = "system:log:operation:delete"
	PermLogOperationList   = "system:log:operation:list"
)

// ── 菜单管理 / system:menu:* ─────────────────────────────────────────────
const (
	PermMenuAdd    = "system:menu:add"
	PermMenuDelete = "system:menu:delete"
	PermMenuEdit   = "system:menu:edit"
	PermMenuList   = "system:menu:list"
	PermMenuQuery  = "system:menu:query"
	PermMenuSort   = "system:menu:sort"
)

// ── 通知公告 / system:notice:* ───────────────────────────────────────────
const (
	PermNoticeAdd     = "system:notice:add"
	PermNoticeDelete  = "system:notice:delete"
	PermNoticeEdit    = "system:notice:edit"
	PermNoticeList    = "system:notice:list"
	PermNoticePublish = "system:notice:publish"
	PermNoticeQuery   = "system:notice:query"
)

// ── pprof 性能分析 / system:pprof:* ──────────────────────────────────────
const (
	PermPprofDisable = "system:pprof:disable"
	PermPprofEnable  = "system:pprof:enable"
	PermPprofList    = "system:pprof:list"
)

// ── 角色管理 / system:role:* ─────────────────────────────────────────────
const (
	PermRoleAdd    = "system:role:add"
	PermRoleAssign = "system:role:assign"
	PermRoleDelete = "system:role:delete"
	PermRoleEdit   = "system:role:edit"
	PermRoleList   = "system:role:list"
	PermRoleQuery  = "system:role:query"
	PermRoleSort   = "system:role:sort"
	PermRoleStatus = "system:role:status"
)

// ── 系统监控 / system:server:*, system:sql:* ─────────────────────────────
const (
	PermServerList = "system:server:list"
	PermSqlList    = "system:sql:list"
)

// ── 在线用户 / system:monitor:online:* ────────────────────────────────────
const (
	PermOnlineKick = "system:monitor:online:kick"
	PermOnlineList = "system:monitor:online:list"
)

// ── 用户管理 / system:user:* ─────────────────────────────────────────────
const (
	PermUserAdd     = "system:user:add"
	PermUserAssign  = "system:user:assign"
	PermUserDelete  = "system:user:delete"
	PermUserDisable = "system:user:disable"
	PermUserEdit    = "system:user:edit"
	PermUserEnable  = "system:user:enable"
	PermUserList    = "system:user:list"
	PermUserQuery   = "system:user:query"
	PermUserReset   = "system:user:reset"
	PermUserUnlock  = "system:user:unlock"
)
