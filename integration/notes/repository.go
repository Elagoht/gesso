package notes

import (
	"context"

	"github.com/Elagoht/gesso/db"

	"github.com/Elagoht/gesso/integration/notes/models"
)

type repository struct {
	db db.DB
}

// newRepository opens the SQL store on d: the pool, or a transaction.
func newRepository(d db.DB) Store { return &repository{db: d} }

func (r *repository) Insert(ctx context.Context, in models.ReqCreateNote) (models.Note, error) {
	n := models.Note{Slug: in.Slug, Title: in.Title}
	err := r.db.QueryRowContext(ctx,
		"INSERT INTO notes (slug, title) VALUES (?, ?) RETURNING id", in.Slug, in.Title).Scan(&n.ID)
	return n, err
}

func (r *repository) BySlug(ctx context.Context, slug string) (models.Note, error) {
	var n models.Note
	err := r.db.QueryRowContext(ctx,
		"SELECT id, slug, title FROM notes WHERE slug = ?", slug).Scan(&n.ID, &n.Slug, &n.Title)
	return n, err
}
