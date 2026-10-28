// Package notes is a gesso module the way an application writes one: a
// repository over db.DB, a service that knows nothing of HTTP, typed handlers, and
// its own migrations. The integration tests drive it end to end.
package notes

import (
	"embed"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/db"
)

//go:embed migrations
var Migrations embed.FS

// Deps is everything the module needs, passed in.
type Deps struct {
	DB db.DB
}

// Module is the notes feature.
type Module struct {
	svc *Service
}

// New wires the module from its dependencies.
func New(d Deps) *Module {
	return &Module{svc: NewService(d.DB, newRepository)}
}

// Routes registers the module's routes.
func (m *Module) Routes(r *gesso.Router) {
	GetNote.Handle(r, m.get)
	CreateNote.Handle(r, m.create, gesso.Status(201))
	BatchNotes.Handle(r, m.batch, gesso.Status(201))
}
