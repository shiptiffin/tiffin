package valkey

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeServer answers RESP commands from a script: each request line set is
// matched to a canned reply.
func fakeServer(t *testing.T, replies map[string]string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					var n int
					if _, err := fmtSscanf(line, &n); err != nil {
						return
					}
					args := make([]string, n)
					for i := range args {
						_, _ = r.ReadString('\n') // $len
						a, _ := r.ReadString('\n')
						args[i] = strings.TrimSuffix(a, "\r\n")
					}
					reply, ok := replies[strings.Join(args, " ")]
					if !ok {
						reply = "-ERR unknown\r\n"
					}
					_, _ = io.WriteString(c, reply)
				}
			}(c)
		}
	}()
	return sock
}

func fmtSscanf(line string, n *int) (int, error) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
	v := 0
	for _, ch := range line {
		if ch < '0' || ch > '9' {
			return 0, io.ErrUnexpectedEOF
		}
		v = v*10 + int(ch-'0')
	}
	*n = v
	return 1, nil
}

func TestRESPClient(t *testing.T) {
	sock := fakeServer(t, map[string]string{
		"AUTH tiffin pw":                "+OK\r\n",
		"PING":                          "+PONG\r\n",
		"GET k":                         "$5\r\nhello\r\n",
		"GET nil":                       "$-1\r\n",
		"INCR n":                        ":7\r\n",
		"SCAN 0 MATCH p_a:* COUNT 1000": "*2\r\n$1\r\n0\r\n*2\r\n$5\r\np_a:x\r\n$5\r\np_a:y\r\n",
		"SET other:x 1":                 "-NOPERM No permissions to access a key\r\n",
	})
	ctx := context.Background()
	c, err := Dial(ctx, "unix", sock, "tiffin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if s, err := c.String(ctx, "PING"); s != "PONG" || err != nil {
		t.Fatalf("PING: %q %v", s, err)
	}
	if s, _ := c.String(ctx, "GET", "k"); s != "hello" {
		t.Fatalf("GET: %q", s)
	}
	if _, err := c.String(ctx, "GET", "nil"); err != ErrNil {
		t.Fatalf("nil reply: %v", err)
	}
	if n, _ := c.Int(ctx, "INCR", "n"); n != 7 {
		t.Fatalf("INCR: %d", n)
	}
	v, err := c.Do(ctx, "SCAN", "0", "MATCH", "p_a:*", "COUNT", "1000")
	if err != nil {
		t.Fatal(err)
	}
	cur, keys := scanReply(v)
	if cur != "0" || !slices.Equal(keys, []string{"p_a:x", "p_a:y"}) {
		t.Fatalf("scan: %q %v", cur, keys)
	}
	_, err = c.Do(ctx, "SET", "other:x", "1")
	if _, ok := err.(RedisError); !ok || !strings.HasPrefix(err.Error(), "NOPERM") {
		t.Fatalf("error reply: %v", err)
	}
	if _, err := Dial(ctx, "unix", sock, "tiffin", "wrong"); err == nil {
		t.Fatal("bad password accepted")
	}
}

func TestACLRules(t *testing.T) {
	rules := strings.Join(ACLRules("my-shop", "secret"), " ")
	for _, want := range []string{"reset", "on", ">secret", "~p_my_shop:*", "&p_my_shop:*", "-@admin", "-@dangerous", "-scan", "-select"} {
		if !strings.Contains(" "+rules+" ", " "+want+" ") {
			t.Errorf("rules lack %q: %s", want, rules)
		}
	}
	if strings.Contains(rules, "~*") || strings.Contains(rules, "allkeys") {
		t.Error("a project user must never see all keys")
	}
	if Prefix("my-shop") != "p_my_shop:" || User("my-shop") != "p_my_shop" {
		t.Error("names")
	}
}

func TestACLFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.acl")
	changed, err := ensureACLFile(path, "pw1")
	if err != nil || !changed {
		t.Fatalf("first write: %v %v", changed, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "user default off") || strings.Contains(string(raw), "pw1") {
		t.Fatalf("default user must be off and the admin password hashed:\n%s", raw)
	}
	// Valkey's ACL SAVE rewrites lines in its own form and adds project users.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("user p_shop on #abc ~p_shop:* &p_shop:* +@all\n")
	f.Close()
	if changed, _ := ensureACLFile(path, "pw1"); changed {
		t.Fatal("unchanged admin password must not rewrite the file (that would restart Valkey)")
	}
	if changed, _ := ensureACLFile(path, "pw2"); !changed {
		t.Fatal("new admin password must rewrite")
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "user p_shop on") {
		t.Fatal("project users must survive")
	}
}

func TestConfig(t *testing.T) {
	c := Config(3000)
	for _, want := range []string{"bind 127.0.0.1 -::1", "unixsocket /var/run/valkey/valkey.sock", "appendonly yes", "aclfile " + ACLFile,
		"maxmemory 375mb", "maxmemory-policy volatile-lru", "dir " + DataDir} {
		if !strings.Contains(c, want) {
			t.Errorf("config lacks %q", want)
		}
	}
	if strings.Contains(c, "requirepass") || strings.Contains(c, "bind 0.0.0.0") {
		t.Error("no shared password, no public bind")
	}
	if MaxMemoryMB(512) != 128 || MaxMemoryMB(1<<20) != 4096 {
		t.Error("maxmemory clamp")
	}
}

func TestParseInfo(t *testing.T) {
	m := parseInfo("# Memory\r\nused_memory:1024\r\nmaxmemory:0\r\n\r\n# Keyspace\r\ndb0:keys=3,expires=0,avg_ttl=0\r\n")
	if m["used_memory"] != "1024" || m["db0"] != "keys=3,expires=0,avg_ttl=0" {
		t.Fatalf("%v", m)
	}
}
