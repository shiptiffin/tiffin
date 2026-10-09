package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/api"
)

// The table editor's view of a table: columns with what the editor needs to
// pick a cell editor (base type, enum labels, identity, generated), keys,
// links to and from other tables, indexes and a row count.

// The box owns every schema named tiffin_* in a project database
// (tiffin_auth: sign-in and its RLS helpers, tiffin_queue: the outbox);
// everything else is the app's. libraryManagedSchemas are the app's too, but
// a library it uses names and migrates them (the Workflow DevKit's Postgres
// world, pg-boss). The dashboard shows both read-only unless asked.
var libraryManagedSchemas = []string{"workflow", "workflow_drizzle", "graphile_worker", "pgboss"}

// Managed reports whether a schema is managed by Tiffin or a library.
func Managed(schema string) bool {
	return strings.HasPrefix(schema, "tiffin_") || slices.Contains(libraryManagedSchemas, schema)
}

// PGForeignKey is a link from some columns of one table to another table.
type PGForeignKey struct {
	Name       string   `json:"name" doc:"Constraint name"`
	Schema     string   `json:"schema" doc:"The table holding the link"`
	Table      string   `json:"table"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete" enum:"no-action,restrict,cascade,set-null,set-default"`
}

// PGColumnDetail is one column as the table editor sees it.
type PGColumnDetail struct {
	Name      string   `json:"name"`
	Type      string   `json:"type" doc:"SQL type as Postgres prints it, e.g. \"numeric(10,2)\", \"text[]\""`
	BaseType  string   `json:"baseType" doc:"The type's internal name: int8, text, bool, timestamptz, jsonb, _text (arrays start with _), or an enum's name"`
	Category  string   `json:"category" enum:"number,text,bool,date,time,timestamp,json,uuid,enum,array,other" doc:"Which editor fits"`
	Nullable  bool     `json:"nullable"`
	Default   *string  `json:"default,omitempty" doc:"Default expression"`
	Primary   bool     `json:"primary,omitempty"`
	Unique    bool     `json:"unique,omitempty" doc:"Has a one-column unique constraint or index"`
	Identity  string   `json:"identity,omitempty" enum:"always,by-default," doc:"Filled by an identity sequence"`
	Generated bool     `json:"generated,omitempty" doc:"Computed from other columns: never written"`
	Enum      []string `json:"enum,omitempty" doc:"The allowed values of an enum column, in order"`
	Comment   string   `json:"comment,omitempty"`
}

