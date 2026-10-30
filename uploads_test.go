package gesso

import (
	"context"
	"strings"
	"testing"

	"github.com/Elagoht/gesso/storage"
)

func TestServeUploads(t *testing.T) {
	files := storage.NewMemory(nil)
	ctx := context.Background()
	for key, data := range map[string]string{"docs/a.png": "\x89PNG\r\n\x1a\n", "docs/b.pdf": "%PDF-1.4", "private/docs/c.png": "\x89PNG"} {
		if _, err := files.Put(ctx, key, strings.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	a := mustNew(t, Config{Storage: files, BasePath: "/api"})
	a.ServeUploads()
	a.ServeUploads() // once, however often
	if got := a.UploadURL("docs/a.png"); got != "/api/uploads/docs/a.png" {
		t.Fatalf("url: %s", got)
	}
	rec := do(a, "GET", "/api/uploads/docs/a.png", "", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") ||
		strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("an image: %d %v", rec.Code, rec.Header())
	}
	rec = do(a, "GET", "/api/uploads/docs/b.pdf", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("another file: %d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"/api/uploads/private/docs/c.png", "/api/uploads/docs/none.png", "/api/uploads/docs/.hidden"} {
		if rec := do(a, "GET", p, "", ""); rec.Code != 404 {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
}
