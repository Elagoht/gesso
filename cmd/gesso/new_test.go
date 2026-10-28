package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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
			app, _ := os.ReadFile(filepath.Join(dir, "internal/api/api.go"))
			for _, want := range []string{"notes.New(notes.Deps{DB: d.DB})", "tags.Migrations", `"example.com/app/internal/tags"`} {
				if !strings.Contains(string(app), want) {
					t.Fatalf("api.go lacks %s:\n%s", want, app)
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
			bin := filepath.Join(t.TempDir(), "app")
			goIn(t, dir, "build", "-o", bin, ".")
			if dialect == "sqlite" {
				migrateRoundTrip(t, bin, filepath.Join(t.TempDir(), "m.db"))
			}
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

// migrateRoundTrip runs the built app's migrate command: up, status, down.
func migrateRoundTrip(t *testing.T, bin, dbPath string) {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command(bin, append([]string{"migrate"}, args...)...)
		cmd.Env = append(os.Environ(), "DATABASE_URL="+dbPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("migrate %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := run("up"); strings.Count(out, "applied") != 2 {
		t.Fatalf("up: %s", out)
	}
	if out := run("down"); strings.Count(out, "reverted") != 1 {
		t.Fatalf("down: %s", out)
	}
	out := run("status")
	if strings.Count(out, "applied") != 1 || strings.Count(out, "pending") != 1 {
		t.Fatalf("status: %s", out)
	}
	cmd := exec.Command(bin, "migrate", "sideways")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dbPath)
	if err := cmd.Run(); err == nil {
		t.Fatal("an unknown subcommand succeeded")
	}
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

// gesso init puts internal/api in a module that exists — a collage site — and
// add module wires into it, as into a gesso new project.
func TestInit(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"init"}, dir, &out, &errOut, fixedNow); code == 0 {
		t.Fatal("init without a go.mod")
	}
	seedModule(t, dir, "require modernc.org/sqlite v1.61.0\n")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"init"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `cmp.Or(os.Getenv("DATABASE_URL"), "app.db")`) {
		t.Fatalf("the next steps:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod.orig")); err == nil {
		t.Fatal("init touched go.mod")
	}
	if main, _ := os.ReadFile(filepath.Join(dir, "main.go")); string(main) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("init wrote over main.go: %s", main)
	}
	if code := run([]string{"init"}, dir, &out, &errOut, fixedNow); code == 0 {
		t.Fatal("init twice")
	}
	out.Reset()
	if code := run([]string{"add", "module", "notes", "-fields", "title:string"}, dir, &out, &errOut, fixedNow); code != 0 || !strings.Contains(out.String(), "wired") {
		t.Fatalf("add module: %d %s %s", code, out.String(), errOut.String())
	}
	goIn(t, dir, "mod", "tidy")
	goIn(t, dir, "vet", "./...")
	goIn(t, dir, "test", "-count=1", "./...")
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
