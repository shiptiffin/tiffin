package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5/pgconn"
)

// The connection pooler. Apps reach Postgres through PgBouncer in
// transaction mode (DATABASE_URL, 127.0.0.1:6432 and the socket in
// /var/run/postgresql): many client connections share a few server
// connections, and while Postgres restarts for a minor update PgBouncer
// holds new queries (PAUSE) instead of failing them. DIRECT_DATABASE_URL
// skips it for what needs a whole session: migrations, LISTEN/NOTIFY,
// advisory locks held across transactions.
//
// PgBouncer looks project roles up in Postgres itself (auth_query through
// a SECURITY DEFINER function that returns only project roles), so a new
// project works at once and no password is copied to disk. The box's own
// databases and roles never go through it.

// Pooler layout on the box.
const (
	PoolerPort    = 6432
	PoolerUnit    = "tiffin-pgbouncer.service"
	PoolerConfDir = "/etc/tiffin/pgbouncer"
	poolerIni     = PoolerConfDir + "/pgbouncer.ini"
	poolsIni      = PoolerConfDir + "/pools.ini"
	poolerAuth    = "tiffin_pgbouncer" // the role PgBouncer looks passwords up as
	poolerBin     = "/usr/sbin/pgbouncer"
)

// Pool sizes. The role's connection limit (RoleLimits.Connections) is what
// Postgres enforces for a project; the pooler keeps a quarter of it free
// for direct connections and multiplexes every app's clients onto the rest.
const (
	// PoolerClientLimit is how many client connections one project may hold
	// open to the pooler (all its apps and previews together). Clients are
	// cheap there: a few kilobytes each, no Postgres process.
	PoolerClientLimit = 1000
	poolerMaxClients  = 5000
	// fallbackPoolSize is the server pool of a database the pooler does not
	// know yet (a branch made a moment ago): the next sync sizes it.
	fallbackPoolSize = 2
	// maxPrepared is how many prepared statements PgBouncer tracks per
	// server connection, so clients that prepare (postgres.js, Bun.SQL,
	// pgx, Prisma) work in transaction mode.
	maxPrepared = 200
	// PoolMaxProduction and PoolMaxPreview are the DATABASE_POOL_MAX the box
	// suggests to each app instance: client connections to the pooler.
	PoolMaxProduction = 20
	PoolMaxPreview    = 5
)

// PoolerServerLimit is how many server connections the pooler may open for
// a project whose role may hold role: all but a quarter, which stays free
// for direct connections (migrations, LISTEN, the SQL console).
func PoolerServerLimit(role int) int {
	return max(1, role-max(1, role/4))
}

// branchPoolSize is the server pool of one branch database (a preview):
// a fifth of the project's, so previews cannot crowd out production.
func branchPoolSize(server int) int { return max(1, server/5) }

// pool is one database the pooler serves.
type pool struct {
	Database string
	Role     string
	Size     int // server connections for this database
}

// poolsConfig renders pools.ini: one entry per project database and the
// per-role cap on server connections. Anything else (a branch made since
// the last sync) falls back to a small pool.
func poolsConfig(pools []pool, userCap map[string]int) string {
	var b strings.Builder
	b.WriteString("; Managed by Tiffin (the postgres module syncs it). Edits are overwritten.\n[databases]\n")
	sort.Slice(pools, func(i, j int) bool { return pools[i].Database < pools[j].Database })
	for _, p := range pools {
		fmt.Fprintf(&b, "%s = host=%s port=%d pool_size=%d\n", p.Database, SocketDir, Port, p.Size)
	}
	fmt.Fprintf(&b, "* = host=%s port=%d pool_size=%d\n\n[users]\n", SocketDir, Port, fallbackPoolSize)
	roles := make([]string, 0, len(userCap))
	for r := range userCap {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		fmt.Fprintf(&b, "%s = max_user_connections=%d\n", r, userCap[r])
	}
	return b.String()
}

