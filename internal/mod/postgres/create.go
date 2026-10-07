package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Making project databases. A new database starts with CONNECT and TEMP for
// PUBLIC (CREATE DATABASE never copies the template's ACL), and every
// project's build role reads all tables of any database it can connect to
// (pg_read_all_data). So a database is created closed to every connection
// (ALLOW_CONNECTIONS false, superusers included), given its owner-only ACL,
// and only then opened: no other role ever gets a session in it.

// dbAdmin connects to a database as the superuser (tests point it at their
// own server).
var dbAdmin = Admin

// createDatabase makes db owned by role, empty (template "") or as a reflink
// clone of template, and opens it to its owner only.
func createDatabase(ctx context.Context, admin *pgx.Conn, db, role, template string) error {
	stmt := fmt.Sprintf(`CREATE DATABASE %s OWNER %s ALLOW_CONNECTIONS false`, quoteIdent(db), quoteIdent(role))
	if template != "" {
		stmt = fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s STRATEGY FILE_COPY OWNER %s ALLOW_CONNECTIONS false`,
			quoteIdent(db), quoteIdent(template), quoteIdent(role))
	}
	if _, err := admin.Exec(ctx, stmt); err != nil {
		return err
	}
	return openDatabase(ctx, admin, db, role)
}

// openDatabase gives db its owner-only ACL, then lets connections in and
// hardens what is inside (hardenDatabase).
func openDatabase(ctx context.Context, admin *pgx.Conn, db, role string) error {
	if _, err := admin.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON DATABASE %[1]s FROM PUBLIC; GRANT ALL ON DATABASE %[1]s TO %[2]s;
ALTER DATABASE %[1]s WITH ALLOW_CONNECTIONS true`, quoteIdent(db), quoteIdent(role))); err != nil {
		return err
	}
	c, err := dbAdmin(ctx, db)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	return hardenDatabase(ctx, c, role)
}

// hardenDatabase takes the large-object constructors from PUBLIC in the
// database c is connected to, leaving them to the project's role. Without
// this, the project's read-only build role could store data: its sessions
// default to read-only, but it may turn that off, and anyone may create a
// large object. Branches inherit it from the database they clone.
func hardenDatabase(ctx context.Context, c *pgx.Conn, role string) error {
	const fns = `pg_catalog.lo_creat(integer), pg_catalog.lo_create(oid), pg_catalog.lo_from_bytea(oid, bytea)`
	_, err := c.Exec(ctx, fmt.Sprintf(`REVOKE EXECUTE ON FUNCTION %[1]s FROM PUBLIC; GRANT EXECUTE ON FUNCTION %[1]s TO %[2]s`, fns, quoteIdent(role)))
	return err
}
