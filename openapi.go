package gesso

import (
	"encoding"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// OpenAPIConfig turns on the OpenAPI 3.1 document, generated from the typed
// handlers: their inputs (path, query, header, body), their validate rules, their
// outputs and statuses. It cannot drift from the code, because it is the code.
//
// Plain http.Handlers are not in it — they declare nothing — and neither is a
// subtree pattern ("/files/"), which OpenAPI cannot express.
type OpenAPIConfig struct {
	Title       string
	Version     string
	Description string
	// Path serves the document, under BasePath. Default "/openapi.json".
	Path string
}

// Summary is a route's one-line summary in the OpenAPI document.
func Summary(s string) HandleOption { return func(c *handleConfig) { c.summary = s } }

// Description is a route's longer description in the OpenAPI document.
func Description(s string) HandleOption { return func(c *handleConfig) { c.description = s } }

// Tags group a route in the OpenAPI document.
func Tags(tags ...string) HandleOption {
	return func(c *handleConfig) { c.tags = append(c.tags, tags...) }
}

// operation is what a typed handler tells the document about itself.
type operation struct {
	in, out reflect.Type
	plan    *inputPlan
	status  int
	cfg     handleConfig
}

type describer interface {
	describe() operation
}

func (h *typed[In, Out]) describe() operation {
	return operation{in: reflect.TypeFor[In](), out: reflect.TypeFor[Out](), plan: h.plan, status: h.cfg.status, cfg: h.cfg}
}

type docRoute struct {
	method, pattern string
	guarded         bool
	op              operation
}

// OpenAPI is the app's OpenAPI document, or nil when Config.OpenAPI is unset.
func (a *App) OpenAPI() []byte { return a.openapi }

func (a *App) buildOpenAPI() error {
	oc := a.cfg.OpenAPI
	if oc == nil {
		return nil
	}
	path := oc.Path
	if path == "" {
		path = "/openapi.json"
	}
	doc, err := a.document(*oc)
	if err != nil {
		return err
	}
	a.openapi = doc
	a.register("GET "+a.basePath+path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(doc)
	}))
	return nil
}

// The document, as JSON. Every type is concrete: no map of anything.

type oaDoc struct {
	OpenAPI    string                      `json:"openapi"`
	Info       oaInfo                      `json:"info"`
	Servers    []oaServer                  `json:"servers,omitempty"`
	Paths      map[string]map[string]*oaOp `json:"paths"`
	Components oaComponents                `json:"components"`
}

type oaInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type oaServer struct {
	URL string `json:"url"`
}

type oaComponents struct {
	Schemas         map[string]*Schema          `json:"schemas"`
	SecuritySchemes map[string]oaSecurityScheme `json:"securitySchemes,omitempty"`
}

type oaSecurityScheme struct {
	Type   string `json:"type"`
	Scheme string `json:"scheme"`
}

