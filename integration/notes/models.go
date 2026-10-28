package notes

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

type getIn struct {
	Slug string `path:"slug"`
}
