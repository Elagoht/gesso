package main

import (
	"strconv"
	"strings"
)

// modelSQL is every statement a model's repository runs, written out for its
// dialect: nothing is put together while the app runs, so every argument list
// is fixed. A filter not asked for, a gate that is open, the first page, pass a
// true that makes their condition hold: (? OR author = ?).
type modelSQL struct {
	Columns     string // the select list
	Insert      string
	ByKey       string // key, then open when there is a gate
	IDOf        string // the same, for the id alone
	ByID        string
	Patch       string // set and value per field, then the id
	Save        string // every field, then the id
	Delete      string // by id
	Visible     string // a sortable model's id, key and position, in order
	SetPosition string // position, id
	Lists       []listSQL
}

// listSQL is the list in one order: Name is the order parameter's value, ""
// for the default.
type listSQL struct {
	Name   string
	SQL    string
	Field  *mfield // nil: by id
	Column string  // the order column: the field's, or position
	Slot   string  // the cursor's field holding the column's value
	Desc   bool
}

type numbering struct {
	dialect string
	n       int
}

func (p *numbering) next() string {
	p.n++
	if p.dialect == "postgres" {
		return "$" + strconv.Itoa(p.n)
	}
	return "?"
}

// cursorSlot is the cursor field a column's value is kept in, by Go type.
func cursorSlot(typ string) string {
	return map[string]string{"string": "S", "int64": "N", "float64": "F", "time.Time": "T", "date.Date": "D"}[typ]
}

func (m *model) buildSQL() {
	d, t := m.Dialect, m.Table
	idents := make([]string, len(m.Fields))
	for i, f := range m.Fields {
		idents[i] = f.Ident
	}
	cols := "id, " + strings.Join(idents, ", ")
	if m.Sortable {
		cols += ", position"
	}
	s := modelSQL{Columns: cols}

	gate := func(p *numbering) string {
		if m.Gate == nil {
			return ""
		}
		return " AND (" + p.next() + " OR " + m.Gate.Ident + ")"
	}
	keyCol := m.KeyCol()

	p := &numbering{dialect: d}
	vals := make([]string, len(m.Fields))
	for i := range m.Fields {
		vals[i] = p.next()
	}
	insertCols := strings.Join(idents, ", ")
	switch {
	case m.Sortable && d == "mysql":
		// MySQL refuses a subquery on the table an INSERT … VALUES writes to;
		// INSERT … SELECT it takes.
		s.Insert = "INSERT INTO " + t + " (" + insertCols + ", position) SELECT " + strings.Join(vals, ", ") + ", COALESCE(MAX(position), 0) + 1 FROM " + t
	case m.Sortable:
		s.Insert = "INSERT INTO " + t + " (" + insertCols + ", position) VALUES (" + strings.Join(vals, ", ") + ", (SELECT COALESCE(MAX(position), 0) + 1 FROM " + t + "))"
	default:
		s.Insert = "INSERT INTO " + t + " (" + insertCols + ") VALUES (" + strings.Join(vals, ", ") + ")"
	}
	if d != "mysql" {
		s.Insert += " RETURNING id"
	}

	p = &numbering{dialect: d}
	s.ByKey = "SELECT " + cols + " FROM " + t + " WHERE " + keyCol + " = " + p.next() + gate(p)
	p = &numbering{dialect: d}
	s.IDOf = "SELECT id FROM " + t + " WHERE " + keyCol + " = " + p.next() + gate(p)
	p = &numbering{dialect: d}
	s.ByID = "SELECT " + cols + " FROM " + t + " WHERE id = " + p.next()

	p = &numbering{dialect: d}
	sets := make([]string, len(m.Fields))
	for i, f := range m.Fields {
		set, val := p.next(), p.next()
		sets[i] = f.Ident + " = CASE WHEN " + set + " THEN " + val + " ELSE " + f.Ident + " END"
	}
	s.Patch = "UPDATE " + t + " SET " + strings.Join(sets, ", ") + " WHERE id = " + p.next()

	p = &numbering{dialect: d}
	for i, f := range m.Fields {
		sets[i] = f.Ident + " = " + p.next()
	}
	s.Save = "UPDATE " + t + " SET " + strings.Join(sets, ", ") + " WHERE id = " + p.next()

	p = &numbering{dialect: d}
	s.Delete = "DELETE FROM " + t + " WHERE id = " + p.next()

	if m.Sortable {
		p = &numbering{dialect: d}
		where := ""
		if m.Gate != nil {
			where = " WHERE (" + p.next() + " OR " + m.Gate.Ident + ")"
		}
		s.Visible = "SELECT id, " + keyCol + ", position FROM " + t + where + " ORDER BY position, id"
		p = &numbering{dialect: d}
		s.SetPosition = "UPDATE " + t + " SET position = " + p.next() + " WHERE id = " + p.next()
	}

	m.SQL = s
	def := listSQL{}
	switch {
	case m.Sortable:
		def.Column, def.Slot = "position", "N"
	case m.Sort != nil:
		def.Field, def.Column, def.Slot = m.Sort, m.Sort.Ident, cursorSlot(m.Sort.Type)
	}
	m.SQL.Lists = append(m.SQL.Lists, m.listSQL(def))
	for i := range m.Fields {
		f := &m.Fields[i]
		if !f.Orderable {
			continue
		}
		for _, desc := range []bool{false, true} {
			l := listSQL{Name: f.JSON, Field: f, Column: f.Ident, Slot: cursorSlot(f.Type), Desc: desc}
			if desc {
				l.Name = "-" + f.JSON
			}
			m.SQL.Lists = append(m.SQL.Lists, m.listSQL(l))
		}
	}
}

