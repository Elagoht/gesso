// Package validate checks a struct against the rules in its `validate` tags.
//
// The rule names are go-playground/validator's, for the rules this package has, so
// a tag people already know means the same thing here:
//
//	required   the value is not its zero value (a pointer is not nil)
//	omitempty  a zero value skips the field's remaining rules
//	min=N      a string has at least N characters, a slice or map at least N
//	           elements, a number is at least N
//	max=N      the same, at most
//	len=N      exactly N characters or elements
//	oneof=a b  the value is one of the space-separated words
//	gt=N gte=N lt=N lte=N
//	           greater than, at least, less than, at most N — counted as min
//	           and max count: characters, elements, or the number's value
//	eq=x ne=x  equal or not equal to x: a string's value, a number's value, a
//	           collection's length
//	email      a bare address: "a@b.c", not "Name <a@b.c>"
//	url        an absolute URL with a scheme and a host
//	uuid       the canonical 8-4-4-4-12 hexadecimal form
//	ip ipv4 ipv6
//	           an IP address, of either or one family
//	hostname   a DNS host name (RFC 1123): labels of letters, digits and hyphens
//	alpha alphanum numeric
//	           ASCII letters; letters and digits; an optional sign and digits
//	lowercase uppercase
//	           no upper-case (lower-case) letter
//	contains=x excludes=x startswith=x endswith=x
//	           the string holds, does not hold, begins or ends with x
//	slug       lower-case words of ASCII letters and digits joined by single
//	           hyphens: "getting-started" (gesso's own; not go-playground's)
//	unique     a slice or array whose elements are all different
//	dive       the rules after it apply to each element of a slice, array or
//	           map: `validate:"max=10,dive,required,max=40"`
//	notnull    a gesso.Optional that is given is not null: a PATCH may leave
//	           the field out, not clear it (gesso's own)
//
// Nested structs, pointers to structs and slices of structs are checked too. A
// field is named in an error by its json tag, else its query, path or header tag,
// else its Go name; nested fields read "address.city", "items[2].name".
//
// A type whose tags name an unknown rule, or give a rule a parameter it cannot
// use, is a programmer error: Compile reports it, and Struct panics on it. gesso
// compiles every handler's input when the route is registered, so a request never
// meets one.
package validate

import (
	"context"
	"encoding"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// FieldError is one failed rule on one field.
type FieldError struct {
	// Field is the field's name as the client spelled it.
	Field string `json:"field"`
	// Code is the rule that failed ("required", "max", …), or a code of the
	// application's own from a SelfValidator. It is the key to translate.
	Code string `json:"code"`
	// Params are the rule's parameters, as written in the tag.
	Params []string `json:"params,omitempty"`
	// Message is an English sentence for the failure.
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + " " + e.Message }

// SelfValidator is a struct that checks what tags cannot: one field against
// another, a rule that needs a lookup. Struct calls it after the tags pass.
type SelfValidator interface {
	Validate(ctx context.Context) []FieldError
}

// Compile checks T's tags without a value, and caches what it learned.
func Compile[T any]() error {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return fmt.Errorf("validate: %s is not a struct", t)
	}
	_, err := planFor(t)
	return err
}

// Struct checks v and returns every failure, or nil. A SelfValidator's own
// checks run only when the tags pass, so it may rely on them.
func Struct[T any](ctx context.Context, v T) []FieldError {
	rv := reflect.ValueOf(&v).Elem()
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		panic(fmt.Sprintf("validate: %s is not a struct", rv.Type()))
	}
	p, err := planFor(rv.Type())
	if err != nil {
		panic(err.Error())
	}
	var out []FieldError
	p.check(rv, "", &out)
	if len(out) > 0 {
		return out
	}
	if sv, ok := selfValidator(rv); ok {
		return sv.Validate(ctx)
	}
	return nil
}

// selfValidator finds a Validate method on the value or on its address.
func selfValidator(rv reflect.Value) (SelfValidator, bool) {
	if rv.CanAddr() {
		if sv, ok := rv.Addr().Interface().(SelfValidator); ok {
			return sv, true
		}
	}
	sv, ok := rv.Interface().(SelfValidator)
	return sv, ok
}

// FieldName is the name a client knows a struct field by: its json tag, else its
// form, query, path or header tag, else its Go name. gesso's decoder names fields the
// same way.
func FieldName(f reflect.StructField) string {
	for _, key := range []string{"json", "form", "query", "path", "header"} {
		if tag, ok := f.Tag.Lookup(key); ok {
			name, _, _ := strings.Cut(tag, ",")
			if name != "" && name != "-" {
				return name
			}
		}
	}
	return f.Name
}

