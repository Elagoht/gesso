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