type oaOp struct {
	OperationID string                `json:"operationId"`
	Summary     string                `json:"summary,omitempty"`
	Description string                `json:"description,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Parameters  []oaParam             `json:"parameters,omitempty"`
	RequestBody *oaBody               `json:"requestBody,omitempty"`
	Responses   map[string]oaResponse `json:"responses"`
	// Security is nil for no word on it, empty for "public", set for bearer.
	Security *[]map[string][]string `json:"security,omitempty"`
}

type oaParam struct {
	Name     string  `json:"name"`
	In       string  `json:"in"`
	Required bool    `json:"required,omitempty"`
	Schema   *Schema `json:"schema"`
	Explode  *bool   `json:"explode,omitempty"`
}

type oaBody struct {
	Required bool                   `json:"required,omitempty"`
	Content  map[string]oaMediaType `json:"content"`
}

type oaMediaType struct {
	Schema *Schema `json:"schema"`
}

type oaResponse struct {
	Description string                 `json:"description"`
	Content     map[string]oaMediaType `json:"content,omitempty"`
}

// Schema is a JSON Schema, as OpenAPI 3.1 uses it.
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 schemaType         `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
	AnyOf                []*Schema          `json:"anyOf,omitempty"`
	Enum                 []json.RawMessage  `json:"enum,omitempty"`
	Default              json.RawMessage    `json:"default,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
}

// schemaType is one type name, or several ("string", "null").
type schemaType []string

func (t schemaType) MarshalJSON() ([]byte, error) {
	if len(t) == 1 {
		return json.Marshal(t[0])
	}
	return json.Marshal([]string(t))
}

func (t *schemaType) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*t = schemaType{one}
		return nil
	}
	return json.Unmarshal(data, (*[]string)(t))
}

func typ(names ...string) schemaType { return schemaType(names) }

func (a *App) document(oc OpenAPIConfig) ([]byte, error) {
	g := &schemaGen{schemas: map[string]*Schema{}, names: map[reflect.Type]string{}}
	g.schemas["Problem"] = problemSchema()
	doc := oaDoc{
		OpenAPI:    "3.1.0",
		Info:       oaInfo{Title: oc.Title, Version: oc.Version, Description: oc.Description},
		Paths:      map[string]map[string]*oaOp{},
		Components: oaComponents{Schemas: g.schemas},
	}
	if doc.Info.Title == "" {
		doc.Info.Title = "API"
	}
	if doc.Info.Version == "" {
		doc.Info.Version = "0.0.0"
	}
	if a.basePath != "" {
		doc.Servers = []oaServer{{URL: a.basePath}}
	}
	ids := map[string]int{}
	secured := false
	for _, rt := range a.docs {
		path, ok := openAPIPath(strings.TrimPrefix(rt.pattern, a.basePath))
		if !ok {
			continue
		}
		op := g.operation(rt, path, ids)
		if rt.guarded {
			secured = true
		}
		if doc.Paths[path] == nil {
			doc.Paths[path] = map[string]*oaOp{}
		}
		doc.Paths[path][strings.ToLower(rt.method)] = op
	}
	if secured {
		doc.Components.SecuritySchemes = map[string]oaSecurityScheme{"bearer": {Type: "http", Scheme: "bearer"}}
	}
	return json.MarshalIndent(doc, "", "  ")
}

var wildcard = regexp.MustCompile(`\{([^}]*)\.\.\.\}`)

// openAPIPath turns a net/http pattern into an OpenAPI path, or reports false for
// one OpenAPI cannot express.
func openAPIPath(p string) (string, bool) {
	p = strings.TrimSuffix(p, "{$}")
	if strings.HasSuffix(p, "/") && p != "/" {
		return "", false
	}
	if p == "" {
		p = "/"
	}
	return wildcard.ReplaceAllString(p, "{$1}"), true
}

func problemSchema() *Schema {
	str := &Schema{Type: typ("string")}
	return &Schema{
		Type: typ("object"),
		Properties: map[string]*Schema{
			"type": str, "title": str, "code": str, "detail": str, "instance": str,
			"status": {Type: typ("integer")},
			"errors": {Type: typ("array"), Items: &Schema{
				Type: typ("object"),
				Properties: map[string]*Schema{
					"field": str, "code": str, "message": str,
					"params": {Type: typ("array"), Items: str},
				},
				Required: []string{"field", "code", "message"},
			}},
		},
		Required: []string{"type", "title", "status", "code"},
	}
}

func problemResponse(desc string) oaResponse {
	return oaResponse{Description: desc, Content: map[string]oaMediaType{
		"application/problem+json": {Schema: &Schema{Ref: "#/components/schemas/Problem"}},
	}}
}

var (
	nonIdent  = regexp.MustCompile(`[^A-Za-z0-9]+`)
	pathParam = regexp.MustCompile(`\{([^}]+)\}`)
)

func (g *schemaGen) operation(rt docRoute, path string, ids map[string]int) *oaOp {
	o := rt.op
	id := strings.Trim(nonIdent.ReplaceAllString(strings.ToLower(rt.method)+"_"+path, "_"), "_")
	ids[id]++
	if n := ids[id]; n > 1 {
		id += "_" + strconv.Itoa(n)
	}
	summary := o.cfg.summary
	if summary == "" {
		summary = rt.method + " " + path
	}
	public := []map[string][]string{}
	op := &oaOp{
		OperationID: id, Summary: summary, Security: &public, Description: o.cfg.description, Tags: o.cfg.tags,
		Responses: map[string]oaResponse{"default": problemResponse("An error, as RFC 9457 problem details")},
	}
	if o.plan != nil {
		for _, pp := range o.plan.params {
			op.Parameters = append(op.Parameters, g.param(o.in, pp))
		}
		if o.plan.hasBody {
			op.RequestBody = &oaBody{Required: true, Content: map[string]oaMediaType{
				"application/json": {Schema: g.schema(o.in)},
			}}
		}
		if len(o.plan.params) > 0 || o.plan.hasBody {
			op.Responses["400"] = problemResponse("The input failed decoding or validation")
		}
	}
	// Every name in the path is a parameter, whether the input reads it or not.
	for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
		if !slices.ContainsFunc(op.Parameters, func(p oaParam) bool { return p.In == "path" && p.Name == m[1] }) {
			op.Parameters = append(op.Parameters, oaParam{Name: m[1], In: "path", Required: true, Schema: &Schema{Type: typ("string")}})
		}
	}
	ok := oaResponse{Description: http.StatusText(o.status)}
	if o.out != emptyType {
		ok.Content = map[string]oaMediaType{"application/json": {Schema: g.schema(o.out)}}
	}
	op.Responses[strconv.Itoa(o.status)] = ok
	if rt.guarded || len(o.cfg.guards) > 0 {
		op.Security = &[]map[string][]string{{"bearer": {}}}
		op.Responses["401"] = problemResponse("No valid credential")
		op.Responses["403"] = problemResponse("The credential does not allow this")
	}
	return op
}

func (g *schemaGen) param(in reflect.Type, pp paramPlan) oaParam {
	f := in.FieldByIndex(pp.index)
	where := map[source]string{fromPath: "path", fromQuery: "query", fromHeader: "header"}[pp.src]
	s := g.schema(f.Type)
	applyRules(s, f.Type, f.Tag.Get("validate"))
	if pp.def != nil {
		if data, err := json.Marshal(pp.def.Interface()); err == nil {
			s.Default = data
		}
	}
	p := oaParam{Name: pp.name, In: where, Schema: s, Required: pp.src == fromPath || hasRule(f.Tag.Get("validate"), "required")}
	if pp.multi {
		t := true
		p.Explode = &t
	}
	return p
}

type schemaGen struct {
	schemas map[string]*Schema
	names   map[reflect.Type]string
}

var (
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	byteSliceType     = reflect.TypeFor[[]byte]()
)

func (g *schemaGen) schema(t reflect.Type) *Schema {
	if t.Kind() == reflect.Pointer {
		inner := g.schema(t.Elem())
		if inner.Ref != "" {
			return &Schema{AnyOf: []*Schema{inner, {Type: typ("null")}}}
		}
		if len(inner.Type) == 1 {
			inner.Type = append(inner.Type, "null")
		}
		return inner
	}
	switch {
	case t == timeTypeOA:
		return &Schema{Type: typ("string"), Format: "date-time"}
	case t == byteSliceType:
		return &Schema{Type: typ("string"), Format: "byte"}
	case t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType):
		return &Schema{Type: typ("string")}
	}
	switch t.Kind() {
	case reflect.Bool:
		return &Schema{Type: typ("boolean")}
	case reflect.Int, reflect.Int64:
		return &Schema{Type: typ("integer"), Format: "int64"}
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return &Schema{Type: typ("integer"), Format: "int32"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		zero := 0.0
		return &Schema{Type: typ("integer"), Minimum: &zero}
	case reflect.Float32:
		return &Schema{Type: typ("number"), Format: "float"}
	case reflect.Float64:
		return &Schema{Type: typ("number"), Format: "double"}
	case reflect.String:
		return &Schema{Type: typ("string")}
	case reflect.Slice, reflect.Array:
		return &Schema{Type: typ("array"), Items: g.schema(t.Elem())}
	case reflect.Map:
		return &Schema{Type: typ("object"), AdditionalProperties: g.schema(t.Elem())}
	case reflect.Struct:
		if t.Name() == "" {
			return g.object(t)
		}
		return &Schema{Ref: "#/components/schemas/" + g.component(t)}
	}
	return &Schema{} // an interface: anything
}

var timeTypeOA = reflect.TypeFor[time.Time]()

// component names t, registers its schema once, and returns the name. The name is
// taken before the properties are built, so a recursive type refers to itself.
func (g *schemaGen) component(t reflect.Type) string {
	if name, ok := g.names[t]; ok {
		return name
	}
	name := cleanName(t.Name())
	if _, taken := g.schemas[name]; taken {
		pkg := t.PkgPath()
		name = cleanName(pkg[strings.LastIndex(pkg, "/")+1:] + "_" + t.Name())
		for i := 2; ; i++ {
			if _, taken := g.schemas[name]; !taken {
				break
			}
			name = fmt.Sprintf("%s_%d", cleanName(t.Name()), i)
		}
	}
	g.names[t] = name
	g.schemas[name] = &Schema{} // reserved while the properties are built
	*g.schemas[name] = *g.object(t)
	return name
}

var (
	badComponentChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
	qualifier         = regexp.MustCompile(`[A-Za-z0-9_./-]*\.`)
)

// cleanName makes a Go type name a valid component name: a generic's
// "Page[example.com/app/notes.Note]" becomes "Page_Note". Names that then
// collide are told apart by component.
func cleanName(name string) string {
	name = qualifier.ReplaceAllString(name, "")
	return strings.Trim(badComponentChars.ReplaceAllString(name, "_"), "_")
}

// object is a struct's schema: its JSON fields, flattened through embedding.
// Fields read from the path, query or header are not in it — the body cannot set
// them.
func (g *schemaGen) object(t reflect.Type) *Schema {
	s := &Schema{Type: typ("object"), Properties: map[string]*Schema{}}
	g.fields(t, s)
	slices.Sort(s.Required)
	return s
}

func (g *schemaGen) fields(t reflect.Type, s *Schema) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag, hasJSON := f.Tag.Lookup("json")
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && f.Type.Kind() == reflect.Struct && name == "" {
			g.fields(f.Type, s)
			continue
		}
		if !f.IsExported() || name == "-" {
			continue
		}
		if !hasJSON && (hasTag(f, "path") || hasTag(f, "query") || hasTag(f, "header")) {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fs := g.schema(f.Type)
		rules := f.Tag.Get("validate")
		applyRules(fs, f.Type, rules)
		s.Properties[name] = fs
		if hasRule(rules, "required") {
			s.Required = append(s.Required, name)
		}
	}
}

func hasTag(f reflect.StructField, key string) bool {
	_, ok := f.Tag.Lookup(key)
	return ok
}

func hasRule(rules, name string) bool {
	for r := range strings.SplitSeq(rules, ",") {
		if code, _, _ := strings.Cut(strings.TrimSpace(r), "="); code == name {
			return true
		}
	}
	return false
}

// applyRules writes a field's validate rules into its schema as JSON Schema
// keywords. A $ref schema takes no keywords beside it, so a struct field keeps
// only its reference.
func applyRules(s *Schema, t reflect.Type, rules string) {
	if s.Ref != "" || len(s.AnyOf) > 0 || rules == "" {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for r := range strings.SplitSeq(rules, ",") {
		code, param, _ := strings.Cut(strings.TrimSpace(r), "=")
		n, numErr := strconv.ParseFloat(param, 64)
		switch code {
		case "min", "max", "len":
			if numErr != nil {
				continue
			}
			i := int(n)
			switch t.Kind() {
			case reflect.String:
				if code != "max" {
					s.MinLength = &i
				}
				if code != "min" {
					s.MaxLength = &i
				}
			case reflect.Slice, reflect.Array, reflect.Map:
				if code != "max" {
					s.MinItems = &i
				}
				if code != "min" {
					s.MaxItems = &i
				}
			default:
				if code != "max" {
					s.Minimum = &n
				}
				if code != "min" {
					s.Maximum = &n
				}
			}
		case "oneof":
			for _, w := range strings.Fields(param) {
				if t.Kind() == reflect.String {
					data, _ := json.Marshal(w)
					s.Enum = append(s.Enum, data)
				} else if _, err := strconv.ParseFloat(w, 64); err == nil {
					s.Enum = append(s.Enum, json.RawMessage(w))
				}
			}
		case "email":
			s.Format = "email"
		case "url":
			s.Format = "uri"
		case "uuid":
			s.Format = "uuid"
		}
	}
}
