package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/gessotest"
	"github.com/Elagoht/gesso/migrate"
	"github.com/Elagoht/gesso/token"
	"github.com/Elagoht/gesso/token/tokentest"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresMigrations(t *testing.T) { migrationSuite(t, pgDB(t), migrate.Postgres) }

func TestPostgresInTx(t *testing.T) { inTxSuite(t, pgDB(t)) }

// Two migrators at once — two replicas starting — apply each migration once.
func TestPostgresMigrationLock(t *testing.T) {
	d := pgDB(t)
	files := fstest.MapFS{
		"20261028100000_a.up.sql": {Data: []byte("CREATE TABLE a (id INTEGER); SELECT pg_sleep(0.3);")},
		"20261028110000_b.up.sql": {Data: []byte("CREATE TABLE b (id INTEGER);")},
	}
	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			m, err := migrate.New(d, migrate.Postgres, files)
			if err != nil {
				errs[i] = err
				return
			}
			done, err := m.Up(context.Background())
			results[i], errs[i] = len(done), err
		})
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0]+results[1] != 2 {
		t.Fatalf("applied %v, errors %v: the lock let both in or kept one out", results, errs)
	}
}

func TestPostgresTokenStore(t *testing.T) {
	d := pgDB(t)
	if err := token.Migrate(context.Background(), d, migrate.Postgres); err != nil {
		t.Fatal(err)
	}
	tokentest.Run(t, func(t *testing.T) token.Store {
		return token.NewSQLStore(gessotest.Tx(t, d), migrate.Postgres)
	})
}

// pgClassify is the one line DESIGN and db.Classifier's doc promise for pgx.
func pgClassify(err error) (db.Violation, bool) {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return db.Violation{}, false
	}
	return db.PostgresViolation(pe.Code, pe.ConstraintName)
}

type tagIn struct {
	Name string `json:"name" validate:"required"`
}

type tagOut struct {
	ID int64 `json:"id"`
}

// tagsApp is a module on PostgreSQL: placeholders are $n, ids are identities.
func tagsApp(t *testing.T, d db.DB) *gessotest.Client {
	app := gessotest.New(t, gesso.Config{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ErrorMappers: []gesso.ErrorMapper{db.Errors(pgClassify, map[string]string{"tags_name_key": "name"})},
	}, moduleFunc(func(r *gesso.Router) {
		r.Post("/tags", gesso.Handle(func(ctx context.Context, in tagIn) (tagOut, error) {
			var out tagOut
			err := d.QueryRowContext(ctx, "INSERT INTO tags (name) VALUES ($1) RETURNING id", in.Name).Scan(&out.ID)
			return out, err
		}, gesso.Status(201)))
		r.Get("/tags/count", gesso.Handle(func(ctx context.Context, _ gesso.Empty) (tagOut, error) {
			var out tagOut
			err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM tags").Scan(&out.ID)
			return out, err
		}))
	}))
	return gessotest.NewClient(t, app)
}

func createTags(t *testing.T, d *sql.DB) {
	t.Helper()
	if _, err := d.Exec("CREATE TABLE tags (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name TEXT NOT NULL UNIQUE)"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresViolationAnswersTheField(t *testing.T) {
	d := pgDB(t)
	createTags(t, d)
	c := tagsApp(t, d)
	c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectStatus(201)
	p := c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectProblem(409, "conflict")
	if len(p.Errors) != 1 || p.Errors[0].Field != "name" || p.Errors[0].Code != "unique" {
		t.Fatalf("got %+v", p)
	}
}

// PostgreSQL fails the whole transaction after an error. A test that expects a
// 409 and then goes on, inside one gessotest.Tx, must still work.
func TestPostgresTestTransactionSurvivesAnError(t *testing.T) {
	d := pgDB(t)
	createTags(t, d)
	c := tagsApp(t, gessotest.Tx(t, d))
	c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectStatus(201)
	c.Post("/tags", gessotest.JSON(tagIn{Name: "go"})).ExpectProblem(409, "conflict")
	c.Post("/tags", gessotest.JSON(tagIn{Name: "rust"})).ExpectStatus(201)
	if n := gessotest.Decode[tagOut](c.Get("/tags/count").ExpectStatus(200)).ID; n != 2 {
		t.Fatalf("count %d", n)
	}
}
