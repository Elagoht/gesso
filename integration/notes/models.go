package notes

import "github.com/Elagoht/gesso"

// Note is a note as clients see it.
type Note struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// CreateIn is the body of POST /notes.
type CreateIn struct {
	Slug  string `json:"slug" validate:"required,max=40"`
	Title string `json:"title" validate:"required,max=120"`
}

// BatchIn is the body of POST /notes/batch: all or none.
type BatchIn struct {
	Notes []CreateIn `json:"notes" validate:"min=1,max=10"`
}

// GetIn is the input of GetNote.
type GetIn struct {
	Slug string `path:"slug"`
}

// The module's endpoints: the server registers them, another process (collage)
// calls them with the same types.
var (
	GetNote    = gesso.Endpoint[GetIn, Note]{Method: "GET", Pattern: "/notes/{slug}"}
	CreateNote = gesso.Endpoint[CreateIn, Note]{Method: "POST", Pattern: "/notes"}
	BatchNotes = gesso.Endpoint[BatchIn, []Note]{Method: "POST", Pattern: "/notes/batch"}
)
