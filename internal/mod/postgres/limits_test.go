package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRoleLimits(t *testing.T) {
	const disk = 100 << 30 // 100 GB
	open := roleLimits(0, 0, disk)
	if open != (RoleLimits{Connections: 80, StatementTimeoutSeconds: 30, IdleInTransactionSeconds: 60, TempFileLimitMB: 25600}) {
		t.Fatalf("no limit: %+v", open)
	}
	// 25% of the box: a quarter of the connections and of the disk for temp files.
	if l := roleLimits(25, 120, disk); l != (RoleLimits{Connections: 25, StatementTimeoutSeconds: 120, IdleInTransactionSeconds: 60, TempFileLimitMB: 25600}) {
		t.Fatalf("25%%: %+v", l)
	}
	// Small shares keep a few connections and a usable temp file limit; big ones never take the box's fifth.
	if l := roleLimits(1, 0, 1<<30); l.Connections != 3 || l.TempFileLimitMB != minTempFileMB {
		t.Fatalf("1%%: %+v", l)
	}
	if l := roleLimits(95, 0, disk); l.Connections != 80 {
		t.Fatalf("95%%: %+v", l)
	}
	if l := roleLimits(0, 0, 0); l.TempFileLimitMB != 0 || !strings.Contains(strings.Join(l.statements("p_x"), ";"), `ALTER ROLE "p_x" RESET temp_file_limit`) {
		t.Fatalf("unknown disk: %+v %v", l, l.statements("p_x"))
	}
	want := []string{
		`ALTER ROLE "p_my_shop" CONNECTION LIMIT 25`,
		`ALTER ROLE "p_my_shop" SET statement_timeout = '120s'`,
		`ALTER ROLE "p_my_shop" SET idle_in_transaction_session_timeout = '60s'`,
		`ALTER ROLE "p_my_shop" SET temp_file_limit = '25600MB'`,
	}
	if got := roleLimits(25, 120, disk).statements("p_my_shop"); !slices.Equal(got, want) {
		t.Fatalf("statements:\n%s", strings.Join(got, "\n"))
	}
}

func TestCountTimeouts(t *testing.T) {
	log := `2026-10-04 10:00:00.123 UTC [1234] p_shop@p_shop ERROR:  canceling statement due to statement timeout
2026-10-04 10:00:00.123 UTC [1234] p_shop@p_shop STATEMENT:  SELECT count(*) FROM generate_series(1, 1e9)
2026-10-04 10:01:00.001 UTC [1240] p_shop@p_shop__pr_1 ERROR:  canceling statement due to statement timeout
2026-10-04 10:02:00.001 UTC [1250] p_blog@p_blog ERROR:  canceling statement due to statement timeout
2026-10-04 10:03:00.001 UTC [1251] p_blog@p_blog ERROR:  canceling statement due to user request
`
	if got := countTimeouts(strings.NewReader(log)); !reflect.DeepEqual(got, map[string]int{"p_shop": 2, "p_blog": 1}) {
		t.Fatalf("timeouts: %v", got)
	}
}

