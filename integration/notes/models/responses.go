package models

// ResNote is a note as clients see it.
type ResNote struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
}
