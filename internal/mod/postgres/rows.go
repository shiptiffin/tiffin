package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/api"
	"github.com/jackc/pgx/v5/pgconn"
)

// Browsing a table: filters, sort and keyset pages. Every value a person
// typed travels as a query parameter ($1, $2, …); only identifiers, quoted,
// are written into the SQL.

// PGFilter narrows the rows of a table.
type PGFilter struct {
	Column string   `json:"column"`
	Op     string   `json:"op" enum:"eq,neq,lt,lte,gt,gte,contains,startsWith,isNull,notNull,in" doc:"contains and startsWith ignore case and compare the value as text"`
	Value  string   `json:"value,omitempty" doc:"The value, as text (unused for isNull and notNull)"`
	Values []string `json:"values,omitempty" doc:"For in: the values"`
}

// PGSort orders rows by a column.
type PGSort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

// PGRowsRequest asks for a page of a table's rows.
type PGRowsRequest struct {
	Branch  string     `json:"branch,omitempty" doc:"A preview branch instead of the main database"`
	Filters []PGFilter `json:"filters,omitempty" maxItems:"20" doc:"All must match"`
	Sort    []PGSort   `json:"sort,omitempty" maxItems:"5" doc:"The primary key breaks ties"`
	After   string     `json:"after,omitempty" maxLength:"100000" doc:"The next cursor of the previous page"`
	Limit   int        `json:"limit,omitempty" minimum:"0" maximum:"1000" doc:"Rows per page (default 100)"`
	Count   bool       `json:"count,omitempty" doc:"Also count the rows that match (up to 100,000)"`
}

// PGRows is one page of a table's rows.
type PGRows struct {
	Columns     []PGColumn                   `json:"columns"`
	Rows        [][]any                      `json:"rows" doc:"Rows in column order, as in sql results"`
	Next        string                       `json:"next,omitempty" doc:"Pass as after for the next page; absent on the last page"`
	Count       *int64                       `json:"count,omitempty" doc:"Rows that match, with count"`
	CountCapped bool                         `json:"countCapped,omitempty" doc:"More rows match than were counted"`
	Labels      map[string]map[string]string `json:"labels,omitempty" doc:"For each column that links to another table: the linked row's key (as text) → its label"`
}

// countCap is where counting matching rows stops.
const countCap = 100_000

// params collects query parameters in text format; nil is NULL.
type params struct{ args [][]byte }

func (p *params) add(v []byte) string {
	p.args = append(p.args, v)
	return "$" + strconv.Itoa(len(p.args))
}

func (p *params) text(s string) string { return p.add([]byte(s)) }

// cursor is a page boundary: the order columns of the last row (keyset), or
// an offset for views, which have no key.
type cursor struct {
	Key    []*string `json:"k,omitempty"`
	Offset int       `json:"o,omitempty"`
}

func (c cursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil {
		return c, api.NewProblem(422, "validation", "after is not a cursor from this table's pages")
	}
	return c, nil
}

// orderKey is one ORDER BY term and how its keyset condition is written.
type orderKey struct {
	expr string // quoted column, or ctid
	desc bool
	tid  bool // ctid: cast the parameter
	pos  int  // its position in the select list
}

