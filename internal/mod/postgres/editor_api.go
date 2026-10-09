package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// The table editor's operations: a table in full, pages of rows, row edits
// with Undo, making and changing tables, and saved queries.

// editorConn opens the project's database (or a branch) as its role.
func editorConn(ctx context.Context, p *platform.Platform, project, branch string, write bool) (*pgx.Conn, string, error) {
	if err := onBox(p); err != nil {
		return nil, "", err
	}
	if write {
		if err := heldProblem(project); err != nil {
			return nil, "", err
		}
	}
	db, err := targetDatabase(ctx, p, project, branch)
	if err != nil {
		return nil, "", err
	}
	c, err := roleConn(ctx, p, project, db, 30*time.Second, !write)
	return c, db, err
}

func mainBranch(b string) string {
	if b == "main" {
		return ""
	}
	return b
}

func registerEditor(a huma.API, p *platform.Platform) {
	const tag = "postgres"

	td := api.Op("db-table", http.MethodGet, "/v1/projects/{project}/tables/{schema}/{table}", "db table", api.RiskRead,
		"Show one table in full",
		"A table's columns (type, which editor fits, nullable, default, primary key, identity, enum values), its links to and from other tables, "+
			"indexes, CHECK rules and a row count (exact under 200,000 rows). Tables without a primary key can't be edited by row.", tag)
	td.Errors = append(td.Errors, 404, 409)
	huma.Register(a, api.Untrusted(td), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Schema  string `path:"schema" maxLength:"63" doc:"Schema, usually public"`
		Table   string `path:"table" maxLength:"63" doc:"Table name"`
		Branch  string `query:"branch" doc:"A preview branch instead of the main database"`
	}) (*struct{ Body *PGTableDetail }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		c, _, err := editorConn(ctx, p, in.Project, in.Branch, false)
		if err != nil {
			return nil, err
		}
		defer c.Close(context.Background())
		out, err := tableDetail(ctx, c, in.Schema, in.Table, true)
		return &struct{ Body *PGTableDetail }{out}, err
	}))

	rq := api.Op("db-rows", http.MethodPost, "/v1/projects/{project}/tables/{schema}/{table}/rows/query", "db rows", api.RiskRead,
		"Read a table's rows",
		"A page of rows, filtered and sorted, with keyset paging (pass next as after). Values you filter by are sent as query parameters. "+
			"Links to other tables come with the linked rows' labels. Read-only.", tag)
	rq.Errors = append(rq.Errors, 404, 409)
	huma.Register(a, api.Untrusted(rq), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Schema  string `path:"schema" maxLength:"63" doc:"Schema, usually public"`
		Table   string `path:"table" maxLength:"63" doc:"Table name"`
		Body    PGRowsRequest
	}) (*struct{ Body *PGRows }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		c, _, err := editorConn(ctx, p, in.Project, in.Body.Branch, false)
		if err != nil {
			return nil, err
		}
		defer c.Close(context.Background())
		t, err := tableDetail(ctx, c, in.Schema, in.Table, false)
		if err != nil {
			return nil, err
		}
		out, err := queryRows(ctx, c.PgConn(), t, in.Body)
		if err != nil {
			return nil, sqlError(err)
		}
		return &struct{ Body *PGRows }{out}, nil
	}))

	// edit runs one row edit and records it.
	edit := func(ctx context.Context, project, schema, table, branch string, run func(*pgx.Conn, *PGTableDetail) (*PGEdit, *editRows, *PGEditResult, error)) (*PGEditResult, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, project); err != nil {
			return nil, err
		}
		c, db, err := editorConn(ctx, p, project, branch, true)
		if err != nil {
			return nil, err
		}
		defer c.Close(context.Background())
		t, err := tableDetail(ctx, c, schema, table, false)
		if err != nil {
			return nil, err
		}
		e, rec, res, err := run(c, t)
		if err != nil {
			return nil, err
		}
		e.Actor, e.Branch, e.Schema, e.Table = pr.Actor(), mainBranch(branch), t.Schema, t.Name
		if err := recordEdit(ctx, p, project, e, rec); err != nil {
			return nil, fmt.Errorf("the rows changed, but recording the edit for Undo failed: %w", err)
		}
		res.Edit = *e
		_ = p.DB.Audit(ctx, pr.TokenID, "postgres.row_"+e.Kind, project+"/"+t.slug(), map[string]any{"session": pr.Session, "database": db, "edit": e.ID, "rows": e.Rows})
		return res, nil
	}
	type editOut struct{ Body *PGEditResult }

	ins := api.Op("db-insert", http.MethodPost, "/v1/projects/{project}/tables/{schema}/{table}/rows", "db insert", api.RiskWrite,
		"Add a row to a table",
		"Inserts one row with the values given (columns left out get their defaults) and returns it as stored. Recorded with Undo "+
			"(db undo deletes it again). Needs full access, like sql write; no snapshot is taken.", tag)
	ins.Errors = append(ins.Errors, 404, 409)
	huma.Register(a, api.Untrusted(ins), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Schema  string `path:"schema" maxLength:"63" doc:"Schema, usually public"`
		Table   string `path:"table" maxLength:"63" doc:"Table name"`
		Body    struct {
			Branch string         `json:"branch,omitempty"`
			Values map[string]any `json:"values" doc:"Column → value; JSON columns take any JSON, arrays a JSON array, null is NULL"`
		}
	}) (*editOut, error) {
		res, err := edit(ctx, in.Project, in.Schema, in.Table, in.Body.Branch, func(c *pgx.Conn, t *PGTableDetail) (*PGEdit, *editRows, *PGEditResult, error) {
			return insertRow(ctx, c.PgConn(), t, in.Body.Values)
		})
		return &editOut{res}, err
	}))

	up := api.Op("db-update", http.MethodPatch, "/v1/projects/{project}/tables/{schema}/{table}/rows", "db update", api.RiskWrite,
		"Change rows of a table",
		"Sets columns of rows found by primary key, all in one transaction, and returns the rows as stored. The old values are recorded, "+
			"so db undo puts them back. Needs full access, like sql write; no snapshot is taken.", tag)
	up.Errors = append(up.Errors, 404, 409)
	huma.Register(a, api.Untrusted(up), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Schema  string `path:"schema" maxLength:"63" doc:"Schema, usually public"`
		Table   string `path:"table" maxLength:"63" doc:"Table name"`
		Body    struct {
			Branch  string        `json:"branch,omitempty"`
			Changes []PGRowChange `json:"changes" minItems:"1" maxItems:"5000"`
		}
	}) (*editOut, error) {
		res, err := edit(ctx, in.Project, in.Schema, in.Table, in.Body.Branch, func(c *pgx.Conn, t *PGTableDetail) (*PGEdit, *editRows, *PGEditResult, error) {
			return updateRows(ctx, c.PgConn(), t, in.Body.Changes)
		})
		return &editOut{res}, err
	}))

	del := api.Op("db-delete", http.MethodPost, "/v1/projects/{project}/tables/{schema}/{table}/rows/delete", "db delete", api.RiskDestructive,
		"Delete rows of a table",
		"Deletes rows by primary key and returns them. They are recorded, so db undo puts them back; deleting more than 100 rows "+
			"takes a snapshot of the database first. Needs full access, like sql write.", tag)
	del.Errors = append(del.Errors, 404, 409)
	huma.Register(a, api.Untrusted(del), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Schema  string `path:"schema" maxLength:"63" doc:"Schema, usually public"`
		Table   string `path:"table" maxLength:"63" doc:"Table name"`
		Body    struct {
			Branch string           `json:"branch,omitempty"`
			Keys   []map[string]any `json:"keys" minItems:"1" maxItems:"10000" doc:"Each row's primary key: column → value"`
		}
	}) (*editOut, error) {
		res, err := edit(ctx, in.Project, in.Schema, in.Table, in.Body.Branch, func(c *pgx.Conn, t *PGTableDetail) (*PGEdit, *editRows, *PGEditResult, error) {
			snap := ""
			if len(in.Body.Keys) > bulkDelete {
				db, _ := targetDatabase(ctx, p, in.Project, in.Body.Branch)
				s, err := takeSnapshot(ctx, in.Project, db, mainBranch(in.Body.Branch), fmt.Sprintf("before deleting %d rows from %s", len(in.Body.Keys), t.slug()))
				if err != nil {
					return nil, nil, nil, fmt.Errorf("snapshot before deleting (nothing was deleted): %w", err)
				}
				snap = s.ID
			}
			e, rec, res, err := deleteRows(ctx, c.PgConn(), t, in.Body.Keys)
			if err == nil {
				e.Snapshot = snap
			}
			return e, rec, res, err
		})
		return &editOut{res}, err
	}))

	el := api.Op("db-edits", http.MethodGet, "/v1/projects/{project}/postgres/edits", "db edits", api.RiskRead,
		"List recent row edits",
		"Row edits made through the table editor (db insert, update, delete), newest first, with who made them and whether they can "+
			"be undone. Kept for 7 days.", tag)
	huma.Register(a, api.Untrusted(el), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Limit   int    `query:"limit" minimum:"0" maximum:"200" doc:"Default 50"`
	}) (*struct{ Body []PGEdit }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		list, err := listEdits(ctx, p, in.Project)
		if n := orInt(in.Limit, 50); len(list) > n {
			list = list[:n]
		}
		return &struct{ Body []PGEdit }{list}, err
	}))

	un := api.Op("db-undo", http.MethodPost, "/v1/projects/{project}/postgres/edits/{id}/undo", "db undo", api.RiskWrite,
		"Undo a row edit",
		"Writes back what a row edit replaced: an added row is deleted, changed values return, deleted rows come back with their keys. "+
			"If a changed row was changed again since, nothing happens (409) unless force. Recorded as an edit of its own.", tag)
	un.Errors = append(un.Errors, 404, 409)
	huma.Register(a, api.Untrusted(un), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" pattern:"^edit_[0-9A-Z]{26}$" doc:"Edit ID"`
		Body    struct {
			Force bool `json:"force,omitempty" doc:"Undo even if the rows changed again since"`
		}
	}) (*editOut, error) {
		if err := onBox(p); err != nil {
			return nil, err
		}
		e, rec, err := getEdit(ctx, p, in.Project, in.ID)
		if err != nil {
			return nil, err
		}
		if e.UndoneBy != "" {
			return nil, api.NewProblem(409, "conflict", "this edit was already undone")
		}
		if rec == nil {
			p := api.NewProblem(409, "conflict", "this edit can't be undone: its old rows were too big to keep")
			if e.Snapshot != "" {
				p.Hint = "restore snapshot " + e.Snapshot + " instead"
			}
			return nil, p
		}
		res, err := edit(ctx, in.Project, e.Schema, e.Table, e.Branch, func(c *pgx.Conn, t *PGTableDetail) (*PGEdit, *editRows, *PGEditResult, error) {
			ue, ur, res, err := undoRows(ctx, c.PgConn(), t, e, rec, in.Body.Force)
			if err == nil {
				ue.UndoOf = e.ID
			}
			return ue, ur, res, err
		})
		if err != nil {
			return nil, err
		}
		_ = markUndone(ctx, p, in.Project, e.ID, res.Edit.ID)
		return &editOut{res}, nil
	}))

	ddlOut := func(out *PGDDLResult) *struct{ Body *PGDDLResult } { return &struct{ Body *PGDDLResult }{out} }

	ct := api.Op("db-create-table", http.MethodPost, "/v1/projects/{project}/tables", "db create-table", api.RiskWrite,
		"Create a table",
		"Makes a table from a list of columns (text, integer, bigint, numeric, boolean, timestamptz, date, uuid, jsonb, text[]) with an id "+
			"primary key, defaults, unique values and links to other tables (each link gets an index). dryRun returns the SQL without running it. "+
			"Needs full access, like sql write.", tag)
	ct.Errors = append(ct.Errors, 404, 409)
	huma.Register(a, ct, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    PGTableCreate
	}) (*struct{ Body *PGDDLResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		c, db, err := editorConn(ctx, p, in.Project, in.Body.Branch, !in.Body.DryRun)
		if err != nil {
			return nil, err
		}
		defer c.Close(context.Background())
		sql, err := createTableSQL(ctx, c, in.Body)
		if err != nil {
			return nil, err
		}
		out := &PGDDLResult{SQL: sql, Schema: orWord(in.Body.Schema, "public"), Table: in.Body.Name}
		if in.Body.DryRun {
			return ddlOut(out), nil
		}
		if err := runDDL(ctx, c, sql); err != nil {
			return nil, err
		}
		out.Applied = true
		_ = p.DB.Audit(ctx, pr.TokenID, "postgres.table_create", in.Project+"/"+out.Schema+"."+out.Table, map[string]any{"session": pr.Session, "database": db, "sql": clip(sql, 2000)})
		return ddlOut(out), nil
	}))

	at := api.Op("db-alter-table", http.MethodPatch, "/v1/projects/{project}/tables/{schema}/{table}", "db alter-table", api.RiskDestructive,
		"Change a table",
		"Renames the table or its columns, adds columns, changes whether a column may be empty, its default and whether its values are unique, "+
			"and drops columns, in one transaction. Dropping columns loses their values: without confirm nothing changes (status 428 with the row count), "+
			"and a snapshot is taken first. dryRun returns the SQL. Needs full access.", tag)
	at.Errors = append(at.Errors, 404, 409, 428)
	at.Extensions[api.ExtConfirm] = true
	huma.Register(a, at, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Schema  string `path:"schema" maxLength:"63" doc:"Schema, usually public"`
		Table   string `path:"table" maxLength:"63" doc:"Table name"`
		Body    PGTableAlter
	}) (*struct{ Body *PGDDLResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		c, db, err := editorConn(ctx, p, in.Project, in.Body.Branch, !in.Body.DryRun)
		if err != nil {
			return nil, err
		}
		defer c.Close(context.Background())
		t, err := tableDetail(ctx, c, in.Schema, in.Table, true)
		if err != nil {
			return nil, err
		}
		sql, err := alterTableSQL(ctx, c, t, in.Body)
		if err != nil {
			return nil, err
		}
		out := &PGDDLResult{SQL: sql, Schema: t.Schema, Table: orWord(in.Body.Rename, t.Name)}
		if in.Body.DryRun {
			return ddlOut(out), nil
		}
		if len(in.Body.DropColumns) > 0 {
			drop := append([]string{}, in.Body.DropColumns...)
			sort.Strings(drop)
			preview := map[string]any{"table": t.slug(), "columns": drop, "rows": t.Rows, "rowsExact": t.RowsExact,
				"loses": fmt.Sprintf("the values of %d column(s) in %d rows of %s are gone for good (a snapshot is taken first)", len(drop), t.Rows, t.slug())}
			if err := datakit.RequireConfirm(in.Body.Confirm, []any{db, t.Schema, t.Name, drop}, preview); err != nil {
				return nil, err
			}
			s, err := takeSnapshot(ctx, in.Project, db, mainBranch(in.Body.Branch), "before dropping columns of "+t.slug())
			if err != nil {
				return nil, fmt.Errorf("snapshot first (nothing was changed): %w", err)
			}
			out.Snapshot = s.ID
		}
		if err := runDDL(ctx, c, sql); err != nil {
			return nil, err
		}
		out.Applied = true
		_ = p.DB.Audit(ctx, pr.TokenID, "postgres.table_alter", in.Project+"/"+t.slug(), map[string]any{"session": pr.Session, "database": db, "sql": clip(sql, 2000), "snapshot": out.Snapshot})
		return ddlOut(out), nil
	}))

	dt := api.Op("db-drop-table", http.MethodPost, "/v1/projects/{project}/tables/{schema}/{table}/drop", "db drop-table", api.RiskDestructive,
		"Delete a table",
		"Drops a table and its rows. Without confirm nothing changes: status 428 with the row count and the confirm value. A snapshot of the "+
			"database is taken first, so snapshots restore brings it back. Tables other tables link to are refused until those links go.", tag)
	dt.Errors = append(dt.Errors, 404, 409, 428)
	dt.Extensions[api.ExtConfirm] = true
	huma.Register(a, dt, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Schema  string `path:"schema" maxLength:"63" doc:"Schema, usually public"`
		Table   string `path:"table" maxLength:"63" doc:"Table name"`
		Body    struct {
			Branch  string `json:"branch,omitempty"`
			Confirm string `json:"confirm,omitempty" doc:"The confirm value from the preview (status 428)"`
		}
	}) (*struct{ Body *PGDDLResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		c, db, err := editorConn(ctx, p, in.Project, in.Body.Branch, true)
		if err != nil {
			return nil, err
		}
		defer c.Close(context.Background())
		t, err := tableDetail(ctx, c, in.Schema, in.Table, true)
		if err != nil {
			return nil, err
		}
		for _, f := range t.ReferencedBy {
			if f.Schema != t.Schema || f.Table != t.Name {
				p := api.NewProblem(409, "conflict", fmt.Sprintf("%s links to this table (%s), so it can't go yet", tableSlug(f.Schema, f.Table), f.Columns[0]))
				p.Hint = "remove that column or its table first"
				return nil, p
			}
		}
		kind := "TABLE"
		switch t.Kind {
		case "view":
			kind = "VIEW"
		case "materialized-view":
			kind = "MATERIALIZED VIEW"
		case "foreign":
			kind = "FOREIGN TABLE"
		}
		sql := fmt.Sprintf("DROP %s %s;", kind, t.ident())
		preview := map[string]any{"table": t.slug(), "rows": t.Rows, "rowsExact": t.RowsExact, "sql": sql,
			"loses": fmt.Sprintf("%s and its %d rows are gone (a snapshot is taken first)", t.slug(), t.Rows)}
		if err := datakit.RequireConfirm(in.Body.Confirm, []any{db, t.Schema, t.Name}, preview); err != nil {
			return nil, err
		}
		s, err := takeSnapshot(ctx, in.Project, db, mainBranch(in.Body.Branch), "before deleting table "+t.slug())
		if err != nil {
			return nil, fmt.Errorf("snapshot first (nothing was changed): %w", err)
		}
		if err := runDDL(ctx, c, sql); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "postgres.table_drop", in.Project+"/"+t.slug(), map[string]any{"session": pr.Session, "database": db, "snapshot": s.ID, "rows": t.Rows})
		return ddlOut(&PGDDLResult{SQL: sql, Applied: true, Schema: t.Schema, Table: t.Name, Snapshot: s.ID}), nil
	}))

	registerQueries(a, p, tag)
}

