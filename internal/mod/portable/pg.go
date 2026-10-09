package portable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/mod/postgres"
)

// pgMeta is postgres/meta.json: what pg_dump without --create leaves out.
type pgMeta struct {
	Roles     []string     `json:"roles"`
	Databases []pgDatabase `json:"databases"`
	Cron      []cronJob    `json:"cron"`
}

// pgDatabase is one database's own settings, recreated before its dump is
// loaded.
type pgDatabase struct {
	Name        string   `json:"name"`
	Owner       string   `json:"owner"`
	Encoding    string   `json:"encoding"`
	Collate     string   `json:"collate"`
	Ctype       string   `json:"ctype"`
	LocProvider string   `json:"locProvider"`
	Locale      string   `json:"locale,omitempty"`
	ConnLimit   int      `json:"connLimit"`
	Grants      []string `json:"grants,omitempty" doc:"GRANT statements for the database ACL"`
	Settings    []string `json:"settings,omitempty" doc:"ALTER DATABASE ... SET statements"`
	HasACL      bool     `json:"hasAcl"`
	SizeBytes   int64    `json:"sizeBytes"`
}

// cronJob is one pg_cron job (pg_cron keeps them in the postgres database).
type cronJob struct {
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Nodename string `json:"nodename"`
	Nodeport int    `json:"nodeport"`
	Database string `json:"database"`
	Username string `json:"username"`
	Active   bool   `json:"active"`
	Jobname  string `json:"jobname,omitempty"`
}

func qi(s string) string { return pgx.Identifier{s}.Sanitize() }
func ql(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// readPGMeta lists the cluster's databases (all but template0/1 and the
// maintenance database "postgres"), roles and pg_cron jobs.
func readPGMeta(ctx context.Context) (*pgMeta, error) {
	c, err := postgres.Admin(ctx, "postgres")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	defer c.Close(ctx)
	m := &pgMeta{}
	rows, err := c.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname NOT LIKE 'pg\_%' AND rolname <> 'postgres' ORDER BY rolname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		m.Roles = append(m.Roles, n)
	}
	rows.Close()
	rows, err = c.Query(ctx, `SELECT d.datname, pg_get_userbyid(d.datdba), pg_encoding_to_char(d.encoding), d.datcollate, d.datctype,
			d.datlocprovider::text, coalesce(d.datlocale, ''), d.datconnlimit, d.datacl IS NOT NULL, pg_database_size(d.oid)
		FROM pg_database d WHERE d.datname NOT IN ('template0', 'template1', 'postgres') AND d.datallowconn ORDER BY d.datname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d pgDatabase
		if err := rows.Scan(&d.Name, &d.Owner, &d.Encoding, &d.Collate, &d.Ctype, &d.LocProvider, &d.Locale, &d.ConnLimit, &d.HasACL, &d.SizeBytes); err != nil {
			rows.Close()
			return nil, err
		}
		m.Databases = append(m.Databases, d)
	}
	rows.Close()
	for i := range m.Databases {
		d := &m.Databases[i]
		if d.HasACL {
			rows, err := c.Query(ctx, `SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(a.grantee)) END,
					a.privilege_type, a.is_grantable
				FROM pg_database db, aclexplode(db.datacl) a WHERE db.datname = $1 ORDER BY 1, 2`, d.Name)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var grantee, priv string
				var grantable bool
				if err := rows.Scan(&grantee, &priv, &grantable); err != nil {
					rows.Close()
					return nil, err
				}
				g := fmt.Sprintf("GRANT %s ON DATABASE %s TO %s", priv, qi(d.Name), grantee)
				if grantable {
					g += " WITH GRANT OPTION"
				}
				d.Grants = append(d.Grants, g)
			}
			rows.Close()
		}
		rows, err := c.Query(ctx, `SELECT unnest(s.setconfig) FROM pg_db_role_setting s JOIN pg_database db ON db.oid = s.setdatabase
			WHERE db.datname = $1 AND s.setrole = 0`, d.Name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var kv string
			if err := rows.Scan(&kv); err != nil {
				rows.Close()
				return nil, err
			}
			if k, v, ok := strings.Cut(kv, "="); ok {
				d.Settings = append(d.Settings, fmt.Sprintf("ALTER DATABASE %s SET %s = %s", qi(d.Name), qi(k), ql(v)))
			}
		}
		rows.Close()
	}
	var hasCron bool
	_ = c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron')`).Scan(&hasCron)
	if hasCron {
		rows, err := c.Query(ctx, `SELECT schedule, command, nodename, nodeport, database, username, active, coalesce(jobname, '') FROM cron.job ORDER BY jobid`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var j cronJob
			if err := rows.Scan(&j.Schedule, &j.Command, &j.Nodename, &j.Nodeport, &j.Database, &j.Username, &j.Active, &j.Jobname); err != nil {
				rows.Close()
				return nil, err
			}
			m.Cron = append(m.Cron, j)
		}
		rows.Close()
	}
	return m, nil
}

