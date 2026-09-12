package config

import (
	"strings"
	"testing"
)

func TestDatabaseConfigDriverName(t *testing.T) {
	cases := []struct {
		name   string
		driver string
		want   string
	}{
		{"empty defaults to mysql", "", "mysql"},
		{"mysql", "mysql", "mysql"},
		{"mysql upper", "MySQL", "mysql"},
		{"postgres", "postgres", "postgres"},
		{"postgresql alias", "postgresql", "postgres"},
		{"pg alias", "pg", "postgres"},
		{"unknown passthrough", "oracle", "oracle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (DatabaseConfig{Driver: tc.driver}).DriverName(); got != tc.want {
				t.Fatalf("DriverName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDatabaseConfigDSN_MySQL(t *testing.T) {
	cfg := DatabaseConfig{
		Driver:   "mysql",
		User:     "root",
		Password: "secret",
		Host:     "127.0.0.1",
		Port:     3306,
		DBName:   "wmf",
		Charset:  "utf8mb4",
	}
	dsn := cfg.DSN()
	for _, want := range []string{
		"root:secret@tcp(127.0.0.1:3306)/wmf",
		"charset=utf8mb4",
		"parseTime=True",
		"sql_mode=PIPES_AS_CONCAT",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("mysql DSN %q missing %q", dsn, want)
		}
	}
}

func TestDatabaseConfigDSN_Postgres(t *testing.T) {
	cfg := DatabaseConfig{
		Driver:   "postgres",
		User:     "app",
		Password: "p@ss word",
		Host:     "db.example.com",
		Port:     5432,
		DBName:   "wmf",
	}
	dsn := cfg.DSN()
	if !strings.HasPrefix(dsn, "postgres://") {
		t.Fatalf("postgres DSN %q must be URL form", dsn)
	}
	for _, want := range []string{
		"postgres://app:",
		"db.example.com:5432",
		"/wmf",
		"sslmode=disable",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("postgres DSN %q missing %q", dsn, want)
		}
	}
	// 密码含空格与 @ 必须被转义，不可原样出现在 URL 中。
	if strings.Contains(dsn, "p@ss word") {
		t.Fatalf("postgres DSN %q did not escape password", dsn)
	}
}

func TestDatabaseConfigDSN_PostgresSSLModeOverride(t *testing.T) {
	cfg := DatabaseConfig{
		Driver:   "postgres",
		User:     "app",
		Password: "x",
		Host:     "h",
		Port:     5432,
		DBName:   "d",
		SSLMode:  "require",
	}
	if dsn := cfg.DSN(); !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("postgres DSN %q missing sslmode=require", dsn)
	}
}

func TestDatabaseConfigDSN_PostgresSchema(t *testing.T) {
	cfg := DatabaseConfig{
		Driver:   "postgres",
		User:     "app",
		Password: "x",
		Host:     "h",
		Port:     5432,
		DBName:   "d",
		Schema:   "tenant_a",
	}
	if dsn := cfg.DSN(); !strings.Contains(dsn, "search_path=tenant_a") {
		t.Fatalf("postgres DSN %q missing search_path", dsn)
	}
	// 未配置 schema 时不携带 search_path，走 PG 默认 public。
	cfg.Schema = ""
	if dsn := cfg.DSN(); strings.Contains(dsn, "search_path") {
		t.Fatalf("postgres DSN %q should not contain search_path when schema empty", dsn)
	}
}

func TestDatabaseConfigDSN_MySQLIgnoresSchema(t *testing.T) {
	cfg := DatabaseConfig{
		Driver:   "mysql",
		User:     "root",
		Password: "s",
		Host:     "127.0.0.1",
		Port:     3306,
		DBName:   "wmf",
		Charset:  "utf8mb4",
		Schema:   "ignored",
	}
	if dsn := cfg.DSN(); strings.Contains(dsn, "search_path") {
		t.Fatalf("mysql DSN %q should ignore Schema", dsn)
	}
}
