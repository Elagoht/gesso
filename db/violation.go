package db

import (
	"net/http"
	"strings"

	"github.com/Elagoht/gesso"
	"github.com/Elagoht/gesso/validate"
)

// Kind is the sort of constraint a statement broke.
type Kind int

const (
	Unique Kind = iota + 1
	ForeignKey
	NotNull
	Check
)

// Violation is a broken constraint, read from a driver's error.
type Violation struct {
	Kind       Kind
	Constraint string
}

// Classifier reads a Violation from an error, or reports false. gesso cannot read
// a driver's error type without importing the driver, so the application, which
// imports it, writes the one line that does:
//
//	// pgx
//	func(err error) (db.Violation, bool) {
//	    var pe *pgconn.PgError
//	    if !errors.As(err, &pe) { return db.Violation{}, false }
//	    return db.PostgresViolation(pe.Code, pe.ConstraintName)
//	}
//
//	// lib/pq: the same with *pq.Error, string(pe.Code) and pe.Constraint.
type Classifier func(error) (Violation, bool)

// PostgresViolation classifies a PostgreSQL SQLSTATE.
func PostgresViolation(sqlState, constraint string) (Violation, bool) {
	kinds := map[string]Kind{"23505": Unique, "23503": ForeignKey, "23502": NotNull, "23514": Check}
	k, ok := kinds[sqlState]
	return Violation{Kind: k, Constraint: constraint}, ok
}

// SQLiteViolation classifies an SQLite extended result code (modernc.org/sqlite's
// Error.Code(), mattn/go-sqlite3's ExtendedCode). SQLite names no constraint; the
// columns it reports ("users.email") stand in for one.
func SQLiteViolation(extendedCode int, message string) (Violation, bool) {
	kinds := map[int]Kind{2067: Unique, 1555: Unique, 787: ForeignKey, 1299: NotNull, 275: Check}
	k, ok := kinds[extendedCode]
	if !ok {
		return Violation{}, false
	}
	var constraint string
	if i := strings.LastIndex(message, "constraint failed: "); i >= 0 {
		constraint, _, _ = strings.Cut(message[i+len("constraint failed: "):], " (")
	}
	return Violation{Kind: k, Constraint: strings.TrimSpace(constraint)}, true
}

// MySQLViolation classifies a MySQL error number. For a duplicate the key's name
// is read from the message: "Duplicate entry 'x' for key 'users.email'".
func MySQLViolation(number uint16, message string) (Violation, bool) {
	kinds := map[uint16]Kind{1062: Unique, 1451: ForeignKey, 1452: ForeignKey, 1048: NotNull, 3819: Check}
	k, ok := kinds[number]
	if !ok {
		return Violation{}, false
	}
	var constraint string
	if i := strings.LastIndex(message, " for key '"); i >= 0 {
		constraint = strings.TrimSuffix(message[i+len(" for key '"):], "'")
	}
	return Violation{Kind: k, Constraint: constraint}, true
}

// Errors makes a gesso.ErrorMapper that answers violations as the client's
// fault instead of a 500: a duplicate is 409 "conflict", a missing or still-used
// reference 409 "reference", a missing value 400 "missing_value", a failed check
// 422 "check_failed".
//
// fields names the field a constraint guards, {"users_email_key": "email"}, so
// the answer points at it. It is written out, not guessed from the name.
func Errors(classify Classifier, fields map[string]string) gesso.ErrorMapper {
	return func(err error) (*gesso.Error, bool) {
		v, ok := classify(err)
		if !ok {
			return nil, false
		}
		var e *gesso.Error
		var fe validate.FieldError
		switch v.Kind {
		case Unique:
			e = gesso.Conflict("conflict", "the resource already exists")
			fe = validate.FieldError{Code: "unique", Message: "is already taken"}
		case ForeignKey:
			e = gesso.Conflict("reference", "the resource refers to, or is referred to by, another")
			fe = validate.FieldError{Code: "reference", Message: "refers to a resource that does not exist or is in use"}
		case NotNull:
			e = gesso.BadRequest("missing_value", "a required value is missing")
			fe = validate.FieldError{Code: "required", Message: "is required"}
		case Check:
			e = gesso.NewError(http.StatusUnprocessableEntity, "check_failed", "a value breaks a rule of the resource")
			fe = validate.FieldError{Code: "check", Message: "is not allowed"}
		default:
			return nil, false
		}
		if name, ok := fields[v.Constraint]; ok {
			fe.Field = name
			e = e.WithFields(fe)
		}
		return e, true
	}
}
