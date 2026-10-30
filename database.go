package gesso

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/Elagoht/gesso/migrate"
	"github.com/Elagoht/gesso/storage"
)

// Database is the app's SQL database: gesso/sqlite, gesso/postgres and
// gesso/mysql make one from a file or a URL. Set as Config.Database, the app
// opens it, migrates it as it starts, closes it at shutdown, answers its
// constraint violations as field errors, and adds the "migrate" command.
type Database interface {
	// Open opens the pool the app's handlers use.
	Open() (*sql.DB, error)
	// OpenMigrator opens the connection migrations run on: one that takes
	// several statements per Exec, as a migration file needs.
	OpenMigrator() (*sql.DB, error)
	Dialect() migrate.Dialect
	// Errors answers the database's constraint violations; fields names the
	// field of each unique constraint.
	Errors(fields map[string]string) ErrorMapper
}

// Initializer is a module that sets itself up from the app when it is
// registered, before its routes: it takes the database, adds its migrations,
// its constraints, its commands.
type Initializer interface {
	Init(app *App) error
}

// ModuleOf is the module of type T registered on app: the sign-in module, for
// another module that looks up users.
func ModuleOf[T Module](app *App) (T, bool) {
	for _, m := range app.modules {
		if t, ok := m.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}

// DB is the app's database pool, nil without Config.Database.
func (a *App) DB() *sql.DB { return a.db }

// Dialect is the app's database dialect, nil without Config.Database.
func (a *App) Dialect() migrate.Dialect {
	if a.cfg.Database == nil {
		return nil
	}
	return a.cfg.Database.Dialect()
}

// Migrations adds migration files the app applies as it starts, with the other
// modules' in one history.
func (a *App) Migrations(fsys fs.FS) {
	if fsys != nil {
		a.migrations = append(a.migrations, fsys)
	}
}

// Schema adds a module's own migration step, run after the migration files on
// the same connection: a library's tables, kept in a history of their own.
func (a *App) Schema(fn func(ctx context.Context, d *sql.DB, dialect migrate.Dialect) error) {
	if fn != nil {
		a.schemas = append(a.schemas, fn)
	}
}

// Constraints names the field each unique constraint is about, so a duplicate
// is answered 409 on that field: {"docs.slug": "slug"}.
func (a *App) Constraints(fields map[string]string) {
	maps.Copy(a.constraints, fields)
}

func (a *App) openDatabase() error {
	d, err := a.cfg.Database.Open()
	if err != nil {
		return fmt.Errorf("gesso: database: %w", err)
	}
	a.db = d
	a.errs.mappers = append(a.errs.mappers, func(err error) (*Error, bool) {
		return a.cfg.Database.Errors(a.constraints)(err)
	})
	if a.cfg.Health != nil {
		checks := maps.Clone(a.cfg.Health.Checks)
		if checks == nil {
			checks = map[string]func(context.Context) error{}
		}
		if _, ok := checks["database"]; !ok {
			checks["database"] = d.PingContext
		}
		a.cfg.Health.Checks = checks
	}
	a.OnShutdown(func(context.Context) error { return d.Close() })
	a.Command("migrate", func(ctx context.Context, args []string, out io.Writer) error {
		m, conn, err := a.migrator()
		if err != nil {
			return err
		}
		return errors.Join(migrate.Command(ctx, m, args, out), conn.Close())
	})
	return nil
}

func (a *App) migrator() (*migrate.Migrator, *sql.DB, error) {
	conn, err := a.cfg.Database.OpenMigrator()
	if err != nil {
		return nil, nil, err
	}
	m, err := migrate.New(conn, a.cfg.Database.Dialect(), a.migrations...)
	if err != nil {
		return nil, nil, errors.Join(err, conn.Close())
	}
	return m, conn, nil
}

// migrateUp brings the database up to date as the app starts: the migration
// files, then the modules' own steps.
func (a *App) migrateUp(ctx context.Context) error {
	m, conn, err := a.migrator()
	if err != nil {
		return fmt.Errorf("gesso: migrate: %w", err)
	}
	if _, err := m.Up(ctx); err != nil {
		return errors.Join(fmt.Errorf("gesso: migrate: %w", err), conn.Close())
	}
	for _, fn := range a.schemas {
		if err := fn(ctx, conn, a.cfg.Database.Dialect()); err != nil {
			return errors.Join(fmt.Errorf("gesso: migrate: %w", err), conn.Close())
		}
	}
	return conn.Close()
}

// Storage is where the app keeps uploaded files: Config.Storage, or the folder
// UPLOADS_DIR names (./uploads), made when first asked for.
func (a *App) Storage() (storage.Storage, error) {
	a.storageOnce.Do(func() {
		if a.cfg.Storage != nil {
			return
		}
		d, err := storage.NewDisk(cmp.Or(os.Getenv("UPLOADS_DIR"), "uploads"))
		if err != nil {
			a.storageErr = fmt.Errorf("gesso: uploads: %w", err)
			return
		}
		a.cfg.Storage = d
		a.OnShutdown(func(context.Context) error { return d.Close() })
	})
	return a.cfg.Storage, a.storageErr
}

// UploadsPath is where ServeUploads serves the stored files, under BasePath.
const UploadsPath = "/uploads/"

// PrivatePrefix begins the keys ServeUploads never serves: the files a route
// of their own serves, behind its guards.
const PrivatePrefix = "private/"

// ServeUploads serves the files in App.Storage at /uploads/<key>, once however
// often it is called: a module's public files, whose keys — random, and new
// for each file — are what keeps them from being guessed. An image is shown,
// any other file downloaded, never run as a page; a key under PrivatePrefix
// is not found.
func (a *App) ServeUploads() {
	a.uploadsOnce.Do(func() {
		a.register("GET "+a.basePath+UploadsPath+"{key...}", uploads{app: a})
	})
}

// UploadURL is where ServeUploads serves key.
func (a *App) UploadURL(key string) string { return a.basePath + UploadsPath + key }

type uploads struct{ app *App }

func (u uploads) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	files, err := u.app.Storage()
	if err != nil {
		u.app.errs.write(w, r, err)
		return
	}
	if storage.ValidKey(key) != nil || strings.HasPrefix(key, PrivatePrefix) {
		u.app.errs.write(w, r, ErrNotFound)
		return
	}
	body, obj, err := files.Open(r.Context(), key)
	if errors.Is(err, storage.ErrNotFound) {
		u.app.errs.write(w, r, ErrNotFound)
		return
	}
	if err != nil {
		u.app.errs.write(w, r, err)
		return
	}
	d := &Download{Body: body, Size: obj.Size, ModTime: obj.ModTime}
	switch ext := path.Ext(key); ext {
	case ".jpg", ".png":
		d.ContentType, d.Inline = mime.TypeByExtension(ext), true
	default:
		d.ContentType, d.Filename = mime.TypeByExtension(ext), path.Base(key)
	}
	// A key names one file for ever: a new file is a new key.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	d.write(w, r)
}

// BasePath is the prefix every route is under: "" or "/api".
func (a *App) BasePath() string { return a.basePath }
