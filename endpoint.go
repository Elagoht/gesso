package gesso

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Elagoht/gesso/validate"
)

// Endpoint is a route's contract — its method, its path, what it reads and what
// it answers — declared once, in a package both sides import (internal/ of a
// module that holds collage and gesso):
//
//	var GetNote = gesso.Endpoint[GetIn, Note]{Method: "GET", Pattern: "/notes/{slug}"}
//
// The server registers its handler on it, and the compiler holds the handler to
// the contract:
//
//	GetNote.Handle(r, m.get)
//
// Another process — collage — calls it, and the compiler holds the call to it:
//
//	note, err := GetNote.Call(ctx, api, GetIn{Slug: "jam"}, gesso.Bearer(token))
//
// Pattern is the path under the app's BasePath, whatever group registers it.
type Endpoint[In, Out any] struct {
	Method  string
	Pattern string
}

// Handle registers fn for the endpoint on r. r may be a group whose prefix the
// pattern begins with; it runs the group's pipeline.
func (e Endpoint[In, Out]) Handle(r *Router, fn HandlerFunc[In, Out], opts ...HandleOption) {
	rest, ok := strings.CutPrefix(e.Pattern, r.prefix)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		r.app.fail(fmt.Errorf("gesso: endpoint %s %s is not under its group %q", e.Method, e.Pattern, r.prefix))
		return
	}
	r.Handle(e.Method, rest, Handle(fn, opts...))
}

// Remote is another process's gesso app, for Endpoint.Call.
type Remote struct {
	base   string
	client *http.Client
}

// NewRemote calls the app at base — "http://api.internal:8081/api", its BasePath
// included — through client (nil for http.DefaultClient).
func NewRemote(base string, client *http.Client) *Remote {
	if client == nil {
		client = http.DefaultClient
	}
	return &Remote{base: strings.TrimSuffix(base, "/"), client: client}
}

// CallOption changes one call.
type CallOption func(*http.Request)

