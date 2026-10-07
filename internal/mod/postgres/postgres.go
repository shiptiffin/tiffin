// Package postgres is the Tiffin postgres module: one Postgres 18 cluster on
// the box's XFS data disk, one database and role per project, preview
// branches as reflink clones (CREATE DATABASE ... STRATEGY FILE_COPY with
// file_copy_method = clone), snapshots before anything destructive, and an
// SQL endpoint for agents and the dashboard's data browser.
//
// How apps reach it. The cluster listens on 127.0.0.1:5432 (scram-sha-256)
// and on the unix socket directory /var/run/postgresql (scram for project
// roles; peer for the box itself). In front of it PgBouncer (pooler.go)
// listens on 127.0.0.1:6432 and the same socket directory. Apps get
// DATABASE_URL through the pooler,
// postgresql://p_<project>:<password>@127.0.0.1:6432/p_<project>?sslmode=disable,
// the libpq PG* variables to match, and DIRECT_DATABASE_URL straight to
// 5432 for sessions the pooler cannot carry. A runtime whose containers do
// not share the host network can bind-mount /var/run/postgresql into the
// container and use ConnEnv(..., viaSocket=true).
package postgres

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// Layout on the box. The cluster lives on the XFS data disk so database
// clones are reflinks.
const (
	Major       = "18"
	BinDir      = "/usr/lib/postgresql/18/bin"
	DataDir     = "/var/lib/tiffin/postgres/18/data"
	ConfDir     = "/etc/tiffin/postgres"
	SocketDir   = "/var/run/postgresql"
	Port        = 5432
	UnitName    = "tiffin-postgres.service"
	SnapshotDir = "/var/lib/tiffin/backups/snapshots"
	// SnapshotKeep is how long snapshots taken before destructive operations are kept.
	SnapshotKeep = 7 * 24 * time.Hour
)

func init() { platform.Register(&Module{}) }

// Module implements the postgres module.
type Module struct{}

func (*Module) Name() string { return "postgres" }
func (*Module) Order() int   { return 10 }

// mu serialises structural changes (create/drop/clone/restore) on the cluster.
var mu sync.Mutex

// Provision installs Postgres 18 from the PGDG apt repository (signed, major
// pinned), creates the cluster on the data disk and runs it as a systemd unit.
func (*Module) Provision(ctx context.Context, s *platform.System) error {
	if err := s.AptRepo(ctx, "pgdg", "https://www.postgresql.org/media/keys/ACCC4CF8.asc",
		"deb [signed-by={key}] https://apt.postgresql.org/pub/repos/apt "+platform.RepoCodename("noble", "resolute")+"-pgdg main"); err != nil {
		return err
	}
	// Never let the Debian wrapper create its own cluster in /var/lib/postgresql.
	if err := s.Apt(ctx, "postgresql-common"); err != nil {
		return err
	}
	if _, err := s.WriteFile("/etc/postgresql-common/createcluster.d/tiffin.conf", []byte("# Tiffin runs its own cluster on the data disk.\ncreate_main_cluster = false\n"), 0o644); err != nil {
		return err
	}
	if _, err := s.WriteFile(pgdgPrefs, []byte(pgdgPin), 0o644); err != nil {
		return err
	}
	if err := s.Apt(ctx, "postgresql-"+Major, "postgresql-client-"+Major, "postgresql-"+Major+"-pgvector", "postgresql-"+Major+"-cron"); err != nil {
		return err
	}
	if _, err := os.Stat("/etc/postgresql/" + Major + "/main"); err == nil {
		s.Log("removing the default Debian cluster (Tiffin's cluster lives on the data disk)")
		if _, err := s.Run(ctx, "pg_dropcluster", "--stop", Major, "main"); err != nil {
			return err
		}
	}
	if _, err := s.Sh(ctx, `install -d -o postgres -g postgres -m 0700 /var/lib/tiffin/postgres /var/lib/tiffin/postgres/18
install -d -m 0700 `+SnapshotDir+`
install -d -m 0755 `+ConfDir); err != nil {
		return err
	}
	if _, err := os.Stat(DataDir + "/PG_VERSION"); err != nil {
		s.Log("creating the Postgres cluster on the data disk")
		if _, err := s.Run(ctx, "runuser", "-u", "postgres", "--", BinDir+"/initdb", "-D", DataDir,
			"--encoding=UTF8", "--locale-provider=builtin", "--builtin-locale=C.UTF-8", "--locale=C.UTF-8",
			"--auth-local=peer", "--auth-host=scram-sha-256", "--data-checksums"); err != nil {
			return err
		}
	}
	// A new postgresql.conf restarts the server (with the pooler paused, so
	// apps' queries wait); new access rules only reload it.
	conf := Config(memTotalMB())
	if _, err := s.WriteFile(ConfDir+"/postgresql.conf", []byte(conf), 0o644); err != nil {
		return err
	}
	reload := false
	for name, body := range map[string]string{"pg_hba.conf": hba, "pg_ident.conf": ident} {
		changed, err := s.WriteFile(ConfDir+"/"+name, []byte(body), 0o644)
		if err != nil {
			return err
		}
		reload = reload || changed
	}
	sum := sha256.Sum256([]byte(conf))
	restarted, err := unitPaused(ctx, s, unit(hex.EncodeToString(sum[:])[:16]))
	if err != nil {
		return err
	}
	if err := waitReady(ctx, 90*time.Second); err != nil {
		out, _ := s.Run(ctx, "journalctl", "-u", UnitName, "-n", "40", "--no-pager")
		return fmt.Errorf("%w\n%s", err, out)
	}
	if reload && !restarted {
		if _, err := s.Run(ctx, "systemctl", "reload", UnitName); err != nil {
			return err
		}
	}
	// A Tiffin release may raise the minimum versions (maint.go): update the
	// packages the way the maintenance window does.
	if err := raiseMinimums(ctx, s.Log); err != nil {
		return err
	}
	// Box-level setup: project roles may not connect to the maintenance
	// databases; pg_cron lives in "postgres" (cron.database_name).
	// Project roles' connection limits follow max_connections (the box may
	// have been resized since they were made).
	_, err = s.Run(ctx, "runuser", "-u", "postgres", "--", BinDir+"/psql", "-h", SocketDir, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", "postgres", "-c",
		`REVOKE CONNECT ON DATABASE postgres, template1 FROM PUBLIC;
CREATE EXTENSION IF NOT EXISTS pg_cron;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
`+retuneRoles(RoleConnLimit(memTotalMB())))
	return err
}

