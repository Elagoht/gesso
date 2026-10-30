package gesso

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/gesso/date"
)

type dualIn struct {
	Title string               `json:"title" form:"title" validate:"required"`
	Count Optional[int64]      `json:"count,omitzero" form:"count"`
	Day   Optional[*date.Date] `json:"day,omitzero" form:"day"`
	Note  Optional[string]     `json:"note,omitzero" form:"note"`
	Tags  []string             `json:"tags" form:"tags"`
	Cover *File                `json:"-" form:"cover" accept:"image/png" maxsize:"1MiB"`
}

type dualOut struct {
	Title     string `json:"title"`
	Count     string `json:"count"`
	Day       string `json:"day"`
	Note      string `json:"note"`
	Tags      int    `json:"tags"`
	CoverSize int64  `json:"coverSize"`
}

func describeOpt[T any](o Optional[T], show func(T) string) string {
	switch {
	case !o.Set:
		return "unset"
	case o.Null:
		return "null"
	}
	return show(o.Value)
}

func TestDualInput(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		Post(r, "/things", func(_ context.Context, in dualIn) (dualOut, error) {
			out := dualOut{Title: in.Title, Tags: len(in.Tags)}
			out.Count = describeOpt(in.Count, func(n int64) string { return "n" + string(rune('0'+n)) })
			out.Day = describeOpt(in.Day, func(d *date.Date) string { return d.String() })
			out.Note = describeOpt(in.Note, func(s string) string { return "'" + s + "'" })
			if in.Cover != nil {
				out.CoverSize = in.Cover.Size
			}
			return out, nil
		})
	}))
	got := func(rec *httptest.ResponseRecorder) dualOut {
		t.Helper()
		var out dualOut
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	// JSON, as before.
	out := got(do(a, "POST", "/things", "application/json", `{"title":"a","count":3,"day":null,"tags":["x","y"]}`))
	if out != (dualOut{Title: "a", Count: "n3", Day: "null", Note: "unset", Tags: 2}) {
		t.Fatalf("json: %+v", out)
	}
	// The same fields as a form, with a file.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("title", "b")
	_ = w.WriteField("count", "")
	_ = w.WriteField("day", "2026-10-30")
	_ = w.WriteField("note", "")
	_ = w.WriteField("tags", "x")
	part, _ := w.CreateFormFile("cover", "c.png")
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\n0000"))
	_ = w.Close()
	out = got(do(a, "POST", "/things", w.FormDataContentType(), body.String()))
	if out != (dualOut{Title: "b", Count: "null", Day: "2026-10-30", Note: "''", Tags: 1, CoverSize: 12}) {
		t.Fatalf("form: %+v", out)
	}
	// A JSON body cannot carry the file.
	if rec := do(a, "POST", "/things", "application/json", `{"title":"c","cover":"x"}`); rec.Code != 400 {
		t.Fatalf("a file in JSON: %d %s", rec.Code, rec.Body)
	}
	// Validation holds either way.
	body.Reset()
	w = multipart.NewWriter(&body)
	_ = w.WriteField("count", "2")
	_ = w.Close()
	if rec := do(a, "POST", "/things", w.FormDataContentType(), body.String()); rec.Code != 400 || !strings.Contains(rec.Body.String(), "title") {
		t.Fatalf("a form without title: %d %s", rec.Code, rec.Body)
	}
}

func TestDualInputDocument(t *testing.T) {
	a := mustNew(t, Config{OpenAPI: &OpenAPIConfig{Title: "T"}}, moduleFunc(func(r *Router) {
		Post(r, "/things", func(context.Context, dualIn) (Empty, error) { return Empty{}, nil })
	}))
	var doc oaDoc
	if err := json.Unmarshal(a.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	c := doc.Paths["/things"]["post"].RequestBody.Content
	if _, ok := c["application/json"]; !ok {
		t.Fatal("no JSON body")
	}
	if form, ok := c["multipart/form-data"]; !ok || form.Schema.Properties["cover"] == nil {
		t.Fatalf("no form body with the file: %+v", c)
	}
}
