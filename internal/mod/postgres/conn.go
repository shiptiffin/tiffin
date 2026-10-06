package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
)

// KV namespaces this module uses in the platform state.
const (
	nsPassword     = "postgres.password"      // project → role password (age-encrypted)
	nsReadPassword = "postgres.read-password" // project → read-only role password (age-encrypted)
	nsExtensions   = "postgres.extensions"    // project → JSON list of extensions Tiffin enabled
	nsDeleted      = "postgres.deleted"       // project → JSON deletedRecord (for undo)
)

// Database returns a project's main database (and role) name: "my-shop" → "p_my_shop".
func Database(project string) string { return datakit.Ident(project) }

// Role returns a project's Postgres role. It owns the project's databases.
func Role(project string) string { return datakit.Ident(project) }

// BranchPattern is the shape of a branch name.
var BranchPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,18}$`)

// BranchDatabase returns a branch's database name: "p_my_shop__pr_12".
// Names stay within Postgres's 63-byte identifier limit.
func BranchDatabase(project, branch string) string {
	return Database(project) + "__" + strings.ReplaceAll(branch, "-", "_")
}

// Admin connects to db as the postgres superuser over the unix socket
// (peer authentication through the "tiffin" ident map; the box runs as root).
func Admin(ctx context.Context, db string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d user=postgres dbname=%s sslmode=disable connect_timeout=5", SocketDir, Port, db))
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["application_name"] = "tiffin"
	return pgx.ConnectConfig(ctx, cfg)
}

// roleConn connects to db as the project's own role, so every query an
// agent or the dashboard runs has exactly the project's privileges.
func roleConn(ctx context.Context, p *platform.Platform, project, db string, timeout time.Duration, readOnly bool) (*pgx.Conn, error) {
	pw, ok, err := datakit.GetSecret(ctx, p, nsPassword, project)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNoService(project)
	}
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d dbname=%s sslmode=disable connect_timeout=5", SocketDir, Port, db))
	if err != nil {
		return nil, err
	}
	cfg.User, cfg.Password = Role(project), pw
	cfg.RuntimeParams["application_name"] = "tiffin-sql"
	cfg.RuntimeParams["statement_timeout"] = strconv.Itoa(int(timeout.Milliseconds()))
	cfg.RuntimeParams["lock_timeout"] = "10000"
	if readOnly {
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}
	return pgx.ConnectConfig(ctx, cfg)
}

// HasService reports whether the project's manifest has a postgres service.
func HasService(ctx context.Context, p *platform.Platform, project string) (bool, error) {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return false, err
	}
	_, ok := res[change.KindService+"/postgres"]
	return ok, nil
}

// ConnEnv returns the connection env for a project's database, or for one
// of its branches when branch is set. DATABASE_URL and PG* go through the
// pooler (transaction pooling); DIRECT_DATABASE_URL goes straight to
// Postgres, for migrations, LISTEN/NOTIFY and session locks. With
// viaSocket the URLs and PGHOST point at the unix socket directory
// (bind-mount /var/run/postgresql into the container); otherwise at
// 127.0.0.1.
func ConnEnv(ctx context.Context, p *platform.Platform, project, branch string, viaSocket bool) (map[string]string, error) {
	pw, err := datakit.EnsureSecret(ctx, p, nsPassword, project)
	if err != nil {
		return nil, err
	}
	return connEnv(Role(project), pw, dbOf(project, branch), viaSocket, true), nil
}

func dbOf(project, branch string) string {
	if branch != "" {
		return BranchDatabase(project, branch)
	}
	return Database(project)
}

// ReadRole is a project's read-only role: it reads every table of the
// databases it is granted (pg_read_all_data), owns nothing, and its sessions
// default to read-only.
func ReadRole(project string) string { return Role(project) + "__read" }

// ReadEnv is the connection env for reading a project's database, or one of
// its branches, without the right to change it: app builds get it, so a
// prerender reads data but never writes. The role is made on first use and
// granted the database each time. Row-level security policies apply to it
// (the app's own role owns the tables, so they do not apply there).
func ReadEnv(ctx context.Context, p *platform.Platform, project, branch string) (map[string]string, error) {
	pw, err := datakit.EnsureSecret(ctx, p, nsReadPassword, project)
	if err != nil {
		return nil, err
	}
	role, db := ReadRole(project), dbOf(project, branch)
	mu.Lock()
	defer mu.Unlock()
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return nil, err
	}
	verb := "ALTER"
	if !exists {
		verb = "CREATE"
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`%[1]s ROLE %[2]s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 20 PASSWORD %[3]s;
ALTER ROLE %[2]s SET default_transaction_read_only = on;
GRANT pg_read_all_data TO %[2]s;
GRANT CONNECT ON DATABASE %[4]s TO %[2]s`, verb, quoteIdent(role), quoteLiteral(pw), quoteIdent(db))); err != nil {
		return nil, fmt.Errorf("read-only role: %w", err)
	}
	return connEnv(role, pw, db, false, false), nil // builds are short; they connect directly
}

// dropReadRole drops a project's read-only role, if it has one.
func dropReadRole(ctx context.Context, admin *pgx.Conn, p *platform.Platform, project string) error {
	role := ReadRole(project)
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return err
	}
	if exists {
		// DROP OWNED also revokes its grants on databases.
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %[1]s; DROP ROLE %[1]s`, quoteIdent(role))); err != nil {
			return fmt.Errorf("drop the read-only role: %w", err)
		}
	}
	return p.DB.KVDelete(ctx, nsReadPassword, project)
}

