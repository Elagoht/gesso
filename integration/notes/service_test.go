package notes

import (
	"context"
	"errors"
	"testing"

	"github.com/Elagoht/gesso/db"
	"github.com/Elagoht/gesso/gessotest"

	"github.com/Elagoht/gesso/integration/notes/models"
)

// fakeStore keeps notes in memory and can be told to fail an insert.
type fakeStore struct {
	notes  map[string]models.Note
	failOn string
}

var errDuplicate = errors.New("duplicate")

func (f *fakeStore) Insert(_ context.Context, in models.ReqCreateNote) (models.Note, error) {
	if in.Slug == f.failOn {
		return models.Note{}, errDuplicate
	}
	n := models.Note{ID: int64(len(f.notes) + 1), Slug: in.Slug, Title: in.Title}
	f.notes[in.Slug] = n
	return n, nil
}

func (f *fakeStore) BySlug(_ context.Context, slug string) (models.Note, error) {
	return f.notes[slug], nil
}

// The service runs with no database at all: a fake store, and FakeDB to answer
// its InTx.
func TestCreateAllWithFakes(t *testing.T) {
	store := &fakeStore{notes: map[string]models.Note{}, failOn: "bad"}
	svc := NewService(gessotest.FakeDB{}, func(db.DB) Store { return store })

	got, err := svc.CreateAll(context.Background(), []models.ReqCreateNote{{Slug: "a", Title: "A"}, {Slug: "b", Title: "B"}})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := svc.CreateAll(context.Background(), []models.ReqCreateNote{{Slug: "c"}, {Slug: "bad"}}); !errors.Is(err, errDuplicate) {
		t.Fatalf("got %v", err)
	}
}