// filterSQL turns filters into a WHERE clause (without the WHERE).
func filterSQL(t *PGTableDetail, fs []PGFilter, ps *params) (string, error) {
	var conds []string
	for _, f := range fs {
		if !t.has(f.Column) {
			return "", api.NewProblem(422, "validation", fmt.Sprintf("%s has no column %q", t.slug(), f.Column))
		}
		col := quoteIdent(f.Column)
		switch f.Op {
		case "eq", "neq", "lt", "lte", "gt", "gte":
			op := map[string]string{"eq": "=", "neq": "IS DISTINCT FROM", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[f.Op]
			conds = append(conds, fmt.Sprintf("%s %s %s", col, op, ps.text(f.Value)))
		case "contains", "startsWith":
			pat := likeEscape(f.Value) + "%"
			if f.Op == "contains" {
				pat = "%" + pat
			}
			conds = append(conds, fmt.Sprintf("%s::text ILIKE %s", col, ps.text(pat)))
		case "isNull":
			conds = append(conds, col+" IS NULL")
		case "notNull":
			conds = append(conds, col+" IS NOT NULL")
		case "in":
			if len(f.Values) == 0 || len(f.Values) > 1000 {
				return "", api.NewProblem(422, "validation", "in takes 1 to 1,000 values")
			}
			ph := make([]string, len(f.Values))
			for i, v := range f.Values {
				ph[i] = ps.text(v)
			}
			conds = append(conds, fmt.Sprintf("%s IN (%s)", col, strings.Join(ph, ", ")))
		default:
			return "", api.NewProblem(422, "validation", fmt.Sprintf("unknown filter op %q", f.Op))
		}
	}
	return strings.Join(conds, " AND "), nil
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// keysetSQL is the condition for rows after the cursor in this order. NULLs
// sort last going up and first going down (the order clause says so).
func keysetSQL(keys []orderKey, vals []*string, ps *params) string {
	cast := func(k orderKey, ph string) string {
		if k.tid {
			return ph + "::tid"
		}
		return ph
	}
	var ors []string
	for i, k := range keys {
		var and []string
		for j := 0; j < i; j++ {
			if vals[j] == nil {
				and = append(and, keys[j].expr+" IS NULL")
			} else {
				and = append(and, fmt.Sprintf("%s = %s", keys[j].expr, cast(keys[j], ps.text(*vals[j]))))
			}
		}
		switch {
		case vals[i] == nil && !k.desc:
			continue // nothing sorts after NULL going up
		case vals[i] == nil:
			and = append(and, k.expr+" IS NOT NULL")
		case k.desc:
			and = append(and, fmt.Sprintf("%s < %s", k.expr, cast(k, ps.text(*vals[i]))))
		default:
			and = append(and, fmt.Sprintf("(%s > %s OR %s IS NULL)", k.expr, cast(k, ps.text(*vals[i])), k.expr))
		}
		ors = append(ors, "("+strings.Join(and, " AND ")+")")
	}
	if len(ors) == 0 {
		return "false"
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

// selectList is every column of t, quoted, in order.
func (t *PGTableDetail) selectList() string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = quoteIdent(c.Name)
	}
	return strings.Join(cols, ", ")
}

func (t *PGTableDetail) index(name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// queryRows reads one page of t. pc must be read-only (roleConn with
// readOnly): each statement runs on its own, so a link whose table the role
// may not read just goes unlabelled.
func queryRows(ctx context.Context, pc *pgconn.PgConn, t *PGTableDetail, in PGRowsRequest) (*PGRows, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 100
	}
	ps := &params{}
	where, err := filterSQL(t, in.Filters, ps)
	if err != nil {
		return nil, err
	}
	whereFilters, filterArgs := where, len(ps.args)
	sel := t.selectList()
	var keys []orderKey
	seen := map[string]bool{}
	for _, s := range in.Sort {
		i := t.index(s.Column)
		if i < 0 {
			return nil, api.NewProblem(422, "validation", fmt.Sprintf("%s has no column %q", t.slug(), s.Column))
		}
		if t.Columns[i].BaseType == "json" {
			return nil, api.NewProblem(422, "validation", fmt.Sprintf("%s is json, which Postgres can't sort; make it jsonb to sort by it", s.Column))
		}
		if !seen[s.Column] {
			seen[s.Column] = true
			keys = append(keys, orderKey{expr: quoteIdent(s.Column), desc: s.Desc, pos: i})
		}
	}
	keyed := true
	switch {
	case len(t.PrimaryKey) > 0:
		for _, k := range t.PrimaryKey {
			if !seen[k] {
				keys = append(keys, orderKey{expr: quoteIdent(k), pos: t.index(k)})
			}
		}
	case t.Kind == "table" || t.Kind == "materialized-view":
		sel += ", ctid::text"
		keys = append(keys, orderKey{expr: "ctid", tid: true, pos: len(t.Columns)})
	default:
		keyed = false // views: offset pages
	}
	var cur cursor
	if in.After != "" {
		if cur, err = decodeCursor(in.After); err != nil {
			return nil, err
		}
		if keyed && len(cur.Key) != len(keys) {
			return nil, api.NewProblem(422, "validation", "after belongs to a different sort; start from the first page")
		}
	}
	if keyed && cur.Key != nil {
		cond := keysetSQL(keys, cur.Key, ps)
		where = joinAnd(where, cond)
	}
	order := make([]string, len(keys))
	for i, k := range keys {
		if k.desc {
			order[i] = k.expr + " DESC NULLS FIRST"
		} else {
			order[i] = k.expr + " ASC NULLS LAST"
		}
	}
	q := fmt.Sprintf("SELECT %s FROM %s", sel, t.ident())
	if where != "" {
		q += " WHERE " + where
	}
	if len(order) > 0 {
		q += " ORDER BY " + strings.Join(order, ", ")
	}
	q += fmt.Sprintf(" LIMIT %d", limit+1)
	if !keyed && cur.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", cur.Offset)
	}
	fields, raw, err := readAll(pc.ExecParams(ctx, q, ps.args, nil, nil, nil))
	if err != nil {
		return nil, err
	}
	out := &PGRows{Columns: []PGColumn{}, Rows: [][]any{}}
	for _, f := range fields[:len(t.Columns)] {
		out.Columns = append(out.Columns, PGColumn{Name: f.Name, Type: typeName(f.DataTypeOID)})
	}
	for i, c := range t.Columns {
		if strings.HasPrefix(out.Columns[i].Type, "oid:") {
			out.Columns[i].Type = c.BaseType
		}
	}
	more := len(raw) > limit
	if more {
		raw = raw[:limit]
	}
	for _, r := range raw {
		row := make([]any, len(t.Columns))
		for j := range t.Columns {
			row[j] = textValue(fields[j], r[j])
		}
		out.Rows = append(out.Rows, row)
	}
	if more {
		last := raw[len(raw)-1]
		next := cursor{Offset: cur.Offset + limit}
		if keyed {
			next = cursor{Key: make([]*string, len(keys))}
			for i, k := range keys {
				if v := last[k.pos]; v != nil {
					s := string(v)
					next.Key[i] = &s
				}
			}
		}
		out.Next = next.encode()
	}
	if in.Count {
		cq := fmt.Sprintf("SELECT count(*) FROM (SELECT 1 FROM %s", t.ident())
		if whereFilters != "" {
			cq += " WHERE " + whereFilters
		}
		cq += fmt.Sprintf(" LIMIT %d) s", countCap+1)
		_, cr, err := readAll(pc.ExecParams(ctx, cq, ps.args[:filterArgs], nil, nil, nil))
		if err != nil {
			return nil, err
		}
		n, _ := strconv.ParseInt(string(cr[0][0]), 10, 64)
		out.CountCapped = n > countCap
		n = min(n, countCap)
		out.Count = &n
	}
	if out.Labels, err = linkLabels(ctx, pc, t, raw); err != nil {
		return nil, err
	}
	return out, nil
}

func joinAnd(a, b string) string {
	if a == "" {
		return b
	}
	return a + " AND " + b
}

// readAll reads a result in text format.
func readAll(rr *pgconn.ResultReader) ([]pgconn.FieldDescription, [][][]byte, error) {
	fields := rr.FieldDescriptions()
	var rows [][][]byte
	for rr.NextRow() {
		vals := rr.Values()
		row := make([][]byte, len(vals))
		for i, v := range vals {
			if v != nil {
				row[i] = append([]byte{}, v...)
			}
		}
		rows = append(rows, row)
	}
	if _, err := rr.Close(); err != nil {
		return nil, nil, err
	}
	return fields, rows, nil
}

// linkLabels finds, for each one-column link to another table that has a
// label column, the labels of the rows this page points at.
func linkLabels(ctx context.Context, pc *pgconn.PgConn, t *PGTableDetail, raw [][][]byte) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, fk := range t.ForeignKeys {
		if len(fk.Columns) != 1 {
			continue
		}
		pos := t.index(fk.Columns[0])
		seen := map[string]bool{}
		ps := &params{}
		var ph []string
		for _, r := range raw {
			if v := r[pos]; v != nil && !seen[string(v)] {
				seen[string(v)] = true
				ph = append(ph, ps.add(v))
			}
		}
		if len(ph) == 0 {
			continue
		}
		label, err := labelOf(ctx, pc, fk.RefSchema, fk.RefTable)
		if err != nil || label == "" {
			continue // a table the role can't read, or nothing to name rows by
		}
		ref := quoteIdent(fk.RefColumns[0])
		q := fmt.Sprintf("SELECT %s::text, %s::text FROM %s.%s WHERE %s IN (%s)", ref, quoteIdent(label),
			quoteIdent(fk.RefSchema), quoteIdent(fk.RefTable), ref, strings.Join(ph, ", "))
		_, rows, err := readAll(pc.ExecParams(ctx, q, ps.args, nil, nil, nil))
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		for _, r := range rows {
			if r[0] != nil && r[1] != nil {
				m[string(r[0])] = clip(string(r[1]), 200)
			}
		}
		out[fk.Columns[0]] = m
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// labelOf picks the label column of another table from the catalog.
func labelOf(ctx context.Context, pc *pgconn.PgConn, schema, table string) (string, error) {
	_, rows, err := readAll(pc.ExecParams(ctx, `
SELECT a.attname, t.typcategory::text, t.typtype = 'e', EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey))
FROM pg_attribute a JOIN pg_type t ON t.oid = a.atttypid
WHERE a.attrelid = (quote_ident($1) || '.' || quote_ident($2))::regclass AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`,
		[][]byte{[]byte(schema), []byte(table)}, nil, nil, nil))
	if err != nil {
		return "", err
	}
	cols := make([]PGColumnDetail, len(rows))
	for i, r := range rows {
		cols[i] = PGColumnDetail{Name: string(r[0]), Category: category("", string(r[1]), string(r[2]) == "t"), Primary: string(r[3]) == "t"}
	}
	return labelColumn(cols), nil
}