// PGIndex is one index of a table.
type PGIndex struct {
	Name       string `json:"name"`
	Definition string `json:"definition" doc:"CREATE INDEX statement"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// PGTableDetail is one table, view or materialized view in full.
type PGTableDetail struct {
	Schema       string           `json:"schema"`
	Name         string           `json:"name"`
	Kind         string           `json:"kind" enum:"table,partitioned,view,materialized-view,foreign"`
	Managed      bool             `json:"managed" doc:"In a schema Tiffin or a library manages (tiffin, tiffin_*, workflow, workflow_drizzle, graphile_worker, pgboss)"`
	Rows         int64            `json:"rows" doc:"Row count: exact when rowsExact, else Postgres's estimate"`
	RowsExact    bool             `json:"rowsExact"`
	SizeBytes    int64            `json:"sizeBytes"`
	RLS          bool             `json:"rls"`
	Comment      string           `json:"comment,omitempty"`
	Columns      []PGColumnDetail `json:"columns"`
	PrimaryKey   []string         `json:"primaryKey" doc:"Primary key columns; empty when the table has none (its rows are read-only in the editor)"`
	Label        string           `json:"label,omitempty" doc:"The column that names a row (name, title, email…), shown for links to it"`
	ForeignKeys  []PGForeignKey   `json:"foreignKeys" doc:"Links from this table to others"`
	ReferencedBy []PGForeignKey   `json:"referencedBy" doc:"Links from other tables to this one"`
	Indexes      []PGIndex        `json:"indexes"`
	Checks       []string         `json:"checks,omitempty" doc:"CHECK constraints, as SQL"`
	Writable     bool             `json:"writable" doc:"Rows can be edited by key (a table with a primary key)"`
}

// exactCountUnder counts a table exactly when it has fewer rows than this.
const exactCountUnder = 200_000

// tableDetail introspects schema.name through c (the project's role sees
// what it may see). With count, the row count is exact for small tables.
func tableDetail(ctx context.Context, c *pgx.Conn, schema, name string, count bool) (*PGTableDetail, error) {
	t := &PGTableDetail{Schema: schema, Name: name, Managed: Managed(schema), Columns: []PGColumnDetail{}, PrimaryKey: []string{},
		ForeignKeys: []PGForeignKey{}, ReferencedBy: []PGForeignKey{}, Indexes: []PGIndex{}}
	var oid uint32
	var kind string
	var comment *string
	err := c.QueryRow(ctx, `
SELECT c.oid, c.relkind::text, c.relrowsecurity, pg_total_relation_size(c.oid), greatest(c.reltuples, 0)::bigint, obj_description(c.oid, 'pg_class')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m','f')`, schema, name).
		Scan(&oid, &kind, &t.RLS, &t.SizeBytes, &t.Rows, &comment)
	if errors.Is(err, pgx.ErrNoRows) {
		p := api.NewProblem(404, "not_found", fmt.Sprintf("there is no table %s.%s", schema, name))
		p.Hint = "list them with `tiffin db tables <project>`"
		return nil, p
	}
	if err != nil {
		return nil, sqlError(err)
	}
	t.Kind = kindName(kind)
	if comment != nil {
		t.Comment = *comment
	}
	cols, err := c.Query(ctx, `
SELECT a.attname, format_type(a.atttypid, a.atttypmod), t.typname, t.typcategory::text,
       coalesce(et.typtype = 'e', false) OR t.typtype = 'e', NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid),
       a.attidentity::text, a.attgenerated::text,
       coalesce((SELECT array_agg(e.enumlabel::text ORDER BY e.enumsortorder) FROM pg_enum e
                 WHERE e.enumtypid = CASE WHEN t.typcategory = 'A' THEN t.typelem ELSE t.oid END), '{}'),
       coalesce(col_description(a.attrelid, a.attnum), '')
FROM pg_attribute a JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_type et ON et.oid = t.typelem AND t.typcategory = 'A'
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, sqlError(err)
	}
	for cols.Next() {
		var col PGColumnDetail
		var cat, ident, gen string
		var isEnum bool
		if err := cols.Scan(&col.Name, &col.Type, &col.BaseType, &cat, &isEnum, &col.Nullable, &col.Default, &ident, &gen, &col.Enum, &col.Comment); err != nil {
			cols.Close()
			return nil, err
		}
		col.Category = category(col.BaseType, cat, isEnum)
		col.Identity = map[string]string{"a": "always", "d": "by-default"}[ident]
		col.Generated = gen != ""
		if len(col.Enum) == 0 {
			col.Enum = nil
		}
		t.Columns = append(t.Columns, col)
	}
	cols.Close()
	if err := cols.Err(); err != nil {
		return nil, err
	}
	fks, err := foreignKeys(ctx, c, `con.conrelid = $1`, oid)
	if err != nil {
		return nil, err
	}
	t.ForeignKeys = fks
	if t.ReferencedBy, err = foreignKeys(ctx, c, `con.confrelid = $1`, oid); err != nil {
		return nil, err
	}
	cons, err := c.Query(ctx, `
SELECT con.contype::text, ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(n, i)
       JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.i), pg_get_constraintdef(con.oid)
FROM pg_constraint con WHERE con.conrelid = $1 AND con.contype IN ('p','u','c') ORDER BY con.conname`, oid)
	if err != nil {
		return nil, sqlError(err)
	}
	for cons.Next() {
		var typ, def string
		var on []string
		if err := cons.Scan(&typ, &on, &def); err != nil {
			cons.Close()
			return nil, err
		}
		switch typ {
		case "p":
			t.PrimaryKey = on
		case "u":
			if len(on) == 1 {
				t.column(on[0]).Unique = true
			}
		case "c":
			t.Checks = append(t.Checks, def)
		}
	}
	cons.Close()
	if err := cons.Err(); err != nil {
		return nil, err
	}
	idx, err := c.Query(ctx, `
SELECT ic.relname, pg_get_indexdef(i.indexrelid), i.indisunique, i.indisprimary, pg_relation_size(i.indexrelid),
       CASE WHEN i.indnatts = 1 AND i.indexprs IS NULL AND i.indpred IS NULL
            THEN (SELECT a.attname::text FROM pg_attribute a WHERE a.attrelid = i.indrelid AND a.attnum = i.indkey[0]) END
FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
WHERE i.indrelid = $1 ORDER BY i.indisprimary DESC, ic.relname`, oid)
	if err != nil {
		return nil, sqlError(err)
	}
	for idx.Next() {
		var x PGIndex
		var single *string
		if err := idx.Scan(&x.Name, &x.Definition, &x.Unique, &x.Primary, &x.SizeBytes, &single); err != nil {
			idx.Close()
			return nil, err
		}
		if x.Unique && single != nil {
			t.column(*single).Unique = true
		}
		t.Indexes = append(t.Indexes, x)
	}
	idx.Close()
	if err := idx.Err(); err != nil {
		return nil, err
	}
	for _, k := range t.PrimaryKey {
		col := t.column(k)
		col.Primary, col.Unique = true, false
	}
	t.Writable = len(t.PrimaryKey) > 0 && (t.Kind == "table" || t.Kind == "partitioned")
	t.Label = labelColumn(t.Columns)
	if count && t.Kind != "view" && t.Kind != "foreign" {
		var n int64
		if err := c.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s LIMIT %d) s`, t.ident(), exactCountUnder)).Scan(&n); err == nil && n < exactCountUnder {
			t.Rows, t.RowsExact = n, true
		}
	}
	return t, nil
}

