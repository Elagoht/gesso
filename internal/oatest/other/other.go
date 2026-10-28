// Package other holds a type whose name collides with one in the openapi tests.
package other

// Note is a second Note, from another package.
type Note struct {
	Body string `json:"body"`
}
