package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestTunnelURL(t *testing.T) {
	for _, c := range []struct{ in, local, remote string }{
		{"postgresql://p_shop:s3cret@127.0.0.1:5432/p_shop?sslmode=disable", "postgresql://p_shop:s3cret@127.0.0.1:15432/p_shop?sslmode=disable", "127.0.0.1:5432"},
		{"redis://u_shop:pw%2F1@127.0.0.1:6379", "redis://u_shop:pw%2F1@127.0.0.1:15432", "127.0.0.1:6379"},
	} {
		local, remote, err := tunnelURL(c.in, 15432)
		if err != nil || local != c.local || remote != c.remote {
			t.Errorf("tunnelURL(%q) = %q, %q, %v; want %q, %q", c.in, local, remote, err, c.local, c.remote)
		}
	}
	for _, bad := range []string{"", "postgresql://127.0.0.1:5432/db", "redis+unix://u:p@/run/valkey.sock", "postgresql://u:p@localhost/db"} {
		if _, _, err := tunnelURL(bad, 15432); err == nil {
			t.Errorf("tunnelURL(%q): want an error", bad)
		}
	}
}

func TestTunnelSSH(t *testing.T) {
	server := &boxConfig{Provider: "hetzner", Server: &serverBox{SSH: "root@203.0.113.5:2222", Identity: "/k/id", KnownHosts: "/k/known_hosts"}}
	got, err := tunnelSSH(server, 15432, "127.0.0.1:5432", "/l", "tiffin")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(got, " ")
	for _, want := range []string{"-N", "-p 2222", "-L 127.0.0.1:15432:127.0.0.1:5432", "ExitOnForwardFailure=yes", "BatchMode=yes",
		"UserKnownHostsFile=/k/known_hosts", "-i /k/id", "IdentitiesOnly=yes"} {
		if !strings.Contains(line, want) {
			t.Errorf("server args %q lack %q", line, want)
		}
	}
	if got[len(got)-2] != "--" || got[len(got)-1] != "root@203.0.113.5" {
		t.Errorf("server args end %q", got[len(got)-2:])
	}

	// A local box goes through the Lima VM's own ssh config and host alias.
	got, err = tunnelSSH(&boxConfig{Provider: "local"}, 16379, "127.0.0.1:6379", "/Users/me/.lima", "tiffin")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "-F" || got[1] != "/Users/me/.lima/tiffin/ssh.config" || got[len(got)-1] != "lima-tiffin" || !slices.Contains(got, "127.0.0.1:16379:127.0.0.1:6379") {
		t.Errorf("local args %q", got)
	}

	if _, err := tunnelSSH(&boxConfig{Provider: "mystery"}, 1, "127.0.0.1:5432", "/l", "tiffin"); err == nil {
		t.Error("a box without SSH access: want an error")
	}
	if _, err := tunnelSSH(&boxConfig{Server: &serverBox{SSH: "no-user-here"}}, 1, "127.0.0.1:5432", "/l", "tiffin"); err == nil {
		t.Error("a bad ssh target: want an error")
	}
}

func TestLimaHome(t *testing.T) {
	if got := limaHome(func(k string) string { return map[string]string{"LIMA_HOME": "/x"}[k] }); got != "/x" {
		t.Errorf("LIMA_HOME: %q", got)
	}
	if got := limaHome(func(k string) string { return map[string]string{"HOME": "/h"}[k] }); got != "/h/.lima" {
		t.Errorf("HOME: %q", got)
	}
}

func TestTunnelNeedsABox(t *testing.T) {
	// A local --home box (no `tiffin up`) has no SSH access to reuse.
	env := newEnv(t)
	env["TIFFIN_CONFIG_DIR"] = t.TempDir()
	code, out, _ := run(t, env, "db", "tunnel", "shop")
	if code != ExitInvalid || !strings.Contains(string(out), "tiffin up") {
		t.Fatalf("code %d, out %s", code, out)
	}
	if code, out, _ := run(t, env, "kv", "tunnel", "shop", "--port", "0"); code != ExitInvalid || !strings.Contains(string(out), "--port") {
		t.Fatalf("--port 0: code %d, out %s", code, out)
	}
}
