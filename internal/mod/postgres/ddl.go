package postgres

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/api"
)

// Making and changing tables from a form. The server writes the SQL (so the
// dashboard, the CLI and agents get the same statements) and shows it with
// dryRun; identifiers are quoted and literals escaped.

// PGNewColumn is a column to add.
type PGNewColumn struct {
	Name       string       `json:"name" minLength:"1" maxLength:"63"`
	Type       string       `json:"type,omitempty" enum:"text,integer,bigint,numeric,boolean,timestamptz,date,uuid,jsonb,text[]" doc:"Optional with references: the linked column's type is used"`
	Nullable   bool         `json:"nullable,omitempty" doc:"Allow empty (NULL) values"`
	Default    *PGDefault   `json:"default,omitempty"`
	Unique     bool         `json:"unique,omitempty"`
	References *PGReference `json:"references,omitempty" doc:"Link each value to a row of another table"`
}

// PGDefault is what a column holds when a row doesn't say.
type PGDefault struct {
	Kind  string `json:"kind" enum:"value,now,today,random-uuid,empty-list,empty-object"`
	Value string `json:"value,omitempty" doc:"For kind value: the value as text, e.g. 0, draft, true"`
}

// PGReference is a foreign key target.
type PGReference struct {
	Schema   string `json:"schema,omitempty" doc:"Default public"`
	Table    string `json:"table"`
	Column   string `json:"column,omitempty" doc:"Default: the table's one-column primary key"`
	OnDelete string `json:"onDelete,omitempty" enum:"no-action,cascade,set-null,restrict" doc:"When the linked row is deleted (default no-action: the delete is refused)"`
}

// PGTableCreate makes a new table.
type PGTableCreate struct {
	Branch     string        `json:"branch,omitempty"`
	Schema     string        `json:"schema,omitempty" doc:"Default public"`
	Name       string        `json:"name" minLength:"1" maxLength:"63"`
	PrimaryKey string        `json:"primaryKey,omitempty" enum:"bigint-identity,uuid" doc:"The id column: a number Postgres counts up (default) or a random UUID"`
	Columns    []PGNewColumn `json:"columns" maxItems:"200"`
	DryRun     bool          `json:"dryRun,omitempty" doc:"Only return the SQL"`
}

// PGColumnChange changes one existing column.
type PGColumnChange struct {
	Column      string     `json:"column"`
	Nullable    *bool      `json:"nullable,omitempty"`
	Default     *PGDefault `json:"default,omitempty"`
	DropDefault bool       `json:"dropDefault,omitempty"`
	Unique      *bool      `json:"unique,omitempty"`
}

// PGRename renames a column.
type PGRename struct {
	From string `json:"from"`
	To   string `json:"to" minLength:"1" maxLength:"63"`
}

// PGTableAlter changes a table.
type PGTableAlter struct {
	Branch        string           `json:"branch,omitempty"`
	Rename        string           `json:"rename,omitempty" maxLength:"63" doc:"A new name for the table"`
	Add           []PGNewColumn    `json:"add,omitempty" maxItems:"100"`
	RenameColumns []PGRename       `json:"renameColumns,omitempty" maxItems:"100"`
	DropColumns   []string         `json:"dropColumns,omitempty" maxItems:"100" doc:"Their values are gone for good: needs confirm, and a snapshot is taken first"`
	Change        []PGColumnChange `json:"change,omitempty" maxItems:"100"`
	DryRun        bool             `json:"dryRun,omitempty" doc:"Only return the SQL"`
	Confirm       string           `json:"confirm,omitempty" doc:"With dropColumns: the confirm value from the preview (status 428)"`
}

// PGDDLResult is the SQL a table change ran (or would run).
type PGDDLResult struct {
	SQL      string `json:"sql" doc:"The statements, in one transaction"`
	Applied  bool   `json:"applied"`
	Schema   string `json:"schema"`
	Table    string `json:"table" doc:"The table's name afterwards"`
	Snapshot string `json:"snapshot,omitempty" doc:"Snapshot taken first (dropping columns or a table)"`
}

var columnTypes = map[string]string{
	"text": "text", "integer": "integer", "bigint": "bigint", "numeric": "numeric", "boolean": "boolean",
	"timestamptz": "timestamptz", "date": "date", "uuid": "uuid", "jsonb": "jsonb", "text[]": "text[]",
}