// Bearer sends token as the call's credential.
func Bearer(token string) CallOption {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// Header sends a header with the call.
func Header(key, value string) CallOption {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// Call sends in to the endpoint and decodes its answer. A problem answer is
// returned as an *Error with the problem's status, code, detail and fields, so
// errors.Is matches it against the declaration the server returned:
//
//	if errors.Is(err, notes.ErrNoteLocked) { … }
//
// In is encoded as the server decodes it: path fields into the pattern (escaped),
// query and header fields as parameters, and the rest as the JSON body. A zero
// query or header value is left out, and the server fills in the field's
// default: a field whose default is not its zero (`default:"true"` on a bool)
// needs a pointer to send its zero.
func (e Endpoint[In, Out]) Call(ctx context.Context, rm *Remote, in In, opts ...CallOption) (Out, error) {
	var out Out
	req, err := e.request(ctx, rm, in)
	if err != nil {
		return out, err
	}
	for _, opt := range opts {
		opt(req)
	}
	res, err := rm.client.Do(req)
	if err != nil {
		return out, fmt.Errorf("gesso: %s %s: %w", e.Method, e.Pattern, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return out, fmt.Errorf("gesso: %s %s: read: %w", e.Method, e.Pattern, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return out, remoteError(res, body)
	}
	if reflect.TypeFor[Out]() == emptyType || len(body) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("gesso: %s %s: decode: %w", e.Method, e.Pattern, err)
	}
	return out, nil
}

func remoteError(res *http.Response, body []byte) error {
	if mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); mt == "application/problem+json" {
		var p Problem
		if json.Unmarshal(body, &p) == nil && p.Code != "" {
			return &Error{Status: res.StatusCode, Code: p.Code, Detail: p.Detail, Fields: p.Errors}
		}
	}
	return &Error{Status: res.StatusCode, Code: "http_" + strconv.Itoa(res.StatusCode), Detail: strings.TrimSpace(string(body[:min(len(body), 200)]))}
}

func (e Endpoint[In, Out]) request(ctx context.Context, rm *Remote, in In) (*http.Request, error) {
	plan, err := planInput(reflect.TypeFor[In]())
	if err != nil {
		return nil, err
	}
	v := reflect.ValueOf(in)
	path := e.Pattern
	query := url.Values{}
	header := http.Header{}
	for _, pp := range plan.params {
		values, err := formatParam(v.FieldByIndex(pp.index), pp)
		if err != nil {
			return nil, fmt.Errorf("gesso: %s %s: %s: %w", e.Method, e.Pattern, pp.name, err)
		}
		switch pp.src {
		case fromPath:
			s := ""
			if len(values) > 0 {
				s = values[0]
			}
			if s == "" {
				return nil, fmt.Errorf("gesso: %s %s: path parameter %q is empty", e.Method, e.Pattern, pp.key)
			}
			path = strings.Replace(path, "{"+pp.key+"}", url.PathEscape(s), 1)
			path = strings.Replace(path, "{"+pp.key+"...}", escapeSegments(s), 1)
		case fromQuery:
			for _, s := range values {
				query.Add(pp.key, s)
			}
		case fromHeader:
			for _, s := range values {
				header.Add(pp.key, s)
			}
		}
	}
	target := rm.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if plan.hasBody {
		data, err := encodeBody(v)
		if err != nil {
			return nil, fmt.Errorf("gesso: %s %s: body: %w", e.Method, e.Pattern, err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, e.Method, target, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, application/problem+json")
	return req, nil
}

func escapeSegments(s string) string {
	segs := strings.Split(s, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}

// formatParam writes a path, query or header field as the server will parse it:
// nothing for a nil pointer or a zero value.
func formatParam(f reflect.Value, pp paramPlan) ([]string, error) {
	if pp.multi {
		out := make([]string, 0, f.Len())
		for i := range f.Len() {
			s, err := formatScalar(f.Index(i))
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	if pp.ptr {
		if f.IsNil() {
			return nil, nil
		}
		f = f.Elem()
	} else if f.IsZero() {
		return nil, nil
	}
	s, err := formatScalar(f)
	if err != nil {
		return nil, err
	}
	return []string{s}, nil
}

func formatScalar(v reflect.Value) (string, error) {
	if v.Type() == reflect.TypeFor[time.Time]() {
		return v.Interface().(time.Time).Format(time.RFC3339Nano), nil
	}
	if tm, ok := v.Interface().(encoding.TextMarshaler); ok {
		b, err := tm.MarshalText()
		return string(b), err
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits()), nil
	}
	return "", errors.New("cannot be written as text")
}

// encodeBody writes the body fields of v — not its path, query or header fields,
// which the server would refuse in a body — as one JSON object, honouring their
// json tags.
func encodeBody(v reflect.Value) ([]byte, error) {
	members := map[string]json.RawMessage{}
	if err := bodyMembers(v, members); err != nil {
		return nil, err
	}
	return json.Marshal(members)
}

func bodyMembers(v reflect.Value, members map[string]json.RawMessage) error {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _ := f.Tag.Lookup("json")
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && f.Type.Kind() == reflect.Struct && name == "" {
			if err := bodyMembers(v.Field(i), members); err != nil {
				return err
			}
			continue
		}
		if !f.IsExported() || name == "-" || hasTag(f, "path") || hasTag(f, "query") || hasTag(f, "header") {
			continue
		}
		fv := v.Field(i)
		if slices.Contains(strings.Split(opts, ","), "omitempty") && isEmptyJSON(fv) {
			continue
		}
		if name == "" {
			name = f.Name
		}
		data, err := json.Marshal(fv.Interface())
		if err != nil {
			return err
		}
		members[name] = data
	}
	return nil
}

// isEmptyJSON is encoding/json's omitempty test.
func isEmptyJSON(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	}
	return v.IsZero()
}

// FieldErrors is the field errors of an error answered as a problem, local or
// from a Remote, for a form to show beside its inputs.
func FieldErrors(err error) []validate.FieldError {
	var e *Error
	if errors.As(err, &e) {
		return e.Fields
	}
	return nil
}
