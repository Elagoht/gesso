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
	"sort"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"
)

// addAuth is gesso add auth: sign-in from gesso/auth — a table of users, its
// users command — registered in register.go, and a signedIn guard. None of
// auth's code is copied in, so a fix to it arrives with go get.
func addAuth(args []string, dir string, stdout io.Writer) error {
	fs := newFlags("add auth")
	parent := fs.String("dir", "internal", "the folder guards/ goes in")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("add auth takes no arguments")
	}
	regGo, src, err := registerFile(dir)
	if err != nil {
		return err
	}
	wired, err := wireRegister(src, "auth.New()", "github.com/Elagoht/gesso/auth")
	if err != nil {
		return fmt.Errorf("%s: %w", regGo, err)
	}
	if err := os.WriteFile(regGo, wired, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "wired", regGo)
	guard := filepath.Join(dir, *parent, "guards", "signedIn.go")
	if _, err := os.Stat(guard); err != nil {
		if err := os.MkdirAll(filepath.Dir(guard), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(guard, []byte(signedInGuard), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", guard)
	}
	fmt.Fprint(stdout, `
next:
  go run . users add <username> -role admin
  gesso add module <name> -guard signedIn -field <field>:<type>
`)
	return nil
}

const signedInGuard = `package guards

import (
	"net/http"

	"github.com/Elagoht/gesso"
)

func SignedIn(r *http.Request) error {
	return gesso.RequireIdentity(r)
}
`

// insert puts text at at, in place of the cut bytes there.
type insert struct {
	at, cut int
	text    string
}

// applyInserts applies inserts, the later offset first so the earlier stay right.
func applyInserts(src []byte, inserts []insert) []byte {
	sort.SliceStable(inserts, func(i, j int) bool { return inserts[i].at > inserts[j].at })
	out := bytes.Clone(src)
	for _, in := range inserts {
		out = append(out[:in.at:in.at], append([]byte(in.text), out[in.at+in.cut:]...)...)
	}
	return out
}

// reformat parses src again, adds imports and formats it.
func reformat(fset *gotoken.FileSet, src []byte, paths ...string) ([]byte, error) {
	f, err := parser.ParseFile(fset, "register.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("wiring broke it: %w", err)
	}
	for _, p := range paths {
		astutil.AddImport(fset, f, p)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	// The project's own imports in a group of their own, after the others.
	return imports.Process("", buf.Bytes(), &imports.Options{Comments: true, TabIndent: true, TabWidth: 8, FormatOnly: true})
}

func isName(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
