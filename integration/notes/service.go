package notes

import (
	"context"

	"github.com/Elagoht/gesso/db"
)

// Store is what the service needs from storage, declared here, where it is
// used; a unit test hands the service a fake one.
type Store interface {
	Insert(ctx context.Context, in CreateIn) (Note, error)
	BySlug(ctx context.Context, slug string) (Note, error)
}

// Service is the notes business logic.
type Service struct {
	db    db.DB
	store func(db.DB) Store
}

// NewService takes the database and how to open a store on it, so a transaction
// can open one of its own.
func NewService(d db.DB, store func(db.DB) Store) *Service {
	return &Service{db: d, store: store}
}

// Get returns a note; sql.ErrNoRows reaches gesso as a 404.
func (s *Service) Get(ctx context.Context, slug string) (Note, error) {
	return s.store(s.db).BySlug(ctx, slug)
}

// Create adds a note.
func (s *Service) Create(ctx context.Context, in CreateIn) (Note, error) {
	return s.store(s.db).Insert(ctx, in)
}

// CreateAll adds every note or none.
func (s *Service) CreateAll(ctx context.Context, in []CreateIn) ([]Note, error) {
	var out []Note
	err := db.InTx(ctx, s.db, func(tx db.DB) error {
		out = nil
		store := s.store(tx)
		for _, n := range in {
			note, err := store.Insert(ctx, n)
			if err != nil {
				return err
			}
			out = append(out, note)
		}
		return nil
	})
	return out, err
}
