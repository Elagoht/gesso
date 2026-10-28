package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var newTemplates = template.Must(template.New("").ParseFS(scaffoldFS, "scaffold/new/*.tmpl"))

type projectSpec struct {
	Module            string // example.com/docs
	Name              string // docs
	Dialect           string // sqlite
	DialectGo         string // SQLite
	DialectName       string // SQLite
	DSNDoc            string
	DefaultDSN        string
	ConstraintExample string
}

var modulePath = regexp.MustCompile(`^[a-z0-9][a-z0-9._~-]*(/[A-Za-z0-9._~-]+)*$`)

// newProject is gesso new: a project with its main, the app that opens the
// database and runs every module's migrations, and a test that starts it.
func newProject(args []string, dir string, stdout io.Writer) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	dialect := fs.String("dialect", "sqlite", "the database: sqlite, postgres or mysql")
	into := fs.String("dir", "", "the folder to create (default: the module path's last element)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !modulePath.MatchString(pos[0]) {
		return errors.New("new takes a module path: gesso new example.com/docs")
	}
	spec, err := newProjectSpec(pos[0], *dialect)
	if err != nil {
		return err
	}
	target := *into
	if target == "" {
		target = spec.Name
	}
	target = filepath.Join(dir, target)
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s exists and is not empty", target)
	}
	files := map[string]string{
		"main.go":                  "main.go.tmpl",
		"internal/api/api.go":      "api.go.tmpl",
		"internal/api/api_test.go": "api_test.go.tmpl",
		".gitignore":               "gitignore.tmpl",
		"README.md":                "README.md.tmpl",
	}
	for _, name := range []string{"main.go", "internal/api/api.go", "internal/api/api_test.go", ".gitignore", "README.md"} {
		var buf bytes.Buffer
		if err := newTemplates.ExecuteTemplate(&buf, files[name], spec); err != nil {
			return err
		}
		src := buf.Bytes()
		if strings.HasSuffix(name, ".go") {
			if src, err = format.Source(src); err != nil {
				return fmt.Errorf("%s: %w\n%s", name, err, buf.String())
			}
		}
		if err := writeNew(filepath.Join(target, name), src, stdout); err != nil {
			return err
		}
	}
	goMod := "module " + spec.Module + "\n\ngo 1.26.10\n"
	if err := writeNew(filepath.Join(target, "go.mod"), []byte(goMod), stdout); err != nil {
		return err
	}
	rel, _ := filepath.Rel(dir, target)
	fmt.Fprintf(stdout, "\nnext:\n  cd %s\n  go mod tidy\n  gesso add module notes -fields title:string,body:text\n  go run .\n", rel)
	return nil
}

func writeNew(p string, data []byte, stdout io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "created", p)
	return nil
}

func newProjectSpec(module, dialect string) (projectSpec, error) {
	name := path.Base(module)
	s := projectSpec{Module: module, Name: name, Dialect: dialect}
	switch dialect {
	case "sqlite":
		s.DialectGo, s.DialectName = "SQLite", "SQLite"
		s.DSNDoc = "the SQLite file"
		s.DefaultDSN = name + ".db"
		s.ConstraintExample = `{"notes.slug": "slug"}, the table and column SQLite names`
	case "postgres":
		s.DialectGo, s.DialectName = "Postgres", "PostgreSQL"
		s.DSNDoc = "the PostgreSQL URL"
		s.DefaultDSN = "postgres://localhost:5432/" + name + "?sslmode=disable"
		s.ConstraintExample = `{"notes_slug_key": "slug"}, the constraint's name`
	case "mysql":
		s.DialectGo, s.DialectName = "MySQL", "MySQL"
		s.DSNDoc = "the MySQL DSN"
		s.DefaultDSN = "root@tcp(127.0.0.1:3306)/" + name
		s.ConstraintExample = `{"notes.slug": "slug"}, the key's name as MySQL reports it`
	default:
		return s, fmt.Errorf("unknown dialect %q: sqlite, postgres or mysql", dialect)
	}
	return s, nil
}

// initProject is gesso init: the API of a gesso new project, internal/api, in a
// module that exists already — a collage site that serves its API in the same
// process. No main: the module has one.
func initProject(args []string, dir string, stdout io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dialect := fs.String("dialect", "sqlite", "the database: sqlite, postgres or mysql")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New("init takes no arguments: run it in the module's root")
	}
	module, err := readModule(dir)
	if err != nil {
		return err
	}
	spec, err := newProjectSpec(module, *dialect)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "api")); err == nil {
		return errors.New("internal/api exists already")
	}
	for name, tmpl := range map[string]string{"internal/api/api.go": "api.go.tmpl", "internal/api/api_test.go": "api_test.go.tmpl"} {
		var buf bytes.Buffer
		if err := newTemplates.ExecuteTemplate(&buf, tmpl, spec); err != nil {
			return err
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := writeNew(filepath.Join(dir, name), src, stdout); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, `
next: build the API where the program starts, and serve it. In a collage site,
in newApp, before collage.New:

	gessoAPI, err := api.New(context.Background(), api.Config{
		BasePath: "/api",
		Database: cmp.Or(os.Getenv("DATABASE_URL"), %q), // %s
		Logger:   slog.Default(),
	})

then, after it, app.Handle("/api/", gessoAPI), and run its lifecycle with
collage plugin add elagoht/gesso (pass gessoAPI to plugins). Pages call it with
gesso.NewLocal(gessoAPI). Modules: gesso add module notes -fields title:string
`, spec.DefaultDSN, spec.DSNDoc)
	return nil
}
