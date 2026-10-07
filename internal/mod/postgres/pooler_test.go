package postgres

import (
	"strings"
	"testing"
	"time"
)

// The pooler's server connections for a project stay inside its role's
// connection limit and leave a quarter of it for direct connections.
func TestPoolerBudget(t *testing.T) {
	for _, mem := range []int{1024, 2048, 4096, 8192, 16384, 65536} {
		role := RoleConnLimit(mem)
		server := PoolerServerLimit(role)
		if server < 1 || server >= role || role-server < role/4 {
			t.Errorf("%d MB: role %d, pooler %d", mem, role, server)
		}
		if b := branchPoolSize(server); b < 1 || b > server/4 {
			t.Errorf("%d MB: branch pool %d of %d", mem, b, server)
		}
	}
	for _, c := range []struct{ role, server int }{{80, 60}, {400, 300}, {10, 8}, {3, 2}, {1, 1}} {
		if got := PoolerServerLimit(c.role); got != c.server {
			t.Errorf("PoolerServerLimit(%d) = %d, want %d", c.role, got, c.server)
		}
	}
	// A project limited to 10% of a 100-connection box: role 10, pooler 8,
	// each preview branch 1.
	l := roleLimits(10, 0, 100, 0)
	pools, caps := desiredPools([]projectDB{
		{Name: "p_shop", Project: "shop"},
		{Name: "p_shop__pv_pr1", Project: "shop", Branch: "pv-pr1"},
		{Name: "p_blog", Project: "blog"},
	}, func(project string) int {
		if project == "shop" {
			return l.Connections
		}
		return RoleConnLimit(4096)
	})
	sizes := map[string]int{}
	for _, p := range pools {
		sizes[p.Database] = p.Size
	}
	if l.Connections != 10 || caps["p_shop"] != 8 || sizes["p_shop"] != 8 || sizes["p_shop__pv_pr1"] != 1 || caps["p_blog"] != 60 || sizes["p_blog"] != 60 {
		t.Fatalf("role %d, caps %v, pools %v", l.Connections, caps, sizes)
	}
}

func TestPoolerConfig(t *testing.T) {
	c := poolerConfig(4096)
	for _, want := range []string{
		"listen_addr = 127.0.0.1\nlisten_port = 6432\nunix_socket_dir = /var/run/postgresql\n",
		"pool_mode = transaction\n", "max_prepared_statements = 200\n",
		"auth_type = hba\n", "auth_user = tiffin_pgbouncer\n", "auth_dbname = postgres\n",
		"auth_query = SELECT usename, passwd FROM public.tiffin_pgbouncer_auth($1)\n",
		"max_user_connections = 60\n", "max_user_client_connections = 1000\n", "default_pool_size = 2\n",
		"server_login_retry = 1\n", "track_extra_parameters = IntervalStyle, search_path, statement_timeout, lock_timeout, idle_in_transaction_session_timeout\n",
		"%include /etc/tiffin/pgbouncer/pools.ini\n",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("pgbouncer.ini lacks %q", want)
		}
	}
	if strings.Contains(c, "%%") {
		t.Error("unexpanded %%")
	}
	got := poolsConfig([]pool{{"p_shop__pv_pr1", "p_shop", 12}, {"p_shop", "p_shop", 60}}, map[string]int{"p_shop": 60})
	want := `; Managed by Tiffin (the postgres module syncs it). Edits are overwritten.
[databases]
p_shop = host=/var/run/postgresql port=5432 pool_size=60
p_shop__pv_pr1 = host=/var/run/postgresql port=5432 pool_size=12
* = host=/var/run/postgresql port=5432 pool_size=2

[users]
p_shop = max_user_connections=60
`
	if got != want {
		t.Fatalf("pools.ini:\n%s\nwant:\n%s", got, want)
	}
	// The admin user and the lookup role log in only as the box: their reject
	// lines come before the catch-all scram lines.
	lines := strings.Split(poolerHBA, "\n")
	if !strings.HasPrefix(lines[1], "local   pgbouncer       postgres") || !strings.Contains(lines[2], "postgres,tiffin_pgbouncer") || !strings.HasSuffix(lines[3], "reject") ||
		!strings.Contains(lines[4], "scram-sha-256") {
		t.Fatalf("hba:\n%s", poolerHBA)
	}
	if !strings.Contains(poolerAuthSQL, `rolname LIKE 'p\_%' AND rolcanlogin AND NOT rolsuper`) || !strings.Contains(poolerAuthSQL, "SECURITY DEFINER") ||
		!strings.Contains(poolerAuthSQL, "REVOKE ALL ON FUNCTION public.tiffin_pgbouncer_auth(name) FROM PUBLIC") {
		t.Fatal("the lookup must return project roles only, to the lookup role only")
	}
	if !strings.Contains(hba, "local   postgres        tiffin_pgbouncer                        peer map=tiffin") || !strings.Contains(ident, "tiffin  postgres  tiffin_pgbouncer") {
		t.Fatal("Postgres must let the pooler's lookup role in over the socket")
	}
	if !strings.Contains(poolerUnit("abc"), "ExecReload=/bin/kill -HUP $MAINPID") || !strings.Contains(poolerUnit("abc"), "KillSignal=SIGINT") {
		t.Fatal("unit")
	}
}

func TestPoolerDurations(t *testing.T) {
	// Pausing must give up well before PgBouncer's own query_wait_timeout.
	if pauseWait >= 120*time.Second || branchDrain >= pauseWait {
		t.Fatalf("pause %s, drain %s", pauseWait, branchDrain)
	}
}
