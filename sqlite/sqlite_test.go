package sqlite_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/auth"
	"github.com/Elagoht/gesso/sqlite"
)

type notesIn struct {
	Title string `json:"title" validate:"required"`
}

type noteOut struct {
	Title  string `json:"title"`
	Author string `json:"author"`
}

// notes is a module as gesso gen writes one: it takes the database in Init.
type notes struct{ app *gesso.App }

func (n *notes) Init(app *gesso.App) error {
	n.app = app
	app.Migrations(fstest.MapFS{
		"1_notes.up.sql":   {Data: []byte("CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL UNIQUE);")},
		"1_notes.down.sql": {Data: []byte("DROP TABLE notes;")},
	})
	app.Constraints(map[string]string{"notes.title": "title"})
	return nil
}

func (n *notes) Routes(r *gesso.Router) {
	gesso.Post(r, "/notes", func(ctx context.Context, in notesIn) (noteOut, error) {
		id, _ := gesso.IdentityFrom(ctx)
		_, err := n.app.DB().ExecContext(ctx, "INSERT INTO notes (title) VALUES (?)", in.Title)
		return noteOut{Title: in.Title, Author: id.Name}, err
	}, gesso.Guards(gesso.RequireIdentity))
}

func TestDatabaseAndRegister(t *testing.T) {
	ctx := context.Background()
	app, err := gesso.New(gesso.Config{Database: sqlite.Open(filepath.Join(t.TempDir(), "app.db"))})
	if err != nil {
		t.Fatal(err)
	}
	// The module that needs a signed-in user comes first: the authenticator is
	// found when a request comes, not when routes are.
	if err := app.Register(&notes{}, auth.New()); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(ctx) })
	authn, _ := gesso.ModuleOf[*auth.Module](app)
	if _, err := authn.Users().(*auth.SQL).Create(ctx, "ada", "a long password"); err != nil {
		t.Fatal(err)
	}

	token := login(t, app)
	post := func(title string) (int, string) {
		req := httptest.NewRequest("POST", "/notes", strings.NewReader(`{"title":"`+title+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := post("first"); code != 200 || !strings.Contains(body, `"author":"ada"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := post("first"); code != 409 || !strings.Contains(body, `"field":"title"`) {
		t.Fatalf("a duplicate is the field's 409: %d %s", code, body)
	}
	if err := app.Register(&notes{}); err == nil {
		t.Fatal("a route registered twice is refused")
	}
}

func login(t *testing.T, app *gesso.App) string {
	t.Helper()
	s, err := auth.Login.Call(context.Background(), gesso.NewLocal(app), auth.ReqLogin{Username: "ada", Password: "a long password"})
	if err != nil {
		t.Fatal(err)
	}
	return s.Token
}
