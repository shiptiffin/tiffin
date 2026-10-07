package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
)

// SQLRequest runs SQL against a project's database.
type PGSQLRequest struct {
	SQL            string `json:"sql" minLength:"1" maxLength:"200000" doc:"The SQL. sql runs exactly one statement; sql_write may run several, separated by semicolons (they run in one implicit transaction)."`
	Branch         string `json:"branch,omitempty" doc:"Run against this preview branch instead of the main database."`
	Params         []any  `json:"params,omitempty" doc:"Values for $1, $2, ... (strings, numbers, booleans or null). With params only one statement runs."`
	Limit          int    `json:"limit,omitempty" minimum:"0" maximum:"10000" doc:"Maximum rows returned per statement (default 500). Extra rows are counted, not returned."`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"600" doc:"Statement timeout (default 30 seconds)."`
}

// Column describes one result column.
type PGColumn struct {
	Name string `json:"name"`
	Type string `json:"type" doc:"Postgres type name, e.g. int8, text, jsonb, timestamptz"`
}

// StatementResult is one statement's outcome.
type PGStatementResult struct {
	Command   string     `json:"command" doc:"The command tag, e.g. \"SELECT 3\", \"INSERT 0 1\", \"CREATE TABLE\""`
	Columns   []PGColumn `json:"columns,omitempty"`
	Rows      [][]any    `json:"rows,omitempty" doc:"Rows as arrays in column order. Numbers and booleans are JSON values, json/jsonb are embedded, everything else is text."`
	RowCount  int64      `json:"rowCount" doc:"Rows returned or affected"`
	Truncated bool       `json:"truncated,omitempty" doc:"More rows matched than the limit; only the first ones are returned"`
}

// SQLResult is the outcome of an SQL request.
type PGSQLResult struct {
	Database   string              `json:"database"`
	ReadOnly   bool                `json:"readOnly"`
	Results    []PGStatementResult `json:"results"`
	DurationMs int64               `json:"durationMs"`
	Snapshot   string              `json:"snapshot,omitempty" doc:"Snapshot taken before a write; restore it with snapshots restore"`
}

// RunSQL runs SQL against a project's database. With write it runs as the
// project's own role. Without, it runs as the project's read role, and
// read-only is enforced by Postgres: one statement (extended protocol),
// inside a READ ONLY transaction that is always rolled back.
func RunSQL(ctx context.Context, p *platform.Platform, project string, in PGSQLRequest, write bool) (*PGSQLResult, error) {
	db, err := targetDatabase(ctx, p, project, in.Branch)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 500
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	out := &PGSQLResult{Database: db, ReadOnly: !write, Results: []PGStatementResult{}}
	if write {
		if err := heldProblem(project); err != nil {
			return nil, err
		}
		branch := in.Branch
		if branch == "main" {
			branch = ""
		}
		snap, err := takeSnapshot(ctx, project, db, branch, "before an SQL write")
		if err != nil {
			return nil, fmt.Errorf("snapshot before writing (nothing ran): %w", err)
		}
		out.Snapshot = snap.ID
	}
	start := time.Now()
	var conn *pgx.Conn
	if write {
		conn, err = roleConn(ctx, p, project, db, timeout, false)
	} else {
		conn, err = readerConn(ctx, p, project, mainBranch(in.Branch), db, timeout)
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	pc := conn.PgConn()
	params, err := encodeParams(in.Params)
	if err != nil {
		return nil, err
	}
	budget := maxResultBytes
	switch {
	case !write:
		if err := pc.Exec(ctx, "BEGIN TRANSACTION READ ONLY").Close(); err != nil {
			return nil, sqlError(err)
		}
		r, err := readResult(pc.ExecParams(ctx, in.SQL, params, nil, nil, nil), limit, &budget)
		_ = pc.Exec(context.Background(), "ROLLBACK").Close()
		if err != nil {
			return nil, sqlError(err)
		}
		out.Results = append(out.Results, *r)
	case len(params) > 0:
		r, err := readResult(pc.ExecParams(ctx, in.SQL, params, nil, nil, nil), limit, &budget)
		if err != nil {
			return nil, sqlError(err)
		}
		out.Results = append(out.Results, *r)
	default:
		mrr := pc.Exec(ctx, in.SQL)
		for mrr.NextResult() {
			r, err := readResult(mrr.ResultReader(), limit, &budget)
			if err != nil {
				_ = mrr.Close()
				return nil, sqlError(err)
			}
			out.Results = append(out.Results, *r)
		}
		if err := mrr.Close(); err != nil {
			return nil, sqlError(err)
		}
	}
	out.DurationMs = time.Since(start).Milliseconds()
	nameExtensionTypes(ctx, conn.PgConn(), out.Results)
	return out, nil
}

// nameExtensionTypes replaces "oid:N" column types (extension types such as
// vector) with their names from pg_type.
func nameExtensionTypes(ctx context.Context, pc *pgconn.PgConn, results []PGStatementResult) {
	var oids []string
	for _, r := range results {
		for _, c := range r.Columns {
			if n, ok := strings.CutPrefix(c.Type, "oid:"); ok {
				oids = append(oids, n)
			}
		}
	}
	if len(oids) == 0 {
		return
	}
	rows, err := pc.Exec(ctx, "SELECT oid::text, typname FROM pg_type WHERE oid IN ("+strings.Join(oids, ",")+")").ReadAll()
	if err != nil || len(rows) == 0 {
		return
	}
	names := map[string]string{}
	for _, row := range rows[0].Rows {
		names["oid:"+string(row[0])] = string(row[1])
	}
	for i := range results {
		for j, c := range results[i].Columns {
			if n, ok := names[c.Type]; ok {
				results[i].Columns[j].Type = n
			}
		}
	}
}

func encodeParams(in []any) ([][]byte, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([][]byte, len(in))
	for i, v := range in {
		switch x := v.(type) {
		case nil:
			out[i] = nil
		case string:
			out[i] = []byte(x)
		case bool:
			out[i] = []byte(strconv.FormatBool(x))
		case float64:
			out[i] = []byte(strconv.FormatFloat(x, 'f', -1, 64))
		case json.Number:
			out[i] = []byte(x.String())
		case map[string]any, []any:
			b, _ := json.Marshal(x)
			out[i] = b
		default:
			return nil, api.NewProblem(422, "validation", fmt.Sprintf("params[%d]: unsupported value %T", i, v))
		}
	}
	return out, nil
}

var typeMap = pgtype.NewMap()

func typeName(oid uint32) string {
	if t, ok := typeMap.TypeForOID(oid); ok {
		return t.Name
	}
	return "oid:" + strconv.FormatUint(uint64(oid), 10)
}

// Limits on what one SQL request keeps in memory: each value is cut to
// maxCellBytes, and rows stop being kept (still counted, the result marked
// truncated) once they hold maxResultBytes. Rows are counted by the limit
// a request gives too.
const (
	maxCellBytes   = 100_000
	maxResultBytes = 32 << 20
)

// readResult reads one result, keeping at most limit rows and what fits in
// budget (bytes, shared by the request's results). Values are copied: the
// driver reuses its buffer for the next row.
func readResult(rr *pgconn.ResultReader, limit int, budget *int) (*PGStatementResult, error) {
	fields := rr.FieldDescriptions()
	r := &PGStatementResult{}
	for _, f := range fields {
		r.Columns = append(r.Columns, PGColumn{Name: f.Name, Type: typeName(f.DataTypeOID)})
	}
	var n int64
	for rr.NextRow() {
		n++
		if r.Truncated {
			continue
		}
		vals := rr.Values()
		size := 0
		for _, v := range vals {
			size += min(len(v), maxCellBytes) + 8
		}
		if int(n) > limit || size > *budget {
			r.Truncated = true
			continue
		}
		*budget -= size
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = textValue(fields[i], v)
		}
		r.Rows = append(r.Rows, row)
	}
	tag, err := rr.Close()
	if err != nil {
		return nil, err
	}
	r.Command = tag.String()
	if len(fields) > 0 {
		r.RowCount = n
	} else {
		r.RowCount = tag.RowsAffected()
	}
	return r, nil
}

// textValue turns a text-format value into a JSON-friendly one, never
// keeping v itself (the driver's buffer) and never more than maxCellBytes
// of it.
func textValue(f pgconn.FieldDescription, v []byte) any {
	if v == nil {
		return nil
	}
	if f.Format != 0 {
		return bytes.Clone(v[:min(len(v), maxCellBytes)]) // binary (never requested): base64 in JSON
	}
	switch f.DataTypeOID {
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.OIDOID:
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return n
		}
	case pgtype.Int8OID:
		// Beyond 2^53 a JSON number loses precision: keep big ones as text.
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil && n < 1<<53 && n > -(1<<53) {
			return n
		}
	case pgtype.Float4OID, pgtype.Float8OID:
		if s := string(v); !strings.ContainsAny(s, "IN") {
			if x, err := strconv.ParseFloat(s, 64); err == nil {
				return x
			}
		}
	case pgtype.BoolOID:
		return len(v) == 1 && v[0] == 't'
	case pgtype.JSONOID, pgtype.JSONBOID:
		if len(v) <= maxCellBytes && json.Valid(v) {
			return json.RawMessage(bytes.Clone(v))
		}
	}
	cut := len(v) > maxCellBytes
	if cut {
		n := maxCellBytes
		for n > 0 && !utf8.RuneStart(v[n]) {
			n--
		}
		v = v[:n]
	}
	if !utf8.Valid(v) {
		return bytes.Clone(v)
	}
	if cut {
		return string(v) + "…"
	}
	return string(v)
}

// sqlError reports a Postgres error as a 422 with its SQLSTATE and position,
// so agents can fix the statement.
func sqlError(err error) error {
	var big *pgproto3.ExceededMaxBodyLenErr
	if errors.As(err, &big) {
		p := api.NewProblem(422, "validation", fmt.Sprintf("a row of the result is larger than %d MiB", maxRowBytes>>20))
		p.Hint = "select fewer or smaller columns, e.g. left(col, 1000) or length(col)"
		return p
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg := pe.Message
		if pe.Detail != "" {
			msg += " (" + pe.Detail + ")"
		}
		p := api.NewProblem(422, "validation", fmt.Sprintf("Postgres error %s: %s", pe.Code, msg))
		switch {
		case pe.Code == "25006":
			p.Hint = "this statement writes; run it with sql_write (tiffin sql write) instead: it needs a key with full access and takes a snapshot first"
		case pe.Code == "42601" && strings.Contains(pe.Message, "multiple commands"):
			p.Hint = "read-only mode runs one statement at a time; send them separately"
		case pe.Code == "57014":
			p.Hint = "the statement hit the timeout; raise timeoutSeconds or add a LIMIT"
		case pe.Hint != "":
			p.Hint = pe.Hint
		}
		if pe.Position > 0 {
			p.Errors = []api.FieldError{{Path: "/sql", Message: fmt.Sprintf("at character %d", pe.Position)}}
		}
		return p
	}
	return err
}
