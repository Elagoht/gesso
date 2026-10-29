package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	gotoken "go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// addAuth is gesso add auth: sign-in from the gesso/auth package, wired into a
// gesso new app's api.go. No code of auth's own is copied in, so a fix to it
// reaches the app with go get.
func addAuth(args []string, dir string, stdout io.Writer) error {
	fs := flag.NewFlagSet("add auth", flag.ContinueOnError)
	users := fs.String("users", "single", "who signs in: single, one user from AUTH_USERNAME and AUTH_PASSWORD; sql, a table of users, managed with the app's users command")
	parent := fs.String("dir", "internal", "the folder internal/api is in")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New("add auth takes no arguments: gesso add auth -users single|sql")
	}
	if *users != "single" && *users != "sql" {
		return fmt.Errorf("unknown -users %q: single or sql", *users)
	}
	appGo := filepath.Join(dir, *parent, "api", "api.go")
	src, err := os.ReadFile(appGo)
	if err != nil {
		return fmt.Errorf("no %s: gesso add auth wires the app gesso new or gesso init made; by hand:\n%s", appGo, authSnippet(*users))
	}
	wired, err := wireAuth(src, *users)
	if err != nil {
		return fmt.Errorf("%s: %w; by hand:\n%s", appGo, err, authSnippet(*users))
	}
	if err := os.WriteFile(appGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", appGo)
	if *users == "single" {
		// The app's tests build it from a Config of their own: give the one
		// user there too, or New refuses it.
		testGo := filepath.Join(dir, *parent, "api", "api_test.go")
		if src, err := os.ReadFile(testGo); err == nil {
			if out, n, err := fillTestConfigs(src); err == nil && n > 0 {
				if err := os.WriteFile(testGo, out, 0o644); err != nil {
					return err
				}
				fmt.Fprintln(stdout, "wired", testGo)
			} else {
				fmt.Fprintf(stdout, "note: give AuthUsername and AuthPassword to the Config in %s\n", testGo)
			}
		}
	}
	fmt.Fprintln(stdout, "\nnext:")
	if v := gessoVersion(); v != "" {
		fmt.Fprintf(stdout, "  go get github.com/Elagoht/gesso@%s && go mod tidy\n", v)
	}
	if *users == "single" {
		fmt.Fprintln(stdout, "  set AUTH_USERNAME and AUTH_PASSWORD, in .env or the environment")
	} else {
		fmt.Fprintln(stdout, "  go run . users add <username> -role admin   the first user; the password is asked for")
	}
	fmt.Fprint(stdout, `  guard what needs a signed-in user: gesso.Guards(gesso.RequireIdentity)
  behind a proxy, give auth.Options its TrustedProxies: otherwise every client
  shares the proxy's address, and one sign-in limit
`)
	return nil
}

// authSnippet is what wireAuth writes, for an api.go it cannot find its way in.
func authSnippet(users string) string {
	return "\n" + authBuild(users) + `
	app, err := gesso.New(gesso.Config{Authenticator: authn, …}, authn, …)
` + map[string]string{
		"single": "\nand " + `token.Migrate where the app's migrations run`,
		"sql":    "\nand " + `auth.Migrate where the app's migrations run, and app.Command("users", users.Command(os.Stdin))`,
	}[users] + "\n"
}

func authBuild(users string) string {
	if users == "single" {
		return `	authn, err := auth.New(auth.Options{DB: d, Dialect: dialect, Users: auth.NewSingle(cfg.AuthUsername, cfg.AuthPassword)})
	if err != nil {
		return nil, errors.Join(err, d.Close())
	}
`
	}
	return `	users := auth.NewSQL(d, dialect)
	authn, err := auth.New(auth.Options{DB: d, Dialect: dialect, Users: users})
	if err != nil {
		return nil, errors.Join(err, d.Close())
	}
`
}

type insert struct {
	at   int
	text string
}

