package gesso

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Elagoht/gesso/validate"
)

// A handler's input is a struct whose fields say where they come from:
//
//	ID    string   `path:"id"`
//	Take  int      `query:"take" default:"20" validate:"max=100"`
//	Tags  []string `query:"tag"`             // ?tag=a&tag=b
//	Agent string   `header:"User-Agent"`
//	Title string   `json:"title"`            // from the JSON body
//
// Fields with no path, query or header tag are the body. A body is read only when
// the input has such fields, must be application/json, and may not name a field
// the input does not have. Path, query and header values may be strings, bools,
// integers, floats, time.Time (RFC 3339), encoding.TextUnmarshaler, a pointer to
// one (nil when absent), or a slice of one (repeated values).

type inputPlan struct {
	params  []paramPlan
	form    []formPlan
	hasBody bool
}

type source int

const (
	fromPath source = iota
	fromQuery
	fromHeader
)

type paramPlan struct {
	index []int
	src   source
	key   string
	name  string // as errors name it
	def   *reflect.Value
	parse func(string) (reflect.Value, error)
	multi bool // a slice: every value of a repeated parameter
	ptr   bool
}

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	emptyType           = reflect.TypeFor[Empty]()
)

func planInput(t reflect.Type) (*inputPlan, error) {
	p := &inputPlan{}
	if t == emptyType {
		return p, nil
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input %s is not a struct", t)
	}
	if err := planFields(t, nil, p); err != nil {
		return nil, err
	}
	if p.hasBody && len(p.form) > 0 {
		return nil, fmt.Errorf("input %s reads a form and a JSON body: give every body field a form tag, or none", t)
	}
	return p, nil
}

func planFields(t reflect.Type, prefix []int, p *inputPlan) error {
	for i := range t.NumField() {
		f := t.Field(i)
		index := append(append([]int(nil), prefix...), i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			if err := planFields(f.Type, index, p); err != nil {
				return err
			}
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct && readsParams(f.Type.Elem()) {
			// encoding/json would fill its promoted fields from the body, path
			// and query fields included, and they could not be planned: a nil
			// pointer has nowhere to put them.
			return fmt.Errorf("input %s: embed %s, not a pointer to it: it has path, query, header or form fields", t, f.Type.Elem())
		}
		if f.Anonymous && f.Tag.Get("json") != "-" {
			// encoding/json fills an embedded struct with a json tag as a field
			// of the body, exported or not.
			if st := structUnder(f.Type); st != nil && readsParams(st) {
				return fmt.Errorf("input %s: embedded %s is read from the body, but has path, query, header or form fields: move them to the input itself", t, st)
			}
		}
		if !f.IsExported() {
			continue
		}
		if hasTag(f, "form") {
			fp, err := planForm(f, index)
			if err != nil {
				return fmt.Errorf("input %s.%s: %w", t, f.Name, err)
			}
			p.form = append(p.form, fp)
			continue
		}
		pp, ok, err := planParam(f, index)
		if err != nil {
			return fmt.Errorf("input %s.%s: %w", t, f.Name, err)
		}
		if ok {
			p.params = append(p.params, pp)
		} else if f.Tag.Get("json") != "-" {
			if st := structUnder(f.Type); st != nil && readsParams(st) {
				// The body would fill them: gesso reads the path, query,
				// headers and forms only at the input's top level.
				return fmt.Errorf("input %s.%s: %s is read from the body, but has path, query, header or form fields: move them to the input itself", t, f.Name, st)
			}
			p.hasBody = true
		}
	}
	return nil
}

// structUnder is the struct a field holds through pointers, slices and maps,
// or nil.
func structUnder(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		case reflect.Struct:
			return t
		default:
			return nil
		}
	}
}

// readsParams reports whether t, or any struct inside it, has a field read
// from the path, query, header or a form.
func readsParams(t reflect.Type) bool { return readsParamsIn(t, map[reflect.Type]bool{}) }

func readsParamsIn(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	for i := range t.NumField() {
		f := t.Field(i)
		if hasTag(f, "path") || hasTag(f, "query") || hasTag(f, "header") || hasTag(f, "form") {
			return true
		}
		if st := structUnder(f.Type); st != nil && readsParamsIn(st, seen) {
			return true
		}
	}
	return false
}

func planParam(f reflect.StructField, index []int) (paramPlan, bool, error) {
	pp := paramPlan{index: index}
	n := 0
	for src, key := range map[source]string{fromPath: "path", fromQuery: "query", fromHeader: "header"} {
		if v, ok := f.Tag.Lookup(key); ok {
			pp.src, pp.key = src, v
			n++
		}
	}
	if n == 0 {
		return pp, false, nil
	}
	if n > 1 {
		return pp, false, errors.New("a field takes one of path, query or header")
	}
	if pp.key == "" {
		return pp, false, errors.New("the tag names no parameter")
	}
	pp.name = pp.key
	if pp.src == fromHeader {
		pp.key = http.CanonicalHeaderKey(pp.key)
	}
	t := f.Type
	if t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8 {
		if pp.src == fromPath {
			return pp, false, errors.New("a path parameter is a single value")
		}
		pp.multi = true
		t = t.Elem()
	} else if t.Kind() == reflect.Pointer {
		pp.ptr = true
		t = t.Elem()
	}
	parse, err := parserFor(t)
	if err != nil {
		return pp, false, err
	}
	pp.parse = parse
	if d, ok := f.Tag.Lookup("default"); ok {
		if pp.src == fromPath {
			return pp, false, errors.New("a path parameter has no default")
		}
		v, err := parse(d)
		if err != nil {
			return pp, false, fmt.Errorf("default %q: %w", d, err)
		}
		pp.def = &v
	}
	return pp, true, nil
}

