package repository

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrNotFound 是**仓储层未命中**哨兵：目标行不存在（或被软删）。
//
// 为什么需要它，而不是直接上抛 gorm.ErrRecordNotFound：
//
//	调用方（service 层）必须区分「未命中」与「DB 故障」——前者 404，后者 500。
//	gorm.ErrRecordNotFound 是 *那一个* 全局错误值：仓储在 Update/Delete 的
//	RowsAffected==0 分支上返回它时，与「查询真没查到」在类型上无法区分；
//	service 层若把任何 err 都当成未命中，就会把 DB 故障伪装成 404
//	（Task 9 上报的观察点）。
//
// 用法约定：仓储对「未命中」返回本哨兵，对「真错误」返回原始 err（或包装它）；
// 调用方用 errors.Is(err, repository.ErrNotFound) 判定，不要用 == 比较。
//
// 本哨兵**刻意包装了** gorm.ErrRecordNotFound：既有调用方与仓储注释契约
// （「未命中返回 gorm.ErrRecordNotFound」）用 errors.Is(err, gorm.ErrRecordNotFound)
// 判定未命中时继续成立 —— 引入哨兵只增加可分辨性，不改变既有语义。
var ErrNotFound = fmt.Errorf("%w (repository: row not found)", gorm.ErrRecordNotFound)

// notFoundOr 把查询的「未命中」统一成 ErrNotFound 哨兵，其它错误原样返回。
//
// 为什么需要这个归一：哨兵**包装了** gorm.ErrRecordNotFound，故
// `errors.Is(err, gorm.ErrRecordNotFound)` 对哨兵成立，而反向
// `errors.Is(gorm.ErrRecordNotFound, ErrNotFound)` **不成立** —— 也就是说
// 「查询路径」若把 gorm 的原始错误直接上抛，service 层用
// `errors.Is(err, repository.ErrNotFound)` 判别未命中就会**判不出来**，
// 于是真实的「设备不存在」被当成 DB 故障映射成 500（S5 的另一半）。
//
// 幂等：notFoundOr(ErrNotFound) == ErrNotFound。
func notFoundOr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