// listSQL fills in l's statement: the gate, the filters, the cursor, the limit.
func (m *model) listSQL(l listSQL) listSQL {
	p := &numbering{dialect: m.Dialect}
	var where []string
	if m.Gate != nil {
		where = append(where, "("+p.next()+" OR "+m.Gate.Ident+")")
	}
	for _, f := range m.Fields {
		if f.Filterable {
			where = append(where, "("+p.next()+" OR "+f.Ident+" = "+p.next()+")")
		}
	}
	after, dir := ">", ""
	if l.Desc {
		after, dir = "<", " DESC"
	}
	if l.Column == "" {
		where = append(where, "("+p.next()+" OR id "+after+" "+p.next()+")")
		l.SQL = "SELECT " + m.SQL.Columns + " FROM " + m.Table + " WHERE " + strings.Join(where, " AND ") + " ORDER BY id" + dir
	} else {
		first, v1, v2, id := p.next(), p.next(), p.next(), p.next()
		where = append(where, "("+first+" OR "+l.Column+" "+after+" "+v1+" OR ("+l.Column+" = "+v2+" AND id "+after+" "+id+"))")
		l.SQL = "SELECT " + m.SQL.Columns + " FROM " + m.Table + " WHERE " + strings.Join(where, " AND ") + " ORDER BY " + l.Column + dir + ", id" + dir
	}
	l.SQL += " LIMIT " + p.next()
	return l
}

// Filters are the filterable fields.
func (m model) Filters() []mfield {
	var out []mfield
	for _, f := range m.Fields {
		if f.Filterable {
			out = append(out, f)
		}
	}
	return out
}

// OrderNames are the values the list's order parameter takes.
func (m model) OrderNames() string {
	var names []string
	for _, l := range m.SQL.Lists[1:] {
		names = append(names, l.Name)
	}
	return strings.Join(names, " ")
}

// Slots are the cursor's value fields the orders use, each once.
func (m model) Slots() [][2]string {
	seen := map[string]bool{}
	var out [][2]string
	for _, l := range m.SQL.Lists {
		if l.Slot == "" || seen[l.Slot] {
			continue
		}
		seen[l.Slot] = true
		typ := "int64"
		if l.Field != nil {
			typ = l.Field.Type
		}
		out = append(out, [2]string{l.Slot, typ})
	}
	return out
}

// ListItem is the type the list answers with.
func (m model) ListItem() string {
	if m.Listable {
		return "Res" + m.Entity + "Item"
	}
	return "Res" + m.Entity
}

// SlotUses reports whether a cursor field is of type typ.
func (m model) SlotUses(typ string) bool {
	for _, s := range m.Slots() {
		if s[1] == typ {
			return true
		}
	}
	return false
}
