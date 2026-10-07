package postgres

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// PGConnection is a project's database connection string. Owner only.
type PGConnection struct {
	DatabaseURL string `json:"databaseUrl" doc:"postgresql:// URL with the project's password (127.0.0.1, inside the box)"`
	SocketURL   string `json:"socketUrl" doc:"The same over the unix socket in /var/run/postgresql"`
	Database    string `json:"database"`
	Role        string `json:"role"`
}

func onBox(p *platform.Platform) error {
	if p == nil || p.DB == nil {
		return api.NewProblem(501, "internal", datakit.ErrOffBox.Error())
	}
	return nil
}

// RegisterAPI adds the database operations.
func (*Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "postgres"

	info := api.Op("db-info", http.MethodGet, "/v1/projects/{project}/postgres", "db info", api.RiskRead,
		"Show a project's database", "The project's Postgres database at a glance: size, open connections, installed extensions, branch and snapshot counts.", tag)
	info.Errors = append(info.Errors, 404, 409)
	huma.Register(a, info, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*struct{ Body *PGInfo }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := GetInfo(ctx, p, in.Project)
		return &struct{ Body *PGInfo }{out}, err
	}))

	type sqlIn struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    PGSQLRequest
	}
	sql := api.Op("sql", http.MethodPost, "/v1/projects/{project}/sql", "sql", api.RiskRead,
		"Query a project's database (read-only)",
		"Runs one SQL statement as the project's read-only Postgres role (p_<project>__read: it reads every table, and row-level "+
			"security applies to it), inside a READ ONLY transaction that is always rolled back, and returns rows as JSON. "+
			"Values longer than 100,000 characters are cut, and rows stop at 32 MiB (truncated is set). "+
			"Needs only read access and changes nothing, so it never asks for confirmation. "+
			"A statement that writes fails with SQLSTATE 25006: use sql_write (CLI: tiffin sql write, or tiffin sql --write) for that. "+
			"Use branch to target a preview branch. Postgres errors come back as 422 with the SQLSTATE.", tag)
	sql.Errors = append(sql.Errors, 404, 409)
	huma.Register(a, api.Untrusted(sql), api.Wrap(func(ctx context.Context, in *sqlIn) (*struct{ Body *PGSQLResult }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := RunSQL(ctx, p, in.Project, in.Body, false)
		return &struct{ Body *PGSQLResult }{out}, err
	}))

	sw := api.Op("sql-write", http.MethodPost, "/v1/projects/{project}/sql/write", "sql write", api.RiskDestructive,
		"Change a project's database with SQL",
		"Runs SQL that may change data and schema (DDL and DML) as the project's own Postgres role; several statements separated by "+
			"semicolons run in one implicit transaction. Needs full access. The database is snapshotted first and the snapshot ID "+
			"is returned, so `snapshots restore` can undo it. For reads use sql, which needs no confirmation. "+
			"Use branch to target a preview branch. Postgres errors come back as 422 with the SQLSTATE.", tag)
	sw.Errors = append(sw.Errors, 404, 409)
	huma.Register(a, api.Untrusted(sw), api.Wrap(func(ctx context.Context, in *sqlIn) (*struct{ Body *PGSQLResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := RunSQL(ctx, p, in.Project, in.Body, true)
		if err == nil {
			_ = p.DB.Audit(ctx, pr.TokenID, "postgres.sql_write", in.Project, map[string]any{"session": pr.Session, "database": out.Database, "snapshot": out.Snapshot, "sql": clip(in.Body.SQL, 2000)})
		}
		return &struct{ Body *PGSQLResult }{out}, err
	}))

	tables := api.Op("db-tables", http.MethodGet, "/v1/projects/{project}/tables", "db tables", api.RiskRead,
		"List a project's tables", "Tables, views and materialized views with their columns, primary keys, row estimates and sizes. For the data browser and for agents writing queries.", tag)
	tables.Errors = append(tables.Errors, 404, 409)
	huma.Register(a, api.Untrusted(tables), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Branch  string `query:"branch" doc:"A preview branch instead of the main database"`
	}) (*struct{ Body []PGTable }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := Tables(ctx, p, in.Project, in.Branch)
		return &struct{ Body []PGTable }{out}, err
	}))

	bl := api.Op("branches-list", http.MethodGet, "/v1/projects/{project}/branches", "branches list", api.RiskRead,
		"List database branches", "Preview branches of the project's database: reflink clones with their own DATABASE_URL.", tag)
	huma.Register(a, bl, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*struct{ Body []PGBranch }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := ListBranches(ctx, p, in.Project)
		return &struct{ Body []PGBranch }{out}, err
	}))

	bc := api.Op("branch-create", http.MethodPost, "/v1/projects/{project}/branches", "branches create", api.RiskWrite,
		"Create a database branch",
		"Clones the project's database (or another branch) into a new branch database. On the box's XFS disk this is a reflink "+
			"clone: about a second for gigabytes, and it shares unchanged blocks with the source. New connections to the source are "+
			"refused for that moment and open ones are closed (apps reconnect). Delete branches you no longer need.", tag)
	bc.Errors = append(bc.Errors, 404, 409)
	huma.Register(a, bc, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			Name string `json:"name" pattern:"^[a-z][a-z0-9-]{0,18}$" doc:"Branch name, e.g. pr-12"`
			From string `json:"from,omitempty" doc:"Clone this branch instead of main"`
		}
	}) (*struct{ Body *PGBranchCreated }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := CreateBranch(ctx, p, in.Project, in.Body.Name, in.Body.From)
		if err == nil {
			_ = p.DB.Audit(ctx, pr.TokenID, "postgres.branch_create", in.Project+"/"+in.Body.Name, map[string]any{"session": pr.Session, "from": out.From, "cloneMs": out.CloneMs})
		}
		return &struct{ Body *PGBranchCreated }{out}, err
	}))

	bd := api.Op("branch-delete", http.MethodDelete, "/v1/projects/{project}/branches/{name}", "branches delete", api.RiskDestructive,
		"Delete a database branch", "Drops a branch database and closes its connections. The branch's data is gone; main is untouched. Needs full access.", tag)
	bd.Errors = append(bd.Errors, 404)
	huma.Register(a, bd, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Name    string `path:"name" pattern:"^[a-z][a-z0-9-]{0,18}$" doc:"Branch name"`
	}) (*struct{}, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if err := DeleteBranch(ctx, p, in.Project, in.Name); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "postgres.branch_delete", in.Project+"/"+in.Name, map[string]any{"session": pr.Session})
		return &struct{}{}, nil
	}))

	sl := api.Op("snapshots-list", http.MethodGet, "/v1/projects/{project}/snapshots", "snapshots list", api.RiskRead,
		"List database snapshots",
		"Copies of the project's databases taken automatically before destructive steps (deleting the service, dropping extensions, SQL writes, restores). Kept 7 days.", tag)
	huma.Register(a, sl, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*struct{ Body []PGSnapshot }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		out, err := ListSnapshots(in.Project)
		return &struct{ Body []PGSnapshot }{out}, err
	}))

	sr := api.Op("snapshot-restore", http.MethodPost, "/v1/projects/{project}/snapshots/{id}/restore", "snapshots restore", api.RiskDestructive,
		"Restore a database snapshot",
		"Replaces the snapshot's database (main or a branch) with the snapshot's contents. The current contents are snapshotted first. "+
			"Without confirm nothing changes: you get status 428 with a preview and the confirm value. Needs full access.", tag)
	sr.Errors = append(sr.Errors, 404, 409, 428)
	sr.Extensions[api.ExtConfirm] = true
	huma.Register(a, sr, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" pattern:"^snap_[0-9A-Z]{26}$" doc:"Snapshot ID"`
		Body    struct {
			Confirm string `json:"confirm,omitempty" doc:"The confirm value from the preview (status 428)"`
		}
	}) (*struct{ Body *PGSnapshotRestored }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		s, err := getSnapshot(in.Project, in.ID)
		if err != nil {
			return nil, api.NewProblem(404, "not_found", "no snapshot "+in.ID+" in project "+in.Project)
		}
		if ok, err := HasService(ctx, p, in.Project); err != nil || !ok {
			if err == nil {
				err = errNoService(in.Project)
			}
			return nil, err
		}
		preview := map[string]any{
			"snapshot": s.ID, "takenAt": s.At, "reason": s.Reason, "database": s.Database, "branch": s.Branch,
			"overwrites": fmt.Sprintf("everything in database %s is replaced by its contents from %s (the current contents are snapshotted first)", s.Database, s.At.Format(time.RFC3339)),
		}
		if err := datakit.RequireConfirm(in.Body.Confirm, []string{s.ID, s.Database}, preview); err != nil {
			return nil, err
		}
		before, err := restoreSnapshot(ctx, p, s)
		if err != nil {
			return nil, err
		}
		out := &PGSnapshotRestored{Restored: s.ID, Database: s.Database}
		if before != nil {
			out.Before = before.ID
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "postgres.snapshot_restore", in.Project+"/"+s.ID, map[string]any{"session": pr.Session, "before": out.Before})
		return &struct{ Body *PGSnapshotRestored }{out}, nil
	}))

	cn := api.Op("db-connection", http.MethodGet, "/v1/projects/{project}/postgres/connection", "db connection", api.RiskRead,
		"Show the database connection string",
		"The project's DATABASE_URL, including its password, for connecting from inside the box or through an SSH tunnel (tiffin db tunnel). "+
			"Needs full access to the project (apply:irreversible on it), since the password reaches all of its data; every reveal is audited.", tag)
	cn.Errors = append(cn.Errors, 409)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Branch  string `query:"branch" doc:"A preview branch instead of the main database"`
	}) (*struct{ Body *PGConnection }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		db, err := targetDatabase(ctx, p, in.Project, in.Branch)
		if err != nil {
			return nil, err
		}
		branch := in.Branch
		if branch == "main" {
			branch = ""
		}
		tcp, err := ConnEnv(ctx, p, in.Project, branch, false)
		if err != nil {
			return nil, err
		}
		sock, err := ConnEnv(ctx, p, in.Project, branch, true)
		if err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "postgres.connection_reveal", in.Project, map[string]any{"session": pr.Session, "database": db})
		return &struct{ Body *PGConnection }{&PGConnection{DatabaseURL: tcp["DATABASE_URL"], SocketURL: sock["DATABASE_URL"], Database: db, Role: Role(in.Project)}}, nil
	}))

	registerEditor(a, p)
}

