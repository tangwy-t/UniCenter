package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
)

// ── 墓碑：v017 的内容已并入 v005（7c 迁移链归一）──────────────────────────
//
// v017 原本给操作日志结果码字典（sys_opt_result_code）补一条「执行失败」(70001，
// docker 指令结果审计 6c 的消费侧)。7c 归一把它并入 v005 的初始字典种子
//（dictDefinitions —— 70001 行随「初始化即最终态」一次种齐），本迁移的定义与
// 执行函数已删除，版本号保留为**墓碑**（Up 为 no-op）。
//
// 为什么是墓碑而不是删文件（与 v016 同一裁决，说明见 v016_seed_docker_overview.go）：
//
//   - 迁移框架按「注册表 vs sys_migration 最高版本」求差集，版本号允许不连续 ——
//     删掉 v017 对已执行过的库同样无伤害；但保留墓碑让 sys_migration 账本保持
//     **单调且自解释**：旧库里「17 已执行」那行有代码可对照，新库里账本多一行
//     「17 = 并入 v005」的记录，考古时不用猜 16 与 18 之间发生过什么；
//   - 框架的 forward-only 教义（migration.go 文件头）本就禁止删已发布的迁移文件 ——
//     开发期重构虽被豁免「不改旧种子」，但**删版本号**比留墓碑多打破一条账本
//     约定，收益为零。
//
// 与 v016 墓碑的一个差别（为什么这次归一更便宜）：v016 并入的是**菜单形状**
// （旧库的菜单面停在收敛前，需要重置口径处理）；v017 并入的是**纯追加的字典行**
// —— 已执行过旧 v017 的开发库本来就有 70001 这行，fresh 库由新 v005 一次种齐，
// 两类库的字典内容一致，不需要任何 reconcile。sys_migration 账本「17 的描述文本
// 与现行代码不一致」这一点维持既定口径：开发库重置，不做对账迁移。
func init() {
	migration.Register(migration.Migration{
		Version:     17,
		Description: "墓碑：执行失败(70001)字典码已并入 v005 初始字典种子（7c 迁移链归一）",
		Up: func(tx *gorm.DB) error {
			// no-op：内容在 v005 的 dictDefinitions 里（fresh 库由 v005 一次种齐；
			// 已执行过旧 v017 的库本来就有这行字典，无需动作）。
			return nil
		},
	})
}
