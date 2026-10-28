// Package storage keeps files — uploads, exports, generated images — behind one
// small interface. Disk and Memory are here, S3 (and what speaks its API:
// MinIO, R2, B2) in storage/s3; every implementation passes storagetest.Run.
//
// Storage holds bytes under keys. What a file is (its type, its owner, its
// original name) the application records itself, in its database. Make keys
// from your own ids, not a client's file name: on a case-insensitive file
// system (macOS's, Windows's) Disk keeps "a.png" and "A.png" as one file.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/gesso/clock"
)

// Object describes a stored file.
type Object struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// Storage keeps files under keys.
type Storage interface {
	// Put stores what r reads under key, replacing what was there. A reader that
	// fails leaves the old file in place.
	Put(ctx context.Context, key string, r io.Reader) (Object, error)
	// Open reads the file under key; the caller closes it.
	Open(ctx context.Context, key string) (io.ReadCloser, Object, error)
	// Stat describes the file under key.
	Stat(ctx context.Context, key string) (Object, error)
	// Delete removes the file under key. A missing file is not an error.
	Delete(ctx context.Context, key string) error
	// List describes the files whose keys begin with prefix, by key.
	List(ctx context.Context, prefix string) ([]Object, error)
}

var (
	// ErrNotFound is returned for a key with no file.
	ErrNotFound = errors.New("storage: not found")
	// ErrInvalidKey is returned for a key ValidKey refuses.
	ErrInvalidKey = errors.New("storage: invalid key")
)

// ValidKey checks a key: slash-separated segments, none empty, none "." or "..",
// none starting with "." (keeping hidden and temporary files out of reach), no
// backslash, no leading slash.
func ValidKey(key string) error {
	if key == "" || !fs.ValidPath(key) || strings.Contains(key, `\`) {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for seg := range strings.SplitSeq(key, "/") {
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}

// Disk keeps files in a directory, through os.Root: no key, link or rename can
// reach outside it.
type Disk struct {
	root *os.Root
}

// NewDisk opens dir, creating it if needed. Close releases it.
func NewDisk(dir string) (*Disk, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Disk{root: root}, nil
}

// Close releases the directory.
func (d *Disk) Close() error { return d.root.Close() }

// Put writes to a temporary file beside the target and renames it into place, so
// a reader never sees half a file.
func (d *Disk) Put(ctx context.Context, key string, r io.Reader) (Object, error) {
	if err := ValidKey(key); err != nil {
		return Object{}, err
	}
	if dir := path.Dir(key); dir != "." {
		if err := d.root.MkdirAll(dir, 0o750); err != nil {
			return Object{}, err
		}
	}
	tmp := path.Join(path.Dir(key), fmt.Sprintf(".tmp-%d-%s", time.Now().UnixNano(), path.Base(key)))
	f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return Object{}, err
	}
	_, copyErr := io.Copy(f, contextReader{ctx, r})
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = d.root.Remove(tmp)
		return Object{}, err
	}
	if err := d.root.Rename(tmp, key); err != nil {
		_ = d.root.Remove(tmp)
		return Object{}, err
	}
	return d.Stat(ctx, key)
}

// Open opens the file under key.
func (d *Disk) Open(_ context.Context, key string) (io.ReadCloser, Object, error) {
	if err := ValidKey(key); err != nil {
		return nil, Object{}, err
	}
	f, err := d.root.Open(key)
	if err != nil {
		return nil, Object{}, notFound(err)
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		_ = f.Close()
		return nil, Object{}, ErrNotFound
	}
	return f, Object{Key: key, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Stat describes the file under key.
func (d *Disk) Stat(_ context.Context, key string) (Object, error) {
	if err := ValidKey(key); err != nil {
		return Object{}, err
	}
	info, err := d.root.Stat(key)
	if err != nil {
		return Object{}, notFound(err)
	}
	if info.IsDir() {
		return Object{}, ErrNotFound
	}
	return Object{Key: key, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Delete removes the file under key.
func (d *Disk) Delete(_ context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	if info, err := d.root.Stat(key); err == nil && info.IsDir() {
		return nil
	}
	if err := d.root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List walks the directory for keys beginning with prefix.
func (d *Disk) List(_ context.Context, prefix string) ([]Object, error) {
	var out []Object
	err := fs.WalkDir(d.root.FS(), ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && strings.HasPrefix(e.Name(), ".") {
			if e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if e.IsDir() || !strings.HasPrefix(p, prefix) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		out = append(out, Object{Key: p, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	return out, err
}

func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// contextReader stops a copy when ctx is done.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Memory keeps files in memory: the fake for tests, held to the same suite as
// Disk.
type Memory struct {
	mu    sync.RWMutex
	files map[string]memFile
	clock clock.Clock
}

type memFile struct {
	data    []byte
	modTime time.Time
}

// NewMemory is an empty memory store; c stamps modification times (nil for the
// system clock).
func NewMemory(c clock.Clock) *Memory {
	if c == nil {
		c = clock.System
	}
	return &Memory{files: map[string]memFile{}, clock: c}
}

// Put stores a copy of what r reads.
func (m *Memory) Put(ctx context.Context, key string, r io.Reader) (Object, error) {
	if err := ValidKey(key); err != nil {
		return Object{}, err
	}
	data, err := io.ReadAll(contextReader{ctx, r})
	if err != nil {
		return Object{}, err
	}
	f := memFile{data: data, modTime: m.clock.Now()}
	m.mu.Lock()
	m.files[key] = f
	m.mu.Unlock()
	return Object{Key: key, Size: int64(len(data)), ModTime: f.modTime}, nil
}

// Open reads the file under key.
func (m *Memory) Open(_ context.Context, key string) (io.ReadCloser, Object, error) {
	if err := ValidKey(key); err != nil {
		return nil, Object{}, err
	}
	m.mu.RLock()
	f, ok := m.files[key]
	m.mu.RUnlock()
	if !ok {
		return nil, Object{}, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(f.data)), Object{Key: key, Size: int64(len(f.data)), ModTime: f.modTime}, nil
}

// Stat describes the file under key.
func (m *Memory) Stat(_ context.Context, key string) (Object, error) {
	if err := ValidKey(key); err != nil {
		return Object{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.files[key]
	if !ok {
		return Object{}, ErrNotFound
	}
	return Object{Key: key, Size: int64(len(f.data)), ModTime: f.modTime}, nil
}

// Delete removes the file under key.
func (m *Memory) Delete(_ context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.files, key)
	m.mu.Unlock()
	return nil
}

// List describes the files whose keys begin with prefix.
func (m *Memory) List(_ context.Context, prefix string) ([]Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Object
	for k, f := range m.files {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Object{Key: k, Size: int64(len(f.data)), ModTime: f.modTime})
		}
	}
	slices.SortFunc(out, func(a, b Object) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}