func checkName(what, s string) error {
	if s == "" || len(s) > 63 || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\n\r\t") {
		return api.NewProblem(422, "validation", fmt.Sprintf("%s name must be 1 to 63 bytes of text on one line", what))
	}
	return nil
}

// defaultSQL writes a column default.
func defaultSQL(d *PGDefault, typ string) (string, error) {
	switch d.Kind {
	case "value":
		return quoteLiteral(d.Value) + "::" + typ, nil
	case "now":
		return "now()", nil
	case "today":
		return "CURRENT_DATE", nil
	case "random-uuid":
		return "gen_random_uuid()", nil
	case "empty-list":
		return "'{}'", nil
	case "empty-object":
		return "'{}'::jsonb", nil
	}
	return "", api.NewProblem(422, "validation", fmt.Sprintf("unknown default %q", d.Kind))
}

// columnSQL writes a column definition, and an index for a link.
func columnSQL(ctx context.Context, c *pgx.Conn, table string, col PGNewColumn) (def, index string, err error) {
	if err := checkName("a column", col.Name); err != nil {
		return "", "", err
	}
	typ := columnTypes[col.Type]
	var ref string
	if r := col.References; r != nil {
		schema := orWord(r.Schema, "public")
		target, err := tableDetail(ctx, c, schema, r.Table, false)
		if err != nil {
			return "", "", err
		}
		refCol := r.Column
		if refCol == "" {
			if len(target.PrimaryKey) != 1 {
				return "", "", api.NewProblem(422, "validation", fmt.Sprintf("%s has no one-column primary key to link to; name the column", target.slug()))
			}
			refCol = target.PrimaryKey[0]
		}
		if !target.has(refCol) {
			return "", "", api.NewProblem(422, "validation", fmt.Sprintf("%s has no column %q", target.slug(), refCol))
		}
		if typ == "" {
			typ = target.column(refCol).Type
		}
		ref = fmt.Sprintf(" REFERENCES %s (%s)", target.ident(), quoteIdent(refCol))
		if od := r.OnDelete; od != "" && od != "no-action" {
			ref += " ON DELETE " + strings.ToUpper(strings.ReplaceAll(od, "-", " "))
		}
		index = fmt.Sprintf("CREATE INDEX ON %s (%s);", table, quoteIdent(col.Name))
	}
	if typ == "" {
		return "", "", api.NewProblem(422, "validation", fmt.Sprintf("%s needs a type", col.Name))
	}
	def = quoteIdent(col.Name) + " " + typ
	if !col.Nullable {
		def += " NOT NULL"
	}
	if col.Default != nil {
		d, err := defaultSQL(col.Default, typ)
		if err != nil {
			return "", "", err
		}
		def += " DEFAULT " + d
	}
	if col.Unique {
		def += " UNIQUE"
		index = "" // the unique constraint's index serves the link too
	}
	return def + ref, index, nil
}

// createTableSQL writes CREATE TABLE for a form.
func createTableSQL(ctx context.Context, c *pgx.Conn, in PGTableCreate) (string, error) {
	schema := orWord(in.Schema, "public")
	if err := checkName("the table", in.Name); err != nil {
		return "", err
	}
	table := quoteIdent(schema) + "." + quoteIdent(in.Name)
	defs := []string{`"id" bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY`}
	if in.PrimaryKey == "uuid" {
		defs[0] = `"id" uuid PRIMARY KEY DEFAULT gen_random_uuid()`
	}
	var after []string
	seen := map[string]bool{"id": true}
	for _, col := range in.Columns {
		if seen[col.Name] {
			return "", api.NewProblem(422, "validation", fmt.Sprintf("two columns are called %s", col.Name))
		}
		seen[col.Name] = true
		def, idx, err := columnSQL(ctx, c, table, col)
		if err != nil {
			return "", err
		}
		defs = append(defs, def)
		if idx != "" {
			after = append(after, idx)
		}
	}
	sql := fmt.Sprintf("CREATE TABLE %s (\n  %s\n);", table, strings.Join(defs, ",\n  "))
	if len(after) > 0 {
		sql += "\n" + strings.Join(after, "\n")
	}
	return sql, nil
}

