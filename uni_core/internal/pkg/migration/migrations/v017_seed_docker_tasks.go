package migrations

import (
	"errors"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/migration"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/util"
)

// v017 给操作日志结果码字典补一条「执行失败」(70001)。
//
// 为什么需要它：docker 指令结果审计（6c）在 sys_operation_log 上落**执行结果**，
// Code 只有 0=成功 与非成功两态能选。既有字典（v005）的失败码全是 HTTP 信封语义
// （40000 参数错误 / 50000 服务器内部错误……），拿「服务器内部错误」去标
// 「agent 回一句拉取镜像失败」是把用户的执行失败伪装成基础设施故障，排障的人
// 会照着错误的方向查。操作日志页的结果列与过滤下拉**完全由字典渲染**
// （uni_console useDict('sys_opt_result_code')），加一条数据即可被 UI 认领，
// 不新增字典类型、不新开表。
//
// 取 70000 族：与既有 1000x/40000/40400/40900/50000 都不相邻，留给后续
// 「执行取消」等结果细分位的编址空间（6b 盘点结论：取消目前无独立生产者，
// 语义由结论句承载；将来协议侧若出 cancelled 标志，接 70002 不入既有族）。
//
// 幂等：迁移框架按版本号只跑一次（v013 注释把这条讲透了），这里仍按
// type+value 判重 —— 半途重启/重复执行不产生重复行。
func init() {
	migration.Register(migration.Migration{
		Version:     17,
		Description: "操作日志结果码字典补「执行失败」(70001，docker 指令结果审计)",
		Up:          seedDockerTasksAuditDict,
	})
}

func seedDockerTasksAuditDict(tx *gorm.DB) error {
	var typ entity.SysDictType
	if err := tx.Where("code = ?", "sys_opt_result_code").First(&typ).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// v005 已保证这个字典类型存在（Version 5 < 17，必然先跑）；真缺了
			// 让迁移失败早暴露，好过把数据挂进一个不存在的 type_id。
			return err
		}
		return err
	}
	var n int64
	if err := tx.Model(&entity.SysDictData{}).
		Where("type_id = ? AND value = ?", typ.ID, "70001").Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return tx.Create(&entity.SysDictData{
		TypeID:    typ.ID,
		Label:     "执行失败",
		Value:     "70001",
		Sort:      util.Ptr(14),
		Status:    util.Ptr[int8](entity.DictDataStatusEnabled),
		ListClass: util.Ptr("danger"),
	}).Error
}