// connEnv is the env for a role and database. DATABASE_URL and PG* go
// through the pooler (transaction pooling) when pooled; DIRECT_DATABASE_URL
// always goes straight to Postgres, for migrations, LISTEN/NOTIFY and
// session locks.
func connEnv(user, pw, db string, viaSocket, pooled bool) map[string]string {
	connURL := func(port int) string {
		u := url.URL{Scheme: "postgresql", User: url.UserPassword(user, pw), Path: "/" + db, RawQuery: "sslmode=disable"}
		if viaSocket {
			u.Host = "localhost:" + strconv.Itoa(port)
			u.RawQuery = "host=" + SocketDir + "&sslmode=disable"
		} else {
			u.Host = "127.0.0.1:" + strconv.Itoa(port)
		}
		return u.String()
	}
	port := Port
	if pooled {
		port = PoolerPort
	}
	env := map[string]string{
		"PGUSER": user, "PGPASSWORD": pw, "PGDATABASE": db, "PGPORT": strconv.Itoa(port), "PGHOST": "127.0.0.1",
		"DATABASE_SOCKET_DIR": SocketDir,
		"DATABASE_URL":        connURL(port),
		"DIRECT_DATABASE_URL": connURL(Port),
	}
	if viaSocket {
		env["PGHOST"] = SocketDir
	}
	return env
}

// BranchEnv is the env for an app that should use a preview branch's
// database instead of the project's main one: DATABASE_URL and PG* point at
// the branch. The runtime merges it over p.ProjectEnv for preview deploys.
// The branch must exist (CreateBranch).
func BranchEnv(ctx context.Context, p *platform.Platform, project, branch string) (map[string]string, error) {
	if !BranchPattern.MatchString(branch) {
		return nil, fmt.Errorf("invalid branch name %q", branch)
	}
	return ConnEnv(ctx, p, project, branch, false)
}

// Env implements platform.EnvProvider: DATABASE_URL and PG* for projects
// with a postgres service.
func (*Module) Env(ctx context.Context, p *platform.Platform, project, app string) (map[string]string, error) {
	ok, err := HasService(ctx, p, project)
	if err != nil || !ok {
		return nil, err
	}
	return ConnEnv(ctx, p, project, "", false)
}

func trackedExtensions(ctx context.Context, p *platform.Platform, project string) []string {
	raw, ok, _ := p.DB.KVGet(ctx, nsExtensions, project)
	var out []string
	if ok {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// quoteLiteral quotes a SQL string literal.
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// quoteIdent quotes a SQL identifier.
func quoteIdent(s string) string { return pgx.Identifier{s}.Sanitize() }