// retuneRoles sets every project role's connection limit to n (not the
// read roles': theirs stay a share of their project's).
func retuneRoles(n int) string {
	return fmt.Sprintf(`DO $$DECLARE r record; BEGIN
FOR r IN SELECT rolname FROM pg_roles WHERE rolname LIKE 'p\_%%' AND rolname NOT LIKE '%%\_\_read' AND rolcanlogin AND NOT rolsuper AND rolconnlimit <> %[1]d LOOP
  EXECUTE format('ALTER ROLE %%I CONNECTION LIMIT %[1]d', r.rolname);
END LOOP; END$$;`, n)
}

func waitReady(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		c, err := Admin(ctx, "postgres")
		if err == nil {
			var rec bool
			err = c.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&rec)
			c.Close(ctx)
			if err == nil && !rec {
				return nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("postgres did not become ready in %s: %v", d, last)
}

// WaitReady waits until the cluster accepts connections and is out of recovery.
func WaitReady(ctx context.Context, d time.Duration) error { return waitReady(ctx, d) }

func memTotalMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 2048
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
			if kb > 0 {
				return kb / 1024
			}
		}
	}
	return 2048
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// MaxConnections is max_connections for a box with memMB of RAM: 100 up
// to 4 GB, 25 more per GB above that, at most 500. Every connection is a
// process of a few MB, plus work_mem while it sorts.
func MaxConnections(memMB int) int { return clamp(memMB*25/1024, 100, 500) }

// RoleConnLimit is how many connections one project's role may hold: four
// fifths of max_connections, so no project can take them all.
func RoleConnLimit(memMB int) int { return MaxConnections(memMB) * 4 / 5 }

