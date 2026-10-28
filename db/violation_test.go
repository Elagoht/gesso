package db

import (
	"errors"
	"net/http"
	"testing"
)

func TestClassifiers(t *testing.T) {
	cases := []struct {
		name string
		got  func() (Violation, bool)
		want Violation
		ok   bool
	}{
		{"pg unique", func() (Violation, bool) { return PostgresViolation("23505", "users_email_key") }, Violation{Unique, "users_email_key"}, true},
		{"pg other", func() (Violation, bool) { return PostgresViolation("42P01", "") }, Violation{}, false},
		{"sqlite unique", func() (Violation, bool) {
			return SQLiteViolation(2067, "constraint failed: UNIQUE constraint failed: users.email (2067)")
		}, Violation{Unique, "users.email"}, true},
		{"sqlite fk", func() (Violation, bool) { return SQLiteViolation(787, "FOREIGN KEY constraint failed") }, Violation{ForeignKey, ""}, true},
		{"mysql duplicate", func() (Violation, bool) {
			return MySQLViolation(1062, "Duplicate entry 'a@b.c' for key 'users.email'")
		}, Violation{Unique, "users.email"}, true},
		{"mysql other", func() (Violation, bool) { return MySQLViolation(1146, "no table") }, Violation{}, false},
	}
	for _, c := range cases {
		got, ok := c.got()
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %+v %v", c.name, got, ok)
		}
	}
}

var errDup = errors.New("duplicate")

func TestErrorsNamesTheMappedField(t *testing.T) {
	mapper := Errors(func(err error) (Violation, bool) {
		if errors.Is(err, errDup) {
			return Violation{Unique, "users_email_key"}, true
		}
		return Violation{}, false
	}, map[string]string{"users_email_key": "email"})

	e, ok := mapper(errDup)
	if !ok || e.Status != http.StatusConflict || e.Code != "conflict" || len(e.Fields) != 1 || e.Fields[0].Field != "email" || e.Fields[0].Code != "unique" {
		t.Fatalf("got %+v %v", e, ok)
	}
	if _, ok := mapper(errors.New("other")); ok {
		t.Fatal("not a violation")
	}
}
