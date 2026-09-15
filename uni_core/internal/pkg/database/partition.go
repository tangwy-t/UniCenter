package database

import (
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// 缺分区的驱动报码（spec §7.3：「缺分区是硬失败」）。
//
// 两个引擎的报码不同源，且**语义分叉**（spec 明文）：MySQL 的 RANGE 分区只有上界，
// 漏建未来分区时插入报 ERROR 1526（ER_NO_PARTITION_FOR_GIVEN_VALUE）；PG 的分区
// 有显式上下界，越界报 SQLSTATE 23514（check_violation）。
const (
	// mysqlErrNoPartitionForValue 是 MySQL 的 ER_NO_PARTITION_FOR_GIVEN_VALUE。
	mysqlErrNoPartitionForValue = 1526
	// pgSQLStateCheckViolation 是 PG 的 check_violation —— 「没有分区能收下这一行」。
	pgSQLStateCheckViolation = "23514"
)

// IsMissingPartition reports whether err means「没有任何分区能收下这一行」
// (MySQL ERROR 1526 / PG SQLSTATE 23514)，即 spec §7.3 的「缺分区硬失败」。
//
// 与 IsDuplicateKey 同一个体例（typed driver error 优先，配一条 GORM 已翻译哨兵）：
//   - MySQL：`*mysql.MySQLError` 且 Number == 1526 —— 生产主力引擎，必须精确匹配；
//   - PG：`*pgconn.PgError` 且 Code == "23514"；
//   - GORM 的 ErrCheckConstraintViolated：本仓库当前**没有**开 TranslateError
//     （database.New 只配了 Logger），故这条分支现在不可达；但 PG 的译文映射表
//     把 23514 映射成它（gorm.io/driver/postgres 的 errCodes），一旦将来打开
//     TranslateError，typed 错误会被替换成这个哨兵 —— 提前接住，
//     免得那时缺分区检测**静默失效**（症状是 flush 退回到「逐桶失败刷日志」）。
//
// 注意（照实说，而不是假装没有）：PG 的 23514 是 check_violation 的通用码，
// 真的 CHECK 约束违规也报它（spec §7.3 提到 `CHECK (resolution IN (300,3600))`）。
// 故 PG 上「真的 CHECK 违规」会被判成缺分区。方向是安全侧：缺分区的处置是
// 「P1 告警 + 中止本轮」，误判的代价是早停一次并报警，而漏判的代价是 500 台设备
// 逐桶重试、把真正的故障淹在日志里。MySQL 侧不存在这个歧义（1526 是专用码）。
func IsMissingPartition(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlErrNoPartitionForValue {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgSQLStateCheckViolation {
		return true
	}
	return errors.Is(err, gorm.ErrCheckConstraintViolated)
}
