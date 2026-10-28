package validate

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type address struct {
	City string `json:"city" validate:"required"`
}

type item struct {
	Name string `json:"name" validate:"required,max=3"`
}

type signup struct {
	Email    string   `json:"email" validate:"required,email"`
	Name     string   `json:"name" validate:"required,min=2,max=5"`
	Role     string   `json:"role" validate:"omitempty,oneof=admin user"`
	Age      *int     `json:"age" validate:"omitempty,min=18"`
	Site     string   `json:"site" validate:"omitempty,url"`
	ID       string   `query:"id" validate:"omitempty,uuid"`
	Tags     []string `json:"tags" validate:"max=2"`
	Address  *address `json:"address"`
	Items    []item   `json:"items"`
	internal string
}

func codes(errs []FieldError) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Field+":"+e.Code)
	}
	return out
}

func TestStructPasses(t *testing.T) {
	age := 20
	in := signup{Email: "a@b.co", Name: "Ada", Role: "admin", Age: &age, Site: "https://x.dev",
		ID: "123e4567-e89b-12d3-a456-426614174000", Address: &address{City: "İzmir"}, Items: []item{{Name: "abc"}},
		internal: "unexported fields are not checked"}
	if errs := Struct(context.Background(), in); errs != nil {
		t.Fatalf("want no errors, got %v", errs)
	}
}

func TestStructFailures(t *testing.T) {
	young := 12
	in := signup{
		Email: "Ada <a@b.co>", Name: "Ç", Role: "root", Age: &young, Site: "/relative",
		ID: "nope", Tags: []string{"a", "b", "c"}, Address: &address{}, Items: []item{{Name: "ok"}, {Name: "toolong"}},
	}
	got := codes(Struct(context.Background(), in))
	want := []string{
		"email:email", "name:min", "role:oneof", "age:min", "site:url", "id:uuid",
		"tags:max", "address.city:required", "items[1].name:max",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %v\nwant %v", got, want)
	}
}

func TestRequiredStopsFurtherRules(t *testing.T) {
	got := Struct(context.Background(), signup{Name: "Ada"})
	if len(got) != 1 || got[0].Field != "email" || got[0].Message != "is required" {
		t.Fatalf("got %v", got)
	}
}

func TestMessagesCountCharactersNotBytes(t *testing.T) {
	in := signup{Email: "a@b.co", Name: "ğğğğğğ"}
	got := Struct(context.Background(), in)
	if len(got) != 1 || got[0].Message != "must be at most 5 characters" || got[0].Params[0] != "5" {
		t.Fatalf("got %v", got)
	}
}

type window struct {
	From int `json:"from" validate:"min=0"`
	To   int `json:"to"`
}

func (w window) Validate(context.Context) []FieldError {
	if w.To < w.From {
		return []FieldError{{Field: "to", Code: "after_from", Message: "must not be before from"}}
	}
	return nil
}

func TestSelfValidatorRunsAfterTags(t *testing.T) {
	if got := codes(Struct(context.Background(), window{From: 5, To: 1})); !reflect.DeepEqual(got, []string{"to:after_from"}) {
		t.Fatalf("got %v", got)
	}
	// A failed tag hides the self check, which may rely on the tags.
	if got := codes(Struct(context.Background(), window{From: -1, To: -5})); !reflect.DeepEqual(got, []string{"from:min"}) {
		t.Fatalf("got %v", got)
	}
}

type badRule struct {
	A string `validate:"required,shout"`
}

type badParam struct {
	A bool `validate:"max=3"`
}

func TestCompileReportsBadTags(t *testing.T) {
	if err := Compile[badRule](); err == nil || !strings.Contains(err.Error(), `unknown rule "shout"`) {
		t.Fatalf("got %v", err)
	}
	if err := Compile[badParam](); err == nil || !strings.Contains(err.Error(), "does not apply to bool") {
		t.Fatalf("got %v", err)
	}
	if err := Compile[signup](); err != nil {
		t.Fatal(err)
	}
}

type node struct {
	Name     string  `json:"name" validate:"required"`
	Children []*node `json:"children"`
}

func TestRecursiveTypes(t *testing.T) {
	in := node{Name: "root", Children: []*node{{Name: "a", Children: []*node{{}}}}}
	if got := codes(Struct(context.Background(), in)); !reflect.DeepEqual(got, []string{"children[0].children[0].name:required"}) {
		t.Fatalf("got %v", got)
	}
}

