package database

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// TestIsMissingPartitionMySQL1526 钉住生产主力引擎的报码：
// MySQL 漏建未来分区时插入报 ERROR 1526（ER_NO_PARTITION_FOR_GIVEN_VALUE）。
func TestIsMissingPartitionMySQL1526(t *testing.T) {
	err := &mysql.MySQLError{Number: 1526, Message: "Table has no partition for value 1800000000"}
	if !IsMissingPartition(err) {
		t.Fatal("MySQL 1526 未被识别为缺分区")
	}
	// 包一层（生产路径上 flush 会给它包上 device/bucket 上下文，用的是 %w）。
	wrapped := fmt.Errorf("写 5m 桶 device=1001 bucket=1800000000: %w", err)
	if !IsMissingPartition(wrapped) {
		t.Fatal("被 %w 包裹的 MySQL 1526 未被识别（errors.As 必须穿透包装）")
	}
	// 邻近报码不得误判：1062 是唯一键冲突（有专门的 IsDuplicateKey）。
	if IsMissingPartition(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}) {
		t.Fatal("MySQL 1062 被误判成缺分区")
	}
	if IsMissingPartition(&mysql.MySQLError{Number: 1463, Message: "MAXVALUE can only be used in last partition"}) {
		t.Fatal("MySQL 1463 被误判成缺分区")
	}
}

// TestIsMissingPartitionPostgres23514 钉住 PG 的 SQLSTATE：
// 分区有显式上下界，越界报 23514（check_violation）。
func TestIsMissingPartitionPostgres23514(t *testing.T) {
	err := &pgconn.PgError{Code: "23514", Message: `no partition of relation "device_metric_5m" found for row`}
	if !IsMissingPartition(err) {
		t.Fatal("PG 23514 未被识别为缺分区")
	}
	if !IsMissingPartition(fmt.Errorf("写 5m 桶 device=1001 bucket=1800000000: %w", err)) {
		t.Fatal("被 %w 包裹的 PG 23514 未被识别")
	}
	// 明确不接别的 SQLSTATE：23505 是唯一键冲突、23503 是外键。
	if IsMissingPartition(&pgconn.PgError{Code: "23505", Message: "duplicate key value"}) {
		t.Fatal("PG 23505 被误判成缺分区")
	}
	if IsMissingPartition(&pgconn.PgError{Code: "23503", Message: "foreign key violation"}) {
		t.Fatal("PG 23503 被误判成缺分区")
	}
}

// TestIsMissingPartitionGormTranslated 钉住「将来打开 TranslateError 也不失效」：
// PG 的译文映射表把 23514 折成 gorm.ErrCheckConstraintViolated，
// 那时 typed 错误不再抵达调用方，必须靠这条哨兵接住。
func TestIsMissingPartitionGormTranslated(t *testing.T) {
	err := fmt.Errorf("%w: %w", gorm.ErrCheckConstraintViolated, errors.New("no partition of relation found for row"))
	if !IsMissingPartition(err) {
		t.Fatal("GORM 已翻译的 ErrCheckConstraintViolated 未被识别（打开 TranslateError 后缺分区检测会静默失效）")
	}
}

// TestIsMissingPartitionNegative 钉住「不误伤」：nil、普通错误、其它类型都不算缺分区。
//
// 这条断言的分量在于**误判的代价**：缺分区的处置是「P1 + 中止本轮」，
// 若一条普通写失败被判成缺分区，整个 flush 轮会停在一个可自愈的错误上
// （本该只失败一台设备、下轮重试）。
func TestIsMissingPartitionNegative(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"普通错误", errors.New("connection refused")},
		{"sqlite no such table", errors.New("SQL logic error: no such table: device_metric_5m (1)")},
		{"唯一键冲突哨兵", gorm.ErrDuplicatedKey},
	}
	for _, c := range cases {
		if IsMissingPartition(c.err) {
			t.Fatalf("%s 被误判成缺分区（会让整轮 flush 无谓中止）", c.name)
		}
	}
}