// snapshots holds one open transaction per database whose exported
// snapshot every pg_dump reads, so all dumps show the same moment.
type snapshots struct {
	conns map[string]*pgx.Conn
	ids   map[string]string
}

func exportSnapshots(ctx context.Context, dbs []pgDatabase) (*snapshots, error) {
	s := &snapshots{conns: map[string]*pgx.Conn{}, ids: map[string]string{}}
	for _, d := range dbs {
		c, err := postgres.Admin(ctx, d.Name)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("connect to %s: %w", d.Name, err)
		}
		s.conns[d.Name] = c
		if _, err := c.Exec(ctx, `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
			s.close()
			return nil, err
		}
		var id string
		if err := c.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&id); err != nil {
			s.close()
			return nil, err
		}
		s.ids[d.Name] = id
	}
	return s, nil
}

func (s *snapshots) close() {
	if s == nil {
		return
	}
	for _, c := range s.conns {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.Close(ctx)
		cancel()
	}
}

// pgTool runs a Postgres client tool as the superuser over the socket.
func pgTool(ctx context.Context, tool string, args ...string) *exec.Cmd {
	base := []string{"-h", postgres.SocketDir, "-p", strconv.Itoa(postgres.Port), "-U", "postgres"}
	cmd := exec.CommandContext(ctx, postgres.BinDir+"/"+tool, append(base, args...)...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PGAPPNAME=tiffin-portable", "LC_ALL=C.UTF-8"}
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// tailBuffer keeps the last bytes of a tool's stderr for error messages.
type tailBuffer = datakit.TailBuffer

// dumpRoles is `pg_dumpall --roles-only` (role attributes, password hashes
// and memberships).
func dumpRoles(ctx context.Context) ([]byte, error) {
	var out bytes.Buffer
	var errb tailBuffer
	cmd := pgTool(ctx, "pg_dumpall", "--roles-only")
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pg_dumpall: %w: %s", err, errb.String())
	}
	return out.Bytes(), nil
}

// dumpDatabase streams a plain-SQL pg_dump of db at the exported snapshot.
func dumpDatabase(ctx context.Context, db, snapshot string, w func(io.Reader) error) error {
	args := []string{"--format=plain", "--no-sync", "--quote-all-identifiers", "-d", db}
	if snapshot != "" {
		args = append(args, "--snapshot="+snapshot)
	}
	cmd := pgTool(ctx, "pg_dump", args...)
	var errb tailBuffer
	cmd.Stderr = &errb
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	werr := w(out)
	if werr != nil {
		_ = cmd.Process.Kill()
	}
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil && werr == nil {
		return fmt.Errorf("pg_dump %s: %w: %s", db, err, errb.String())
	}
	return werr
}

// psql runs SQL from r against db. With strict, the first error stops it.
func psql(ctx context.Context, db string, r io.Reader, strict bool) (string, error) {
	onErr := "0"
	if strict {
		onErr = "1"
	}
	cmd := pgTool(ctx, "psql", "-X", "-q", "-v", "ON_ERROR_STOP="+onErr, "-d", db, "-f", "-")
	var errb tailBuffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r, io.Discard, &errb
	err := cmd.Run()
	if err != nil {
		return errb.String(), fmt.Errorf("psql %s: %w: %s", db, err, errb.String())
	}
	return errb.String(), nil
}

// restoreRoles loads the roles dump. Roles this box already has (the
// superuser, platform roles) make CREATE ROLE fail harmlessly; their
// attributes and passwords still follow from the ALTER ROLE lines.
func restoreRoles(ctx context.Context, sqlText []byte) error {
	stderr, err := psql(ctx, "postgres", bytes.NewReader(sqlText), false)
	if err != nil {
		return err
	}
	var bad []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.Contains(l, "ERROR:") && !strings.Contains(l, "already exists") && !strings.Contains(l, "is a member of role") {
			bad = append(bad, strings.TrimSpace(l))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("restoring roles: %s", strings.Join(bad, "; "))
	}
	return nil
}

// dropDatabase removes a database, disconnecting its sessions.
func dropDatabase(ctx context.Context, c *pgx.Conn, name string) error {
	_, err := c.Exec(ctx, `DROP DATABASE IF EXISTS `+qi(name)+` WITH (FORCE)`)
	return err
}

// createDatabase makes an empty database with the archive's settings. It
// takes no connections (except the superuser's) until finishDatabase, so
// nothing on this box writes into it while its dump loads.
func createDatabase(ctx context.Context, c *pgx.Conn, d pgDatabase) error {
	q := fmt.Sprintf(`CREATE DATABASE %s OWNER %s TEMPLATE template0 ENCODING %s CONNECTION LIMIT 0`, qi(d.Name), qi(d.Owner), ql(d.Encoding))
	switch d.LocProvider {
	case "i":
		q += fmt.Sprintf(` LOCALE_PROVIDER icu ICU_LOCALE %s LC_COLLATE %s LC_CTYPE %s`, ql(d.Locale), ql(d.Collate), ql(d.Ctype))
	case "b":
		q += fmt.Sprintf(` LOCALE_PROVIDER builtin BUILTIN_LOCALE %s LC_COLLATE %s LC_CTYPE %s`, ql(d.Locale), ql(d.Collate), ql(d.Ctype))
	default:
		q += fmt.Sprintf(` LOCALE_PROVIDER libc LC_COLLATE %s LC_CTYPE %s`, ql(d.Collate), ql(d.Ctype))
	}
	_, err := c.Exec(ctx, q)
	return err
}

func finishDatabase(ctx context.Context, c *pgx.Conn, d pgDatabase) error {
	stmts := []string{fmt.Sprintf(`ALTER DATABASE %s CONNECTION LIMIT %d`, qi(d.Name), d.ConnLimit)}
	if d.HasACL {
		stmts = append(stmts, fmt.Sprintf(`REVOKE ALL ON DATABASE %s FROM PUBLIC`, qi(d.Name)))
		stmts = append(stmts, d.Grants...)
	}
	stmts = append(stmts, d.Settings...)
	for _, s := range stmts {
		if _, err := c.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

// restoreCron replaces pg_cron's jobs with the archive's.
func restoreCron(ctx context.Context, c *pgx.Conn, jobs []cronJob, replace bool) error {
	var has bool
	if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron')`).Scan(&has); err != nil || !has {
		if len(jobs) > 0 {
			return errors.New("the archive has pg_cron jobs but pg_cron is not installed here")
		}
		return err
	}
	if replace {
		if _, err := c.Exec(ctx, `DELETE FROM cron.job`); err != nil {
			return err
		}
	}
	for _, j := range jobs {
		var name any
		if j.Jobname != "" {
			name = j.Jobname
		}
		if _, err := c.Exec(ctx, `INSERT INTO cron.job (schedule, command, nodename, nodeport, database, username, active, jobname)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, j.Schedule, j.Command, j.Nodename, j.Nodeport, j.Database, j.Username, j.Active, name); err != nil {
			return fmt.Errorf("pg_cron job %q: %w", j.Jobname, err)
		}
	}
	return nil
}