func orInt(n, alt int) int {
	if n == 0 {
		return alt
	}
	return n
}

func tableSlug(schema, table string) string {
	if schema == "public" {
		return table
	}
	return schema + "." + table
}

// ---- saved queries ----

const nsQueries = "postgres.queries" // project → JSON []PGSavedQuery

// PGSavedQuery is a named query kept on the box for a project.
type PGSavedQuery struct {
	Name      string    `json:"name"`
	SQL       string    `json:"sql"`
	UpdatedAt time.Time `json:"updatedAt"`
	By        string    `json:"by,omitempty" doc:"Who saved it last"`
}

func savedQueries(ctx context.Context, p *platform.Platform, project string) ([]PGSavedQuery, error) {
	out := []PGSavedQuery{}
	raw, ok, err := p.DB.KVGet(ctx, nsQueries, project)
	if err != nil || !ok {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func registerQueries(a huma.API, p *platform.Platform, tag string) {
	type nameIn struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Name    string `path:"name" minLength:"1" maxLength:"80" doc:"The query's name"`
	}
	ql := api.Op("db-queries", http.MethodGet, "/v1/projects/{project}/postgres/queries", "db queries", api.RiskRead,
		"List saved queries", "Named SQL queries saved for a project in the dashboard's SQL editor (or with db save-query), by name.", tag)
	huma.Register(a, api.Untrusted(ql), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*struct{ Body []PGSavedQuery }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := savedQueries(ctx, p, in.Project)
		return &struct{ Body []PGSavedQuery }{out}, err
	}))

	qs := api.Op("db-save-query", http.MethodPut, "/v1/projects/{project}/postgres/queries/{name}", "db save-query", api.RiskWrite,
		"Save a query", "Saves (or replaces) a named SQL query for a project. Saving runs nothing.", tag)
	huma.Register(a, qs, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Name    string `path:"name" minLength:"1" maxLength:"80" doc:"The query's name"`
		Body    struct {
			SQL string `json:"sql" minLength:"1" maxLength:"200000"`
		}
	}) (*struct{ Body *PGSavedQuery }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		editsMu.Lock()
		defer editsMu.Unlock()
		list, err := savedQueries(ctx, p, in.Project)
		if err != nil {
			return nil, err
		}
		q := PGSavedQuery{Name: in.Name, SQL: in.Body.SQL, UpdatedAt: time.Now().UTC(), By: orWord(pr.PersonName, pr.Name)}
		kept := []PGSavedQuery{q}
		for _, x := range list {
			if x.Name != in.Name {
				kept = append(kept, x)
			}
		}
		if len(kept) > 200 {
			return nil, api.NewProblem(409, "conflict", "a project keeps up to 200 saved queries; delete some first")
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
		raw, _ := json.Marshal(kept)
		return &struct{ Body *PGSavedQuery }{&q}, p.DB.KVPut(ctx, nsQueries, in.Project, raw)
	}))

	qd := api.Op("db-delete-query", http.MethodDelete, "/v1/projects/{project}/postgres/queries/{name}", "db delete-query", api.RiskWrite,
		"Delete a saved query", "Forgets a saved query. The database is not touched.", tag)
	qd.Errors = append(qd.Errors, 404)
	huma.Register(a, qd, api.Wrap(func(ctx context.Context, in *nameIn) (*struct{}, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		editsMu.Lock()
		defer editsMu.Unlock()
		list, err := savedQueries(ctx, p, in.Project)
		if err != nil {
			return nil, err
		}
		kept := list[:0]
		for _, x := range list {
			if x.Name != in.Name {
				kept = append(kept, x)
			}
		}
		if len(kept) == len(list) {
			return nil, api.NewProblem(404, "not_found", "no saved query called "+in.Name)
		}
		raw, _ := json.Marshal(kept)
		return &struct{}{}, p.DB.KVPut(ctx, nsQueries, in.Project, raw)
	}))
}
