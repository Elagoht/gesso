package gesso

import (
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"strings"
)

// RouteInfo is a registered route, as App.Routes lists it for tools: gesso
// routes, gesso inspect, an editor.
type RouteInfo struct {
	Method  string    `json:"method"`
	Pattern string    `json:"pattern"`
	Module  string    `json:"module,omitempty"` // its module's type: "docs.Module"
	Guards  []string  `json:"guards"`           // "guards.SignedIn", "gesso.RequireRole"
	Input   *TypeInfo `json:"input,omitempty"`  // a typed handler's In
	Output  *TypeInfo `json:"output,omitempty"` // and Out
	Status  int       `json:"status,omitempty"` // the success status of a typed handler
}

// TypeInfo is a type a route takes or answers.
type TypeInfo struct {
	Name   string      `json:"name"` // "models.ReqCreateDoc"
	Fields []FieldInfo `json:"fields,omitempty"`
}

// FieldInfo is one field of a TypeInfo, embedded ones flattened.
type FieldInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	From     string `json:"from"` // json, path, query, header or form
	Key      string `json:"key"`  // the name it has there
	Validate string `json:"validate,omitempty"`
}

// Routes are the app's routes, in the order they were registered.
func (a *App) Routes() []RouteInfo { return a.routes }

// routeDescriber is a typed handler, which knows its types and guards.
type routeDescriber interface {
	routeInfo() (in, out reflect.Type, guards []Guard, status int)
}

func (h *typed[In, Out]) routeInfo() (reflect.Type, reflect.Type, []Guard, int) {
	return reflect.TypeFor[In](), reflect.TypeFor[Out](), h.cfg.guards, h.cfg.status
}

// noteRoute records a route for Routes: its router's guards, and a typed
// handler's own, types and status.
func (a *App) noteRoute(method, pattern string, routerGuards []Guard, h http.Handler) {
	ri := RouteInfo{Method: method, Pattern: pattern, Module: a.registering, Guards: guardNames(routerGuards)}
	if d, ok := h.(routeDescriber); ok {
		in, out, guards, status := d.routeInfo()
		ri.Guards = append(ri.Guards, guardNames(guards)...)
		ri.Input, ri.Output, ri.Status = typeInfo(in), typeInfo(out), status
	}
	a.routes = append(a.routes, ri)
}

func funcName(fn reflect.Value) string {
	f := runtime.FuncForPC(fn.Pointer())
	if f == nil {
		return "?"
	}
	name := f.Name()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	// A closure is named after the function it is in: RequireRole.func1, or
	// RequireRole.1 from newer compilers; the function is what a person knows.
	for {
		i := strings.LastIndex(name, ".")
		rest := strings.TrimPrefix(name[i+1:], "func")
		if i < 0 || rest == "" || strings.Trim(rest, "0123456789") != "" {
			break
		}
		name = name[:i]
	}
	// Inlined into a caller, it is named inside the caller's closure:
	// pkg.Caller.func2.RequireRole. What follows the caller is the guard.
	if m := inlined.FindStringSubmatchIndex(name); m != nil {
		name = name[m[1]:]
	}
	return name
}

// importPath is the import path before a package name in a type's spelling:
// gesso.Page[example.com/shop/internal/notes/models.ResNote] reads
// gesso.Page[models.ResNote].
var importPath = regexp.MustCompile(`[\w.~-]+(/[\w.~-]+)*/`)

var inlined = regexp.MustCompile(`^.*\.(func)?\d+\.`)

func guardNames(guards []Guard) []string {
	out := make([]string, 0, len(guards))
	for _, g := range guards {
		out = append(out, funcName(reflect.ValueOf(g)))
	}
	return out
}

func typeInfo(t reflect.Type) *TypeInfo {
	if t == emptyType {
		return nil
	}
	ti := &TypeInfo{Name: importPath.ReplaceAllString(t.String(), "")}
	if t.Kind() == reflect.Struct {
		ti.Fields = fieldInfos(t)
	}
	return ti
}

func fieldInfos(t reflect.Type) []FieldInfo {
	var out []FieldInfo
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			out = append(out, fieldInfos(f.Type)...)
			continue
		}
		fi := FieldInfo{Name: f.Name, Type: f.Type.String(), From: "json", Key: f.Name, Validate: f.Tag.Get("validate")}
		for _, src := range []string{"path", "query", "header", "form", "json"} {
			if v, ok := f.Tag.Lookup(src); ok {
				key, _, _ := strings.Cut(v, ",")
				if key == "-" {
					continue
				}
				fi.From = src
				if key != "" {
					fi.Key = key
				}
				break
			}
		}
		out = append(out, fi)
	}
	return out
}
