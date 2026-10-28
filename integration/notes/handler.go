package notes

import (
	"context"
)

func (m *Module) get(ctx context.Context, in getIn) (Note, error) {
	return m.svc.Get(ctx, in.Slug)
}

func (m *Module) create(ctx context.Context, in CreateIn) (Note, error) {
	return m.svc.Create(ctx, in)
}

func (m *Module) batch(ctx context.Context, in BatchIn) ([]Note, error) {
	return m.svc.CreateAll(ctx, in.Notes)
}
