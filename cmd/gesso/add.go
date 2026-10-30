package main

import (
	"bytes"
	"embed"
	"errors"
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

//go:embed scaffold/*.tmpl scaffold/new/*.tmpl scaffold/gen/*.tmpl
var scaffoldFS embed.FS

var scaffold = template.Must(template.New("").ParseFS(scaffoldFS, "scaffold/*.tmpl"))

// moduleSpec is what a new module's models.go is rendered from.
type moduleSpec struct {
	*model
	Fields      []specField
	Write, Read string // the guards' names, for //gesso:write and //gesso:read
	Sortable    bool   // //gesso:sortable
	Auth        bool   // //gesso:auth
}

type specField struct {
	mfield
	Tag string // its gesso tag in models.go
}

// fieldFlags collects -field, which may be given again and again.
type fieldFlags []string

func (f *fieldFlags) String() string     { return strings.Join(*f, " ") }
func (f *fieldFlags) Set(v string) error { *f = append(*f, v); return nil }

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const addModuleHelp = `Each -field is name:type[:options and rules].

Types: string, text, int, bool, float, time, date, file, image.
Options:
  key            the URL names a record by it, /<module>/{<field>}, instead of the id; unique
  unique         a unique index; a duplicate is a 409 on the field
  index          an index
  optional       not required; an optional time or date is NULL when left out
  sort           the list is in its order, then the id's
  listable       the list answers it; a record answers every field
  filterable     the list filters by it: ?author=ada
  orderable      the client may order the list by it: ?order=title, ?order=-title
  default=<v>    left out of a create or a PUT, it is v: draft, 5, true,
                 @today (a date), @now (a time), @user (who signs in)
  guard=<guard>  only the guard's callers see and write it
  gate=<guard>   on a bool: a record with it false is the guard's callers' only
  trim, lower, upper, collapse, slugify
                 format a string once it is checked, then it is checked again
A file or an image comes with the record in a multipart create, PUT or PATCH
(sent empty, it goes), and is served on GET /<module>/{key}/files/<field>:
  accept=<types> the types it takes, space-separated (an image: PNG, JPEG,
                 GIF, WebP, never SVG)
  maxsize=<n>    its largest: 5MiB (default 10MiB)
  public         anyone may fetch it, past //gesso:read
  routes         PUT and DELETE /<module>/{key}/<field> for the file alone
  fit=<WxH>      an image shrinks to fit inside; fill=<WxH> is cropped to it
  format=<f>     an image is kept as jpeg or png; quality=<1-100>
On a //gesso:auth model, self lets users change the field on themselves:
PATCH /auth/me, and PUT /auth/me/<field> for a file.
Rules: validate's — max=200, email, slug, oneof=draft published …

  gesso add module <name> -guard <guard> -field <field>:<type>[:options,rules]
  a field: slug:string:key,slug,max=100

The model lands in models/models.go: change it later, then gesso gen.`

func addModule(args []string, dir string, stdout io.Writer, now func() time.Time) error {
	fs := newFlags("add module")
	var fieldList fieldFlags
	fs.Var(&fieldList, "field", "a field, name:type[:options and rules]: slug:string:key,slug,max=100; again for each")
	short := fs.String("fields", "", "fields without options, comma-separated: title:string,body:text")
	dialect := fs.String("dialect", "", "postgres, sqlite or mysql (default: the app's)")
	parent := fs.String("dir", "internal", "the folder the module's folder goes in")
	entity := fs.String("entity", "", "the entity's Go name (default: the module name, singular, capitalised)")
	guard := fs.String("guard", "", "a guard from internal/guards on create, update, patch, delete and reorder: //gesso:write")
	read := fs.String("read", "", "a guard from internal/guards on get and list: //gesso:read")
	authModel := fs.Bool("auth", false, "the users who sign in: //gesso:auth (gesso add auth makes it)")
	sortable := fs.Bool("sortable", false, "kept in the client's order, with PATCH /<name> {\"order\": [...]}: //gesso:sortable")
	empty := fs.Bool("empty", false, "a module with no table, for endpoints of your own")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("add module takes one name: gesso add module <name>")
	}
	if *short != "" {
		for part := range strings.SplitSeq(*short, ",") {
			fieldList = append(fieldList, strings.TrimSpace(part))
		}
	}
	if *empty && (len(fieldList) > 0 || *guard != "" || *read != "" || *sortable) {
		return usage("-empty takes no fields, guards or -sortable: the module has no table")
	}
	if !*empty && len(fieldList) == 0 {
		fieldList = fieldFlags{"name:string"}
	}
	if *dialect == "" {
		*dialect = projectDialect(dir)
	}
	if *dialect == "" && !*empty {
		return errors.New("which database? the app's main.go opens none of gesso/sqlite, gesso/postgres, gesso/mysql: give -dialect")
	}
	spec, err := newSpec(pos[0], *entity, fieldList, cmpOr(*dialect, "sqlite"))
	if err != nil {
		return err
	}
	for _, g := range []string{*guard, *read} {
		if g != "" {
			if _, err := guardFor(dir, *parent, g); err != nil {
				return err
			}
		}
	}
	spec.Write, spec.Read, spec.Sortable, spec.Auth = *guard, *read, *sortable, *authModel
	target := filepath.Join(dir, *parent, spec.Pkg)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	mod, err := readModule(dir)
	if err != nil {
		return err
	}
	files := []struct{ name, tmpl string }{{"models/models.go", "models_models.go.tmpl"}}
	if *empty {
		files = []struct{ name, tmpl string }{
			{"module.go", "empty_module.go.tmpl"},
			{"models/requests.go", "empty_requests.go.tmpl"},
			{"models/responses.go", "empty_responses.go.tmpl"},
		}
	}
	if err := os.MkdirAll(filepath.Join(target, "models"), 0o755); err != nil {
		return err
	}
	for _, f := range files {
		var buf bytes.Buffer
		if err := scaffold.ExecuteTemplate(&buf, f.tmpl, spec); err != nil {
			return err
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", f.name, err, buf.String())
		}
		p := filepath.Join(target, filepath.FromSlash(f.name))
		if err := os.WriteFile(p, src, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", p)
	}
	if !*empty {
		paths, err := genModel(filepath.Join(target, "models"), genModelOptions{root: dir, dialect: *dialect, now: now})
		for _, p := range paths {
			fmt.Fprintln(stdout, "created", p)
		}
		if err != nil {
			return err
		}
		if *guard == "" {
			fmt.Fprintf(stdout, "note: anyone may write to %s: -guard <name>, or //gesso:write <guard> in its models.go, guards it\n", spec.Pkg)
		}
	}
	return register(dir, spec.Pkg+".New()", mod+"/"+filepath.ToSlash(filepath.Join(*parent, spec.Pkg)), stdout)
}

// register adds entry to the app's register.go, when there is one.
func register(dir, entry, importPath string, stdout io.Writer) error {
	regGo, src, err := registerFile(dir)
	if err != nil {
		fmt.Fprintf(stdout, "note: no register.go here: register %s with app.Register yourself\n", entry)
		return nil
	}
	wired, err := wireRegister(src, entry, importPath)
	if err != nil {
		return fmt.Errorf("%s: %w; add %s to app.Register by hand", regGo, err, entry)
	}
	if err := os.WriteFile(regGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", regGo)
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// newSpec parses the -field values: name:type[:options], type one of string,
// text, int, bool, float, time, date; an optional time or date is a pointer,
// for a column that takes NULL.
func newSpec(name, entity string, fields []string, dialect string) (moduleSpec, error) {
	if !identifier.MatchString(name) {
		return moduleSpec{}, fmt.Errorf("a module name is lower case letters, digits and underscores: %q", name)
	}
	if dialect != "postgres" && dialect != "sqlite" && dialect != "mysql" {
		return moduleSpec{}, fmt.Errorf("unknown dialect %q: postgres, sqlite or mysql", dialect)
	}
	if entity == "" {
		entity = camel(strings.TrimSuffix(name, "s"))
	}
	m := &model{Pkg: strings.ReplaceAll(name, "_", ""), Entity: entity, Table: name, Plural: camel(name), Dialect: dialect}
	if m.Plural == m.Entity {
		m.Plural += "List"
	}
	spec := moduleSpec{model: m}
	seen := map[string]bool{"id": true}
	for _, part := range fields {
		col, rest, ok := strings.Cut(part, ":")
		typ, opts, _ := strings.Cut(rest, ":")
		if !ok || !identifier.MatchString(col) {
			return moduleSpec{}, fmt.Errorf("a field is name:type[:options], got %q", part)
		}
		if seen[col] {
			return moduleSpec{}, fmt.Errorf("field %q given twice (id is always there)", col)
		}
		seen[col] = true
		goType, ok := map[string]string{"string": "string", "text": "string", "int": "int64", "bool": "bool", "float": "float64", "time": "time.Time", "date": "date.Date", "file": "string", "image": "string"}[typ]
		if !ok {
			return moduleSpec{}, fmt.Errorf("field %q: unknown type %q (string, text, int, bool, float, time, date, file, image)", col, typ)
		}
		tag := opts
		if typ == "text" || typ == "file" || typ == "image" {
			tag = strings.TrimSuffix(typ+","+opts, ",")
		}
		if (typ == "time" || typ == "date") && strings.Contains(","+opts+",", ",optional,") {
			goType = "*" + goType
		}
		f, err := newMField(camel(col), goType, tag)
		if err != nil {
			return moduleSpec{}, fmt.Errorf("field %q: %w", col, err)
		}
		f.Ident = quoteIdent(f.Column, dialect)
		spec.Fields = append(spec.Fields, specField{f, tag})
		m.Fields = append(m.Fields, f)
	}
	for i := range m.Fields {
		f := &m.Fields[i]
		if f.Key {
			if m.Key != nil {
				return moduleSpec{}, errors.New("two key fields")
			}
			m.Key = f
		}
		if f.Sort && m.Sort != nil {
			return moduleSpec{}, errors.New("two sort fields")
		}
		if f.Sort {
			m.Sort = f
		}
		m.UsesTime = m.UsesTime || f.Base == "time"
		m.UsesDate = m.UsesDate || f.Base == "date"
	}
	return spec, nil
}

// keywords are words a column cannot be named in one of the dialects without
// quoting: SQL's reserved words, PostgreSQL's and MySQL's.
var keywords = map[string]bool{}

func init() {
	for w := range strings.FieldsSeq(`all alter analyse analyze and any array as asc asymmetric authorization
		between binary both by call case cast change check collate column condition constraint
		create cross current_catalog current_date current_role current_time current_timestamp
		current_user database databases default deferrable delete desc describe distinct do drop
		else end except exists explain false fetch for foreign freeze from full grant group having
		ilike in index initially inner insert intersect interval into is isnull join key keys
		lateral leading left like limit localtime localtimestamp match natural not notnull null
		offset on only or order outer overlaps placing primary range rank read references
		regexp rename replace returning right row rows select session_user set show similar some
		symmetric table tablesample then to trailing trigger true union unique update usage
		user using values variadic verbose when where window with write`) {
		keywords[w] = true
	}
}

// quoteIdent is col as the dialect's SQL must spell it: as it is, or, when it is
// a keyword, in double quotes (PostgreSQL, SQLite) or backquotes (MySQL).
func quoteIdent(col, dialect string) string {
	if !keywords[col] {
		return col
	}
	if dialect == "mysql" {
		return "`" + col + "`"
	}
	return `"` + col + `"`
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
