package sqlite_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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

func TestJWTSignIn(t *testing.T) {
	ctx := context.Background()
	weak, err := gesso.New(gesso.Config{Database: sqlite.Open(filepath.Join(t.TempDir(), "weak.db"))},
		auth.New(auth.Options{JWT: &auth.JWT{Secret: []byte("short")}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := weak.Start(ctx); err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("a weak secret starts: %v", err)
	}
	_ = weak.Shutdown(ctx)

	app, err := gesso.New(gesso.Config{Database: sqlite.Open(filepath.Join(t.TempDir(), "jwt.db"))},
		auth.New(auth.Options{JWT: &auth.JWT{Secret: []byte(strings.Repeat("s", 32)), Access: time.Minute, Refresh: time.Hour}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(ctx) })
	authn, _ := gesso.ModuleOf[*auth.Module](app)
	users := authn.Users().(*auth.SQL)
	if _, err := users.Create(ctx, "ada", "a long password"); err != nil {
		t.Fatal(err)
	}
	api := gesso.NewLocal(app)
	s, err := auth.Login.Call(ctx, api, auth.ReqLogin{Username: "ada", Password: "a long password"})
	if err != nil || s.RefreshToken == "" || strings.Count(s.Token, ".") != 2 {
		t.Fatalf("login: %+v %v", s, err)
	}
	if _, err := auth.Me.Call(ctx, api, gesso.Empty{}, gesso.Bearer(s.Token)); err != nil {
		t.Fatalf("me: %v", err)
	}
	if _, err := auth.Me.Call(ctx, api, gesso.Empty{}, gesso.Bearer(s.RefreshToken)); err == nil {
		t.Fatal("a refresh token as the bearer")
	}
	n, err := auth.Refresh.Call(ctx, api, auth.ReqRefresh{RefreshToken: s.RefreshToken})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := auth.Refresh.Call(ctx, api, auth.ReqRefresh{RefreshToken: s.RefreshToken}); err == nil {
		t.Fatal("a refresh token twice")
	}
	if _, err := auth.Me.Call(ctx, api, gesso.Empty{}, gesso.Bearer(n.Token)); err == nil {
		t.Fatal("the session of a reused refresh token goes on")
	}
	s, _ = auth.Login.Call(ctx, api, auth.ReqLogin{Username: "ada", Password: "a long password"})
	if _, err := auth.Logout.Call(ctx, api, gesso.Empty{}, gesso.Bearer(s.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Refresh.Call(ctx, api, auth.ReqRefresh{RefreshToken: s.RefreshToken}); err == nil {
		t.Fatal("a refresh token after logout")
	}
	s, _ = auth.Login.Call(ctx, api, auth.ReqLogin{Username: "ada", Password: "a long password"})
	time.Sleep(time.Millisecond)
	if err := users.SetPassword(ctx, "ada", "another password"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Me.Call(ctx, api, gesso.Empty{}, gesso.Bearer(s.Token)); err == nil {
		t.Fatal("a token from before the password was set")
	}
}