// SnapshotRestored is the outcome of a snapshot restore.
type PGSnapshotRestored struct {
	Restored string `json:"restored" doc:"The snapshot that was restored"`
	Database string `json:"database"`
	Before   string `json:"before,omitempty" doc:"Snapshot of what was there before; restore it to go back"`
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Start reopens databases an interrupted branch clone left blocked, prunes
// expired snapshots hourly and runs the limits watcher (cgroups.go).
func (*Module) Start(ctx context.Context, p *platform.Platform) error {
	go watch.run(ctx, p)
	go func() {
		// Postgres may still be starting with the box: retry for a while.
		for i := 0; i < 60; i++ {
			admin, err := Admin(ctx, "postgres")
			if err == nil {
				err = reopenBlocked(ctx, admin, p.Log)
				admin.Close(context.WithoutCancel(ctx))
			}
			if err == nil || ctx.Err() != nil {
				return
			}
			if i == 59 {
				p.Log.Error("postgres: reopen blocked databases", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			pruneSnapshots(p.Log.Info)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

// Checks reports whether Postgres answers.
func (*Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := Admin(ctx, "postgres")
	if err != nil {
		return []platform.Check{{Name: "postgres", OK: false, Detail: "not answering on " + SocketDir + ": " + err.Error()}}
	}
	defer c.Close(ctx)
	var version string
	var dbs int
	if err := c.QueryRow(ctx, `SELECT current_setting('server_version'), (SELECT count(*) FROM pg_database WHERE datname LIKE 'p\_%')`).Scan(&version, &dbs); err != nil {
		return []platform.Check{{Name: "postgres", OK: false, Detail: err.Error()}}
	}
	return []platform.Check{{Name: "postgres", OK: true, Detail: fmt.Sprintf("Postgres %s up, %d project database(s)", version, dbs)}}
}
