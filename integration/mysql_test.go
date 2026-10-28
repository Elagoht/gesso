package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// A real MySQL for the tests: GESSO_MYSQL_DSN, which CI sets and a developer
// points at a server of their own —
//
//	docker run -d -e MYSQL_ROOT_PASSWORD=gesso -p 127.0.0.1:13306:3306 mysql:8.4
//	GESSO_MYSQL_DSN='root:gesso@tcp(127.0.0.1:13306)/' go test ./...
//
// There is no embedded MySQL, so without it the MySQL tests skip, loudly: what
// they cover is then unverified, not passing.
func mysqlConfig(t *testing.T) *mysql.Config {
	t.Helper()
	dsn := os.Getenv("GESSO_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MYSQL UNVERIFIED — GESSO_MYSQL_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// What an application on gesso sets: a migration file runs as one Exec, and
	// DATETIME scans into time.Time.
	cfg.MultiStatements = true
	cfg.ParseTime = true
	return cfg
}

// myDB is a fresh, empty database of its own for one test.
func myDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := mysqlConfig(t)
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "gesso_test_" + hex.EncodeToString(b[:])
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	cfg.DBName = name
	d, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.Close()
		cfg.DBName = ""
		if admin, err := sql.Open("mysql", cfg.FormatDSN()); err == nil {
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name)
			_ = admin.Close()
		}
	})
	return d
}

func TestMySQLIsUp(t *testing.T) {
	d := myDB(t)
	var v string
	if err := d.QueryRow("SELECT VERSION()").Scan(&v); err != nil {
		t.Fatal(err)
	}
	t.Logf("MySQL %s", v)
}
