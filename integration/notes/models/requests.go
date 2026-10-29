package models

// ReqCreateNote is the body of POST /notes.
type ReqCreateNote struct {
	Slug  string `json:"slug" validate:"required,max=40"`
	Title string `json:"title" validate:"required,max=120"`
}

// ReqBatchNotes is the body of POST /notes/batch: all or none.
type ReqBatchNotes struct {
	Notes []ReqCreateNote `json:"notes" validate:"min=1,max=10"`
}

// ReqGetNote names one note by its slug.
type ReqGetNote struct {
	Slug string `path:"slug"`
}