// alterTableSQL writes the statements for a table change.
func alterTableSQL(ctx context.Context, c *pgx.Conn, t *PGTableDetail, in PGTableAlter) (string, error) {
	table := t.ident()
	var stmts []string
	for _, r := range in.RenameColumns {
		if !t.has(r.From) {
			return "", api.NewProblem(422, "validation", fmt.Sprintf("%s has no column %q", t.slug(), r.From))
		}
		if err := checkName("a column", r.To); err != nil {
			return "", err
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", table, quoteIdent(r.From), quoteIdent(r.To)))
	}
	for _, d := range in.DropColumns {
		if !t.has(d) {
			return "", api.NewProblem(422, "validation", fmt.Sprintf("%s has no column %q", t.slug(), d))
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", table, quoteIdent(d)))
	}
	for _, col := range in.Add {
		def, idx, err := columnSQL(ctx, c, table, col)
		if err != nil {
			return "", err
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", table, def))
		if idx != "" {
			stmts = append(stmts, idx)
		}
	}
	for _, ch := range in.Change {
		if !t.has(ch.Column) {
			return "", api.NewProblem(422, "validation", fmt.Sprintf("%s has no column %q", t.slug(), ch.Column))
		}
		col := quoteIdent(ch.Column)
		if ch.Nullable != nil {
			verb := "SET"
			if *ch.Nullable {
				verb = "DROP"
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s NOT NULL;", table, col, verb))
		}
		if ch.DropDefault {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", table, col))
		} else if ch.Default != nil {
			d, err := defaultSQL(ch.Default, t.column(ch.Column).Type)
			if err != nil {
				return "", err
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", table, col, d))
		}
		if ch.Unique != nil && *ch.Unique != t.column(ch.Column).Unique {
			if *ch.Unique {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD UNIQUE (%s);", table, col))
			} else {
				var con string
				err := c.QueryRow(ctx, `SELECT con.conname FROM pg_constraint con JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = con.conkey[1]
					WHERE con.conrelid = $1::regclass AND con.contype = 'u' AND array_length(con.conkey, 1) = 1 AND a.attname = $2`, table, ch.Column).Scan(&con)
				if err != nil {
					return "", api.NewProblem(422, "validation", fmt.Sprintf("%s is unique through an index, not a constraint: drop the index with sql write", ch.Column))
				}
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", table, quoteIdent(con)))
			}
		}
	}
	if in.Rename != "" && in.Rename != t.Name {
		if err := checkName("the table", in.Rename); err != nil {
			return "", err
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", table, quoteIdent(in.Rename)))
	}
	if len(stmts) == 0 {
		return "", api.NewProblem(422, "validation", "nothing to change")
	}
	return strings.Join(stmts, "\n"), nil
}

// runDDL runs statements in one transaction as the project's role.
func runDDL(ctx context.Context, c *pgx.Conn, sql string) error {
	if err := c.PgConn().Exec(ctx, "BEGIN;\n"+sql+"\nCOMMIT;").Close(); err != nil {
		_ = c.PgConn().Exec(context.WithoutCancel(ctx), "ROLLBACK").Close()
		return ddlError(err)
	}
	return nil
}

// ddlError says common DDL failures in plain words.
func ddlError(err error) error {
	p, ok := sqlError(err).(*api.Problem)
	if !ok {
		return err
	}
	switch {
	case strings.HasPrefix(p.Detail, "Postgres error 42P07"):
		p.Detail, p.Hint = "a table with that name already exists", "pick another name"
	case strings.HasPrefix(p.Detail, "Postgres error 42701"):
		p.Detail, p.Hint = "a column with that name already exists", "pick another name"
	case strings.HasPrefix(p.Detail, "Postgres error 23502"):
		p.Detail, p.Hint = "some rows have no value in that column", "fill those rows in first, or give the column a default"
	case strings.HasPrefix(p.Detail, "Postgres error 23505"):
		p.Detail, p.Hint = "some rows share a value in that column, so it can't be unique", "find them with a GROUP BY … HAVING count(*) > 1 query and fix them first"
	case strings.HasPrefix(p.Detail, "Postgres error 2BP01"):
		p.Hint = "other tables link to it: remove those links first"
	case strings.HasPrefix(p.Detail, "Postgres error 42804"):
		p.Hint = "the default doesn't fit the column's type"
	}
	return p
}
