// Package models holds the notes module's types: Note, as it is stored; the
// Req types, what clients send; the Res types, what they get back.
package models

// Note is a note as it is stored.
type Note struct {
	ID    int64
	Slug  string
	Title string
}

// Response is n as clients see it.
func (n Note) Response() ResNote {
	//lint:ignore S1016 field by field on purpose: a column added to Note is not shown until it is added here
	return ResNote{ID: n.ID, Slug: n.Slug, Title: n.Title}
}
