package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Keys are their exact spelling on any file system: never one file for two.
func TestDiskCase(t *testing.T) {
	ctx := context.Background()
	d, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Put(ctx, "pics/a.png", strings.NewReader("lower")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Stat(ctx, "pics/A.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another spelling found the file: %v", err)
	}
	_, err = d.Put(ctx, "pics/A.png", strings.NewReader("upper"))
	if d.folds {
		if !errors.Is(err, ErrKeyCollides) {
			t.Fatalf("on a case-folding file system: %v", err)
		}
		if _, err := d.Put(ctx, "PICS/b.png", strings.NewReader("x")); !errors.Is(err, ErrKeyCollides) {
			t.Fatalf("a folder spelled otherwise: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	_ = d.Delete(ctx, "pics/A.png")
	rc, _, err := d.Open(ctx, "pics/a.png")
	if err != nil {
		t.Fatalf("deleting another spelling removed the file: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "lower" {
		t.Fatalf("got %q", b)
	}
	t.Logf("case-folding file system: %v", d.folds)
}