// foreignKeys lists foreign keys matching where ($1 is a table oid, or an
// array of them with = ANY).
func foreignKeys(ctx context.Context, c *pgx.Conn, where string, arg any) ([]PGForeignKey, error) {
	rows, err := c.Query(ctx, `
SELECT con.conname, n.nspname, cl.relname,
       ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(n, i) JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.i),
       rn.nspname, rc.relname,
       ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY k(n, i) JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n ORDER BY k.i),
       con.confdeltype::text
FROM pg_constraint con
JOIN pg_class cl ON cl.oid = con.conrelid JOIN pg_namespace n ON n.oid = cl.relnamespace
JOIN pg_class rc ON rc.oid = con.confrelid JOIN pg_namespace rn ON rn.oid = rc.relnamespace
WHERE con.contype = 'f' AND `+where+` ORDER BY n.nspname, cl.relname, con.conname`, arg)
	if err != nil {
		return nil, sqlError(err)
	}
	defer rows.Close()
	out := []PGForeignKey{}
	for rows.Next() {
		var f PGForeignKey
		var del string
		if err := rows.Scan(&f.Name, &f.Schema, &f.Table, &f.Columns, &f.RefSchema, &f.RefTable, &f.RefColumns, &del); err != nil {
			return nil, err
		}
		f.OnDelete = map[string]string{"a": "no-action", "r": "restrict", "c": "cascade", "n": "set-null", "d": "set-default"}[del]
		out = append(out, f)
	}
	return out, rows.Err()
}

func kindName(relkind string) string {
	return map[string]string{"r": "table", "p": "partitioned", "v": "view", "m": "materialized-view", "f": "foreign"}[relkind]
}

// category picks the editor for a column from its type.
func category(base, typcategory string, isEnum bool) string {
	switch {
	case typcategory == "A":
		return "array"
	case isEnum:
		return "enum"
	case base == "bool":
		return "bool"
	case base == "json" || base == "jsonb":
		return "json"
	case base == "uuid":
		return "uuid"
	case base == "date":
		return "date"
	case base == "time" || base == "timetz":
		return "time"
	case base == "timestamp" || base == "timestamptz":
		return "timestamp"
	case typcategory == "N":
		return "number"
	case typcategory == "S":
		return "text"
	}
	return "other"
}

// labelColumn picks the column that names a row: a well-known name first,
// then the first text column that isn't the key.
func labelColumn(cols []PGColumnDetail) string {
	for _, want := range []string{"name", "title", "label", "display_name", "full_name", "username", "email", "slug", "sku", "code"} {
		for _, c := range cols {
			if strings.EqualFold(c.Name, want) && (c.Category == "text" || c.Category == "enum") {
				return c.Name
			}
		}
	}
	for _, c := range cols {
		if c.Category == "text" && !c.Primary {
			return c.Name
		}
	}
	return ""
}

func (t *PGTableDetail) column(name string) *PGColumnDetail {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return &PGColumnDetail{}
}

func (t *PGTableDetail) has(name string) bool {
	for _, c := range t.Columns {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (t *PGTableDetail) ident() string { return quoteIdent(t.Schema) + "." + quoteIdent(t.Name) }

// slug is how people name a table: "books", or "auth.users" outside public.
func (t *PGTableDetail) slug() string {
	if t.Schema == "public" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}
