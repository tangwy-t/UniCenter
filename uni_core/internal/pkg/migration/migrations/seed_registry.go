package migrations

// menuDefBatches 汇总**全部菜单种子批次**，供权限双向守卫
// (v002_seed_menu_perm_test.go) 统一扫描。
//
// 为什么需要它：该守卫既要检查「菜单引用的权限码都已注册进 All()」，
// 也要检查「All() 里每个权限码都被某个菜单引用」。菜单定义会随新迁移
// 增加批次（v002 系统菜单、v008 设备管理…），若守卫只扫其中一批，
// 另一批的权限码就会被误判成「死常量」（这正是加上 device:* 时的现象）。
//
// 新增菜单种子迁移时：把该批定义追加进来，否则守卫会红。
var menuDefBatches = [][]menuDef{
	menuDefinitions,               // v002 系统菜单
	deviceMenuDefinitions,         // v008 设备管理菜单
	deviceOverviewMenuDefinitions, // v012 设备监控总览菜单
}
