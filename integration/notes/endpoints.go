package notes

import (
	"github.com/Elagoht/gesso"

	"github.com/Elagoht/gesso/integration/notes/models"
)

// The module's endpoints: the server registers them, another process (collage)
// calls them with the same types.
var (
	GetNote    = gesso.Endpoint[models.ReqGetNote, models.ResNote]{Method: "GET", Pattern: "/notes/{slug}"}
	CreateNote = gesso.Endpoint[models.ReqCreateNote, models.ResNote]{Method: "POST", Pattern: "/notes"}
	BatchNotes = gesso.Endpoint[models.ReqBatchNotes, []models.ResNote]{Method: "POST", Pattern: "/notes/batch"}
)
