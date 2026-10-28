package date

import (
	"database/sql/driver"
	"encoding/json"
	"testing"
	"time"
)

func TestParseAndString(t *testing.T) {
	d, err := Parse("2024-02-29")
	if err != nil || d != (Date{2024, time.February, 29}) || d.String() != "2024-02-29" {
		t.Fatalf("got %v %v", d, err)
	}
	for _, bad := range []string{"2023-02-29", "2024-13-01", "28.10.2026", "2026-10-28T00:00:00Z", ""} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestJSON(t *testing.T) {
	type doc struct {
		Date Date  `json:"date"`
		Opt  *Date `json:"opt"`
	}
	b, err := json.Marshal(doc{Date: Date{2026, time.October, 28}})
	if err != nil || string(b) != `{"date":"2026-10-28","opt":null}` {
		t.Fatalf("got %s %v", b, err)
	}
	var back doc
	if err := json.Unmarshal([]byte(`{"date":"2026-01-05","opt":"2026-01-06"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Date != (Date{2026, time.January, 5}) || back.Opt == nil || *back.Opt != (Date{2026, time.January, 6}) {
		t.Fatalf("got %+v", back)
	}
	if err := json.Unmarshal([]byte(`{"date":"2026-02-30"}`), &back); err == nil {
		t.Fatal("February 30 decoded")
	}
	if err := json.Unmarshal([]byte(`{"date":20261028}`), &back); err == nil {
		t.Fatal("a number decoded")
	}
	// The zero Date is null, and comes back as the zero Date: a client that
	// leaves a defaulted date out sends what the server reads as "not given".
	b, _ = json.Marshal(doc{})
	if string(b) != `{"date":null,"opt":null}` {
		t.Fatalf("zero: %s", b)
	}
	back = doc{Date: Date{2026, time.May, 1}}
	for _, in := range []string{`{"date":null}`, `{"date":""}`} {
		if err := json.Unmarshal([]byte(in), &back); err != nil || !back.Date.IsZero() {
			t.Fatalf("%s: %+v %v", in, back, err)
		}
	}
}

func TestScanAndValue(t *testing.T) {
	want := Date{2026, time.October, 28}
	// A time.Time keeps its own calendar day: midnight in Istanbul is still the
	// 28th, though it is the 27th in UTC.
	ist := time.FixedZone("TRT", 3*3600)
	for _, src := range []driver.Value{
		time.Date(2026, 10, 28, 0, 0, 0, 0, ist),
		"2026-10-28",
		[]byte("2026-10-28"),
		"2026-10-28 00:00:00+00:00",
		"2026-10-28T00:00:00Z",
	} {
		var d Date
		if err := d.Scan(src); err != nil || d != want {
			t.Errorf("Scan(%#v) = %v, %v", src, d, err)
		}
	}
	var d Date
	if err := d.Scan(nil); err == nil {
		t.Error("NULL scanned into a Date")
	}
	if err := d.Scan(int64(3)); err == nil {
		t.Error("an int scanned")
	}
	if v, err := want.Value(); err != nil || v != "2026-10-28" {
		t.Fatalf("Value = %v %v", v, err)
	}
	if v, err := (Date{}).Value(); err != nil || v != nil {
		t.Fatalf("the zero Date is NULL: %v %v", v, err)
	}
	var z Date
	if err := z.UnmarshalText(nil); err != nil || !z.IsZero() || (Date{}).String() != "" {
		t.Fatal("the zero Date's text is empty")
	}
}

func TestArithmetic(t *testing.T) {
	d := Date{2024, time.December, 31}
	if got := d.AddDays(1); got != (Date{2025, time.January, 1}) {
		t.Fatalf("got %v", got)
	}
	if got := d.AddDays(-366); got != (Date{2023, time.December, 31}) {
		t.Fatalf("got %v", got)
	}
	e := d.AddDays(1)
	if !d.Before(e) || !e.After(d) || d.Compare(d) != 0 || d.Compare(e) != -1 {
		t.Fatal("ordering")
	}
	if !(Date{}).IsZero() || d.IsZero() {
		t.Fatal("IsZero")
	}
	ist := time.FixedZone("TRT", 3*3600)
	if got := Today(time.Date(2026, 10, 27, 22, 30, 0, 0, time.UTC), ist); got != (Date{2026, time.October, 28}) {
		t.Fatalf("Today in Istanbul at 22:30 UTC: %v", got)
	}
	if got := d.In(ist); !got.Equal(time.Date(2024, 12, 31, 0, 0, 0, 0, ist)) {
		t.Fatalf("In: %v", got)
	}
}