// poolerConfig renders pgbouncer.ini for a box with memMB of RAM. Roles not
// in pools.ini yet get the cap of a project without a limit.
func poolerConfig(memMB int) string {
	return fmt.Sprintf(`; Managed by Tiffin (tiffin provision). Edits are overwritten.
[pgbouncer]
listen_addr = 127.0.0.1
listen_port = %[1]d
unix_socket_dir = %[2]s
unix_socket_mode = 0777
pidfile =
logfile =
syslog = 0

; Project roles authenticate with their own passwords, looked up in Postgres.
auth_type = hba
auth_hba_file = %[3]s/hba.conf
auth_ident_file = %[3]s/ident.conf
auth_file = %[3]s/userlist.txt
auth_user = %[4]s
auth_dbname = postgres
auth_query = SELECT usename, passwd FROM public.tiffin_pgbouncer_auth($1)
admin_users = postgres
stats_users = postgres

pool_mode = transaction
max_prepared_statements = %[5]d
max_client_conn = %[6]d
max_user_client_connections = %[7]d
max_user_connections = %[8]d
default_pool_size = %[9]d
min_pool_size = 0
reserve_pool_size = 0
server_idle_timeout = 60
server_lifetime = 3600
server_login_retry = 1
query_wait_timeout = 120
client_tls_sslmode = disable
server_tls_sslmode = disable
ignore_startup_parameters = extra_float_digits
track_extra_parameters = IntervalStyle, search_path, statement_timeout, lock_timeout, idle_in_transaction_session_timeout
log_connections = 0
log_disconnections = 0
stats_period = 60

%%include %[10]s
`, PoolerPort, SocketDir, PoolerConfDir, poolerAuth, maxPrepared, poolerMaxClients, PoolerClientLimit,
		PoolerServerLimit(RoleConnLimit(memMB)), fallbackPoolSize, poolsIni)
}

// The admin console (database "pgbouncer") is for the box only: root and
// postgres over the socket, mapped to PgBouncer's admin user. The two names
// in userlist.txt never log in any other way.
const poolerHBA = `# Managed by Tiffin. TYPE DATABASE USER ADDRESS METHOD
local   pgbouncer       postgres                                peer map=tiffin
local   all             postgres,` + poolerAuth + `                reject
host    all             postgres,` + poolerAuth + `  0.0.0.0/0      reject
local   all             all                                     scram-sha-256
host    all             all             127.0.0.1/32            scram-sha-256
`

const poolerIdent = `# Managed by Tiffin. MAPNAME SYSTEM-USER PG-USER
tiffin  root      postgres
tiffin  postgres  postgres
`

// The admin user and the lookup role are known to PgBouncer by name only:
// one logs in with peer authentication, the other is never a client
// (PgBouncer logs it into Postgres over the socket, peer again).
const poolerUsers = `"postgres" ""` + "\n" + `"` + poolerAuth + `" ""` + "\n"

// poolerAuthSQL makes the lookup role and function. Only project roles
// (p_*) that may log in and are not superusers come back, so nothing else
// can log in through the pooler.
const poolerAuthSQL = `DO $$BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + poolerAuth + `') THEN
    CREATE ROLE ` + poolerAuth + ` WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
END$$;
CREATE OR REPLACE FUNCTION public.tiffin_pgbouncer_auth(p_user name, OUT usename name, OUT passwd text)
  RETURNS SETOF record LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog
  AS $$ SELECT rolname, rolpassword FROM pg_authid
        WHERE rolname = p_user AND rolname LIKE 'p\_%' AND rolcanlogin AND NOT rolsuper $$;
REVOKE ALL ON FUNCTION public.tiffin_pgbouncer_auth(name) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.tiffin_pgbouncer_auth(name) TO ` + poolerAuth + `;
GRANT CONNECT ON DATABASE postgres TO ` + poolerAuth + `;
`

func poolerUnit(rev string) string {
	return `[Unit]
Description=Tiffin connection pooler (PgBouncer)
After=network.target ` + UnitName + `
RequiresMountsFor=/var/lib/tiffin