var (
	timeType            = reflect.TypeFor[time.Time]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

type typePlan struct {
	fields []fieldPlan
}

type fieldPlan struct {
	index []int
	name  string
	rules []rule
	// elem holds the rules after "dive", for each element.
	elem []rule
	// nested is set for a field whose value (through pointers and slices) is a
	// struct to check in turn.
	nested *typePlan
}

type rule struct {
	code   string
	params []string
	n      float64
	check  func(v reflect.Value, r rule) bool
	msg    func(r rule, v reflect.Value) string
}

var plans sync.Map // reflect.Type → *typePlan

func planFor(t reflect.Type) (*typePlan, error) {
	if p, ok := plans.Load(t); ok {
		return p.(*typePlan), nil
	}
	building := map[reflect.Type]*typePlan{}
	p, err := build(t, building)
	if err != nil {
		return nil, err
	}
	for bt, bp := range building {
		plans.LoadOrStore(bt, bp)
	}
	return p, nil
}

func build(t reflect.Type, building map[reflect.Type]*typePlan) (*typePlan, error) {
	if p, ok := plans.Load(t); ok {
		return p.(*typePlan), nil
	}
	if p, ok := building[t]; ok {
		return p, nil // a recursive type: the plan is being filled in
	}
	p := &typePlan{}
	building[t] = p
	if err := addFields(t, nil, p, building); err != nil {
		return nil, err
	}
	return p, nil
}

func addFields(t reflect.Type, prefix []int, p *typePlan, building map[reflect.Type]*typePlan) error {
	for i := range t.NumField() {
		f := t.Field(i)
		index := append(append([]int(nil), prefix...), i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			if err := addFields(f.Type, index, p, building); err != nil {
				return err
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		fp := fieldPlan{index: index, name: FieldName(f)}
		ruleType := f.Type
		if elem, ok := optionalElem(f.Type); ok {
			ruleType = elem // the rules are about the value an Optional holds
		}
		if tag := f.Tag.Get("validate"); tag != "" && tag != "-" {
			own, elem, dive := strings.Cut(tag, ",dive")
			if strings.HasPrefix(tag, "dive") {
				own, elem, dive = "", strings.TrimPrefix(tag, "dive"), true
			}
			rules, err := parseRules(own, ruleType)
			if err != nil {
				return fmt.Errorf("validate: %s.%s: %w", t, f.Name, err)
			}
			if _, opt := optionalElem(f.Type); !opt && slices.ContainsFunc(rules, func(r rule) bool { return r.code == "notnull" }) {
				return fmt.Errorf("validate: %s.%s: notnull is for gesso.Optional fields", t, f.Name)
			}
			fp.rules = rules
			if dive {
				et, err := elemType(ruleType)
				if err != nil {
					return fmt.Errorf("validate: %s.%s: %w", t, f.Name, err)
				}
				elem = strings.TrimPrefix(elem, ",")
				if strings.Contains(","+elem+",", ",dive,") {
					return fmt.Errorf("validate: %s.%s: one dive per field", t, f.Name)
				}
				if fp.elem, err = parseRules(elem, et); err != nil {
					return fmt.Errorf("validate: %s.%s: after dive: %w", t, f.Name, err)
				}
				if fp.elem == nil {
					fp.elem = []rule{}
				}
			}
		}
		if st := structBeneath(ruleType); st != nil {
			nested, err := build(st, building)
			if err != nil {
				return err
			}
			fp.nested = nested
		}
		if fp.rules != nil || fp.elem != nil || fp.nested != nil {
			p.fields = append(p.fields, fp)
		}
	}
	return nil
}

// optionalElem is T for a gesso.Optional[T] — a struct with OptionalPresent
// and OptionalNull methods and a Value field — and whether t is one.
func optionalElem(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	if _, ok := t.MethodByName("OptionalPresent"); !ok {
		return nil, false
	}
	if _, ok := t.MethodByName("OptionalNull"); !ok {
		return nil, false
	}
	f, ok := t.FieldByName("Value")
	if !ok {
		return nil, false
	}
	return f.Type, true
}

// structBeneath is the struct type a field holds through pointers and slices, or
// nil. time.Time, and any struct that reads itself from text (date.Date), is a
// value, not a struct to look inside.
func structBeneath(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			t = t.Elem()
		case reflect.Struct:
			if t == timeType || reflect.PointerTo(t).Implements(textUnmarshalerType) {
				return nil
			}
			return t
		default:
			return nil
		}
	}
}

func (p *typePlan) check(rv reflect.Value, prefix string, out *[]FieldError) {
	for _, fp := range p.fields {
		fv := rv.FieldByIndex(fp.index)
		name := fp.name
		if prefix != "" {
			name = prefix + "." + name
		}
		if _, ok := optionalElem(fv.Type()); ok {
			// An Optional left out, or null, has no value to check; "required"
			// asks for one.
			present := fv.MethodByName("OptionalPresent").Call(nil)[0].Bool()
			null := fv.MethodByName("OptionalNull").Call(nil)[0].Bool()
			if !present || null {
				for _, r := range fp.rules {
					if r.code == "required" || (r.code == "notnull" && present) {
						*out = append(*out, fail(name, r, fv))
						break
					}
				}
				continue
			}
			fv = fv.FieldByName("Value")
		}
		if !checkRules(fv, fp.rules, name, out) {
			continue
		}
		if fp.elem != nil {
			checkElems(fv, fp.elem, name, out)
		}
		if fp.nested != nil {
			checkNested(fv, fp.nested, name, out)
		}
	}
}

// checkRules applies a field's rules and reports whether checking may go on into
// its nested fields.
func checkRules(fv reflect.Value, rules []rule, name string, out *[]FieldError) bool {
	for _, r := range rules {
		switch r.code {
		case "omitempty":
			if fv.IsZero() {
				return false
			}
			continue
		case "required":
			if isEmpty(fv) {
				*out = append(*out, fail(name, r, fv))
				return false
			}
			continue
		}
		v := fv
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return false // an absent optional value has nothing to check
			}
			v = v.Elem()
		}
		if !r.check(v, r) {
			*out = append(*out, fail(name, r, v))
			return false
		}
	}
	return true
}

