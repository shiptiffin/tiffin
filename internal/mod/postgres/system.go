package postgres

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"

	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
)

const nsSystem = "postgres.system" // name → role password (age-encrypted)

var systemName = regexp.MustCompile(`^tiffin_[a-z0-9_]{1,40}$`)

// SystemDatabase returns the DSN of a platform-owned database (for example
// "tiffin_queue"), creating it and its own role on first use. The role owns
// the database, so the caller may run DDL in it; no project role can
// connect to it. It is part of every backup (pgBackRest covers the whole
// cluster). Other modules find this method through platform.Modules().
func (*Module) SystemDatabase(ctx context.Context, p *platform.Platform, name string) (string, error) {
	if !systemName.MatchString(name) {
		return "", fmt.Errorf("system database names look like tiffin_<name> (lowercase, digits, _), got %q", name)
	}
	pw, err := datakit.EnsureSecret(ctx, p, nsSystem, name)
	if err != nil {
		return "", err
	}
	mu.Lock()
	defer mu.Unlock()
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return "", fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, name).Scan(&exists); err != nil {
		return "", err
	}
	verb := "ALTER"
	if !exists {
		verb = "CREATE"
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`%s ROLE %s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %s`, verb, quoteIdent(name), quoteLiteral(pw))); err != nil {
		return "", err
	}
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, quoteIdent(name), quoteIdent(name))); err != nil {
			return "", err
		}
		if _, err := admin.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON DATABASE %[1]s FROM PUBLIC; GRANT ALL ON DATABASE %[1]s TO %[1]s`, quoteIdent(name))); err != nil {
			return "", err
		}
	}
	u := url.URL{Scheme: "postgresql", User: url.UserPassword(name, pw), Host: "127.0.0.1:" + strconv.Itoa(Port), Path: "/" + name, RawQuery: "sslmode=disable"}
	return u.String(), nil
}
