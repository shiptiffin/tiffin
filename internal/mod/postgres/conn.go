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
	nsPassword   = "postgres.password"   // project → role password (age-encrypted)
	nsExtensions = "postgres.extensions" // project → JSON list of extensions Tiffin enabled
	nsDeleted    = "postgres.deleted"    // project → JSON deletedRecord (for undo)
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
// of its branches when branch is set. With viaSocket the URL and PGHOST
// point at the unix socket directory (bind-mount /var/run/postgresql into
// the container); otherwise at 127.0.0.1:5432.
func ConnEnv(ctx context.Context, p *platform.Platform, project, branch string, viaSocket bool) (map[string]string, error) {
	pw, err := datakit.EnsureSecret(ctx, p, nsPassword, project)
	if err != nil {
		return nil, err
	}
	db := Database(project)
	if branch != "" {
		db = BranchDatabase(project, branch)
	}
	user := Role(project)
	u := url.URL{Scheme: "postgresql", User: url.UserPassword(user, pw), Path: "/" + db}
	env := map[string]string{
		"PGUSER": user, "PGPASSWORD": pw, "PGDATABASE": db, "PGPORT": strconv.Itoa(Port),
		"DATABASE_SOCKET_DIR": SocketDir,
	}
	if viaSocket {
		u.Host = "localhost"
		u.RawQuery = "host=" + SocketDir + "&sslmode=disable"
		env["PGHOST"] = SocketDir
	} else {
		u.Host = "127.0.0.1:" + strconv.Itoa(Port)
		u.RawQuery = "sslmode=disable"
		env["PGHOST"] = "127.0.0.1"
	}
	env["DATABASE_URL"] = u.String()
	return env, nil
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
