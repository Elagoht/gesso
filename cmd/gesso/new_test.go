package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/go-sql-driver/mysql"
)

// A project from gesso new, with two modules added in the same second, builds,
// vets, starts on its database and passes its tests — on each dialect.
func TestNew(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a new project")
	}
	cases := map[string]struct {
		require  string
		database func(t *testing.T) string // TEST_DATABASE_URL, "" for none
	}{
		"sqlite":   {"require modernc.org/sqlite v1.61.0\n", nil},
		"postgres": {"require github.com/jackc/pgx/v5 v5.11.0\n", embeddedPostgres},
		"mysql":    {"require github.com/go-sql-driver/mysql v1.10.1\n", mysqlDatabase},
	}
	for dialect, c := range cases {
		t.Run(dialect, func(t *testing.T) {
			root := t.TempDir()
			var out, errOut bytes.Buffer
			if code := run([]string{"new", "example.com/app", "-dialect", dialect}, root, &out, &errOut, fixedNow); code != 0 {
				t.Fatalf("code %d: %s", code, errOut.String())
			}
			dir := filepath.Join(root, "app")
			seedModule(t, dir, c.require)
			for _, m := range []string{"notes", "tags"} {
				out.Reset()
				if code := run([]string{"add", "module", m, "-fields", "title:string,order:int,day:date"}, dir, &out, &errOut, fixedNow); code != 0 {
					t.Fatalf("add %s: code %d: %s", m, code, errOut.String())
				}
				if !strings.Contains(out.String(), "wired") {
					t.Fatalf("add %s did not wire the module:\n%s", m, out.String())
				}
			}
			app, _ := os.ReadFile(filepath.Join(dir, "internal/app/app.go"))
			for _, want := range []string{"notes.New(notes.Deps{DB: d.DB})", "tags.Migrations", `"example.com/app/internal/tags"`} {
				if !strings.Contains(string(app), want) {
					t.Fatalf("app.go lacks %s:\n%s", want, app)
				}
			}
			if code := run([]string{"add", "module", "tags"}, dir, &out, &errOut, fixedNow); code == 0 {
				t.Fatal("an existing module is never overwritten")
			}
			if c.database != nil {
				if dsn := c.database(t); dsn != "" {
					t.Setenv("TEST_DATABASE_URL", dsn)
				}
			}
			goIn(t, dir, "mod", "tidy")
			goIn(t, dir, "vet", "./...")
			testOut := goIn(t, dir, "test", "-count=1", "-v", "./...")
			if strings.Contains(testOut, "SKIP: TestNew") {
				t.Logf("%s UNVERIFIED: the new app's test skipped without a database", strings.ToUpper(dialect))
			}
			goIn(t, dir, "build", "-o", filepath.Join(t.TempDir(), "app"), ".")
			if n, err := check(nil, dir, &out); err != nil || n != 0 {
				t.Fatalf("the project breaks the ctx rule: %v %s", err, out.String())
			}
		})
	}
	t.Run("refuses", func(t *testing.T) {
		root := t.TempDir()
		var out, errOut bytes.Buffer
		if code := run([]string{"new", "example.com/x", "-dialect", "oracle"}, root, &out, &errOut, fixedNow); code == 0 {
			t.Fatal("an unknown dialect")
		}
		if code := run([]string{"new", "Not A Path"}, root, &out, &errOut, fixedNow); code == 0 {
			t.Fatal("a bad module path")
		}
		_ = os.MkdirAll(filepath.Join(root, "x"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "x", "keep"), nil, 0o644)
		if code := run([]string{"new", "example.com/x"}, root, &out, &errOut, fixedNow); code == 0 {
			t.Fatal("a folder that is not empty")
		}
	})
}

// embeddedPostgres starts a server for the test and names its database.
func embeddedPostgres(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	dir := t.TempDir()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "rt")).DataPath(filepath.Join(dir, "data")).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		t.Logf("POSTGRES UNVERIFIED — no server: %v", err)
		return ""
	}
	t.Cleanup(func() { _ = pg.Stop() })
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
}

// mysqlDatabase makes a database on GESSO_MYSQL_DSN's server and names it.
func mysqlDatabase(t *testing.T) string {
	dsn := os.Getenv("GESSO_MYSQL_DSN")
	if dsn == "" {
		t.Log("MYSQL UNVERIFIED — GESSO_MYSQL_DSN is not set")
		return ""
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("gesso_new_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a, err := sql.Open("mysql", cfg.FormatDSN()); err == nil {
			_, _ = a.Exec("DROP DATABASE " + name)
			_ = a.Close()
		}
	})
	cfg.DBName = name
	return cfg.FormatDSN()
}

func TestWireModule(t *testing.T) {
	src := `package app

import "github.com/Elagoht/gesso"

func modules(d Deps) []gesso.Module {
	return []gesso.Module{
		a.New(a.Deps{DB: d.DB}), b.New(b.Deps{DB: d.DB})}
}

func migrations() []fs.FS { return []fs.FS{} }
`
	got, err := wireModule([]byte(src), "notes", "example.com/app/internal/notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"b.New(b.Deps{DB: d.DB}),\n\t\tnotes.New(notes.Deps{DB: d.DB}),\n\t}", "[]fs.FS{\n\t\tnotes.Migrations,\n\t}", `"example.com/app/internal/notes"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if _, err := wireModule(got, "notes", "example.com/app/internal/notes"); err == nil {
		t.Error("wiring a module twice")
	}
	if _, err := wireModule([]byte("package app\n"), "notes", "x/notes"); err == nil || !strings.Contains(err.Error(), "no func") {
		t.Errorf("no lists: %v", err)
	}
}
