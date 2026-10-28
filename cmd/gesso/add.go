package main

import (
	"bytes"
	"embed"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode"
)

//go:embed scaffold/*.tmpl
var scaffoldFS embed.FS

var scaffold = template.Must(template.New("").Funcs(template.FuncMap{
	"ph": func(dialect string, n int) string {
		if dialect == "postgres" {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	},
	"add": func(a, b int) int { return a + b },
}).ParseFS(scaffoldFS, "scaffold/*.tmpl"))

// field is one column of the scaffolded entity.
type field struct {
	Go     string // Title
	JSON   string // title
	Column string // title
	Type   string // string
	Rule   string // required,max=255
	SQL    string // TEXT NOT NULL
}

type moduleSpec struct {
	Pkg      string // events
	Entity   string // Event
	Plural   string // Events
	Table    string // events
	Dialect  string
	Fields   []field
	UsesTime bool
	IDSQL    string
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func addModule(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("add module", flag.ContinueOnError)
	fields := fs.String("fields", "name:string", "the entity's fields, name:type, comma-separated; types: string, int, bool, float, time")
	dialect := fs.String("dialect", "postgres", "the SQL the repository and migration are written in: postgres, sqlite or mysql")
	parent := fs.String("dir", "internal", "the folder the module's folder goes in")
	entity := fs.String("entity", "", "the entity's Go name (default: the module name, singular, capitalised)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("add module takes one name: gesso add module events")
	}
	spec, err := newSpec(pos[0], *entity, *fields, *dialect)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, *parent, spec.Pkg)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"module.go", "models.go", "handler.go", "service.go", "repository.go", "service_test.go"} {
		var buf bytes.Buffer
		if err := scaffold.ExecuteTemplate(&buf, name+".tmpl", spec); err != nil {
			return err
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", name, err, buf.String())
		}
		p := filepath.Join(target, name)
		if err := os.WriteFile(p, src, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", p)
	}
	var up, down bytes.Buffer
	if err := scaffold.ExecuteTemplate(&up, "up.sql.tmpl", spec); err != nil {
		return err
	}
	if err := scaffold.ExecuteTemplate(&down, "down.sql.tmpl", spec); err != nil {
		return err
	}
	paths, err := writeMigration(filepath.Join(target, "migrations"), "create_"+spec.Table, up.String(), down.String(), now)
	for _, p := range paths {
		fmt.Fprintln(stdout, "created", p)
	}
	return err
}

func newSpec(name, entity, fieldList, dialect string) (moduleSpec, error) {
	if !identifier.MatchString(name) {
		return moduleSpec{}, fmt.Errorf("a module name is lower case letters, digits and underscores: %q", name)
	}
	if dialect != "postgres" && dialect != "sqlite" && dialect != "mysql" {
		return moduleSpec{}, fmt.Errorf("unknown dialect %q: postgres, sqlite or mysql", dialect)
	}
	if entity == "" {
		entity = camel(strings.TrimSuffix(name, "s"))
	}
	spec := moduleSpec{
		Pkg: strings.ReplaceAll(name, "_", ""), Entity: entity, Plural: camel(name), Table: name, Dialect: dialect,
		IDSQL: map[string]string{
			"postgres": "id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY",
			"sqlite":   "id INTEGER PRIMARY KEY",
			"mysql":    "id BIGINT AUTO_INCREMENT PRIMARY KEY",
		}[dialect],
	}
	if spec.Plural == spec.Entity {
		spec.Plural += "List"
	}
	seen := map[string]bool{"id": true}
	for part := range strings.SplitSeq(fieldList, ",") {
		col, typ, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok || !identifier.MatchString(col) {
			return moduleSpec{}, fmt.Errorf("a field is name:type, got %q", part)
		}
		if seen[col] {
			return moduleSpec{}, fmt.Errorf("field %q given twice (id is always there)", col)
		}
		seen[col] = true
		f, err := newField(col, typ, dialect)
		if err != nil {
			return moduleSpec{}, err
		}
		spec.UsesTime = spec.UsesTime || f.Type == "time.Time"
		spec.Fields = append(spec.Fields, f)
	}
	return spec, nil
}

func newField(col, typ, dialect string) (field, error) {
	f := field{Go: camel(col), JSON: lowerCamel(col), Column: col}
	sqlTypes := map[string]map[string]string{
		"string": {"postgres": "TEXT", "sqlite": "TEXT", "mysql": "VARCHAR(255)"},
		"int":    {"postgres": "BIGINT", "sqlite": "INTEGER", "mysql": "BIGINT"},
		"bool":   {"postgres": "BOOLEAN", "sqlite": "BOOLEAN", "mysql": "BOOLEAN"},
		"float":  {"postgres": "DOUBLE PRECISION", "sqlite": "REAL", "mysql": "DOUBLE"},
		"time":   {"postgres": "TIMESTAMPTZ", "sqlite": "TIMESTAMP", "mysql": "DATETIME(6)"},
	}
	types, ok := sqlTypes[typ]
	if !ok {
		return f, fmt.Errorf("field %q: unknown type %q (string, int, bool, float, time)", col, typ)
	}
	f.SQL = types[dialect] + " NOT NULL"
	switch typ {
	case "string":
		f.Type, f.Rule = "string", "required,max=255"
	case "int":
		f.Type = "int64"
	case "bool":
		f.Type = "bool"
	case "float":
		f.Type = "float64"
	case "time":
		f.Type, f.Rule = "time.Time", "required"
	}
	return f, nil
}

// camel is snake_case in CamelCase, with Go's initialisms: user_id is UserID.
func camel(s string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(s, "_") {
		switch up := strings.ToUpper(part); up {
		case "ID", "URL", "API", "HTTP", "JSON", "SQL", "UUID", "IP":
			b.WriteString(up)
		default:
			r := []rune(part)
			if len(r) > 0 {
				r[0] = unicode.ToUpper(r[0])
			}
			b.WriteString(string(r))
		}
	}
	return b.String()
}

// lowerCamel is snake_case in lowerCamelCase, for JSON: user_id is userId.
func lowerCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		r := []rune(parts[i])
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
		}
		parts[i] = string(r)
	}
	return strings.Join(parts, "")
}
