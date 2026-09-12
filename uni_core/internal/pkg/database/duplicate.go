package database

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// IsDuplicateKey reports whether err represents a unique-constraint violation
// across the supported drivers:
//   - GORM's translated ErrDuplicatedKey (when TranslateError is enabled),
//   - MySQL error 1062,
//   - SQLite's "UNIQUE constraint failed" message (glebarez/sqlite).
//
// Use this instead of matching driver-specific error text at call sites —
// message formats differ per dialect and drift silently.
func IsDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return true
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return true
	}
	return false
}
