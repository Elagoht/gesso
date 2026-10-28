package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"path/filepath"
	"slices"
	"strings"

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

// check loads the packages and reports, one per line, every call that reaches
// the database without a context, and every exported method of a service or
// repository whose first parameter is not ctx. It returns how many it found.
func check(patterns []string, dir string, out io.Writer) (int, error) {
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
	for _, f := range found {
		name := f.pos.Filename
		if rel, err := filepath.Rel(dir, name); err == nil && !strings.HasPrefix(rel, "..") {
			name = rel
		}
		fmt.Fprintf(out, "%s:%d:%d: %s\n", name, f.pos.Line, f.pos.Column, f.msg)
	}
	return len(found), nil
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
