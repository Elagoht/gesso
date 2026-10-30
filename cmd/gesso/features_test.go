package main

// eventsModel is the model TestNew gives a new project: every option and
// directive gen knows, a keyword column (order), and every type.
const eventsModel = `package models

import (
	"time"

	"github.com/Elagoht/gesso/date"
)

//gesso:table events
//gesso:sortable
//gesso:write signedIn
type Event struct {
	ID       int64
	Title    string     ` + "`" + `gesso:"listable,orderable,collapse,max=200"` + "`" + `
	Slug     string     ` + "`" + `gesso:"key,slug,max=100"` + "`" + `
	Body     string     ` + "`" + `gesso:"text,optional"` + "`" + `
	Order    int64      ` + "`" + `gesso:"listable,orderable"` + "`" + `
	Day      *date.Date ` + "`" + `gesso:"listable,default=@today"` + "`" + `
	Status   string     ` + "`" + `gesso:"listable,default=draft,oneof=draft live"` + "`" + `
	StartsAt *time.Time ` + "`" + `gesso:"optional"` + "`" + `
	Price    float64    ` + "`" + `gesso:"optional"` + "`" + `
	Host     string     ` + "`" + `gesso:"listable,filterable,default=@user,max=100"` + "`" + `
	Notes    string     ` + "`" + `gesso:"guard=admin,max=500"` + "`" + `
	Public   bool       ` + "`" + `gesso:"gate=admin"` + "`" + `
	Cover    string     ` + "`" + `gesso:"image,listable,routes,fit=200x100,format=jpeg,maxsize=2MiB"` + "`" + `
	Flyer    string     ` + "`" + `gesso:"file,accept=application/pdf,maxsize=1MiB"` + "`" + `
}
`

// eventsHook is the app's own code: gen finds the hooks and calls them.
const eventsHook = `package events

import (
	"context"
	"strings"

	"github.com/Elagoht/gesso"

	"example.com/app/internal/events/models"
)

func (m *Module) BeforeCreate(ctx context.Context, e *models.Event) error {
	if e.Price == 0 {
		e.Price = 1
	}
	return nil
}

func (m *Module) BeforeUpdate(ctx context.Context, e *models.Event) error {
	e.Title = strings.TrimSpace(e.Title)
	return nil
}

// ProcessFlyer is a scanner's turn: a file it refuses is never kept.
func (m *Module) ProcessFlyer(ctx context.Context, data []byte, ext string) ([]byte, string, error) {
	if strings.Contains(string(data), "EICAR") {
		return nil, "", gesso.Unprocessable("infected", "the file carries a virus")
	}
	return data, ext, nil
}

func (m *Module) BeforeDelete(ctx context.Context, e models.Event) error {
	if e.Order == 99 {
		return gesso.Unprocessable("in_use", "the event is in use")
	}
	return nil
}
`

