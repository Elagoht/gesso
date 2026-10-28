// Package storagetest holds every storage.Storage to the same contract. Run it
// against an implementation of your own:
//
//	func TestMyStorage(t *testing.T) {
//	    storagetest.Run(t, func(t *testing.T) storage.Storage { return newMyStorage(t) })
//	}
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Elagoht/gesso/storage"
)

// Run checks the contract on a fresh, empty store from open for each case.
func Run(t *testing.T, open func(t *testing.T) storage.Storage) {
	ctx := context.Background()

	t.Run("put then open", func(t *testing.T) {
		s := open(t)
		obj, err := s.Put(ctx, "a/b/file.txt", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}
		if obj.Key != "a/b/file.txt" || obj.Size != 5 || obj.ModTime.IsZero() {
			t.Fatalf("put returned %+v", obj)
		}
		rc, got, err := s.Open(ctx, "a/b/file.txt")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		data, _ := io.ReadAll(rc)
		if string(data) != "hello" || got.Size != 5 {
			t.Fatalf("open: %q %+v", data, got)
		}
	})

	t.Run("put replaces", func(t *testing.T) {
		s := open(t)
		mustPut(t, s, "k", "first")
		mustPut(t, s, "k", "second, longer")
		if obj, err := s.Stat(ctx, "k"); err != nil || obj.Size != int64(len("second, longer")) {
			t.Fatalf("stat: %+v %v", obj, err)
		}
	})

	t.Run("a failed put leaves the old file", func(t *testing.T) {
		s := open(t)
		mustPut(t, s, "k", "old")
		boom := errors.New("reader failed")
		if _, err := s.Put(ctx, "k", io.MultiReader(strings.NewReader("partial"), errReader{boom})); !errors.Is(err, boom) {
			t.Fatalf("want the reader's error, got %v", err)
		}
		if got := read(t, s, "k"); got != "old" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("missing is ErrNotFound", func(t *testing.T) {
		s := open(t)
		if _, _, err := s.Open(ctx, "nope"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("open: %v", err)
		}
		if _, err := s.Stat(ctx, "nope"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("stat: %v", err)
		}
		mustPut(t, s, "dir/file", "x")
		if _, err := s.Stat(ctx, "dir"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("a directory is not a file: %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := open(t)
		mustPut(t, s, "k", "x")
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Stat(ctx, "k"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("deleted file still there: %v", err)
		}
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatalf("deleting a missing file is not an error: %v", err)
		}
	})

	t.Run("list by prefix", func(t *testing.T) {
		s := open(t)
		for _, k := range []string{"img/b.png", "img/a.png", "docs/x.pdf"} {
			mustPut(t, s, k, k)
		}
		objs, err := s.List(ctx, "img/")
		if err != nil {
			t.Fatal(err)
		}
		if len(objs) != 2 || objs[0].Key != "img/a.png" || objs[1].Key != "img/b.png" {
			t.Fatalf("got %+v", objs)
		}
		all, _ := s.List(ctx, "")
		if len(all) != 3 {
			t.Fatalf("got %+v", all)
		}
	})

	t.Run("keys cannot escape", func(t *testing.T) {
		s := open(t)
		for _, k := range []string{"", "/etc/passwd", "../outside", "a/../../b", "a//b", ".hidden", "a/.tmp", `a\b`, "a/"} {
			if _, err := s.Put(ctx, k, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("put %q: %v", k, err)
			}
			if _, _, err := s.Open(ctx, k); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("open %q: %v", k, err)
			}
		}
	})

	t.Run("a cancelled context stops a put", func(t *testing.T) {
		s := open(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.Put(cctx, "k", bytes.NewReader(make([]byte, 1<<16))); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func mustPut(t *testing.T, s storage.Storage, key, data string) {
	t.Helper()
	if _, err := s.Put(context.Background(), key, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, s storage.Storage, key string) string {
	t.Helper()
	rc, _, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	return string(data)
}
