package gesso

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Optional is a field of a partial update (PATCH): a body says, for each, that
// it is not there — leave it as it is — that it is null — clear it — or what
// it is now:
//
//	type PatchNote struct {
//		Title gesso.Optional[string]  `json:"title,omitzero" validate:"max=120"`
//		Due   gesso.Optional[date.Date] `json:"due,omitzero"`
//	}
//
// Validation rules apply to Value when one is given; "required" asks that the
// field be given and not null. A value of the wrong type is a 400 that names
// the field where encoding/json does; some Go versions name none for an error
// from a type's own UnmarshalJSON, and answer invalid JSON. A Remote leaves an Optional that is not Set out
// of the body; omitzero does the same for encoding/json.
type Optional[T any] struct {
	Set   bool // the body had the field
	Null  bool // it was null
	Value T    // what it was, when Set and not Null
}

// Some is an Optional holding v.
func Some[T any](v T) Optional[T] { return Optional[T]{Set: true, Value: v} }

// Null is an Optional set to null.
func Null[T any]() Optional[T] { return Optional[T]{Set: true, Null: true} }

// IsZero reports whether o is not set, for omitzero.
func (o Optional[T]) IsZero() bool { return !o.Set }

// OptionalPresent and OptionalNull let validation, decoding and the OpenAPI
// generator see through an Optional without knowing its T.
func (o Optional[T]) OptionalPresent() bool { return o.Set }

// OptionalNull reports whether o was set to null.
func (o Optional[T]) OptionalNull() bool { return o.Null }

// MarshalJSON writes Value, or null.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if !o.Set || o.Null {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}

// UnmarshalJSON reads null or a value; either way the field was Set.
func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		var zero T
		o.Value = zero
		return nil
	}
	o.Null = false
	return json.Unmarshal(b, &o.Value)
}

// optionalType is T for an Optional[T], and whether t is one.
func optionalType(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() != reflect.Struct || t.PkgPath() != reflect.TypeFor[Empty]().PkgPath() {
		return nil, false
	}
	if _, ok := t.MethodByName("OptionalPresent"); !ok {
		return nil, false
	}
	f, ok := t.FieldByName("Value")
	if !ok {
		return nil, false
	}
	return f.Type, true
}