// wireAuth adds sign-in to an api.go as gesso new writes it, each piece found by
// name: the auth module built in NewWith before gesso.New, as the Config's
// Authenticator and the first module after schema; its tables in stores; and,
// for one user, the Config fields New fills from AUTH_USERNAME and
// AUTH_PASSWORD — or, for SQL, the users command. Anything not where gesso new
// put it is refused, not guessed at.
func wireAuth(src []byte, users string) ([]byte, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "api.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "github.com/Elagoht/gesso/auth" {
			return nil, errors.New("auth is wired already")
		}
	}
	off := func(p gotoken.Pos) int { return fset.Position(p).Offset }
	var inserts []insert

	newWith := funcDecl(f, "NewWith")
	if newWith == nil {
		return nil, errors.New("no func NewWith")
	}
	if !assigns(newWith.Body, "d") {
		return nil, errors.New("NewWith opens no database d")
	}
	stmt, call := gessoNew(newWith.Body)
	if call == nil || len(call.Args) < 2 {
		return nil, errors.New("NewWith calls no gesso.New with modules")
	}
	inserts = append(inserts, insert{off(stmt.Pos()), strings.TrimLeft(authBuild(users), "\t") + "\t"})

	cfg, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return nil, errors.New("gesso.New's Config is not written out")
	}
	for _, e := range cfg.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Authenticator" {
				return nil, errors.New("the app has an Authenticator already")
			}
		}
	}
	inserts = append(inserts, listInsert(fset, cfg, "Authenticator: authn"))

	var mods *ast.CompositeLit
	ast.Inspect(call, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok && mods == nil && isModuleSlice(lit.Type) {
			mods = lit
		}
		return mods == nil
	})
	if mods == nil {
		return nil, errors.New("gesso.New is given no []gesso.Module{…}")
	}
	inserts = append(inserts, listInsert(fset, mods, "authn"))

	store := map[string]string{"single": "token.Migrate", "sql": "auth.Migrate"}[users]
	lit := returnedList(f, "stores")
	if lit == nil {
		return nil, errors.New("no func stores returning a list literal (gesso new before v0.10 wrote none)")
	}
	inserts = append(inserts, listInsert(fset, lit, store))

	if users == "single" {
		fields := configStruct(f)
		if fields == nil {
			return nil, errors.New("no type Config struct")
		}
		inserts = append(inserts, insert{off(fields.Closing), "AuthUsername string // the one user who signs in\nAuthPassword string\n"})
		newFn := funcDecl(f, "New")
		var fill *ast.CompositeLit
		if newFn != nil {
			ast.Inspect(newFn.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok && fill == nil {
					if id, ok := lit.Type.(*ast.Ident); ok && id.Name == "Config" {
						fill = lit
					}
				}
				return fill == nil
			})
		}
		if fill == nil {
			return nil, errors.New("func New builds no Config{…} from the environment")
		}
		inserts = append(inserts, listInsert(fset, fill, `AuthUsername: os.Getenv("AUTH_USERNAME"),
AuthPassword: os.Getenv("AUTH_PASSWORD")`))
		if doc := newFn.Doc; doc != nil {
			for _, c := range doc.List {
				if strings.HasPrefix(c.Text, "//\tDATABASE_URL") {
					inserts = append(inserts, insert{off(c.Pos()), "//\tAUTH_USERNAME, AUTH_PASSWORD  the one user who signs in\n"})
				}
			}
		}
	} else {
		ret := lastReturn(newWith.Body)
		if ret == nil {
			return nil, errors.New("NewWith ends in no return")
		}
		inserts = append(inserts, insert{off(ret.Pos()), "app.Command(\"users\", users.Command(os.Stdin))\n\t"})
	}

	sort.Slice(inserts, func(i, j int) bool { return inserts[i].at > inserts[j].at })
	out := bytes.Clone(src)
	for _, in := range inserts { // the later offset first, so the earlier stays right
		out = append(out[:in.at:in.at], append([]byte(in.text), out[in.at:]...)...)
	}
	f, err = parser.ParseFile(fset, "api.go", out, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("wiring broke it: %w\n%s", err, out)
	}
	astutil.AddImport(fset, f, "github.com/Elagoht/gesso/auth")
	if users == "single" {
		astutil.AddImport(fset, f, "github.com/Elagoht/gesso/token")
	}
	astutil.AddImport(fset, f, "os")
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return format.Source(buf.Bytes())
}

// fillTestConfigs gives every Config{…} with a Database in a test file a user.
func fillTestConfigs(src []byte) ([]byte, int, error) {
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "api_test.go", src, parser.ParseComments)
	if err != nil {
		return nil, 0, err
	}
	var inserts []insert
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != "Config" {
			return true
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Database" {
					inserts = append(inserts, listInsert(fset, lit, `AuthUsername: "test", AuthPassword: "test password"`))
				}
			}
		}
		return true
	})
	sort.Slice(inserts, func(i, j int) bool { return inserts[i].at > inserts[j].at })
	out := bytes.Clone(src)
	for _, in := range inserts {
		out = append(out[:in.at:in.at], append([]byte(in.text), out[in.at:]...)...)
	}
	out, err = format.Source(out)
	return out, len(inserts), err
}

// listInsert adds entry as the last element of lit.
func listInsert(fset *gotoken.FileSet, lit *ast.CompositeLit, entry string) insert {
	at := fset.Position(lit.Rbrace).Offset
	text := entry + ","
	if len(lit.Elts) == 0 || fset.Position(lit.Lbrace).Line == fset.Position(lit.Rbrace).Line {
		if len(lit.Elts) > 0 { // on one line: {a, b} becomes {a, b, entry}
			return insert{at, ", " + entry}
		}
		text = "\n" + text + "\n"
	} else if last := lit.Elts[len(lit.Elts)-1]; fset.Position(last.End()).Line == fset.Position(lit.Rbrace).Line {
		text = ",\n" + entry + ",\n" // the last element sits on the brace's line
	} else {
		text += "\n"
	}
	return insert{at, text}
}

func funcDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	return nil
}

func configStruct(f *ast.File) *ast.FieldList {
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != gotoken.TYPE {
			continue
		}
		for _, s := range g.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == "Config" {
				if st, ok := ts.Type.(*ast.StructType); ok {
					return st.Fields
				}
			}
		}
	}
	return nil
}

// assigns reports whether body declares name with := at its top level.
func assigns(body *ast.BlockStmt, name string) bool {
	for _, st := range body.List {
		if as, ok := st.(*ast.AssignStmt); ok && as.Tok == gotoken.DEFINE {
			for _, l := range as.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// gessoNew is the top-level statement of body that assigns gesso.New(…), and
// the call.
func gessoNew(body *ast.BlockStmt) (ast.Stmt, *ast.CallExpr) {
	for _, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		if call, ok := as.Rhs[0].(*ast.CallExpr); ok && isSelector(call.Fun, "gesso", "New") {
			return st, call
		}
	}
	return nil, nil
}

func lastReturn(body *ast.BlockStmt) *ast.ReturnStmt {
	if n := len(body.List); n > 0 {
		r, _ := body.List[n-1].(*ast.ReturnStmt)
		return r
	}
	return nil
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

func isModuleSlice(e ast.Expr) bool {
	at, ok := e.(*ast.ArrayType)
	return ok && at.Len == nil && isSelector(at.Elt, "gesso", "Module")
}
