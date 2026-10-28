package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Elagoht/gesso/storage"
	"github.com/Elagoht/gesso/storage/storagetest"
)

func TestDisk(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Storage {
		d, err := storage.NewDisk(filepath.Join(t.TempDir(), "files"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	})
}

func TestMemory(t *testing.T) {
	storagetest.Run(t, func(*testing.T) storage.Storage { return storage.NewMemory(nil) })
}

// A link planted inside the directory still cannot reach outside it.
func TestDiskRefusesLinksOut(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "files")
	d, err := storage.NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := os.Symlink(filepath.Join(base, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	if _, _, err := d.Open(t.Context(), "link"); err == nil {
		t.Fatal("a link out of the root was followed")
	}
}
