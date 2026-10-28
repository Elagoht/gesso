package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// A real PostgreSQL for the tests: GESSO_POSTGRES_DSN when set (CI), else an
// embedded server started on first use and stopped when the tests end. When
// neither can be had, the Postgres tests skip, loudly: what they cover is then
// unverified, not passing.
var pg struct {
	once sync.Once
	dsn  string
	err  error
	stop func()
}

func TestMain(m *testing.M) {
	code := m.Run()
	if pg.stop != nil {
		pg.stop()
	}
	os.Exit(code)
}

func startPostgres() {
	if dsn := os.Getenv("GESSO_POSTGRES_DSN"); dsn != "" {
		pg.dsn = dsn
		return
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		pg.err = err
		return
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	dir, err := os.MkdirTemp("", "gesso-pg-")
	if err != nil {
		pg.err = err
		return
	}
	server := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		Username("gesso").Password("gesso").Database("postgres").
		Logger(io.Discard))
	if err := server.Start(); err != nil {
		_ = os.RemoveAll(dir)
		pg.err = err
		return
	}
	pg.stop = func() {
		_ = server.Stop()
		_ = os.RemoveAll(dir)
	}
	pg.dsn = fmt.Sprintf("postgres://gesso:gesso@127.0.0.1:%d/postgres?sslmode=disable", port)
}

// pgDB is a fresh, empty database of its own for one test.
func pgDB(t *testing.T) *sql.DB {
	t.Helper()
	pg.once.Do(startPostgres)
	if pg.err != nil {
		t.Skipf("POSTGRES UNVERIFIED — no server: %v", pg.err)
	}
	admin, err := sql.Open("pgx", pg.dsn)
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
	u, err := url.Parse(pg.dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	d, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.Close()
		if admin, err := sql.Open("pgx", pg.dsn); err == nil {
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
			_ = admin.Close()
		}
	})
	return d
}

func TestPostgresIsUp(t *testing.T) {
	d := pgDB(t)
	var v string
	if err := d.QueryRow("SHOW server_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL %s", v)
}
