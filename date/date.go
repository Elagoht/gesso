// Package date is a calendar date — a day, with no time and no time zone: a
// birthday, a publication date, a due date. time.Time is an instant, and an
// instant written as a date shifts by a day when a time zone is applied to it.
//
// A Date is "2026-10-28" in JSON, in a query or path parameter and in the
// database (a DATE column, or TEXT in SQLite), and "format": "date" in OpenAPI.
// Its zero value is no date: null in JSON, "" as text, NULL in the database —
// validate's "required" refuses it.
package date

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Date is a day in the proleptic Gregorian calendar.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

const layout = "2006-01-02"

// Of is the date t falls on, in t's own location.
func Of(t time.Time) Date {
	y, m, d := t.Date()
	return Date{Year: y, Month: m, Day: d}
}

// Today is the date now falls on in loc.
func Today(now time.Time, loc *time.Location) Date { return Of(now.In(loc)) }

// Parse reads "2006-01-02". A day the month does not have is an error.
func Parse(s string) (Date, error) {
	t, err := time.Parse(layout, s)
	if err != nil {
		return Date{}, fmt.Errorf("date: %q is not a date like 2006-01-02", s)
	}
	return Of(t), nil
}

// String is the date as "2006-01-02", and "" for the zero Date.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.In(time.UTC).Format(layout)
}

// IsZero reports whether d is the zero Date.
func (d Date) IsZero() bool { return d == Date{} }

// In is the first instant of d in loc.
func (d Date) In(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

// AddDays is d moved by n days, across months and years.
func (d Date) AddDays(n int) Date { return Of(d.In(time.UTC).AddDate(0, 0, n)) }

// Compare is -1, 0 or +1 as d is before, the same as, or after e.
func (d Date) Compare(e Date) int { return d.In(time.UTC).Compare(e.In(time.UTC)) }

// Before reports whether d is before e.
func (d Date) Before(e Date) bool { return d.Compare(e) < 0 }

// After reports whether d is after e.
func (d Date) After(e Date) bool { return d.Compare(e) > 0 }

// MarshalText writes "2006-01-02", or nothing for the zero Date.
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText reads "2006-01-02"; empty text is the zero Date.
func (d *Date) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Date{}
		return nil
	}
	v, err := Parse(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// MarshalJSON writes "2006-01-02", or null for the zero Date.
func (d Date) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + d.String() + `"`), nil
}

// UnmarshalJSON reads "2006-01-02"; null and "" are the zero Date.
func (d *Date) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*d = Date{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("date: %s is not a date like \"2006-01-02\"", b)
	}
	return d.UnmarshalText([]byte(s))
}

// Value stores d as "2006-01-02", which a DATE column takes in PostgreSQL and
// MySQL, and SQLite keeps as text that sorts by date. The zero Date is NULL, so
// a NOT NULL column refuses it rather than store a date nobody gave.
func (d Date) Value() (driver.Value, error) {
	if d.IsZero() {
		return nil, nil
	}
	return d.String(), nil
}

// Scan reads a DATE as the driver returns it: a time.Time (its own calendar
// day, whatever its location), or text — "2006-01-02", or a timestamp that
// begins with one, as SQLite may hand back. A NULL is an error: a nullable
// column scans into a *Date. src is any because sql.Scanner says so.
func (d *Date) Scan(src any) error {
	switch v := src.(type) {
	case time.Time:
		*d = Of(v)
		return nil
	case string:
		return d.scanText(v)
	case []byte:
		return d.scanText(string(v))
	case nil:
		return fmt.Errorf("date: NULL into a date.Date: scan a nullable column into a *date.Date")
	}
	return fmt.Errorf("date: cannot scan %T", src)
}

func (d *Date) scanText(s string) error {
	if len(s) > len(layout) && (s[len(layout)] == 'T' || s[len(layout)] == ' ') {
		s = s[:len(layout)]
	}
	return d.UnmarshalText([]byte(s))
}