// checkElems applies the rules after dive to each element: "tags[2]" for a
// slice, "names[en]" for a map.
func checkElems(fv reflect.Value, rules []rule, name string, out *[]FieldError) {
	for fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return
		}
		fv = fv.Elem()
	}
	switch fv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := range fv.Len() {
			checkRules(fv.Index(i), rules, name+"["+strconv.Itoa(i)+"]", out)
		}
	case reflect.Map:
		keys := fv.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
		for _, k := range keys {
			checkRules(fv.MapIndex(k), rules, name+"["+fmt.Sprint(k)+"]", out)
		}
	}
}

// elemType is what dive reaches: the element of a slice, array or map.
func elemType(t reflect.Type) (reflect.Type, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return t.Elem(), nil
	}
	return nil, fmt.Errorf("dive does not apply to %s", t)
}

func checkNested(fv reflect.Value, p *typePlan, name string, out *[]FieldError) {
	for fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			return
		}
		fv = fv.Elem()
	}
	switch fv.Kind() {
	case reflect.Struct:
		p.check(fv, name, out)
	case reflect.Slice, reflect.Array:
		for i := range fv.Len() {
			checkNested(fv.Index(i), p, name+"["+strconv.Itoa(i)+"]", out)
		}
	}
}

func isEmpty(v reflect.Value) bool {
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		return v.IsNil()
	}
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Map {
		return v.Len() == 0
	}
	return v.IsZero()
}

func fail(name string, r rule, v reflect.Value) FieldError {
	return FieldError{Field: name, Code: r.code, Params: r.params, Message: r.msg(r, v)}
}

