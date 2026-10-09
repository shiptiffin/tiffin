package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/api"
)

// tenantCluster is a real Postgres with a project "shop" set up as the box
// would: role p_shop owning database p_shop, and a read role.
type tenantCluster struct {
	port  int
	admin *pgx.Conn // superuser, database postgres
}

func newTenantCluster(t *testing.T) *tenantCluster {
	t.Helper()
	port := embeddedPort(t)
	tc := &tenantCluster{port: port, admin: connectAs(t, port, "tiffin", "tiffin", "postgres")}
	old := dbAdmin
	dbAdmin = func(ctx context.Context, db string) (*pgx.Conn, error) { return tc.superuser(ctx, db) }
	t.Cleanup(func() { dbAdmin = old })
	ctx := context.Background()
	tc.exec(t, `CREATE ROLE p_shop LOGIN PASSWORD 'shop' NOSUPERUSER NOCREATEDB NOCREATEROLE`)
	tc.exec(t, `CREATE ROLE p_shop__read LOGIN PASSWORD 'read' NOSUPERUSER; GRANT pg_read_all_data TO p_shop__read`)
	tc.exec(t, `CREATE ROLE p_other LOGIN PASSWORD 'other' NOSUPERUSER`)
	if err := createDatabase(ctx, tc.admin, "p_shop", "p_shop", ""); err != nil {
		t.Fatal(err)
	}
	tc.exec(t, `GRANT CONNECT ON DATABASE p_shop TO p_shop__read`)
	return tc
}

// superuser connects to db the way Admin does (its settings), as the test
// server's superuser.
func (tc *tenantCluster) superuser(ctx context.Context, db string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://tiffin:tiffin@127.0.0.1:%d/%s", tc.port, db))
	if err != nil {
		return nil, err
	}
	SecureAdmin(cfg)
	return pgx.ConnectConfig(ctx, cfg)
}