type parseError struct{ want string }

func (e parseError) Error() string { return "must be " + e.want }

func parserFor(t reflect.Type) (func(string) (reflect.Value, error), error) {
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return func(s string) (reflect.Value, error) {
			v := reflect.New(t)
			if err := v.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s)); err != nil {
				// The type's message may quote what the client sent: keep it
				// short.
				msg := err.Error()
				if len(msg) > 100 {
					msg = msg[:100] + "…"
				}
				return v, parseError{"valid: " + msg}
			}
			return v.Elem(), nil
		}, nil
	}
	if t == reflect.TypeFor[time.Time]() {
		return func(s string) (reflect.Value, error) {
			tm, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return reflect.Value{}, parseError{"an RFC 3339 time"}
			}
			return reflect.ValueOf(tm), nil
		}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return func(s string) (reflect.Value, error) { return reflect.ValueOf(s).Convert(t), nil }, nil
	case reflect.Bool:
		return func(s string) (reflect.Value, error) {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return reflect.Value{}, parseError{"true or false"}
			}
			return reflect.ValueOf(b).Convert(t), nil
		}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(s string) (reflect.Value, error) {
			n, err := strconv.ParseInt(s, 10, t.Bits())
			if err != nil {
				return reflect.Value{}, parseError{"an integer"}
			}
			return reflect.ValueOf(n).Convert(t), nil
		}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return func(s string) (reflect.Value, error) {
			n, err := strconv.ParseUint(s, 10, t.Bits())
			if err != nil {
				return reflect.Value{}, parseError{"a non-negative integer"}
			}
			return reflect.ValueOf(n).Convert(t), nil
		}, nil
	case reflect.Float32, reflect.Float64:
		return func(s string) (reflect.Value, error) {
			n, err := strconv.ParseFloat(s, t.Bits())
			if err != nil {
				return reflect.Value{}, parseError{"a number"}
			}
			return reflect.ValueOf(n).Convert(t), nil
		}, nil
	}
	return nil, fmt.Errorf("%s cannot be read from a path, query or header", t)
}

// decode fills dst from r. Its error is an *Error ready to answer.
func (p *inputPlan) decode(r *http.Request, dst reflect.Value, maxBody, formMem int64) error {
	var fields []validate.FieldError
	if len(p.form) > 0 {
		if err := p.decodeForm(r, dst, maxBody, formMem); err != nil {
			return err
		}
	}
	if p.hasBody {
		if err := decodeBody(r, dst, maxBody); err != nil {
			return err
		}
		// encoding/json fills a field with no json tag by its Go name, so a body
		// could set a query or header field ({"take": 99999}) and slip past where
		// it is meant to come from. Such a key is not a field of the body.
		for _, pp := range p.params {
			field := dst.FieldByIndex(pp.index)
			if !field.IsZero() {
				fields = append(fields, validate.FieldError{
					Field: pp.name, Code: "unknown", Message: "is not a field of the body",
				})
				field.SetZero()
			}
		}
		if fields != nil {
			return ErrInvalidInput.WithFields(fields...)
		}
	}
	var query map[string][]string
	for _, pp := range p.params {
		var values []string
		switch pp.src {
		case fromPath:
			values = []string{r.PathValue(pp.key)}
		case fromQuery:
			if query == nil {
				query = r.URL.Query()
			}
			values = query[pp.key]
		case fromHeader:
			values = r.Header.Values(pp.key)
		}
		field := dst.FieldByIndex(pp.index)
		if len(values) == 0 {
			if pp.def != nil {
				setParsed(field, pp, []reflect.Value{*pp.def})
			}
			continue
		}
		if !pp.multi {
			values = values[:1]
		}
		parsed := make([]reflect.Value, 0, len(values))
		for _, s := range values {
			v, err := pp.parse(s)
			if err != nil {
				fields = append(fields, validate.FieldError{Field: pp.name, Code: "type", Message: err.Error()})
				break
			}
			parsed = append(parsed, v)
		}
		if len(parsed) == len(values) {
			setParsed(field, pp, parsed)
		}
	}
	if fields != nil {
		return ErrInvalidInput.WithFields(fields...)
	}
	return nil
}

func setParsed(field reflect.Value, pp paramPlan, vs []reflect.Value) {
	switch {
	case pp.multi:
		s := reflect.MakeSlice(field.Type(), 0, len(vs))
		field.Set(reflect.Append(s, vs...))
	case pp.ptr:
		ptr := reflect.New(field.Type().Elem())
		ptr.Elem().Set(vs[0])
		field.Set(ptr)
	default:
		field.Set(vs[0])
	}
}

func decodeBody(r *http.Request, dst reflect.Value, maxBody int64) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	body := http.MaxBytesReader(nil, r.Body, maxBody)
	// Peek one byte: an empty body is no body, whatever its headers say.
	var first [1]byte
	n, err := io.ReadFull(body, first[:])
	if n == 0 {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		return bodyError(err)
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return ErrUnsupportedMedia
	}
	dec := json.NewDecoder(io.MultiReader(bytes.NewReader(first[:]), body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst.Addr().Interface()); err != nil {
		return bodyError(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrInvalidJSON.WithDetail("the body holds more than one JSON value")
	}
	return nil
}

func bodyError(err error) error {
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		return ErrBodyTooLarge
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return ErrInvalidInput.WithFields(validate.FieldError{
			Field: typeErr.Field, Code: "type", Message: "must be " + jsonKind(typeErr.Type),
		})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		name := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return ErrInvalidInput.WithFields(validate.FieldError{
			Field: name, Code: "unknown", Message: "is not a field of this request",
		})
	}
	return ErrInvalidJSON.Wrap(err)
}

func jsonKind(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	}
	return "an object"
}