func TestPointerAndNil(t *testing.T) {
	var nilIn *signup
	if Struct(context.Background(), nilIn) != nil {
		t.Fatal("nil pointer has nothing to check")
	}
	if got := Struct(context.Background(), &signup{Name: "Ada"}); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

// one runs a single field's tag over each value and reports which pass.
func one[T any](t *testing.T, tag string, values ...T) []bool {
	t.Helper()
	typ := reflect.StructOf([]reflect.StructField{{Name: "V", Type: reflect.TypeFor[T](), Tag: reflect.StructTag(`json:"v" validate:"` + tag + `"`)}})
	if _, err := planFor(typ); err != nil {
		t.Fatalf("%s: %v", tag, err)
	}
	var out []bool
	for _, v := range values {
		rv := reflect.New(typ).Elem()
		rv.Field(0).Set(reflect.ValueOf(v))
		p, _ := planFor(typ)
		var errs []FieldError
		p.check(rv, "", &errs)
		out = append(out, len(errs) == 0)
	}
	return out
}

func TestRules(t *testing.T) {
	cases := []struct {
		name string
		got  []bool
		want []bool
	}{
		{"gt int", one(t, "gt=3", 3, 4), []bool{false, true}},
		{"gte int", one(t, "gte=3", 2, 3), []bool{false, true}},
		{"lt string length", one(t, "lt=3", "ab", "abc"), []bool{true, false}},
		{"lte float", one(t, "lte=1.5", 1.5, 1.6), []bool{true, false}},
		{"eq string", one(t, "eq=yes", "yes", "no"), []bool{true, false}},
		{"ne int", one(t, "ne=0", 0, 7), []bool{false, true}},
		{"eq slice length", one(t, "eq=2", []int{1, 2}, []int{1}), []bool{true, false}},
		{"alpha", one(t, "alpha", "abcXYZ", "abc1", "ç"), []bool{true, false, false}},
		{"alphanum", one(t, "alphanum", "abc123", "abc-1"), []bool{true, false}},
		{"numeric", one(t, "numeric", "-42", "+7", "4.2", "--1", "-"), []bool{true, true, false, false, false}},
		{"lowercase", one(t, "lowercase", "çay 1", "Çay"), []bool{true, false}},
		{"uppercase", one(t, "uppercase", "ÇAY", "ÇAy"), []bool{true, false}},
		{"contains", one(t, "contains=@", "a@b", "ab"), []bool{true, false}},
		{"excludes", one(t, "excludes=/", "ab", "a/b"), []bool{true, false}},
		{"startswith", one(t, "startswith=gso_", "gso_x", "x_gso_"), []bool{true, false}},
		{"endswith", one(t, "endswith=.md", "a.md", "a.mdx"), []bool{true, false}},
		{"slug", one(t, "slug", "getting-started", "v2", "Getting", "a--b", "-a", "a-", "a_b"), []bool{true, true, false, false, false, false, false}},
		{"ip", one(t, "ip", "10.0.0.1", "::1", "10.0.0"), []bool{true, true, false}},
		{"ipv4", one(t, "ipv4", "10.0.0.1", "::1"), []bool{true, false}},
		{"ipv6", one(t, "ipv6", "::1", "10.0.0.1", "::ffff:10.0.0.1"), []bool{true, false, false}},
		{"hostname", one(t, "hostname", "api.example.com", "localhost", "a_b.com", "-a.com", "example.com."), []bool{true, true, false, false, true}},
		{"unique", one(t, "unique", []string{"a", "b"}, []string{"a", "a"}), []bool{true, false}},
		{"omitempty then slug", one(t, "omitempty,slug", "", "Bad"), []bool{true, false}},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

type dived struct {
	Tags  []string          `json:"tags" validate:"max=3,dive,required,max=4"`
	Slugs []string          `json:"slugs" validate:"unique,dive,slug"`
	Names map[string]string `json:"names" validate:"dive,required"`
}

func TestDive(t *testing.T) {
	in := dived{
		Tags:  []string{"go", "", "toolong"},
		Slugs: []string{"a", "B"},
		Names: map[string]string{"tr": "Merhaba", "en": ""},
	}
	got := codes(Struct(context.Background(), in))
	want := []string{"tags[1]:required", "tags[2]:max", "slugs[1]:slug", "names[en]:required"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// A rule before dive that fails stops the elements from being checked.
	in = dived{Tags: []string{"", "", "", ""}}
	if got := codes(Struct(context.Background(), in)); !reflect.DeepEqual(got, []string{"tags:max"}) {
		t.Fatalf("got %v", got)
	}
}

type badDive struct {
	N int `validate:"dive,required"`
}

type badAfterDive struct {
	Tags []string `validate:"dive,uuid=4"`
}

type badUnique struct {
	S string `validate:"unique"`
}

func TestCompileReportsBadNewRules(t *testing.T) {
	for _, err := range []error{Compile[badDive](), Compile[badAfterDive](), Compile[badUnique]()} {
		if err == nil {
			t.Error("a bad tag compiled")
		}
	}
	if err := Compile[dived](); err != nil {
		t.Fatal(err)
	}
}
