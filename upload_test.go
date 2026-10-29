package gesso

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Elagoht/gesso/date"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 2000)...)

type uploadIn struct {
	Title  string   `form:"title" validate:"required,max=20"`
	Tags   []string `form:"tag"`
	Avatar *File    `form:"avatar" accept:"image/png,image/jpeg" maxsize:"1KiB" validate:"required"`
	Extra  []*File  `form:"extra" accept:"image/*"`
}

type uploadOut struct {
	Title    string   `json:"title"`
	Tags     []string `json:"tags"`
	Filename string   `json:"filename"`
	Type     string   `json:"type"`
	Size     int64    `json:"size"`
	Head     string   `json:"head"`
	Extra    int      `json:"extra"`
}

var upload = Endpoint[uploadIn, uploadOut]{Method: "POST", Pattern: "/upload"}

func uploadApp(t *testing.T) *App {
	return mustNew(t, Config{OpenAPI: &OpenAPIConfig{Title: "U", Version: "1"}}, moduleFunc(func(r *Router) {
		upload.Handle(r, func(_ context.Context, in uploadIn) (uploadOut, error) {
			rc, err := in.Avatar.Open()
			if err != nil {
				return uploadOut{}, err
			}
			defer rc.Close()
			head := make([]byte, 4)
			_, _ = io.ReadFull(rc, head)
			return uploadOut{Title: in.Title, Tags: in.Tags, Filename: in.Avatar.Filename, Type: in.Avatar.ContentType,
				Size: in.Avatar.Size, Head: string(head[1:]), Extra: len(in.Extra)}, nil
		}, MaxBodyBytes(4<<10))
	}))
}

type part struct {
	field, filename, claimed string
	data                     []byte
}

