package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
)

// The database/sql methods that run without a context, and what to call instead.
var contextless = map[string]string{
	"Query": "QueryContext", "QueryRow": "QueryRowContext", "Exec": "ExecContext",
	"Begin": "BeginTx", "Prepare": "PrepareContext", "Ping": "PingContext",
}

var sqlTypes = []string{"DB", "Tx", "Conn", "Stmt"}

// finding is one place that breaks the ctx rule.
type finding struct {
	pos token.Position
	msg string
}

const checkHelp = `Rules:
  ctx         a database call without a context, or a service or repository
              method whose first parameter is not ctx            (error)
  stale-gen   a generated file models or queries.sql would change (error)
  open-write  a POST, PUT, PATCH or DELETE with no guard           (warning)

It exits 1 when it finds an error; a warning is reported only. open-write needs
the project to build and run (gesso-inspect), and is skipped when it does not.`

// report is one thing check found.
type report struct {
	Level   string `json:"level"` // error or warning
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// check reports, one per line or as JSON, what breaks gesso's rules in the
// project, and returns how many errors it found.
func check(args []string, dir string, out io.Writer) (int, error) {
	fs := newFlags("check")
	asJSON := fs.Bool("json", false, "print the findings as a JSON array of {level, rule, message}")
	pkg := fs.String("pkg", ".", "the main package, run for open-write")
	patterns, err := parseFlags(fs, args)
	if err != nil {
		return 0, err
	}
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  dir,
	}, patterns...)
	if err != nil {
		return 0, err
	}
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return 0, fmt.Errorf("the packages do not load:\n  %s", strings.Join(loadErrs, "\n  "))
	}
	var found []finding
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			found = append(found, checkFile(p, f)...)
		}
	}
	slices.SortFunc(found, func(a, b finding) int {
		if c := strings.Compare(a.pos.Filename, b.pos.Filename); c != 0 {
			return c
		}
		return a.pos.Line - b.pos.Line
	})
	reports := []report{}
	for _, f := range found {
		name := f.pos.Filename
		if rel, err := filepath.Rel(dir, name); err == nil && !strings.HasPrefix(rel, "..") {
			name = rel
		}
		reports = append(reports, report{"error", "ctx", fmt.Sprintf("%s:%d:%d: %s", name, f.pos.Line, f.pos.Column, f.msg)})
	}
	var stale bytes.Buffer
	if _, err := gen([]string{"-check"}, dir, &stale, time.Now); err == nil || !strings.Contains(err.Error(), "no queries.sql") {
		for line := range strings.Lines(stale.String()) {
			if path, ok := strings.CutPrefix(strings.TrimSpace(line), "stale "); ok {
				reports = append(reports, report{"error", "stale-gen", path + ": run gesso gen"})
			}
		}
		if err != nil {
			reports = append(reports, report{"error", "stale-gen", err.Error()})
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "api")); err == nil {
		if in, err := loadInspection(dir, *pkg); err == nil {
			for _, r := range in.Routes {
				if r.Method != "GET" && r.Method != "HEAD" && len(r.Guards) == 0 && r.Module != "auth.Module" {
					reports = append(reports, report{"warning", "open-write", r.Method + " " + r.Pattern + " has no guard: anyone may call it"})
				}
			}
		}
	}
	errs := 0
	for _, r := range reports {
		if r.Level == "error" {
			errs++
		}
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return errs, enc.Encode(reports)
	}
	for _, r := range reports {
		fmt.Fprintf(out, "%s [%s] %s\n", r.Level, r.Rule, r.Message)
	}
	return errs, nil
}

func checkFile(p *packages.Package, f *ast.File) []finding {
	var found []finding
	base := filepath.Base(p.Fset.Position(f.Pos()).Filename)
	layered := isLayerFile(base)
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if msg, ok := contextlessCall(p.TypesInfo, n); ok {
				found = append(found, finding{p.Fset.Position(n.Pos()), msg})
			}
		case *ast.FuncDecl:
			if layered && n.Recv != nil && n.Name.IsExported() && !takesContextFirst(p.TypesInfo, n) {
				found = append(found, finding{p.Fset.Position(n.Pos()),
					fmt.Sprintf("%s takes no ctx: a service or repository method's first parameter is ctx context.Context", n.Name.Name)})
			}
		}
		return true
	})
	return found
}

// isLayerFile reports whether a file holds a service or a repository, by gesso's
// layout: service.go, repository.go, or a name ending in _service.go or
// _repository.go.
func isLayerFile(base string) bool {
	if strings.HasSuffix(base, "_test.go") {
		return false
	}
	name := strings.TrimSuffix(base, ".go")
	for _, layer := range []string{"service", "repository"} {
		if name == layer || strings.HasSuffix(name, "_"+layer) {
			return true
		}
	}
	return false
}

func contextlessCall(info *types.Info, call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	instead, ok := contextless[sel.Sel.Name]
	if !ok {
		return "", false
	}
	selection := info.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal {
		return "", false
	}
	recv := selection.Recv()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "database/sql" || !slices.Contains(sqlTypes, named.Obj().Name()) {
		return "", false
	}
	return fmt.Sprintf("sql.%s.%s runs without a context: call %s(ctx, …), so a cancelled request stops its query", named.Obj().Name(), sel.Sel.Name, instead), true
}

func takesContextFirst(info *types.Info, fn *ast.FuncDecl) bool {
	params := fn.Type.Params.List
	if len(params) == 0 {
		return false
	}
	t := info.TypeOf(params[0].Type)
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "context" && named.Obj().Name() == "Context"
}