// The role settings on a real Postgres: a query past the time limit is
// stopped, the connection limit and temp file limit are the role's.
func TestRoleLimitsOnPostgres(t *testing.T) {
	admin, _ := embedded(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `CREATE ROLE p_shop LOGIN PASSWORD 'pw'`); err != nil {
		t.Fatal(err)
	}
	l := RoleLimits{Connections: 7, StatementTimeoutSeconds: 1, IdleInTransactionSeconds: 60, TempFileLimitMB: 512}
	if err := applyRoleLimits(ctx, admin, "p_shop", l); err != nil {
		t.Fatal(err)
	}
	// Applying twice is fine.
	if err := applyRoleLimits(ctx, admin, "p_shop", l); err != nil {
		t.Fatal(err)
	}
	var limit int
	var conf []string
	if err := admin.QueryRow(ctx, `SELECT r.rolconnlimit, s.setconfig FROM pg_roles r JOIN pg_db_role_setting s ON s.setrole = r.oid AND s.setdatabase = 0
		WHERE r.rolname = 'p_shop'`).Scan(&limit, &conf); err != nil {
		t.Fatal(err)
	}
	slices.Sort(conf)
	if limit != 7 || !slices.Equal(conf, []string{"idle_in_transaction_session_timeout=60s", "statement_timeout=1s", "temp_file_limit=512MB"}) {
		t.Fatalf("role: limit %d, settings %v", limit, conf)
	}

	cfg := admin.Config().Copy()
	cfg.User, cfg.Password = "p_shop", "pw"
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	_, err = c.Exec(ctx, `SELECT pg_sleep(3)`)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "57014" || !strings.Contains(pe.Message, "statement timeout") {
		t.Fatalf("a 3 s query under a 1 s limit: %v", err)
	}
	// A query may raise the limit for itself.
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_sleep(1.2)`); err != nil {
		t.Fatalf("raised limit: %v", err)
	}
	_ = tx.Commit(ctx)
}

// cgroups on a temp dir standing in for the service's delegated cgroup.
func TestCgroups(t *testing.T) {
	if d := GroupDir("/r", "my-shop"); d != "/r/sys/fs/cgroup/system.slice/tiffin-postgres.service/p-my-shop" {
		t.Fatal(d)
	}
	if u := unit("x"); !strings.Contains(u, "\nDelegate=cpu io\nDelegateSubgroup=shared\n") {
		t.Fatalf("the unit must delegate its cgroup:\n%s", u)
	}
	if cpuMax(0.5) != "50000 100000" || cpuMax(0.001) != "1000 100000" || cpuMax(1.75) != "175000 100000" {
		t.Fatal(cpuMax(0.5), cpuMax(0.001))
	}
	root := t.TempDir()
	c := &cgroups{dir: filepath.Join(root, serviceCgroup)}
	if c.ready() {
		t.Fatal("ready without a shared group (no delegation)")
	}
	writeFile(t, filepath.Join(c.dir, "shared", "cgroup.procs"), "1\n2\n")
	writeFile(t, filepath.Join(c.dir, "cgroup.subtree_control"), "")
	if !c.ready() || c.enable() != nil {
		t.Fatal("ready")
	}
	if b, _ := os.ReadFile(filepath.Join(c.dir, "cgroup.subtree_control")); string(b) != "+cpu +io" {
		t.Fatalf("subtree_control: %q", b)
	}
	if err := c.ensure("shop", 0.5, 25); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"cpu.max": "50000 100000", "io.weight": "default 25"} {
		if b, _ := os.ReadFile(filepath.Join(c.dir, "p-shop", f)); string(b) != want {
			t.Fatalf("%s = %q", f, b)
		}
	}
	writeFile(t, filepath.Join(c.dir, "p-shop", "cgroup.procs"), "")
	if err := c.move("p-shop", 42); err != nil {
		t.Fatal(err)
	}
	if got := c.procs("p-shop"); !slices.Equal(got, []int{42}) || !slices.Equal(c.projects(), []string{"shop"}) {
		t.Fatalf("procs %v projects %v", got, c.projects())
	}
}

func TestPlace(t *testing.T) {
	limited := map[string]bool{"shop": true, "blog": true}
	backends := map[int]string{10: "shop", 11: "shop", 20: "blog", 30: "open"}
	members := map[string][]int{
		"shop": {10, 20, 99}, // 20 is blog's now (a reused pid), 99 exited or not a backend
		"old":  {50},         // a project whose limit was lifted
		"gone": {},           // lifted and empty
	}
	moves, gone := place(limited, backends, members)
	want := []move{{11, "p-shop"}, {20, "p-blog"}, {50, sharedGroup}, {99, sharedGroup}}
	if !reflect.DeepEqual(moves, want) {
		t.Fatalf("moves: %+v", moves)
	}
	if !slices.Equal(gone, []string{"gone", "old"}) {
		t.Fatalf("gone: %v", gone)
	}
	// Settled: nothing to do.
	members = map[string][]int{"shop": {10, 11}, "blog": {20}}
	if moves, gone := place(limited, backends, members); len(moves) != 0 || len(gone) != 0 {
		t.Fatalf("settled: %v %v", moves, gone)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
