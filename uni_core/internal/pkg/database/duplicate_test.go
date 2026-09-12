package database

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestIsDuplicateKeyNil(t *testing.T) {
	if IsDuplicateKey(nil) {
		t.Fatal("nil error must not be a duplicate-key error")
	}
}

func TestIsDuplicateKeyGormTranslated(t *testing.T) {
	if !IsDuplicateKey(gorm.ErrDuplicatedKey) {
		t.Fatal("gorm.ErrDuplicatedKey must be recognized")
	}
}

func TestIsDuplicateKeyMySQL1062(t *testing.T) {
	err := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry 'admin' for key 'uk_user_username'"}
	if !IsDuplicateKey(err) {
		t.Fatal("MySQL 1062 must be recognized as duplicate key")
	}
	errOther := &mysql.MySQLError{Number: 1045, Message: "Access denied"}
	if IsDuplicateKey(errOther) {
		t.Fatal("MySQL non-1062 must not be treated as duplicate key")
	}
}

func TestIsDuplicateKeyMySQLWrapped(t *testing.T) {
	inner := &mysql.MySQLError{Number: 1062}
	wrapped := errors.Join(errors.New("create user: "), inner)
	if !IsDuplicateKey(wrapped) {
		t.Fatal("wrapped MySQL 1062 must be recognized via errors.As")
	}
}

// TestIsDuplicateKeySQLite reproduces a real SQLite unique violation through
// glebarez/sqlite — the dialect used in tests and local dev, whose message
// differs from MySQL's ("UNIQUE constraint failed" vs "Duplicate entry").
func TestIsDuplicateKeySQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	type row struct {
		ID       uint64 `gorm:"primaryKey;autoIncrement:false"`
		Username string `gorm:"uniqueIndex"`
	}
	if err := db.AutoMigrate(&row{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&row{ID: 1, Username: "admin"}).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err = db.Create(&row{ID: 2, Username: "admin"}).Error
	if err == nil {
		t.Fatal("second insert should violate the unique index")
	}
	if !IsDuplicateKey(err) {
		t.Fatalf("SQLite unique violation not recognized: %v", err)
	}
}