func parseRules(tag string, t reflect.Type) ([]rule, error) {
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	var rules []rule
	if strings.TrimSpace(tag) == "" {
		return nil, nil
	}
	for part := range strings.SplitSeq(tag, ",") {
		code, param, hasParam := strings.Cut(strings.TrimSpace(part), "=")
		r := rule{code: code}
		switch code {
		case "notnull":
			if hasParam {
				return nil, fmt.Errorf("%q takes no parameter", code)
			}
			r.check = func(reflect.Value, rule) bool { return true } // checked where the Optional is seen
			r.msg = func(rule, reflect.Value) string { return "must not be null" }
		case "omitempty", "required":
			if hasParam {
				return nil, fmt.Errorf("%q takes no parameter", code)
			}
			r.msg = func(rule, reflect.Value) string { return "is required" }
		case "min", "max", "len", "gt", "gte", "lt", "lte":
			n, err := strconv.ParseFloat(param, 64)
			if !hasParam || err != nil {
				return nil, fmt.Errorf("%q needs a number, got %q", code, param)
			}
			if !sized(base) {
				return nil, fmt.Errorf("%q does not apply to %s", code, base)
			}
			r.n = n
			r.params = []string{param}
			r.check = checkSize
			r.msg = sizeMessage
		case "oneof":
			words := strings.Fields(param)
			if len(words) == 0 {
				return nil, fmt.Errorf("%q needs at least one word", code)
			}
			if base.Kind() != reflect.String && !isInt(base) {
				return nil, fmt.Errorf("%q does not apply to %s", code, base)
			}
			r.params = words
			r.check = checkOneOf
			r.msg = func(r rule, _ reflect.Value) string {
				return "must be one of: " + strings.Join(r.params, ", ")
			}
		case "eq", "ne":
			if !hasParam {
				return nil, fmt.Errorf("%q needs a value", code)
			}
			if base.Kind() != reflect.String && !sized(base) {
				return nil, fmt.Errorf("%q does not apply to %s", code, base)
			}
			if base.Kind() != reflect.String {
				n, err := strconv.ParseFloat(param, 64)
				if err != nil {
					return nil, fmt.Errorf("%q needs a number for %s, got %q", code, base, param)
				}
				r.n = n
			}
			r.params = []string{param}
			r.check = checkEq
			r.msg = func(r rule, _ reflect.Value) string {
				if r.code == "eq" {
					return "must be " + r.params[0]
				}
				return "must not be " + r.params[0]
			}
		case "contains", "excludes", "startswith", "endswith":
			if !hasParam || param == "" {
				return nil, fmt.Errorf("%q needs a value", code)
			}
			if base.Kind() != reflect.String {
				return nil, fmt.Errorf("%q does not apply to %s", code, base)
			}
			r.params = []string{param}
			r.check = checkText
			r.msg = func(r rule, _ reflect.Value) string {
				verb := map[string]string{"contains": "must contain", "excludes": "must not contain",
					"startswith": "must start with", "endswith": "must end with"}[r.code]
				return verb + " " + strconv.Quote(r.params[0])
			}
		case "unique":
			if hasParam {
				return nil, fmt.Errorf("%q takes no parameter", code)
			}
			if (base.Kind() != reflect.Slice && base.Kind() != reflect.Array) || !base.Elem().Comparable() {
				return nil, fmt.Errorf("%q does not apply to %s", code, base)
			}
			r.check = checkUnique
			r.msg = func(rule, reflect.Value) string { return "must not hold the same value twice" }
		case "email", "url", "uuid", "ip", "ipv4", "ipv6", "hostname",
			"alpha", "alphanum", "numeric", "lowercase", "uppercase", "slug":
			if hasParam {
				return nil, fmt.Errorf("%q takes no parameter", code)
			}
			if base.Kind() != reflect.String {
				return nil, fmt.Errorf("%q does not apply to %s", code, base)
			}
			r.check = stringChecks[code]
			r.msg = func(r rule, _ reflect.Value) string { return stringMessages[r.code] }
		default:
			return nil, fmt.Errorf("unknown rule %q", code)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func sized(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array, reflect.Float32, reflect.Float64:
		return true
	}
	return isInt(t)
}

func isInt(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// size is a string's character count, a collection's length or a number's value.
func size(v reflect.Value) (n float64, counted bool) {
	switch v.Kind() {
	case reflect.String:
		return float64(utf8.RuneCountInString(v.String())), true
	case reflect.Slice, reflect.Map, reflect.Array:
		return float64(v.Len()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), false
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), false
	case reflect.Float32, reflect.Float64:
		return v.Float(), false
	}
	return 0, false
}

func checkSize(v reflect.Value, r rule) bool {
	n, _ := size(v)
	switch r.code {
	case "min", "gte":
		return n >= r.n
	case "max", "lte":
		return n <= r.n
	case "gt":
		return n > r.n
	case "lt":
		return n < r.n
	default:
		return n == r.n
	}
}

func sizeMessage(r rule, v reflect.Value) string {
	_, counted := size(v)
	unit := ""
	if counted {
		unit = " items"
		if v.Kind() == reflect.String {
			unit = " characters"
		}
	}
	switch r.code {
	case "min", "gte":
		return "must be at least " + r.params[0] + unit
	case "max", "lte":
		return "must be at most " + r.params[0] + unit
	case "gt":
		return "must be more than " + r.params[0] + unit
	case "lt":
		return "must be less than " + r.params[0] + unit
	default:
		return "must be exactly " + r.params[0] + unit
	}
}

func checkOneOf(v reflect.Value, r rule) bool {
	var s string
	switch {
	case v.Kind() == reflect.String:
		s = v.String()
	case v.CanInt():
		s = strconv.FormatInt(v.Int(), 10)
	default:
		s = strconv.FormatUint(v.Uint(), 10)
	}
	for _, w := range r.params {
		if s == w {
			return true
		}
	}
	return false
}

func checkEq(v reflect.Value, r rule) bool {
	var same bool
	if v.Kind() == reflect.String {
		same = v.String() == r.params[0]
	} else {
		n, _ := size(v)
		same = n == r.n
	}
	return same == (r.code == "eq")
}

func checkText(v reflect.Value, r rule) bool {
	s, x := v.String(), r.params[0]
	switch r.code {
	case "contains":
		return strings.Contains(s, x)
	case "excludes":
		return !strings.Contains(s, x)
	case "startswith":
		return strings.HasPrefix(s, x)
	default:
		return strings.HasSuffix(s, x)
	}
}

func checkUnique(v reflect.Value, _ rule) bool {
	for i := range v.Len() {
		for j := range i {
			if v.Index(i).Equal(v.Index(j)) {
				return false
			}
		}
	}
	return true
}

func allASCII(s string, ok func(c byte) bool) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func isDigit(c byte) bool  { return '0' <= c && c <= '9' }

var stringMessages = map[string]string{
	"email": "must be a valid email", "url": "must be a valid url", "uuid": "must be a valid uuid",
	"ip": "must be an IP address", "ipv4": "must be an IPv4 address", "ipv6": "must be an IPv6 address",
	"hostname": "must be a host name", "alpha": "must hold only letters",
	"alphanum": "must hold only letters and digits", "numeric": "must be a number",
	"lowercase": "must be lower-case", "uppercase": "must be upper-case",
	"slug": "must be lower-case words joined by hyphens",
}

var stringChecks = map[string]func(reflect.Value, rule) bool{
	"ip": func(v reflect.Value, _ rule) bool {
		_, err := netip.ParseAddr(v.String())
		return err == nil
	},
	"ipv4": func(v reflect.Value, _ rule) bool {
		a, err := netip.ParseAddr(v.String())
		return err == nil && a.Is4()
	},
	"ipv6": func(v reflect.Value, _ rule) bool {
		a, err := netip.ParseAddr(v.String())
		return err == nil && a.Is6() && !a.Is4In6()
	},
	"hostname": func(v reflect.Value, _ rule) bool { return IsHostname(v.String()) },
	"alpha":    func(v reflect.Value, _ rule) bool { return allASCII(v.String(), isLetter) },
	"alphanum": func(v reflect.Value, _ rule) bool {
		return allASCII(v.String(), func(c byte) bool { return isLetter(c) || isDigit(c) })
	},
	"numeric": func(v reflect.Value, _ rule) bool {
		s := strings.TrimLeft(v.String(), "+-")
		if len(s) < len(v.String())-1 {
			return false
		}
		return allASCII(s, isDigit)
	},
	"lowercase": func(v reflect.Value, _ rule) bool {
		s := v.String()
		return s != "" && s == strings.ToLower(s)
	},
	"uppercase": func(v reflect.Value, _ rule) bool {
		s := v.String()
		return s != "" && s == strings.ToUpper(s)
	},
	"slug": func(v reflect.Value, _ rule) bool { return IsSlug(v.String()) },
	"email": func(v reflect.Value, _ rule) bool {
		s := v.String()
		a, err := mail.ParseAddress(s)
		return err == nil && a.Name == "" && a.Address == s
	},
	"url": func(v reflect.Value, _ rule) bool {
		u, err := url.Parse(v.String())
		return err == nil && u.Scheme != "" && u.Host != ""
	},
	"uuid": func(v reflect.Value, _ rule) bool { return IsUUID(v.String()) },
}

// IsSlug reports whether s is lower-case words of ASCII letters and digits
// joined by single hyphens: "getting-started", "v2".
func IsSlug(s string) bool {
	if s == "" || s[0] == '-' || s[len(s)-1] == '-' || strings.Contains(s, "--") {
		return false
	}
	return allASCII(s, func(c byte) bool { return 'a' <= c && c <= 'z' || isDigit(c) || c == '-' })
}

// IsHostname reports whether s is a DNS host name as RFC 1123 has it: dot-
// separated labels of 1 to 63 letters, digits and inner hyphens, 253 characters
// at most; a trailing dot is allowed.
func IsHostname(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		if !allASCII(label, func(c byte) bool { return isLetter(c) || isDigit(c) || c == '-' }) {
			return false
		}
	}
	return true
}

// IsUUID reports whether s is a UUID in its canonical 8-4-4-4-12 form.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}
