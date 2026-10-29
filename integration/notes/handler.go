package notes

import (
	"context"

	"github.com/Elagoht/gesso/integration/notes/models"
)

func (m *Module) get(ctx context.Context, in models.ReqGetNote) (models.ResNote, error) {
	n, err := m.svc.Get(ctx, in.Slug)
	return n.Response(), err
}

func (m *Module) create(ctx context.Context, in models.ReqCreateNote) (models.ResNote, error) {
	n, err := m.svc.Create(ctx, in)
	return n.Response(), err
}

func (m *Module) batch(ctx context.Context, in models.ReqBatchNotes) ([]models.ResNote, error) {
	created, err := m.svc.CreateAll(ctx, in.Notes)
	out := make([]models.ResNote, 0, len(created))
	for _, n := range created {
		out = append(out, n.Response())
	}
	return out, err
}
