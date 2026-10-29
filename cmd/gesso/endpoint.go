package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	gotoken "go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var endpointTemplate = template.Must(template.New("").Parse(`package {{.Pkg}}

import (
	"context"

	"github.com/Elagoht/gesso"
{{- if .HasTypes}}

	"{{.Models}}"
{{- end}}
)

var {{.Go}} = gesso.Endpoint[{{.In}}, {{.Out}}]{Method: "{{.Method}}", Pattern: "{{.Path}}"}

func (m *Module) {{.Handler}}(ctx context.Context, in {{.In}}) ({{.Out}}, error) {
	return {{.Out}}{}, nil
}
`))

type endpointSpec struct {
	Pkg, Models  string
	Go, Handler  string // the Endpoint and its handler
	Method, Path string
	In, Out      string // models.Req…, gesso.Empty
	HasTypes     bool
	reqFields    []string // Go field lines
	resFields    []string
}

var pathParam = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)(\.\.\.)?\}`)

var endpointTypes = map[string]string{
	"string": "string", "text": "string", "int": "int64", "bool": "bool", "float": "float64",
	"time": "time.Time", "date": "date.Date", "strings": "[]string", "ints": "[]int64",
}

// addEndpoint is gesso add endpoint <module>/<name>: the Endpoint and its
// handler in <module>/<name>.go, its Req and Res in the module's models, and
// its line in Routes.
func addEndpoint(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("add endpoint")
	method := fs.String("method", "POST", "GET, POST, PUT, PATCH or DELETE")
	pattern := fs.String("path", "", `the URL pattern (default "/<module>/<name>"); a {param} in it is a field of the Req`)
	guard := fs.String("guard", "", "a guard from internal/guards")
	parent := fs.String("dir", "internal", "the folder the module is in")
	var req, res fieldFlags
	fs.Var(&req, "field", "a field of the Req, name:type[:rules] — types: string, text, int, bool, float, time, date, strings, ints; again for each")
	fs.Var(&res, "res", "a field of the Res, name:type; without any the endpoint answers nothing (204)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usage("add endpoint takes <module>/<name>")
	}
	modName, name, ok := strings.Cut(pos[0], "/")
	if !ok || !identifier.MatchString(modName) || !identifier.MatchString(name) {
		return usage("add endpoint takes <module>/<name>, lower case")
	}
	*method = strings.ToUpper(*method)
	if !map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}[*method] {
		return usage("unknown -method " + *method + ": GET, POST, PUT, PATCH or DELETE")
	}
	pkg := strings.ReplaceAll(modName, "_", "")
	modDir := filepath.Join(dir, *parent, pkg)
	moduleGo := filepath.Join(modDir, "module.go")
	modSrc, err := os.ReadFile(moduleGo)
	if err != nil {
		return fmt.Errorf("no module %s: gesso add module %s first", moduleGo, modName)
	}
	mod, err := readModule(dir)
	if err != nil {
		return err
	}
	s := endpointSpec{Pkg: pkg, Models: mod + "/" + filepath.ToSlash(filepath.Join(*parent, pkg, "models")),
		Go: camel(name) + camel(modName), Method: *method, Path: *pattern}
	s.Handler = strings.ToLower(s.Go[:1]) + s.Go[1:]
	if s.Path == "" {
		s.Path = "/" + modName + "/" + strings.ReplaceAll(name, "_", "-")
	}
	if !strings.HasPrefix(s.Path, "/") {
		return usage("-path begins with /")
	}
	target := filepath.Join(modDir, name+".go")
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists", target)
	}
	for _, m := range pathParam.FindAllStringSubmatch(s.Path, -1) {
		s.reqFields = append(s.reqFields, fmt.Sprintf("%s string `path:%q`", camel(m[1]), m[1]))
	}
	for _, f := range req {
		line, err := s.field(f, true)
		if err != nil {
			return err
		}
		s.reqFields = append(s.reqFields, line)
	}
	for _, f := range res {
		line, err := s.field(f, false)
		if err != nil {
			return err
		}
		s.resFields = append(s.resFields, line)
	}
	s.In, s.Out = "gesso.Empty", "gesso.Empty"
	if len(s.reqFields) > 0 {
		s.In, s.HasTypes = "models.Req"+s.Go, true
	}
	if len(s.resFields) > 0 {
		s.Out, s.HasTypes = "models.Res"+s.Go, true
	}
	var guardName, guardsImport string
	if *guard != "" {
		if guardName, err = guardFor(dir, *parent, *guard); err != nil {
			return err
		}
		guardsImport = mod + "/" + filepath.ToSlash(filepath.Join(*parent, "guards"))
	}
	wired, err := addRoute(modSrc, s, guardName, guardsImport)
	if err != nil {
		return fmt.Errorf("%s: %w; add %s.Handle(r, m.%s) to Routes by hand", moduleGo, err, s.Go, s.Handler)
	}
	var buf bytes.Buffer
	if err := endpointTemplate.Execute(&buf, s); err != nil {
		return err
	}
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("%w\n%s", err, buf.String())
	}
	if len(s.reqFields) > 0 {
		if err := appendType(filepath.Join(modDir, "models", "requests.go"), "Req"+s.Go, s.reqFields, stdout); err != nil {
			return err
		}
	}
	if len(s.resFields) > 0 {
		if err := appendType(filepath.Join(modDir, "models", "responses.go"), "Res"+s.Go, s.resFields, stdout); err != nil {
			return err
		}
	}
	if err := os.WriteFile(target, src, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "created", target)
	if err := os.WriteFile(moduleGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", moduleGo)
	return nil
}

