package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ourRequires is this module's require lines, as a require block.
func ourRequires(t *testing.T) string {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("require (\n")
	inBlock := false
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "" && !strings.HasPrefix(line, "github.com/Elagoht/gesso "):
			path, rest, _ := strings.Cut(line, " ")
			version, _, _ := strings.Cut(rest, " ")
			b.WriteString("\t" + path + " " + version + "\n")
		}
	}
	b.WriteString(")\n")
	return b.String()
}

func fixedNow() time.Time { return time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC) }

func gessoRoot(t *testing.T) string {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMigrateNew(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"migrate", "new", "add_events", "-dir", "db/migrations"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	for _, f := range []string{"20261028153000_add_events.up.sql", "20261028153000_add_events.down.sql"} {
		if _, err := os.Stat(filepath.Join(dir, "db/migrations", f)); err != nil {
			t.Fatal(err)
		}
	}
	if code := run([]string{"migrate", "new", "add_events", "-dir", "db/migrations"}, dir, &out, &errOut, fixedNow); code == 0 {
		t.Fatal("a second migration of the same name is refused")
	}
	// Another in the same second takes the next one: the versions are one
	// history across the project.
	if code := run([]string{"migrate", "new", "add_city", "-dir", "other/migrations"}, dir, &out, &errOut, fixedNow); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "other/migrations/20261028153001_add_city.up.sql")); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"migrate", "new", "Add Events"}, dir, &out, &errOut, fixedNow); code != 2 {
		t.Fatal("a bad name is refused")
	}
}

// goIn runs the go command in dir.
func goIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newApp is an empty module that uses the gesso in this repository. It requires
// every module ours does, at our versions: go never selects lower, so each module
// it resolves is one whose checksum our go.sum already holds.
func newApp(t *testing.T, require string) string {
	dir := t.TempDir()
	seedModule(t, dir, require)
	return dir
}

// seedModule writes dir's go.mod and go.sum as newApp describes.
func seedModule(t *testing.T, dir, require string) {
	t.Helper()
	mod := "module example.com/app\n\ngo 1.26.10\n\nrequire github.com/Elagoht/gesso v0.0.0\n" + require +
		ourRequires(t) + "\nreplace github.com/Elagoht/gesso => " + gessoRoot(t) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	// Every checksum the module will need is already in ours (deps_test.go).
	sums, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sums, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	dir := newApp(t, "")
	files := map[string]string{
		"internal/users/repository.go": `package users

import (
	"context"
	"database/sql"
)

type Repository struct{ db *sql.DB }

func (r *Repository) Count() (int, error) {
	var n int
	err := r.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

func (r *Repository) Rename(ctx context.Context, id int64, name string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE users SET name = $1 WHERE id = $2", name, id)
	return err
}

func (r *Repository) helper() {}
`,
		"internal/users/service.go": `package users

import "context"

type Service struct{ r *Repository }

func (s *Service) Total() (int, error) { return s.r.Count() }

func (s *Service) Rename(ctx context.Context, id int64, name string) error { return s.r.Rename(ctx, id, name) }
`,
		"internal/users/jobs.go": `package users

import "database/sql"

func Purge(d *sql.DB) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM users WHERE deleted")
	if err != nil {
		return tx.Rollback()
	}
	return tx.Commit()
}

// Total is not a service method: jobs.go is not a layer file.
func (s *Service) Report() int { return 0 }
`,
	}
	for name, src := range files {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goIn(t, dir, "mod", "tidy")
	var out, errOut bytes.Buffer
	code := run([]string{"check"}, dir, &out, &errOut, fixedNow)
	got := out.String()
	if code != 1 {
		t.Fatalf("code %d, out %s, err %s", code, got, errOut.String())
	}
	for _, want := range []string{
		"internal/users/jobs.go:6:13: sql.DB.Begin runs without a context: call BeginTx",
		"internal/users/jobs.go:10:11: sql.Tx.Exec runs without a context: call ExecContext",
		"internal/users/repository.go:10:1: Count takes no ctx",
		"internal/users/repository.go:12:9: sql.DB.QueryRow runs without a context",
		"internal/users/service.go:7:1: Total takes no ctx",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 5 {
		t.Errorf("want 5 findings, got %d:\n%s", n, got)
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "-v", "--version"} {
		var out, errOut bytes.Buffer
		if code := run([]string{arg}, t.TempDir(), &out, &errOut, fixedNow); code != 0 || !strings.HasPrefix(out.String(), "gesso ") {
			t.Fatalf("%s: %d %q %q", arg, code, out.String(), errOut.String())
		}
	}
}

// mysqlOpen is the generated tests' MySQL: a database of their own on the
// server GESSO_MYSQL_DSN names.
const mysqlOpen = `
func openMySQL(t *testing.T) *sql.DB {
	dsn := os.Getenv("GESSO_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MYSQL UNVERIFIED — GESSO_MYSQL_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MultiStatements, cfg.ParseTime = true, true
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("gesso_cli_%d", time.Now().UnixNano())
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
	d, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
`

// reportUnverified shows a generated test's skip: a database it could not
// reach left its SQL unverified, which CI must see, not mistake for a pass.
func reportUnverified(t *testing.T, out string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "UNVERIFIED") {
			t.Log(strings.TrimSpace(line))
		}
	}
}