[Service]
Type=notify
User=postgres
Group=postgres
# Config revision ` + rev + ` (a new revision restarts the pooler; pool
# changes only reload it).
Environment=TIFFIN_PGBOUNCER_CONF=` + rev + `
ExecStartPre=+/usr/bin/install -d -m 2775 -o postgres -g postgres ` + SocketDir + `
ExecStart=` + poolerBin + ` ` + poolerIni + `
ExecReload=/bin/kill -HUP $MAINPID
KillSignal=SIGINT
TimeoutStopSec=30
Restart=always
RestartSec=1
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`
}

// provisionPooler installs PgBouncer from the PGDG repository, keeps the
// package's own service off and runs Tiffin's. Postgres must be up.
func provisionPooler(ctx context.Context, s *platform.System) error {
	if err := s.Apt(ctx, "pgbouncer"); err != nil {
		return err
	}
	// The package starts its own pgbouncer.service on 6432: never let it.
	if out, _ := exec.CommandContext(ctx, "systemctl", "is-enabled", "pgbouncer.service").Output(); strings.TrimSpace(string(out)) != "masked" {
		_, _ = s.Run(ctx, "systemctl", "disable", "--now", "pgbouncer.service", "pgbouncer.socket")
		if _, err := s.Run(ctx, "systemctl", "mask", "pgbouncer.service", "pgbouncer.socket"); err != nil {
			return err
		}
	}
	if err := checkMinimum("pgbouncer", s.InstalledVersion(ctx, "pgbouncer")); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "runuser", "-u", "postgres", "--", BinDir+"/psql", "-h", SocketDir, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", "postgres", "-c", poolerAuthSQL); err != nil {
		return err
	}
	files := []struct {
		path, body string
	}{
		{poolerIni, poolerConfig(memTotalMB())},
		{PoolerConfDir + "/hba.conf", poolerHBA},
		{PoolerConfDir + "/ident.conf", poolerIdent},
		{PoolerConfDir + "/userlist.txt", poolerUsers},
	}
	sum := sha256.New()
	for _, f := range files {
		if _, err := s.WriteFile(f.path, []byte(f.body), 0o644); err != nil {
			return err
		}
		sum.Write([]byte(f.body))
	}
	// The box syncs the pools once it runs; until then every database gets
	// the fallback pool.
	if _, err := os.Stat(poolsIni); err != nil {
		if _, err := s.WriteFile(poolsIni, []byte(poolsConfig(nil, nil)), 0o644); err != nil {
			return err
		}
	}
	if err := s.Unit(ctx, PoolerUnit, poolerUnit(hex.EncodeToString(sum.Sum(nil))[:16])); err != nil {
		return err
	}
	return s.WaitTCP(ctx, "127.0.0.1:"+strconv.Itoa(PoolerPort), 30*time.Second)
}

// ---- the admin console ----

// poolerAdmin connects to PgBouncer's admin console over the socket.
func poolerAdmin(ctx context.Context) (*pgconn.PgConn, error) {
	return pgconn.Connect(ctx, fmt.Sprintf("host=%s port=%d user=postgres dbname=pgbouncer sslmode=disable connect_timeout=5", SocketDir, PoolerPort))
}

// poolerRunning reports whether the pooler is installed and answering.
func poolerRunning(ctx context.Context) bool {
	if _, err := os.Stat(poolerIni); err != nil {
		return false
	}
	c, err := poolerAdmin(ctx)
	if err != nil {
		return false
	}
	c.Close(ctx)
	return true
}

// poolerCommand runs one admin command (PAUSE, RESUME, RELOAD...).
func poolerCommand(ctx context.Context, cmd string) error {
	c, err := poolerAdmin(ctx)
	if err != nil {
		return err
	}
	defer c.Close(context.WithoutCancel(ctx))
	_, err = c.Exec(ctx, cmd).ReadAll()
	return err
}

// pausePooler holds new queries at the pooler and waits (up to wait) until
// every transaction in flight has finished and the server connections are
// closed; db "" pauses every database. It returns a resume function that
// is safe to call more than once. Without a pooler both are no-ops.
func pausePooler(ctx context.Context, db string, wait time.Duration) (resume func() error, err error) {
	noop := func() error { return nil }
	if !poolerRunning(ctx) {
		return noop, nil
	}
	cmd, res := "PAUSE", "RESUME"
	if db != "" {
		// The admin console takes bare names; project databases are p_[a-z0-9_]+.
		if strings.Trim(db, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			return noop, fmt.Errorf("cannot pause %q", db)
		}
		cmd, res = "PAUSE "+db, "RESUME "+db
	}
	done := false
	resume = func() error {
		if done {
			return nil
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := poolerCommand(rctx, res)
		if err != nil && strings.Contains(err.Error(), "not paused") {
			err = nil
		}
		if err == nil {
			done = true
		}
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := poolerCommand(pctx, cmd); err != nil {
		_ = resume()
		if pctx.Err() != nil && ctx.Err() == nil {
			return noop, fmt.Errorf("transactions still running after %s; nothing was changed", wait)
		}
		return noop, err
	}
	return resume, nil
}

// unitPaused installs Postgres's unit and (re)starts the server when the
// unit changed or it is not running; a restart pauses the pooler, so apps'
// queries wait instead of failing. It reports whether it (re)started.
func unitPaused(ctx context.Context, s *platform.System, content string) (bool, error) {
	changed, err := s.WriteFile("/etc/systemd/system/"+UnitName, []byte(content), 0o644)
	if err != nil {
		return false, err
	}
	if changed {
		if _, err := s.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return false, err
		}
	}
	if _, err := s.Run(ctx, "systemctl", "enable", UnitName); err != nil {
		return false, err
	}
	active := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", UnitName).Run() == nil
	if active && !changed {
		return false, nil
	}
	resume := func() error { return nil }
	if active {
		s.Log("restarting postgres for new settings (queries wait at the pooler)")
		if resume, err = pausePooler(ctx, "", pauseWait); err != nil {
			s.Log("could not pause the pooler (" + err.Error() + "); restarting anyway")
		}
	} else {
		s.Log("starting postgres")
	}
	_, err = s.Run(ctx, "systemctl", "restart", UnitName)
	if err == nil {
		err = waitReady(ctx, 90*time.Second)
	}
	if rerr := resume(); err == nil {
		err = rerr
	}
	return true, err
}

// PoolerStats is the pooler at a glance.
type PoolerStats struct {
	Version string
	Pools   int
	Clients int
	Waiting int
	Servers int
}

func poolerStats(ctx context.Context) (*PoolerStats, error) {
	c, err := poolerAdmin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close(context.WithoutCancel(ctx))
	st := &PoolerStats{}
	res, err := c.Exec(ctx, "SHOW VERSION").ReadAll()
	if err != nil {
		return nil, err
	}
	if len(res) > 0 && len(res[0].Rows) > 0 && len(res[0].Rows[0]) > 0 {
		st.Version = strings.TrimPrefix(string(res[0].Rows[0][0]), "PgBouncer ")
	}
	res, err = c.Exec(ctx, "SHOW POOLS").ReadAll()
	if err != nil {
		return nil, err
	}
	if len(res) > 0 {
		col := map[string]int{}
		for i, f := range res[0].FieldDescriptions {
			col[f.Name] = i
		}
		num := func(row [][]byte, name string) int {
			i, ok := col[name]
			if !ok || i >= len(row) {
				return 0
			}
			n, _ := strconv.Atoi(string(row[i]))
			return n
		}
		for _, row := range res[0].Rows {
			if string(row[col["database"]]) == "pgbouncer" {
				continue
			}
			st.Pools++
			st.Clients += num(row, "cl_active") + num(row, "cl_waiting")
			st.Waiting += num(row, "cl_waiting")
			st.Servers += num(row, "sv_active") + num(row, "sv_idle") + num(row, "sv_used")
		}
	}
	return st, nil
}

// ---- keeping the pools in step with the projects ----

var poolKick = make(chan struct{}, 1)

// poolsChanged asks the sync loop to look again now (a database or a
// project's limits changed).
func poolsChanged() {
	select {
	case poolKick <- struct{}{}:
	default:
	}
}

// desiredPools lists the pools for the project databases in the cluster.
// limit gives a project's role connection limit.
func desiredPools(dbs map[string]dbMeta, limit func(project string) int) ([]pool, map[string]int) {
	var pools []pool
	caps := map[string]int{}
	for name, m := range dbs {
		server := PoolerServerLimit(limit(m.Project))
		role := Role(m.Project)
		caps[role] = server
		size := server
		if m.Tiffin == "branch" {
			size = branchPoolSize(server)
		}
		pools = append(pools, pool{Database: name, Role: role, Size: size})
	}
	return pools, caps
}

// syncPools rewrites pools.ini when the project databases or limits
// changed, and reloads the pooler.
func syncPools(ctx context.Context) error {
	if _, err := os.Stat(poolerIni); err != nil {
		return nil // no pooler on this machine
	}
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return err
	}
	rows, err := admin.Query(ctx, `SELECT datname, shobj_description(oid, 'pg_database') FROM pg_database WHERE datname LIKE 'p\_%'`)
	if err != nil {
		admin.Close(ctx)
		return err
	}
	dbs := map[string]dbMeta{}
	for rows.Next() {
		var name string
		var comment *string
		if err := rows.Scan(&name, &comment); err != nil {
			rows.Close()
			admin.Close(ctx)
			return err
		}
		if m, ok := parseMeta(comment); ok && m.Project != "" {
			dbs[name] = m
		}
	}
	rows.Close()
	admin.Close(ctx)
	if err := rows.Err(); err != nil {
		return err
	}
	pools, caps := desiredPools(dbs, func(project string) int { return LimitUsage(project).ConnectionLimit })
	want := poolsConfig(pools, caps)
	if cur, err := os.ReadFile(poolsIni); err == nil && string(cur) == want {
		return nil
	}
	tmp := poolsIni + ".tmp"
	if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, poolsIni); err != nil {
		return err
	}
	return poolerCommand(ctx, "RELOAD")
}

// syncLoop keeps pools.ini in step: at start, when kicked and every 30 s
// (limits change with the budget).
func syncLoop(ctx context.Context, p *platform.Platform) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	failing := false
	for {
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := syncPools(sctx)
		cancel()
		switch {
		case err != nil && !failing && ctx.Err() == nil:
			p.Log.Warn("postgres: sync the pooler's pools", "err", err)
			failing = true
		case err == nil:
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-poolKick:
		}
	}
}