// field is one -field or -res as a Go struct field line.
func (s *endpointSpec) field(spec string, isReq bool) (string, error) {
	col, rest, ok := strings.Cut(spec, ":")
	typ, rules, _ := strings.Cut(rest, ":")
	goType, known := endpointTypes[typ]
	if !ok || !identifier.MatchString(col) || !known {
		return "", usage(fmt.Sprintf("a field is name:type[:rules], types string, text, int, bool, float, time, date, strings, ints; got %q", spec))
	}
	line := fmt.Sprintf("%s %s `json:%q", camel(col), goType, lowerCamel(col))
	if isReq {
		var vs []string
		optional := false
		for r := range strings.SplitSeq(rules, ",") {
			switch r = strings.TrimSpace(r); {
			case r == "":
			case r == "optional":
				optional = true
			default:
				if name, _, _ := strings.Cut(r, "="); !ruleNames[name] {
					return "", usage(fmt.Sprintf("field %s: unknown rule %q", col, r))
				}
				vs = append(vs, r)
			}
		}
		if !optional && typ != "int" && typ != "bool" && typ != "float" {
			vs = append([]string{"required"}, vs...)
		}
		if len(vs) > 0 {
			line += fmt.Sprintf(" validate:%q", strings.Join(vs, ","))
		}
	}
	return line + "`", nil
}

// addRoute adds the endpoint's Handle to the end of the module's Routes.
func addRoute(src []byte, s endpointSpec, guard, guardsImport string) ([]byte, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "module.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var routes *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Name.Name == "Routes" && fn.Body != nil {
			routes = fn
		}
	}
	if routes == nil || len(routes.Type.Params.List) != 1 || len(routes.Type.Params.List[0].Names) != 1 {
		return nil, fmt.Errorf("no Routes(r *gesso.Router) method")
	}
	r := routes.Type.Params.List[0].Names[0].Name
	if r == "_" {
		return nil, fmt.Errorf("the Routes method names its router _")
	}
	recv := ""
	if names := routes.Recv.List[0].Names; len(names) == 1 {
		recv = names[0].Name
	}
	if recv == "" || recv == "_" {
		return nil, fmt.Errorf("the Routes method has no receiver name")
	}
	line := fmt.Sprintf("%s.Handle(%s, %s.%s", s.Go, r, recv, s.Handler)
	if guard != "" {
		line += ", gesso.Guards(guards." + guard + ")"
	}
	line += ")\n"
	end := fset.Position(routes.Body.Rbrace).Offset
	var imports []string
	if guardsImport != "" {
		imports = append(imports, guardsImport)
	}
	return reformat(fset, applyInserts(src, []insert{{at: end, text: line}}), imports...)
}

// appendType adds a struct to a models file, making the file if need be.
func appendType(p, name string, fields []string, stdout io.Writer) error {
	src, err := os.ReadFile(p)
	if err != nil {
		src = []byte("package models\n")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
	}
	if regexp.MustCompile(`(?m)^type ` + name + `\b`).Match(src) {
		return fmt.Errorf("%s has a %s already", p, name)
	}
	src = append(src, []byte("\ntype "+name+" struct {\n\t"+strings.Join(fields, "\n\t")+"\n}\n")...)
	var imports []string
	joined := strings.Join(fields, "\n")
	if strings.Contains(joined, "time.Time") {
		imports = append(imports, "time")
	}
	if strings.Contains(joined, "date.Date") {
		imports = append(imports, "github.com/Elagoht/gesso/date")
	}
	out, err := reformat(gotoken.NewFileSet(), src, imports...)
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wrote", p)
	return nil
}
