package notes

import (
	"context"

	"github.com/Elagoht/gesso/db"
)

type repository struct {
	db db.DB
}

func newRepository(d db.DB) noteStore { return &repository{db: d} }

func (r *repository) Insert(ctx context.Context, in CreateIn) (Note, error) {
	n := Note{Slug: in.Slug, Title: in.Title}
	err := r.db.QueryRowContext(ctx,
		"INSERT INTO notes (slug, title) VALUES (?, ?) RETURNING id", in.Slug, in.Title).Scan(&n.ID)
	return n, err
}

func (r *repository) BySlug(ctx context.Context, slug string) (Note, error) {
	var n Note
	err := r.db.QueryRowContext(ctx,
		"SELECT id, slug, title FROM notes WHERE slug = ?", slug).Scan(&n.ID, &n.Slug, &n.Title)
	return n, err
}