func (tc *tenantCluster) exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := tc.admin.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (tc *tenantCluster) as(t *testing.T, user, pw, db string) (*pgx.Conn, error) {
	t.Helper()
	return pgx.Connect(context.Background(), fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s", user, pw, tc.port, db))
}

func TestTenantHardening(t *testing.T) {
	tc := newTenantCluster(t)
	ctx := context.Background()
	app, err := tc.as(t, "p_shop", "shop", "p_shop")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)

	t.Run("search path", func(t *testing.T) {
		// The app shadows a function the box calls, and puts its schema first.
		if _, err := app.Exec(ctx, `CREATE FUNCTION public.current_setting(text) RETURNS text LANGUAGE sql AS $$ SELECT 'pwned' $$;
			ALTER DATABASE p_shop SET search_path = public, pg_catalog`); err != nil {
			t.Fatal(err)
		}
		defer app.Exec(ctx, `ALTER DATABASE p_shop RESET search_path; DROP FUNCTION public.current_setting(text)`)
		plain := connectAs(t, tc.port, "tiffin", "tiffin", "p_shop")
		var v string
		if err := plain.QueryRow(ctx, `SELECT current_setting('server_version')`).Scan(&v); err != nil || v != "pwned" {
			t.Fatalf("the setup should hijack a plain superuser session: %q %v", v, err)
		}
		c, err := tc.superuser(ctx, "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
		if err := c.QueryRow(ctx, `SELECT current_setting('server_version')`).Scan(&v); err != nil || v == "pwned" || !strings.HasPrefix(v, "18") {
			t.Fatalf("Admin's session ran the app's function: %q %v", v, err)
		}
	})

	t.Run("new databases are the owner's only", func(t *testing.T) {
		var acl string
		var open bool
		if err := tc.admin.QueryRow(ctx, `SELECT datacl::text, datallowconn FROM pg_database WHERE datname = 'p_shop'`).Scan(&acl, &open); err != nil {
			t.Fatal(err)
		}
		if !open || strings.Contains(acl, "{=") || strings.Contains(acl, ",=") {
			t.Fatalf("acl %s open %v: PUBLIC keeps access", acl, open)
		}
		if c, err := tc.as(t, "p_other", "other", "p_shop"); err == nil {
			c.Close(ctx)
			t.Fatal("another project's role connected")
		}
	})

	t.Run("a database left closed reopens owner-only", func(t *testing.T) {
		// What a crash between CREATE and openDatabase leaves: closed, with
		// PUBLIC's default access.
		tc.exec(t, `CREATE DATABASE p_shop__crash OWNER p_shop ALLOW_CONNECTIONS false`)
		if err := reopenBlocked(ctx, tc.admin, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatal(err)
		}
		if c, err := tc.as(t, "p_other", "other", "p_shop__crash"); err == nil {
			c.Close(ctx)
			t.Fatal("another project's role connected to the reopened database")
		}
		c, err := tc.as(t, "p_shop", "shop", "p_shop__crash")
		if err != nil {
			t.Fatalf("the owner cannot connect: %v", err)
		}
		c.Close(ctx)
		tc.exec(t, `DROP DATABASE p_shop__crash`)
	})

	t.Run("the read role stores nothing", func(t *testing.T) {
		r, err := tc.as(t, "p_shop__read", "read", "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close(ctx)
		_, err = r.Exec(ctx, `SET default_transaction_read_only = off; SELECT lo_from_bytea(0, '\xdeadbeef'::bytea)`)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("the read role made a large object: %v", err)
		}
		if _, err := app.Exec(ctx, `SELECT lo_unlink(lo_from_bytea(0, '\xdeadbeef'::bytea))`); err != nil {
			t.Fatalf("the app's own role lost large objects: %v", err)
		}
	})

	t.Run("a held app that writes anyway is locked out", func(t *testing.T) {
		w, err := tc.as(t, "p_shop", "shop", "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close(ctx)
		if err := setReadOnly(ctx, tc.admin, "shop", true); err != nil {
			t.Fatal(err)
		}
		defer setReadOnly(ctx, tc.admin, "shop", false)
		w, err = tc.as(t, "p_shop", "shop", "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		// The read-only default is the app's to turn off: why EnforceHold exists.
		if _, err := w.Exec(ctx, `SET default_transaction_read_only = off`); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Exec(ctx, `CREATE TABLE grow (x text)`); err != nil {
			t.Fatalf("the bypass this guards against: %v", err)
		}
		holds.Lock()
		holds.why["shop"] = "shop is read-only"
		holds.Unlock()
		defer func() {
			holds.Lock()
			delete(holds.why, "shop")
			delete(holds.base, "shop")
			delete(holds.locked, "shop")
			holds.Unlock()
		}()
		for _, n := range []int64{1000, 1000 + holdSlack} {
			if err := EnforceHold(ctx, "shop", n); err != nil || lockedOut("shop") {
				t.Fatalf("%d bytes: locked %v %v", n, lockedOut("shop"), err)
			}
		}
		if err := EnforceHold(ctx, "shop", 1001+holdSlack); err != nil || !lockedOut("shop") {
			t.Fatalf("grew past the slack: locked %v %v", lockedOut("shop"), err)
		}
		if _, err := w.Exec(ctx, `INSERT INTO grow VALUES ('x')`); err == nil {
			t.Fatal("the writing session is still open")
		}
		if c, err := tc.as(t, "p_shop", "shop", "p_shop"); err == nil {
			c.Close(ctx)
			t.Fatal("the locked-out role logged in")
		}
		if err := setLogin(ctx, tc.admin, "shop", true); err != nil {
			t.Fatal(err)
		}
		c, err := tc.as(t, "p_shop", "shop", "p_shop")
		if err != nil {
			t.Fatalf("lifted, the role cannot log in: %v", err)
		}
		c.Close(ctx)
	})

	t.Run("read-only SQL cannot end the app's sessions", func(t *testing.T) {
		victim, err := tc.as(t, "p_shop", "shop", "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		defer victim.Close(ctx)
		const kill = `SELECT count(*) FILTER (WHERE pg_terminate_backend(pid)) FROM pg_stat_activity WHERE usename = 'p_shop' AND pid <> pg_backend_pid()`
		r, err := tc.as(t, "p_shop__read", "read", "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close(ctx)
		var n int
		if err := r.QueryRow(ctx, kill).Scan(&n); err == nil {
			t.Fatalf("the read role ended %d of the app's sessions", n)
		}
		if err := victim.Ping(ctx); err != nil {
			t.Fatalf("the app's session is gone: %v", err)
		}
	})

	t.Run("SQL results are copied and bounded", func(t *testing.T) {
		c, err := tc.as(t, "p_shop", "shop", "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
		c.PgConn().Frontend().SetMaxBodyLen(maxRowBytes)
		budget := maxResultBytes
		r, err := readResult(c.PgConn().ExecParams(ctx, `SELECT jsonb_build_object('i', i, 'pad', repeat('x', 300)) FROM generate_series(1, 3000) g(i)`, nil, nil, nil, nil), 10000, &budget)
		if err != nil || len(r.Rows) != 3000 {
			t.Fatal(err)
		}
		for i, row := range r.Rows {
			var v struct{ I int }
			if err := json.Unmarshal(row[0].(json.RawMessage), &v); err != nil || v.I != i+1 {
				t.Fatalf("row %d holds %s (%v): the driver's buffer was kept", i, row[0], err)
			}
		}
		budget = maxResultBytes
		r, err = readResult(c.PgConn().ExecParams(ctx, `SELECT repeat('x', 200000) FROM generate_series(1, 400)`, nil, nil, nil, nil), 10000, &budget)
		if err != nil || !r.Truncated || r.RowCount != 400 || len(r.Rows)*maxCellBytes > maxResultBytes {
			t.Fatalf("kept %d rows, truncated %v, count %d: %v", len(r.Rows), r.Truncated, r.RowCount, err)
		}
		if s := r.Rows[0][0].(string); len(s) > maxCellBytes+len("…") {
			t.Fatalf("a value of %d bytes", len(s))
		}
		_, err = readResult(c.PgConn().ExecParams(ctx, `SELECT repeat('x', 70 << 20)`, nil, nil, nil, nil), 10, &budget)
		if p, ok := sqlError(err).(*api.Problem); !ok || p.Status != 422 {
			t.Fatalf("a 70 MiB row: %v", err)
		}
	})

	t.Run("retuning leaves read roles their share", func(t *testing.T) {
		tc.exec(t, `ALTER ROLE p_shop__read CONNECTION LIMIT 5`)
		tc.exec(t, retuneRoles(77))
		var own, read int
		if err := tc.admin.QueryRow(ctx, `SELECT (SELECT rolconnlimit FROM pg_roles WHERE rolname = 'p_shop'),
			(SELECT rolconnlimit FROM pg_roles WHERE rolname = 'p_shop__read')`).Scan(&own, &read); err != nil || own != 77 || read != 5 {
			t.Fatalf("p_shop %d, p_shop__read %d: %v", own, read, err)
		}
		if l := readLimits(roleLimits(5, 0, 100, 0)); l.Connections != 5 || l.StatementTimeoutSeconds != LimitedStatementTimeout {
			t.Fatalf("a 5%% project's read role: %+v", l)
		}
		if l := readLimits(roleLimits(0, 0, 400, 0)); l.Connections != readConnections {
			t.Fatalf("an open project's read role: %+v", l)
		}
	})

	t.Run("ownership ignores the comment", func(t *testing.T) {
		// The app rewrites its database's metadata to name a role line
		// that would add a superuser pool, and hides a branch.
		app, err := tc.as(t, "p_shop", "shop", "p_shop")
		if err != nil {
			t.Fatal(err)
		}
		defer app.Close(ctx)
		evil := `{"tiffin":"main","project":"shop = max_user_connections=60\n[databases]\np_backdoor = host=/var/run/postgresql dbname=postgres user=postgres\n[users]\np_unused","createdAt":"2026-01-01T00:00:00Z"}`
		if _, err := app.Exec(ctx, fmt.Sprintf(`COMMENT ON DATABASE p_shop IS %s`, quoteLiteral(evil))); err != nil {
			t.Fatal(err)
		}
		tc.exec(t, `CREATE DATABASE p_shop__pr_1 OWNER p_shop`)
		tc.exec(t, `CREATE DATABASE p_shop__pr_2`) // a name like a branch, not the project's
		defer tc.exec(t, `DROP DATABASE p_shop__pr_2`)
		defer tc.exec(t, `DROP DATABASE p_shop__pr_1`)

		dbs, err := listDatabases(ctx, tc.admin, "", true)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range dbs {
			names = append(names, d.Project+"/"+d.Name+"/"+d.Branch)
		}
		if !slices.Equal(names, []string{"shop/p_shop/", "shop/p_shop__pr_1/pr-1"}) {
			t.Fatalf("databases %v", names)
		}
		pools, caps := desiredPools(dbs, func(string) int { return 10 })
		ini := poolsConfig(pools, caps)
		if strings.Contains(ini, "backdoor") || strings.Count(ini, "[databases]") != 1 || strings.Count(ini, "[users]") != 1 {
			t.Fatalf("pools.ini took the comment:\n%s", ini)
		}
		sizes, err := databaseSizes(ctx, tc.admin, []string{"shop"})
		if err != nil || sizes["shop"] <= dbs[0].Size {
			t.Fatalf("the uncommented branch is not counted: %v %v", sizes, err)
		}
		bs, err := listBranches(ctx, tc.admin, "shop")
		if err != nil || len(bs) != 1 || bs[0].Name != "pr-1" {
			t.Fatalf("branches %+v %v", bs, err)
		}
	})
}

func TestClassifyDB(t *testing.T) {
	for _, c := range []struct {
		name, owner, project, branch string
		ok                           bool
	}{
		{"p_shop", "p_shop", "shop", "", true},
		{"p_my_shop", "p_my_shop", "my-shop", "", true},
		{"p_my_shop__pv_pr_12", "p_my_shop", "my-shop", "pv-pr-12", true},
		{"p_shop__x", "postgres", "", "", false},
		{"p_shop__x", "p_other", "", "", false},
		{"p_shop", "p_shop__read", "", "", false},
		{"postgres", "postgres", "", "", false},
		{"p_shop__", "p_shop", "", "", false},
		{"p_shop__main", "p_shop", "", "", false},
		{"p_shop__a__b", "p_shop", "shop", "a--b", true},
		{"p_shop__A", "p_shop", "", "", false},
	} {
		pr, br, ok := classifyDB(c.name, c.owner)
		if pr != c.project || br != c.branch || ok != c.ok {
			t.Errorf("classifyDB(%q, %q) = %q %q %v", c.name, c.owner, pr, br, ok)
		}
	}
}

func TestPoolsConfigRefusesOddNames(t *testing.T) {
	ini := poolsConfig([]pool{{Database: "p_ok", Size: 2}, {Database: "p_x = host=evil\n[users]", Size: 2}}, map[string]int{"p_ok": 3, "p_y\nx": 4})
	if strings.Contains(ini, "evil") || strings.Contains(ini, "p_y") || !strings.Contains(ini, "p_ok = host=") {
		t.Fatal(ini)
	}
}

func TestRestoreList(t *testing.T) {
	toc := `;
; Archive created at 2026-10-07 10:00:00 UTC
;
; Selected TOC Entries:
;
2; 3079 16385 EXTENSION - vector
4012; 0 0 COMMENT - EXTENSION vector
3; 3079 16390 EXTENSION - uuid-ossp
4013; 0 0 COMMENT - EXTENSION "uuid-ossp"
218; 1259 16466 TABLE public t p_shop
4100; 0 16466 TABLE DATA public t p_shop
4101; 0 0 COMMENT public TABLE t p_shop
`
	list, exts := restoreList(toc)
	if !slices.Equal(exts, []string{"vector", "uuid-ossp"}) {
		t.Fatalf("extensions %v", exts)
	}
	for _, l := range strings.Split(strings.TrimSpace(list), "\n") {
		skipped := strings.HasPrefix(l, ";")
		if want := strings.Contains(l, "EXTENSION"); skipped != (want || !strings.Contains(l, "public")) {
			t.Errorf("line %q skipped=%v", l, skipped)
		}
	}
	args := strings.Join(restoreArgs("p_shop", "p_shop", "/x.list", "/s.dump"), " ")
	if !strings.Contains(args, "-U p_shop ") || !strings.Contains(args, "--no-owner") || strings.Contains(args, "-U postgres") {
		t.Fatalf("restore runs as %s", args)
	}
}
