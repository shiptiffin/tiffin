package valkey

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// These tests run a real valkey-server (skipped when there is none on PATH):
// what a project's ACL user may run, and the script guard.

type realServer struct {
	sock string
	cmd  *exec.Cmd
	done chan struct{}
}

func startValkey(t *testing.T) *realServer {
	t.Helper()
	bin, err := exec.LookPath("valkey-server")
	if err != nil {
		t.Skip("valkey-server not on PATH")
	}
	dir, err := os.MkdirTemp("", "vk") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &realServer{sock: filepath.Join(dir, "v.sock"), done: make(chan struct{})}
	s.cmd = exec.Command(bin, "--port", "0", "--unixsocket", s.sock, "--dir", dir, "--save", "", "--appendonly", "no",
		"--busy-reply-threshold", "200", "--user", adminUser, "on", ">pw", "~*", "&*", "+@all")
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.cmd.Wait(); close(s.done) }()
	t.Cleanup(func() {
		_ = s.cmd.Process.Kill()
		<-s.done
	})
	for i := 0; ; i++ {
		if c, err := s.admin(context.Background()); err == nil {
			c.Close()
			return s
		}
		if i == 100 {
			t.Fatal("valkey-server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *realServer) admin(ctx context.Context) (*Client, error) {
	return Dial(ctx, "unix", s.sock, adminUser, "pw")
}

// project makes a project's ACL user with the box's rules and connects as it.
func (s *realServer) project(t *testing.T, project string) *Client {
	t.Helper()
	ctx := context.Background()
	a, err := s.admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Do(ctx, append([]string{"ACL", "SETUSER", User(project)}, ACLRules(project, "ppw")...)...); err != nil {
		t.Fatal(err)
	}
	c, err := Dial(ctx, "unix", s.sock, User(project), "ppw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// A project may run Lua scripts on its own keys, but not load functions
// (a library is server-wide: one project could replace another's) or kill
// and flush scripts.
func TestProjectACLScripting(t *testing.T) {
	s := startValkey(t)
	ctx := context.Background()
	shop := s.project(t, "shop")
	if v, err := shop.Do(ctx, "EVAL", "return redis.call('SET', KEYS[1], 'x')", "1", Prefix("shop")+"k"); err != nil || v != "OK" {
		t.Fatalf("EVAL on its own key: %v %v", v, err)
	}
	if _, err := shop.Do(ctx, "SCRIPT", "LOAD", "return 1"); err != nil {
		t.Fatalf("SCRIPT LOAD: %v", err)
	}
	// The SDK's Next.js cache prunes tags with a compare-and-delete script
	// (packages/sdk/src/next/store.ts, PRUNE_SCRIPT).
	const prune = "local n = 0\nfor i = 1, #ARGV, 2 do\n  if redis.call('HGET', KEYS[1], ARGV[i]) == ARGV[i + 1] then n = n + redis.call('HDEL', KEYS[1], ARGV[i]) end\nend\nreturn n"
	tags := Prefix("shop") + "tags"
	if _, err := shop.Do(ctx, "HSET", tags, "a", "1", "b", "2"); err != nil {
		t.Fatal(err)
	}
	if n, err := shop.Int(ctx, "EVAL", prune, "1", tags, "a", "1", "b", "3"); err != nil || n != 1 {
		t.Fatalf("the Next.js cache's prune script: %d %v", n, err)
	}
	for _, cmd := range [][]string{
		{"FUNCTION", "LOAD", "#!lua name=lib\nserver.register_function('f', function() return 1 end)"},
		{"FUNCTION", "DELETE", "lib"},
		{"FUNCTION", "FLUSH"},
		{"FCALL", "f", "0"},
		{"SCRIPT", "KILL"},
		{"SCRIPT", "FLUSH"},
	} {
		if _, err := shop.Do(ctx, cmd...); !replyIs(err, "NOPERM") {
			t.Errorf("%s: %v", strings.Join(cmd[:2], " "), err)
		}
	}
}

// The script guard kills a script that runs too long; one that already
// wrote can't be killed, so the guard stops the server without saving.
func TestScriptGuard(t *testing.T) {
	s := startValkey(t)
	ctx := context.Background()
	if did := stopLongScript(ctx, s.admin); did != "" {
		t.Fatalf("nothing running: %q", did)
	}
	shop := s.project(t, "shop")
	loop := func(script string) chan error {
		done := make(chan error, 1)
		go func() {
			_, err := shop.Do(ctx, "EVAL", script, "1", Prefix("shop")+"hang")
			done <- err
		}()
		time.Sleep(400 * time.Millisecond) // past busy-reply-threshold (200 ms)
		return done
	}
	done := loop("while true do end")
	if did := stopLongScript(ctx, s.admin); did != "killed" {
		t.Fatalf("a read-only loop: %q", did)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("the script's caller: %v", err)
	}
	other := s.project(t, "other")
	if _, err := other.Do(ctx, "SET", Prefix("other")+"k", "v"); err != nil {
		t.Fatalf("another project after the kill: %v", err)
	}

	loop("redis.call('SET', KEYS[1], 'x') while true do end")
	if did := stopLongScript(ctx, s.admin); did != "restarted" {
		t.Fatalf("a loop that wrote: %q", did)
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server is still running")
	}
}

// The cache limit clears only keys that still expire: one the app made
// permanent after it was listed stays.
func TestUnlinkExpiring(t *testing.T) {
	s := startValkey(t)
	ctx := context.Background()
	c, err := s.admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, cmd := range [][]string{{"SET", "p_a:cache", "x", "PX", "60000"}, {"SET", "p_a:kept", "x", "PX", "60000"}, {"PERSIST", "p_a:kept"}} {
		if _, err := c.Do(ctx, cmd...); err != nil {
			t.Fatal(err)
		}
	}
	if gone, err := unlinkExpiring(ctx, c, "p_a:cache"); err != nil || !gone {
		t.Fatalf("an expiring key: %v %v", gone, err)
	}
	if gone, err := unlinkExpiring(ctx, c, "p_a:kept"); err != nil || gone {
		t.Fatalf("a key made permanent: %v %v", gone, err)
	}
	if n, _ := c.Int(ctx, "EXISTS", "p_a:cache", "p_a:kept"); n != 1 {
		t.Fatalf("keys left: %d", n)
	}
}

// Against the real server: WATCH, PEXPIRETIME and MULTI/EXEC as write()
// uses them, so Undo puts the key back with its expiry.
func TestKVUndoRealServer(t *testing.T) {
	s := startValkey(t)
	s.project(t, "app")
	h := newREST(func(_ context.Context, project string) (string, bool, error) { return "ppw", project == "app", nil },
		func(ctx context.Context, user, pw string) (*Client, error) {
			return Dial(ctx, "unix", s.sock, user, pw)
		})
	ctx := context.Background()
	w, _ := buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "k"}, Value: "v1", TTLSeconds: 600})
	if _, err := h.write(ctx, "app", w, nil); err != nil {
		t.Fatal(err)
	}
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "k"}, Value: "v2"})
	res, err := h.write(ctx, "app", w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.undo(ctx, "app", res.Undo, "", nil); err != nil {
		t.Fatalf("undo: %v", err)
	}
	a, _ := s.admin(ctx)
	defer a.Close()
	if v, _ := a.String(ctx, "GET", "p_app:k"); v != "v1" {
		t.Fatalf("value after undo: %q", v)
	}
	if ttl, _ := a.Int(ctx, "PTTL", "p_app:k"); ttl <= 0 || ttl > 600_000 {
		t.Fatalf("expiry after undo: %d", ttl)
	}
}

