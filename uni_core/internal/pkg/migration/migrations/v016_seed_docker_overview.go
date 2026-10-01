package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
)

// ── 墓碑：v016 的内容已并入 v015（7a 旧页收敛）──────────────────────────
//
// v016 原本把「Docker 总览」菜单（/docker）作为独立批次追加到 v015 种下的菜单树上。
// 7a 收敛把 Docker 菜单种子重构为「初始化即最终态」：v015 直接种子最终菜单面
// （总览 / 容器 / 镜像与存储 / 项目，见 v015 的 dockerMenuDefinitions），本迁移
// 的定义与执行函数已删除，版本号保留为**墓碑**（Up 为 no-op）。
//
// 为什么是墓碑而不是删文件：
//
//   - 迁移框架按「注册表 vs sys_migration 最高版本」求差集（Run 的 lastApplied
//     扫的是注册表），版本号允许不连续 —— 删掉 v016 对已执行过的库同样无伤害；
//     但保留墓碑让 sys_migration 账本保持**单调且自解释**：旧库里「16 已执行」
//     那行有代码可对照，新库里账本多一行「16 = 并入 v015」的记录，考古时不用
//     猜 15→17 之间发生过什么。
//   - 框架的 forward-only 教义（migration.go 文件头）本就禁止删已发布的迁移文件
//     —— 这里是开发期重构，协调方明确豁免了「不改旧种子」的约束，但**删版本号**
//     比留墓碑多打破一条账本约定，收益为零。
//
// 已迁移的开发库怎么办（协调方定的口径：重置，不做 reconcile）：
//
//	旧库的 sys_migration 记着 15/16/17，新 v015 定义不会重放 —— 它的菜单面停在
//	收敛前（5 个列表菜单 + 总览），缺「镜像与存储」且多三条旧列表行；旧 path 的
//	前端路由已删，那三条菜单会被 MenuProcessor 判定「无对应页面」而静默消失，
//	功能上只剩「侧边栏缺镜像与存储入口」。处理口径：**开发库直接重置**（删
//	sys_menu 里 parent 为「Docker 管理」目录的子树后重放种子，或整库重建）。
//	不为几台开发机写一条 reconcile 迁移 —— 它要把刚被宣告死亡的旧菜单形状
//	（/docker/images 三行）永久编码进迁移史，纯粹的死代码。
func init() {
	migration.Register(migration.Migration{
		Version:     16,
		Description: "墓碑：Docker 总览菜单已并入 v015 最终种子（7a 旧页收敛）",
		Up: func(tx *gorm.DB) error {
			// no-op：内容在 v015 的 dockerMenuDefinitions 里（fresh 库由 v015
			// 一次种齐；已执行过旧 v016 的库本来就有这行菜单，无需动作）。
			return nil
		},
	})
}