// featuresTest drives the generated events module over HTTP, on the
// project's database, as an admin (ada), a writer (bob) and anyone.
const featuresTest = `package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/auth"

	"example.com/app/internal/events/models"
	"example.com/app/internal/users"
)

type caller struct {
	t     *testing.T
	app   *gesso.App
	token string
}

func (c caller) do(method, path, body string) (int, string) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.app.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// upload sends data as the form's file, and answers the status, the body and
// the content type.
func (c caller) upload(path, name string, data []byte) (int, string) {
	c.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", name)
	_, _ = part.Write(data)
	_ = w.Close()
	req := httptest.NewRequest("PUT", path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.app.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// form sends fields and files as one multipart form.
func (c caller) form(method, path string, fields map[string]string, files map[string][]byte) (int, string) {
	c.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	for k, data := range files {
		part, _ := w.CreateFormFile(k, k+".bin")
		_, _ = part.Write(data)
	}
	_ = w.Close()
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.app.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// fetch answers path's status, type and body.
func (c caller) fetch(path string) (int, string, []byte) {
	req := httptest.NewRequest("GET", path, nil)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	rec := httptest.NewRecorder()
	c.app.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Content-Type") + " " + rec.Header().Get("Content-Disposition"), rec.Body.Bytes()
}

func pngOf(w, h int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

func size(t *testing.T, data []byte) string {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return format + " " + strconv.Itoa(cfg.Width) + "x" + strconv.Itoa(cfg.Height)
}

func urlIn(t *testing.T, body, field string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	var u string
	if err := json.Unmarshal(m[field], &u); err != nil || u == "" {
		t.Fatalf("no %s in %s", field, body)
	}
	return u
}

func files(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func (c caller) list(query string) ([]string, string) {
	c.t.Helper()
	code, body := c.do("GET", "/events"+query, "")
	if code != 200 {
		c.t.Fatalf("GET /events%s: %d %s", query, code, body)
	}
	var page gesso.Page[models.ResEventItem]
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		c.t.Fatal(err)
	}
	var slugs []string
	for _, e := range page.Items {
		slugs = append(slugs, e.Slug)
	}
	return slugs, page.Next
}

func (c caller) want(method, path, body string, status int, contains ...string) string {
	c.t.Helper()
	code, got := c.do(method, path, body)
	if code != status {
		c.t.Fatalf("%s %s %s: %d, want %d: %s", method, path, body, code, status, got)
	}
	for _, s := range contains {
		if strings.HasPrefix(s, "!") {
			if strings.Contains(got, s[1:]) {
				c.t.Fatalf("%s %s: %s has %s", method, path, got, s[1:])
			}
		} else if !strings.Contains(got, s) {
			c.t.Fatalf("%s %s: %s lacks %s", method, path, got, s)
		}
	}
	return got
}

func TestEvents(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Setenv("DATABASE_URL", filepath.Join(t.TempDir(), "events.db"))
	}
	uploads := t.TempDir()
	t.Setenv("UPLOADS_DIR", uploads)
	ctx := context.Background()
	app, err := newApp(gesso.Env{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(ctx) })
	u, _ := gesso.ModuleOf[*users.Module](app)
	login := func(name string, roles ...string) caller {
		if _, err := u.Users().Create(ctx, name, "a long password", roles...); err != nil {
			t.Fatal(err)
		}
		s, err := auth.Login.Call(ctx, gesso.NewLocal(app), auth.ReqLogin{Username: name, Password: "a long password"})
		if err != nil {
			t.Fatal(err)
		}
		return caller{t, app, s.Token}
	}
	anyone, ada, bob := caller{t: t, app: app}, login("ada", "admin"), login("bob")

	anyone.want("POST", "/events", ` + "`" + `{"title":"Jam","slug":"jam","day":"2026-11-01"}` + "`" + `, 401)
	bob.want("POST", "/events", ` + "`" + `{"title":"Jam","slug":"jam","day":"2026-11-01","public":true}` + "`" + `, 201,
		` + "`" + `"host":"bob"` + "`" + `, ` + "`" + `"status":"draft"` + "`" + `, ` + "`" + `"price":1` + "`" + `, "!notes")
	bob.want("POST", "/events", ` + "`" + `{"title":"Sneaky","slug":"sneaky","day":"2026-11-01","notes":"x"}` + "`" + `, 403)
	ada.want("POST", "/events", ` + "`" + `{"title":"Talk","slug":"talk","notes":"private","status":"live"}` + "`" + `, 201,
		` + "`" + `"notes":"private"` + "`" + `, ` + "`" + `"day":"` + "`" + `+time.Now().Format("2006-01-02")+` + "`" + `"` + "`" + `, ` + "`" + `"status":"live"` + "`" + `)
	bob.want("POST", "/events", ` + "`" + `{"title":"Bad","slug":"bad","status":"gone"}` + "`" + `, 400, "oneof")
	bob.want("POST", "/events", ` + "`" + `{"title":"Walk","slug":"walk","day":"2026-11-03","public":true,"order":5,"host":"x","price":9.5,"startsAt":"2026-11-03T10:00:00Z"}` + "`" + `, 201)
	bob.want("POST", "/events", ` + "`" + `{"title":"Again","slug":"jam","day":"2026-11-01"}` + "`" + `, 409, ` + "`" + `"field":"slug"` + "`" + `)
	bob.want("POST", "/events", ` + "`" + `{"title":"Order","slug":"order","day":"2026-11-01","public":true}` + "`" + `, 201)
	bob.want("PUT", "/events/order", ` + "`" + `{"title":"Order 2","slug":"order","day":"2026-11-01","public":true}` + "`" + `, 200, "Order 2")
	bob.want("DELETE", "/events/order", "", 204)

	// The gate: Talk is not public, and only an admin sees it.
	if got, _ := anyone.list(""); !slices.Equal(got, []string{"jam", "walk"}) {
		t.Fatalf("anyone lists %v", got)
	}
	if got, _ := ada.list(""); !slices.Equal(got, []string{"jam", "talk", "walk"}) {
		t.Fatalf("ada lists %v", got)
	}
	bob.want("GET", "/events/talk", "", 404)
	bob.want("PATCH", "/events/talk", ` + "`" + `{"title":"Mine"}` + "`" + `, 404)
	ada.want("GET", "/events/talk", "", 200, ` + "`" + `"notes":"private"` + "`" + `)

	// The list answers the listable fields; a record, all it may see.
	anyone.want("GET", "/events", "", 200, ` + "`" + `"order":` + "`" + `, "!body", "!notes", "!price")
	bob.want("GET", "/events/walk", "", 200, ` + "`" + `"price":9.5` + "`" + `, "!notes")

	// Orders, a filter, pages.
	if got, _ := ada.list("?order=-title"); !slices.Equal(got, []string{"walk", "talk", "jam"}) {
		t.Fatalf("by title, down: %v", got)
	}
	if got, _ := ada.list("?order=-order"); got[0] != "walk" {
		t.Fatalf("by order, down: %v", got)
	}
	if got, _ := anyone.list("?host=x"); !slices.Equal(got, []string{"walk"}) {
		t.Fatalf("hosted by x: %v", got)
	}
	anyone.want("GET", "/events?order=body", "", 400)
	first, next := ada.list("?order=title&take=2")
	second, last := ada.list("?order=title&take=2&after=" + next)
	if !slices.Equal(append(first, second...), []string{"jam", "talk", "walk"}) || next == "" || last != "" {
		t.Fatalf("pages: %v %v %q", first, second, last)
	}
	ada.want("GET", "/events?take=1&after="+next, "", 400)

	// The client's order: bob moves what he sees, and Talk, which he does not,
	// keeps its place.
	bob.want("PATCH", "/events", ` + "`" + `{"order":["walk","jam"]}` + "`" + `, 204)
	if got, _ := ada.list(""); !slices.Equal(got, []string{"walk", "talk", "jam"}) {
		t.Fatalf("after bob's reorder: %v", got)
	}
	bob.want("PATCH", "/events", ` + "`" + `{"order":["walk"]}` + "`" + `, 422)
	ada.want("PATCH", "/events", ` + "`" + `{"order":["jam","talk","walk"]}` + "`" + `, 204)
	if got, _ := anyone.list(""); !slices.Equal(got, []string{"jam", "walk"}) {
		t.Fatalf("after ada's reorder: %v", got)
	}

	// A guarded field: bob may not write it, and his PUT keeps it.
	ada.want("PATCH", "/events/walk", ` + "`" + `{"notes":"kept"}` + "`" + `, 200, ` + "`" + `"notes":"kept"` + "`" + `)
	bob.want("PATCH", "/events/walk", ` + "`" + `{"notes":"mine"}` + "`" + `, 403)
	bob.want("PUT", "/events/walk", ` + "`" + `{"title":"  Walk 2 ","slug":"walk","day":"2026-11-03","public":true}` + "`" + `, 200, ` + "`" + `"title":"Walk 2"` + "`" + `, "!notes")
	ada.want("GET", "/events/walk", "", 200, ` + "`" + `"notes":"kept"` + "`" + `, ` + "`" + `"title":"Walk 2"` + "`" + `, ` + "`" + `"host":"bob"` + "`" + `, ` + "`" + `"status":"draft"` + "`" + `)
	ada.want("PATCH", "/events/talk", ` + "`" + `{"order":99}` + "`" + `, 200, ` + "`" + `"host":"ada"` + "`" + `)
	ada.want("DELETE", "/events/talk", "", 422, "in_use")

	// Files: an image shrunk and kept as JPEG, a PDF downloaded, never an SVG.
	if code, body := anyone.upload("/events/walk/cover", "c.png", pngOf(400, 300)); code != 401 {
		t.Fatalf("anyone uploads: %d %s", code, body)
	}
	if code, body := bob.upload("/events/walk/cover", "x.svg", []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")); code != 400 {
		t.Fatalf("an svg cover: %d %s", code, body)
	}
	code, body := bob.upload("/events/walk/cover", "c.png", pngOf(400, 300))
	if code != 200 {
		t.Fatalf("cover: %d %s", code, body)
	}
	cover := urlIn(t, body, "cover")
	if code, typ, data := anyone.fetch(cover); code != 200 || !strings.HasPrefix(typ, "image/jpeg") || size(t, data) != "jpeg 133x100" {
		t.Fatalf("the cover: %d %s %s", code, typ, size(t, data))
	}
	anyone.want("GET", "/events", "", 200, ` + "`" + `"cover":"/events/walk/files/cover?v=` + "`" + `)
	// A file without routes comes only with the record, in a form.
	if code, body := bob.upload("/events/walk/flyer", "f.pdf", []byte("%PDF-1.4\n%flyer\n")); code != 404 && code != 405 {
		t.Fatalf("a flyer has no route of its own: %d %s", code, body)
	}
	if code, body := bob.form("PATCH", "/events/walk", nil, map[string][]byte{"flyer": []byte("%PDF-1.4\n%flyer\n")}); code != 200 || !strings.Contains(body, ` + "`" + `"flyer":"/events/walk/files/flyer?v=` + "`" + `) {
		t.Fatalf("flyer: %d %s", code, body)
	}
	if code, body := bob.form("PATCH", "/events/walk", nil, map[string][]byte{"flyer": []byte("%PDF-1.4\nEICAR\n")}); code != 422 {
		t.Fatalf("an infected flyer: %d %s", code, body)
	}
	if code, body := bob.form("PATCH", "/events/walk", nil, map[string][]byte{"flyer": pngOf(10, 10)}); code != 400 {
		t.Fatalf("a png as the flyer: %d %s", code, body)
	}
	anyone.want("GET", "/events/walk/files/nothing", "", 400)
	if code, typ, _ := anyone.fetch("/events/walk/files/flyer"); code != 200 || !strings.Contains(typ, "application/pdf") || !strings.Contains(typ, "attachment") {
		t.Fatalf("the flyer: %d %s", code, typ)
	}
	bob.want("PUT", "/events/walk", ` + "`" + `{"title":"Walk 3","slug":"walk","day":"2026-11-03","public":true}` + "`" + `, 200, ` + "`" + `"cover":"/events/walk/files/cover` + "`" + `)
	// Sent empty in a form, a file goes.
	if code, body := bob.form("PATCH", "/events/walk", map[string]string{"flyer": ""}, nil); code != 200 || strings.Contains(body, ` + "`" + `"flyer"` + "`" + `) {
		t.Fatalf("an empty flyer: %d %s", code, body)
	}
	anyone.want("GET", "/events/walk/files/flyer", "", 404)
	bob.want("DELETE", "/events/walk/cover", "", 204)
	anyone.want("GET", "/events/walk/files/cover", "", 404)
	if code, body := bob.upload("/events/walk/cover", "c.png", pngOf(400, 300)); code != 200 {
		t.Fatalf("the cover again: %d %s", code, body)
	}
	if n := files(t, uploads); n != 1 {
		t.Fatalf("files kept: %d", n)
	}

	// A record and its files in one form: all written, or nothing.
	code, body = bob.form("POST", "/events", map[string]string{"title": "Fair", "slug": "fair", "day": "2026-11-05", "public": "true", "order": "2"},
		map[string][]byte{"cover": pngOf(400, 300)})
	if code != 201 || !strings.Contains(body, ` + "`" + `"cover":"/events/fair/files/cover?v=` + "`" + `) || !strings.Contains(body, ` + "`" + `"host":"bob"` + "`" + `) {
		t.Fatalf("a form with a cover: %d %s", code, body)
	}
	firstCover := urlIn(t, body, "cover")
	if code, body := bob.form("POST", "/events", map[string]string{"title": "Bad", "slug": "bad"}, map[string][]byte{"cover": []byte("<svg/>")}); code != 400 {
		t.Fatalf("a form with an svg: %d %s", code, body)
	}
	anyone.want("GET", "/events/bad", "", 404)
	if n := files(t, uploads); n != 2 {
		t.Fatalf("files after the forms: %d", n)
	}
	code, body = bob.form("PATCH", "/events/fair", map[string]string{"title": "  Fair   2 "}, map[string][]byte{"cover": pngOf(50, 50)})
	if code != 200 || !strings.Contains(body, ` + "`" + `"title":"Fair 2"` + "`" + `) || urlIn(t, body, "cover") == firstCover || !strings.Contains(body, ` + "`" + `"order":2` + "`" + `) {
		t.Fatalf("a patch with a cover: %d %s", code, body)
	}
	if n := files(t, uploads); n != 2 {
		t.Fatalf("the old cover stays: %d files", n)
	}
	if code, body := bob.form("PATCH", "/events/fair", map[string]string{"price": ""}, nil); code != 400 {
		t.Fatalf("an empty number clears it, and price may not be null: %d %s", code, body)
	}

	// Users: the model's own, an avatar of one's own, admins over the rest.
	bob.want("GET", "/auth/me", "", 200, ` + "`" + `"username":"bob"` + "`" + `, ` + "`" + `"roles":[]` + "`" + `)
	bob.want("PATCH", "/auth/me", ` + "`" + `{"name":"  Bob   Builder "}` + "`" + `, 200, ` + "`" + `"name":"Bob Builder"` + "`" + `)
	bob.want("PATCH", "/auth/me", ` + "`" + `{"roles":["admin"]}` + "`" + `, 400)
	if code, _ := bob.upload("/auth/me/avatar", "a.png", pngOf(300, 200)); code != 404 && code != 405 {
		t.Fatalf("an avatar without routes has none of its own: %d", code)
	}
	code, body = bob.form("PATCH", "/auth/me", nil, map[string][]byte{"avatar": pngOf(300, 200)})
	if code != 200 {
		t.Fatalf("avatar: %d %s", code, body)
	}
	if code, _, data := anyone.fetch(urlIn(t, body, "avatar")); code != 200 || size(t, data) != "png 64x64" {
		t.Fatalf("the avatar: %d", code)
	}
	bob.want("GET", "/users", "", 403)
	ada.want("GET", "/users", "", 200, ` + "`" + `"username":"bob"` + "`" + `, ` + "`" + `"avatar":"/users/bob/files/avatar` + "`" + `, "!password")
	ada.want("POST", "/users", ` + "`" + `{"username":"Carol","password":"carol password","roles":["editor"]}` + "`" + `, 201, ` + "`" + `"username":"carol"` + "`" + `, "!password")
	ada.want("PUT", "/users/carol/password", ` + "`" + `{"password":"another password"}` + "`" + `, 204)
	s, err := auth.Login.Call(ctx, gesso.NewLocal(app), auth.ReqLogin{Username: "carol", Password: "another password"})
	if err != nil {
		t.Fatal(err)
	}
	caller{t, app, s.Token}.want("GET", "/auth/me", "", 200, ` + "`" + `"roles":["editor"]` + "`" + `)
	if code, body := bob.form("PATCH", "/auth/me", map[string]string{"avatar": ""}, nil); code != 200 || strings.Contains(body, ` + "`" + `"avatar"` + "`" + `) {
		t.Fatalf("an empty avatar: %d %s", code, body)
	}
	code, body = bob.form("PATCH", "/auth/me", map[string]string{"name": "Bob"}, map[string][]byte{"avatar": pngOf(80, 80)})
	if code != 200 || !strings.Contains(body, ` + "`" + `"name":"Bob"` + "`" + `) || !strings.Contains(body, ` + "`" + `"avatar":"/users/bob/files/avatar` + "`" + `) {
		t.Fatalf("name and avatar in one form: %d %s", code, body)
	}
	if code, body := bob.form("PATCH", "/auth/me", map[string]string{"roles": "admin"}, nil); code != 400 {
		t.Fatalf("roles in a form: %d %s", code, body)
	}
	if code, _ := bob.form("PATCH", "/auth/me", map[string]string{"avatar": ""}, nil); code != 200 {
		t.Fatal("the avatar goes")
	}

	bob.want("DELETE", "/events/jam", "", 204)
	anyone.want("GET", "/events/jam", "", 404)
	bob.want("DELETE", "/events/jam", "", 404)
	bob.want("DELETE", "/events/walk", "", 204)
	bob.want("DELETE", "/events/fair", "", 204)
	if n := files(t, uploads); n != 0 {
		t.Fatalf("a record's files go with it: %d left", n)
	}
}
`