// Config renders postgresql.conf for a box with memMB of RAM. Postgres
// shares the box with Valkey, apps and the platform, so it takes about an
// eighth of RAM for shared buffers. Provision re-reads the machine, so a
// resized box is retuned (and Postgres restarted) on the next tiffin up.
func Config(memMB int) string {
	shared := clamp(memMB/8, 128, 4096)
	cache := clamp(memMB/2, 256, 16384)
	maint := clamp(memMB/16, 64, 1024)
	work := clamp(memMB/512, 8, 32)
	return fmt.Sprintf(`# Managed by Tiffin (tiffin provision). Edits are overwritten.
data_directory = '%[1]s'
hba_file = '%[2]s/pg_hba.conf'
ident_file = '%[2]s/pg_ident.conf'

# Unix socket for the box and bind-mounted containers; loopback TCP for apps.
listen_addresses = '127.0.0.1'
port = %[3]d
unix_socket_directories = '%[4]s'
unix_socket_permissions = 0777
max_connections = %[9]d
password_encryption = scram-sha-256

# Memory, sized for a %[5]d MB box.
shared_buffers = %[6]dMB
effective_cache_size = %[7]dMB
maintenance_work_mem = %[8]dMB
work_mem = %[10]dMB
huge_pages = try

# PG18: CREATE DATABASE ... STRATEGY FILE_COPY clones files with reflinks (XFS).
file_copy_method = clone

# WAL and archiving for pgBackRest (local repository; see the backup module).
wal_level = replica
archive_mode = on
archive_command = 'pgbackrest --stanza=tiffin archive-push %%p'
archive_timeout = 300
max_wal_size = 1GB
min_wal_size = 80MB
checkpoint_completion_target = 0.9
wal_compression = zstd

shared_preload_libraries = 'pg_cron,pg_stat_statements'
cron.database_name = 'postgres'
cron.use_background_workers = on
max_worker_processes = 16

# Logging goes to the journal (journalctl -u tiffin-postgres).
logging_collector = off
log_destination = 'stderr'
log_line_prefix = '%%m [%%p] %%q%%u@%%d '
log_min_duration_statement = 2000
log_checkpoints = off
log_timezone = 'UTC'
timezone = 'UTC'
datestyle = 'iso, mdy'
default_text_search_config = 'pg_catalog.english'
lc_messages = 'C.UTF-8'
lc_monetary = 'C.UTF-8'
lc_numeric = 'C.UTF-8'
lc_time = 'C.UTF-8'
idle_in_transaction_session_timeout = '10min'
`, DataDir, ConfDir, Port, SocketDir, memMB, shared, cache, maint, MaxConnections(memMB), work)
}

// The box itself (root, via the ident map) connects as the postgres
// superuser over the socket; everything else authenticates with a password.
// The pooler looks project passwords up as tiffin_pgbouncer (pooler.go),
// over the socket as the postgres system user.
const hba = `# Managed by Tiffin. TYPE DATABASE USER ADDRESS METHOD
local   all             postgres                                peer map=tiffin
local   postgres        tiffin_pgbouncer                        peer map=tiffin
local   all             all                                     scram-sha-256
host    all             all             127.0.0.1/32            scram-sha-256
host    all             all             ::1/128                 scram-sha-256
`

const ident = `# Managed by Tiffin. MAPNAME SYSTEM-USER PG-USER
tiffin  root      postgres
tiffin  postgres  postgres
tiffin  postgres  tiffin_pgbouncer
`

// The PGDG repository wins over Ubuntu's own builds of the same packages,
// so one source updates them (Ubuntu's security pocket carries Postgres
// too, at other times and with other version numbers).
const (
	pgdgPrefs = "/etc/apt/preferences.d/tiffin-pgdg.pref"
	pgdgPin   = `# Managed by tiffin provision: Postgres, its extensions and PgBouncer come from PGDG.
Package: postgresql-` + Major + ` postgresql-` + Major + `-* postgresql-client-` + Major + ` libpq5 postgresql-common postgresql-client-common pgbouncer
Pin: release o=apt.postgresql.org
Pin-Priority: 600
`
)

func unit(confSum string) string {
	return `[Unit]
Description=Tiffin Postgres ` + Major + `
After=network.target local-fs.target
RequiresMountsFor=/var/lib/tiffin

[Service]
Type=notify
User=postgres
Group=postgres
# Config revision ` + confSum + ` (a new revision restarts the server).
Environment=TIFFIN_PG_CONF=` + confSum + `
ExecStartPre=+/usr/bin/install -d -m 2775 -o postgres -g postgres ` + SocketDir + `
ExecStart=` + BinDir + `/postgres -c config_file=` + ConfDir + `/postgresql.conf
ExecReload=/bin/kill -HUP $MAINPID
# The box holds each limited project's backends to its share of the CPUs
# (tiffin-postgres.service/p-<project>); the server and everything else
# runs in the "shared" group.
Delegate=cpu io
DelegateSubgroup=` + sharedGroup + `
KillMode=mixed
KillSignal=SIGINT
TimeoutStartSec=600
TimeoutStopSec=120
OOMScoreAdjust=-900
Restart=on-failure
RestartSec=2
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`
}