// The key browser cuts big items to their first 64 KB, says which, and
// pages a list of big items by size, picking up where the last page
// stopped.
func TestKVReadClipsBigItems(t *testing.T) {
	s := startValkey(t)
	ctx := context.Background()
	c, err := s.admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pre := Prefix("app")
	big := strings.Repeat("é", 2_500_000) // 5 MB, two bytes a character
	if _, err := c.Do(ctx, "HSET", pre+"h", "small", "x", "big", big, "huge", big); err != nil {
		t.Fatal(err)
	}
	fields, clipped, cursor := map[string]string{}, []KVClipped{}, ""
	for page := 0; page == 0 || cursor != ""; page++ {
		v, err := getKey(ctx, c, "app", "h", valueQuery{count: 200, cursor: cursor})
		if err != nil || page > 10 {
			t.Fatalf("hash page %d: %v", page, err)
		}
		maps.Copy(fields, v.Value.(map[string]string))
		clipped, cursor = append(clipped, v.Clipped...), v.Cursor
	}
	if len(fields) != 3 || fields["small"] != "x" || len(clipped) != 2 {
		t.Fatalf("hash: %d fields, clipped %+v", len(fields), clipped)
	}
	for _, cl := range clipped {
		if cl.Bytes != 5_000_000 || cl.Name || len(fields[cl.Item]) != itemMax || !utf8.ValidString(fields[cl.Item]) {
			t.Fatalf("clipped field %q: %+v, %d bytes shown", cl.Item, cl, len(fields[cl.Item]))
		}
	}

	item := strings.Repeat("a", 100_000)
	cmd := []string{"RPUSH", pre + "l"}
	for range 40 {
		cmd = append(cmd, item)
	}
	if _, err := c.Do(ctx, cmd...); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for page := 0; ; page++ {
		v, err := getKey(ctx, c, "app", "l", valueQuery{count: 200, cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		items := v.Value.([]any)
		if len(items) == 0 || len(items)*itemMax > pageMax+itemMax || len(v.Clipped) != len(items) || v.Clipped[0].Item != strconv.Itoa(seen) || v.Clipped[0].Bytes != 100_000 {
			t.Fatalf("list page %d: %d items, clipped %+v", page, len(items), v.Clipped[:min(2, len(v.Clipped))])
		}
		seen += len(items)
		if cursor = v.Cursor; cursor == "" {
			break
		}
		if cursor != strconv.Itoa(seen) || page > 40 {
			t.Fatalf("list cursor %q after %d items", cursor, seen)
		}
	}
	if seen != 40 {
		t.Fatalf("list pages held %d items, want 40", seen)
	}
}

// One tree request looks at no more than treeScanMax keys and then says its
// counts are partial; a search from the top counts only the keys it matches.
func TestKVTreeScanCap(t *testing.T) {
	s := startValkey(t)
	ctx := context.Background()
	c, err := s.admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	was := treeScanMax
	treeScanMax = 1000
	t.Cleanup(func() { treeScanMax = was })
	cmds := make([][]string, 0, 5000)
	for i := range 5000 {
		cmds = append(cmds, []string{"SET", Prefix("app") + "session:" + strconv.Itoa(i), "v"})
	}
	if _, err := c.Pipe(ctx, cmds...); err != nil {
		t.Fatal(err)
	}
	top, err := tree(ctx, c, "app", treeQuery{delimiter: ":"})
	if err != nil {
		t.Fatal(err)
	}
	if !top.Partial || top.Scanned < 1000 || top.Scanned > 3000 || len(top.Groups) != 1 || top.Groups[0].Keys != top.Scanned {
		t.Fatalf("capped: %+v", top)
	}
	found, err := tree(ctx, c, "app", treeQuery{match: "session:4??"})
	if err != nil {
		t.Fatal(err)
	}
	if found.Partial || found.Total != 100 {
		t.Fatalf("search: total %d partial %v", found.Total, found.Partial)
	}
}