func multipartBody(t *testing.T, parts ...part) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.filename == "" {
			_ = mw.WriteField(p.field, string(p.data))
			continue
		}
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{`form-data; name="` + p.field + `"; filename="` + p.filename + `"`}
		h["Content-Type"] = []string{p.claimed}
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(p.data)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func postForm(a *App, body io.Reader, ctype string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/upload", body)
	r.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestUpload(t *testing.T) {
	a := uploadApp(t)
	body, ctype := multipartBody(t,
		part{field: "title", data: []byte("Me")},
		part{field: "tag", data: []byte("a")}, part{field: "tag", data: []byte("b")},
		// The client says text and names a path: the bytes say PNG, and only the
		// name is kept.
		part{field: "avatar", filename: `..\..\windows\me.png`, claimed: "text/plain", data: pngBytes[:700]},
		part{field: "extra", filename: "x.png", claimed: "image/png", data: pngBytes[:20]},
	)
	w := postForm(a, body, ctype)
	var out uploadOut
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.Title != "Me" || strings.Join(out.Tags, ",") != "a,b" || out.Filename != "me.png" ||
		out.Type != "image/png" || out.Size != 700 || out.Head != "PNG" || out.Extra != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestUploadRefusals(t *testing.T) {
	a := uploadApp(t)
	cases := map[string]struct {
		parts []part
		code  string
		field string
	}{
		"a text file as an image": {[]part{{field: "title", data: []byte("x")}, {field: "avatar", filename: "a.png", claimed: "image/png", data: []byte("hello, not a picture")}}, "file_type", "avatar"},
		"too large a file":        {[]part{{field: "title", data: []byte("x")}, {field: "avatar", filename: "a.png", claimed: "image/png", data: pngBytes[:1500]}}, "max_size", "avatar"},
		"no file":                 {[]part{{field: "title", data: []byte("x")}}, "required", "avatar"},
		"a field it has not":      {[]part{{field: "title", data: []byte("x")}, {field: "admin", data: []byte("true")}, {field: "avatar", filename: "a.png", claimed: "image/png", data: pngBytes[:20]}}, "unknown", "admin"},
		"two files in one":        {[]part{{field: "title", data: []byte("x")}, {field: "avatar", filename: "a.png", claimed: "image/png", data: pngBytes[:20]}, {field: "avatar", filename: "b.png", claimed: "image/png", data: pngBytes[:20]}}, "max", "avatar"},
		"a bad extra":             {[]part{{field: "title", data: []byte("x")}, {field: "avatar", filename: "a.png", data: pngBytes[:20]}, {field: "extra", filename: "e.txt", data: []byte("text")}}, "file_type", "extra[0]"},
	}
	for name, c := range cases {
		body, ctype := multipartBody(t, c.parts...)
		w := postForm(a, body, ctype)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"field":"`+c.field+`"`) || !strings.Contains(w.Body.String(), `"code":"`+c.code+`"`) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	body, ctype := multipartBody(t, part{field: "title", data: []byte("x")}, part{field: "avatar", filename: "a.png", data: bytes.Repeat(pngBytes, 10)})
	if w := postForm(a, body, ctype); w.Code != 413 {
		t.Errorf("past the route's MaxBody: %d %s", w.Code, w.Body)
	}
	if w := postForm(a, strings.NewReader(`{"title":"x"}`), "application/json"); w.Code != 415 {
		t.Errorf("JSON to a form: %d", w.Code)
	}
	if w := postForm(a, strings.NewReader("title=x"), "application/x-www-form-urlencoded"); w.Code != 415 {
		t.Errorf("an input with files takes multipart only: %d %s", w.Code, w.Body)
	}
}

// A Remote, or NewLocal, sends a form as multipart, files and all.
func TestUploadThroughEndpoint(t *testing.T) {
	a := uploadApp(t)
	srv := httptest.NewServer(a)
	defer srv.Close()
	for name, rm := range map[string]*Remote{"remote": NewRemote(srv.URL, nil), "local": NewLocal(a)} {
		out, err := upload.Call(context.Background(), rm, uploadIn{
			Title: "Hi", Tags: []string{"x"}, Avatar: NewFile("me.png", pngBytes[:100]),
			Extra: []*File{NewFile("1.png", pngBytes[:10]), NewFile("2.png", pngBytes[:10])},
		})
		if err != nil || out.Filename != "me.png" || out.Size != 100 || out.Extra != 2 || out.Tags[0] != "x" {
			t.Fatalf("%s: %+v %v", name, out, err)
		}
	}
}

type mixedForm struct {
	Name string `form:"name"`
	Body string `json:"body"`
}

func TestUploadPlanning(t *testing.T) {
	_, err := New(Config{Logger: quiet()}, moduleFunc(func(r *Router) {
		r.Post("/x", Handle(func(context.Context, mixedForm) (Empty, error) { return Empty{}, nil }))
	}))
	if err == nil || !strings.Contains(err.Error(), "a form and a JSON body") {
		t.Fatalf("got %v", err)
	}
	var doc oaDoc
	if err := json.Unmarshal(uploadApp(t).OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	body := doc.Paths["/upload"]["post"].RequestBody
	s := body.Content["multipart/form-data"].Schema
	if !body.Required || s.Properties["avatar"].Format != "binary" || s.Properties["extra"].Items.Format != "binary" ||
		*s.Properties["title"].MaxLength != 20 || strings.Join(s.Required, ",") != "avatar,title" {
		t.Fatalf("%+v", s)
	}
	if _, ok := body.Content["application/json"]; ok {
		t.Fatal("a form is not JSON")
	}
	_ = http.MethodPost
}

type textForm struct {
	Name string `form:"name" validate:"required"`
}

// A form with no file is read urlencoded too, and whole into memory, so at
// most 1 MiB of it, whatever the route's MaxBodyBytes.
func TestUrlencodedForm(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Post("/t", Handle(func(_ context.Context, in textForm) (whoOut, error) { return whoOut{Subject: in.Name}, nil }, MaxBodyBytes(64<<20)))
	}))
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/t", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := post("name=ada"); w.Code != 200 || !strings.Contains(w.Body.String(), `"ada"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post("name=" + strings.Repeat("a", 2<<20)); w.Code != 413 {
		t.Fatalf("2 MiB urlencoded on a 64 MiB route: %d", w.Code)
	}
}

type Owner struct {
	UID string `path:"uid"`
}

type renameIn struct {
	*Owner
	Name string `json:"name"`
}

// An embedded pointer's path field would be filled from the body: refused.
func TestEmbeddedPointerWithParams(t *testing.T) {
	_, err := New(Config{Logger: quiet()}, moduleFunc(func(r *Router) {
		r.Post("/users/{uid}/rename", Handle(func(context.Context, renameIn) (Empty, error) { return Empty{}, nil }))
	}))
	if err == nil || !strings.Contains(err.Error(), "not a pointer to it") {
		t.Fatalf("got %v", err)
	}
}

type ownerBody struct {
	UID string `path:"uid"`
}

type nestedIn struct {
	Who  ownerBody `json:"who"`
	Name string    `json:"name"`
}

// A path, query, header or form tag inside a body field would be filled from
// the body: refused, however deep.
func TestNestedParamTagsRefused(t *testing.T) {
	_, err := New(Config{Logger: quiet()}, moduleFunc(func(r *Router) {
		r.Post("/users/{uid}", Handle(func(context.Context, nestedIn) (Empty, error) { return Empty{}, nil }))
	}))
	if err == nil || !strings.Contains(err.Error(), "is read from the body, but has path") {
		t.Fatalf("got %v", err)
	}
}

type hiddenOwner struct {
	UID string `path:"uid"`
}

type hiddenIn struct {
	hiddenOwner `json:"owner"`
	Name        string `json:"name"`
}

// Unexported, embedded, with a json tag: encoding/json fills it from the body,
// so its path field is refused too.
func TestUnexportedEmbeddedParamTagsRefused(t *testing.T) {
	_, err := New(Config{Logger: quiet()}, moduleFunc(func(r *Router) {
		r.Post("/users/{uid}", Handle(func(context.Context, hiddenIn) (Empty, error) { return Empty{}, nil }))
	}))
	if err == nil || !strings.Contains(err.Error(), "is read from the body") {
		t.Fatalf("got %v", err)
	}
}

// Past FormMemory an upload goes to a temporary file, and is read the same.
func TestFormMemory(t *testing.T) {
	a := mustNew(t, Config{FormMemory: 1 << 10}, moduleFunc(func(r *Router) {
		upload.Handle(r, func(_ context.Context, in uploadIn) (uploadOut, error) {
			return uploadOut{Size: in.Avatar.Size, Type: in.Avatar.ContentType}, nil
		}, MaxBodyBytes(64<<10))
	}))
	body, ctype := multipartBody(t, part{field: "title", data: []byte("x")},
		part{field: "avatar", filename: "a.png", data: append(pngBytes[:8:8], make([]byte, 900)...)})
	w := postForm(a, body, ctype)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"size":908`) || !strings.Contains(w.Body.String(), "image/png") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

type dateQuery struct {
	Day date.Date `query:"day"`
}

// A type's own parse error may quote the client's value: it is cut short.
func TestParseMessagesAreShort(t *testing.T) {
	a := mustNew(t, Config{}, moduleFunc(func(r *Router) {
		r.Get("/d", Handle(func(context.Context, dateQuery) (Empty, error) { return Empty{}, nil }))
	}))
	w := send(a, "GET", "/d?day="+strings.Repeat("A", 300), nil)
	if w.Code != 400 || strings.Contains(w.Body.String(), strings.Repeat("A", 150)) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
