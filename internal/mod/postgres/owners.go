package postgres

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Which project a database belongs to comes from two things only the box
// sets: its name and its owning role. A project role can neither rename its
// databases nor give them to another role (both need CREATEDB, which it
// lacks). The database's COMMENT (dbMeta) is the project's to change, so it
// only ever adds display details (what a branch was cloned from, when, for
// which preview) and never decides ownership, the pooler's config, storage
// accounting, holds or deletion.

// slugRe is a project slug (manifest/validate.go): no "_", no "--", so a
// role name maps back to one project and a read role ("…__read") to none.
var slugRe = regexp.MustCompile(`^[a-z](-?[a-z0-9]){0,39}$`)

// projectOfRole returns the project whose role is role.
func projectOfRole(role string) (string, bool) {
	rest, ok := strings.CutPrefix(role, "p_")
	if !ok {
		return "", false
	}
	project := strings.ReplaceAll(rest, "_", "-")
	if !slugRe.MatchString(project) || Role(project) != role {
		return "", false
	}
	return project, true
}

// classifyDB returns the project and branch ("" for main) of the database
// name owned by owner, or ok false for a database that is not a project's.
func classifyDB(name, owner string) (project, branch string, ok bool) {
	project, ok = projectOfRole(owner)
	if !ok {
		return "", "", false
	}
	if name == Database(project) {
		return project, "", true
	}
	rest, ok := strings.CutPrefix(name, Database(project)+"__")
	if !ok {
		return "", "", false
	}
	branch = strings.ReplaceAll(rest, "_", "-")
	if !BranchPattern.MatchString(branch) || branch == "main" || BranchDatabase(project, branch) != name {
		return "", "", false
	}
	return project, branch, true
}

// projectDB is one database of a project: the main one (Branch "") or a branch.
type projectDB struct {
	Name    string
	Project string
	Branch  string
	OID     uint32 // creation order
	Size    int64  // only when listed withSize
	Meta    dbMeta // display details from the COMMENT, when it matches; never trusted
}

// listDatabases lists the project databases in the cluster (role "") or
// those of one project's role, main databases first, each project's in name
// order.
func listDatabases(ctx context.Context, c *pgx.Conn, role string, withSize bool) ([]projectDB, error) {
	size := `0::bigint`
	if withSize {
		size = `pg_database_size(d.oid)`
	}
	rows, err := c.Query(ctx, `SELECT d.oid, d.datname, r.rolname, shobj_description(d.oid, 'pg_database'), `+size+`
		FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba
		WHERE NOT d.datistemplate AND ($1::text = '' OR r.rolname = $1::text)
		ORDER BY r.rolname, d.datname = r.rolname DESC, d.datname`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []projectDB
	for rows.Next() {
		var oid uint32
		var name, owner string
		var comment *string
		var n int64
		if err := rows.Scan(&oid, &name, &owner, &comment, &n); err != nil {
			return nil, err
		}
		project, branch, ok := classifyDB(name, owner)
		if !ok {
			continue
		}
		d := projectDB{Name: name, Project: project, Branch: branch, OID: oid, Size: n}
		if m, ok := parseMeta(comment); ok && m.Project == project && m.Branch == branch {
			d.Meta = m
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// projectDatabases lists the main database and branch databases of a
// project, main first.
func projectDatabases(ctx context.Context, c *pgx.Conn, project string) ([]projectDB, error) {
	return listDatabases(ctx, c, Role(project), false)
}

// databaseOwner returns the role that owns db, or "" when there is no such
// database.
func databaseOwner(ctx context.Context, c *pgx.Conn, db string) (string, error) {
	var owner string
	err := c.QueryRow(ctx, `SELECT r.rolname FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba WHERE d.datname = $1`, db).Scan(&owner)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return owner, err
}

func hasDB(list []projectDB, name string) bool {
	for _, d := range list {
		if d.Name == name {
			return true
		}
	}
	return false
}
