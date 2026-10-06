package valkey

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// cmdReply is a small COMMAND reply in Valkey's shape (key specs included).
func cmdReply() []any {
	idx := func(i int64) []any { return []any{"type", "index", "spec", []any{"index", i}} }
	rng := func(last, step, limit int64) []any {
		return []any{"type", "range", "spec", []any{"lastkey", last, "keystep", step, "limit", limit}}
	}
	keynum := []any{"type", "keynum", "spec", []any{"keynumidx", int64(0), "firstkey", int64(1), "keystep", int64(1)}}
	spec := func(bs, fk []any) []any {
		return []any{"notes", "", "flags", []any{"RW"}, "begin_search", bs, "find_keys", fk}
	}
	cmd := func(name string, flags []any, specs []any, subs []any) []any {
		return []any{name, int64(-1), flags, int64(1), int64(1), int64(1), []any{}, []any{}, specs, subs}
	}
	ro, wr := []any{"readonly", "fast"}, []any{"write", "denyoom"}
	one := []any{spec(idx(1), rng(0, 1, 0))}
	return []any{
		cmd("get", ro, one, []any{}),
		cmd("set", wr, one, []any{}),
		cmd("incr", wr, one, []any{}),
		cmd("mset", wr, []any{spec(idx(1), rng(-1, 2, 0))}, []any{}),
		cmd("eval", []any{"noscript"}, []any{spec(idx(2), keynum)}, []any{}),
		cmd("evalsha", []any{"noscript"}, []any{spec(idx(2), keynum)}, []any{}),
		cmd("blpop", wr, []any{spec(idx(1), rng(-2, 1, 0))}, []any{}),
		cmd("xread", ro, []any{spec([]any{"type", "keyword", "spec", []any{"keyword", "STREAMS", "startfrom", int64(1)}}, rng(-1, 1, 2))}, []any{}),
		cmd("lmove", wr, []any{spec(idx(1), rng(0, 1, 0)), spec(idx(2), rng(0, 1, 0))}, []any{}),
		cmd("object", []any{}, []any{}, []any{cmd("object|encoding", ro, []any{spec(idx(2), rng(0, 1, 0))}, []any{})}),
		cmd("script", []any{}, []any{}, []any{cmd("script|load", []any{"noscript"}, []any{}, []any{})}),
		cmd("ping", []any{"fast"}, []any{}, []any{}),
		cmd("publish", []any{"pubsub"}, []any{}, []any{}),
	}
}

func TestKeyPositions(t *testing.T) {
	cmds := parseCommands(cmdReply())
	for _, c := range []struct {
		args string
		want []int
	}{
		{"GET k", []int{1}},
		{"SET k v EX 10", []int{1}},
		{"MSET a 1 b 2", []int{1, 3}},
		{"EVAL s 2 a b x y", []int{3, 4}},
		{"EVAL s 0 x", nil},
		{"BLPOP a b 0", []int{1, 2}},
		{"XREAD COUNT 2 STREAMS s1 s2 0 0", []int{4, 5}},
		{"LMOVE a b LEFT RIGHT", []int{1, 2}},
		{"OBJECT ENCODING k", []int{2}},
		{"PING", nil},
	} {
		args := strings.Fields(c.args)
		info := cmds.lookup(args)
		if info == nil {
			t.Fatalf("%s: no command info", c.args)
		}
		if got := info.keyPositions(args); !slices.Equal(got, c.want) {
			t.Errorf("%s: keys at %v, want %v", c.args, got, c.want)
		}
	}
	if !cmds.lookup([]string{"object", "encoding", "k"}).readonly || cmds.lookup([]string{"set"}).readonly {
		t.Error("readonly flags")
	}
}

func TestCleanShebang(t *testing.T) {
	for in, want := range map[string]string{
		"#!lua flags=allow-key-locking\n  return 1":         "#!lua\n  return 1",
		"#!lua flags=no-writes,allow-key-locking\nreturn 1": "#!lua flags=no-writes\nreturn 1",
		"#!lua flags=no-writes\nreturn 1":                   "",
		"return 1":                                          "",
	} {
		got, ok := cleanShebang(in)
		if (want == "") == ok || (ok && got != want) {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
}

// fakeValkey is a tiny Valkey for the REST tests: strings, MULTI/EXEC,
// scripts that echo their KEYS and ARGV, and an ACL that only allows keys
// and channels under "p_app:" or "p_other:".
type fakeValkey struct {
	mu      sync.Mutex
	data    map[string]string
	scripts map[string]string
	got     [][]string
	dials   int
}

func respEncode(v any) string {
	switch x := v.(type) {
	case nil:
		return "$-1\r\n"
	case int64:
		return ":" + strconv.FormatInt(x, 10) + "\r\n"
	case int:
		return ":" + strconv.Itoa(x) + "\r\n"
	case string:
		return "$" + strconv.Itoa(len(x)) + "\r\n" + x + "\r\n"
	case RedisError:
		return "-" + string(x) + "\r\n"
	case []any:
		var b strings.Builder
		b.WriteString("*" + strconv.Itoa(len(x)) + "\r\n")
		for _, e := range x {
			b.WriteString(respEncode(e))
		}
		return b.String()
	}
	panic(fmt.Sprintf("respEncode %T", v))
}

func (f *fakeValkey) exec(args []string) any {
	f.got = append(f.got, args)
	name := strings.ToLower(args[0])
	key := func(i int) bool {
		return i < len(args) && (strings.HasPrefix(args[i], "p_app:") || strings.HasPrefix(args[i], "p_other:"))
	}
	sha := func(s string) string { h := sha1.Sum([]byte(s)); return hex.EncodeToString(h[:]) }
	switch name {
	case "ping":
		return "PONG"
	case "command":
		return cmdReply()
	case "get", "set", "incr", "blpop", "lmove", "publish":
		if !key(1) {
			return RedisError("NOPERM No permissions to access a key")
		}
	}
	switch name {
	case "get":
		v, ok := f.data[args[1]]
		if !ok {
			return nil
		}
		return v
	case "set":
		f.data[args[1]] = args[2]
		return "OK"
	case "incr":
		n, _ := strconv.Atoi(f.data[args[1]])
		f.data[args[1]] = strconv.Itoa(n + 1)
		return int64(n + 1)
	case "blpop":
		return []any{args[1], "v"}
	case "publish":
		return int64(0)
	case "script":
		if strings.Contains(args[2], "allow-key-locking") {
			return RedisError("ERR Unexpected flag in script shebang: allow-key-locking")
		}
		f.scripts[sha(args[2])] = args[2]
		return sha(args[2])
	case "eval", "evalsha":
		s := args[1]
		if name == "evalsha" {
			var ok bool
			if s, ok = f.scripts[s]; !ok {
				return RedisError("NOSCRIPT No matching script. Please use EVAL.")
			}
		}
		if strings.Contains(s, "allow-key-locking") {
			return RedisError("ERR Unexpected flag in script shebang: allow-key-locking")
		}
		f.scripts[sha(s)] = s
		out := []any{}
		for _, a := range args[3:] {
			out = append(out, a)
		}
		return out
	}
	return RedisError("ERR unknown command '" + args[0] + "'")
}

func (f *fakeValkey) serve(t *testing.T) string {
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
				var queue [][]string
				inTx, admin := false, false
				for {
					args, err := readCommand(r)
					if err != nil {
						return
					}
					f.mu.Lock()
					var reply any
					switch strings.ToLower(args[0]) {
					case "auth":
						reply = "OK"
						if args[2] != "pw" && args[2] != "pw2" && args[2] != "adminpw" {
							reply = RedisError("WRONGPASS")
						}
						admin = args[1] == "tiffin"
					case "scan": // only the admin may; MATCH is the 4th argument
						reply = RedisError("NOPERM User has no permissions to run the 'scan' command")
						if admin {
							keys := []any{}
							for k := range f.data {
								if globMatch(args[3], k) {
									keys = append(keys, k)
								}
							}
							reply = []any{"0", keys}
						}
					case "multi":
						inTx, queue, reply = true, nil, "OK"
					case "discard":
						inTx, reply = false, "OK"
					case "exec":
						out := []any{}
						for _, q := range queue {
							out = append(out, f.exec(q))
						}
						inTx, reply = false, out
					default:
						if inTx {
							if strings.ToLower(args[0]) == "nosuch" {
								reply = RedisError("ERR unknown command 'nosuch'")
							} else {
								queue, reply = append(queue, args), "QUEUED"
							}
						} else {
							reply = f.exec(args)
						}
					}
					f.mu.Unlock()
					// Simple strings for status replies, like the real server.
					s := respEncode(reply)
					if x, ok := reply.(string); ok && (x == "OK" || x == "QUEUED" || x == "PONG") {
						s = "+" + x + "\r\n"
					}
					if _, err := io.WriteString(c, s); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return sock
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, n)
	for i := range args {
		l, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, "$")))
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args[i] = string(buf[:size])
	}
	return args, nil
}

func newTestREST(t *testing.T) (*rest, *fakeValkey) {
	f := &fakeValkey{data: map[string]string{}, scripts: map[string]string{}}
	sock := f.serve(t)
	h := newREST(func(_ context.Context, project string) (string, bool, error) {
		switch project {
		case "app":
			return "pw", true, nil
		case "other":
			return "pw2", true, nil
		}
		return "", false, nil
	}, func(ctx context.Context, user, pw string) (*Client, error) {
		f.mu.Lock()
		f.dials++
		f.mu.Unlock()
		return Dial(ctx, "unix", sock, user, pw)
	})
	return h, f
}

type restReply struct {
	code int
	body map[string]any
	list []map[string]any
}

func call(t *testing.T, h http.Handler, method, path, token, body string, b64 bool) restReply {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if b64 {
		req.Header.Set("Upstash-Encoding", "base64")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	out := restReply{code: w.Code}
	raw := w.Body.Bytes()
	if json.Unmarshal(raw, &out.body) != nil {
		if err := json.Unmarshal(raw, &out.list); err != nil {
			t.Fatalf("%s %s: not JSON: %s", method, path, raw)
		}
	}
	return out
}

func TestRESTCommands(t *testing.T) {
	h, f := newTestREST(t)
	tok, ro := restTokens("app", "pw")
	otherTok, _ := restTokens("other", "pw2")

	// Auth: no token, a wrong one, a project without Valkey.
	for _, bad := range []string{"", "tvk_app_0000", "nope", "tvk_gone_" + strings.Repeat("a", 40), otherTok + "x"} {
		if r := call(t, h, "POST", "/", bad, `["GET","k"]`, false); r.code != 401 || r.body["error"] != "Unauthorized" {
			t.Fatalf("token %q: %d %v", bad, r.code, r.body)
		}
	}

	// One command: keys get the prefix, the reply is plain or base64.
	if r := call(t, h, "POST", "/", tok, `["SET","greeting","hello"]`, true); r.code != 200 || r.body["result"] != "OK" {
		t.Fatalf("SET: %d %v", r.code, r.body)
	}
	if f.data["p_app:greeting"] != "hello" {
		t.Fatalf("stored: %v", f.data)
	}
	if r := call(t, h, "POST", "/", tok, `["GET","greeting"]`, true); r.body["result"] != "aGVsbG8=" {
		t.Fatalf("GET base64: %v", r.body)
	}
	if r := call(t, h, "POST", "/", tok, `["GET","greeting"]`, false); r.body["result"] != "hello" {
		t.Fatalf("GET: %v", r.body)
	}
	if r := call(t, h, "POST", "/", tok, `["GET","missing"]`, false); r.code != 200 || r.body["result"] != nil {
		t.Fatalf("GET missing: %v", r.body)
	}
	if r := call(t, h, "POST", "/", tok, `["INCR","n"]`, false); r.body["result"] != float64(1) {
		t.Fatalf("INCR: %v", r.body)
	}
	// Path style, with a query argument and a POST body as the value.
	if r := call(t, h, "GET", "/set/a%2Fb/v1?EX=60", tok, "", false); r.body["result"] != "OK" || f.data["p_app:a/b"] != "v1" {
		t.Fatalf("path SET: %v %v", r.body, f.got[len(f.got)-1])
	}
	if got := f.got[len(f.got)-1]; !slices.Equal(got, []string{"set", "p_app:a/b", "v1", "EX", "60"}) {
		t.Fatalf("path SET args: %v", got)
	}
	if r := call(t, h, "POST", "/set/posted", tok, "body value", false); r.body["result"] != "OK" || f.data["p_app:posted"] != "body value" {
		t.Fatalf("path POST: %v", r.body)
	}
	if r := call(t, h, "GET", "/get/greeting", tok, "", false); r.body["result"] != "hello" {
		t.Fatalf("path GET: %v", r.body)
	}
	// Errors: a command error is 400 with the server's message; refused commands too.
	if r := call(t, h, "POST", "/", tok, `["NOSUCH"]`, false); r.code != 400 || !strings.Contains(r.body["error"].(string), "unknown command") {
		t.Fatalf("unknown: %d %v", r.code, r.body)
	}
	for _, c := range []string{`["CLIENT","REPLY","OFF"]`, `["SUBSCRIBE","c"]`, `["MULTI"]`, `["HELLO","3"]`, `["AUTH","x","y"]`} {
		if r := call(t, h, "POST", "/", tok, c, false); r.code != 400 || !strings.Contains(r.body["error"].(string), "not available over REST") {
			t.Fatalf("%s: %d %v", c, r.code, r.body)
		}
	}
	if r := call(t, h, "POST", "/", tok, `{"cmd":"GET"}`, false); r.code != 400 {
		t.Fatalf("bad body: %d", r.code)
	}
	// Replies that name keys come back without the prefix; PUBLISH channels get it.
	if r := call(t, h, "POST", "/", tok, `["BLPOP","q","0"]`, false); !reflect.DeepEqual(r.body["result"], []any{"q", "v"}) {
		t.Fatalf("BLPOP: %v", r.body)
	}
	call(t, h, "POST", "/", tok, `["PUBLISH","news","hi"]`, false)
	if got := f.got[len(f.got)-1]; got[1] != "p_app:news" {
		t.Fatalf("PUBLISH: %v", got)
	}

	// Another project's token reaches only its own prefix: same name, other key.
	if r := call(t, h, "POST", "/", otherTok, `["GET","greeting"]`, false); r.code != 200 || r.body["result"] != nil {
		t.Fatalf("other project: %d %v", r.code, r.body)
	}
	if got := f.got[len(f.got)-1]; got[1] != "p_other:greeting" {
		t.Fatalf("other project key: %v", got)
	}

	// The read-only token reads but does not write.
	if r := call(t, h, "POST", "/", ro, `["GET","greeting"]`, false); r.body["result"] != "hello" {
		t.Fatalf("ro GET: %v", r.body)
	}
	if r := call(t, h, "POST", "/", ro, `["SET","greeting","x"]`, false); r.code != 400 || !strings.Contains(r.body["error"].(string), "read-only") {
		t.Fatalf("ro SET: %d %v", r.code, r.body)
	}

	// Connections are reused.
	if f.dials > 3 {
		t.Fatalf("%d dials for one client at a time", f.dials)
	}
}

func TestRESTPipelineAndMulti(t *testing.T) {
	h, f := newTestREST(t)
	tok, _ := restTokens("app", "pw")

	r := call(t, h, "POST", "/pipeline", tok, `[["SET","a","1"],["INCR","a"],["NOSUCH"],["GET","a"]]`, true)
	if r.code != 200 || len(r.list) != 4 {
		t.Fatalf("pipeline: %d %v %v", r.code, r.body, r.list)
	}
	if r.list[0]["result"] != "OK" || r.list[1]["result"] != float64(2) || r.list[2]["error"] == nil || r.list[3]["result"] != "Mg==" {
		t.Fatalf("pipeline items: %v", r.list)
	}

	r = call(t, h, "POST", "/multi-exec", tok, `[["SET","b","x"],["GET","b"],["GET","/etc"]]`, false)
	if r.code != 200 || len(r.list) != 3 || r.list[0]["result"] != "OK" || r.list[1]["result"] != "x" {
		t.Fatalf("multi-exec: %d %v", r.code, r.list)
	}
	if f.data["p_app:b"] != "x" {
		t.Fatalf("multi-exec stored: %v", f.data)
	}
	// A command the server refuses while queueing aborts the transaction.
	r = call(t, h, "POST", "/multi-exec", tok, `[["SET","c","x"],["NOSUCH"]]`, false)
	if r.code != 400 || !strings.HasPrefix(r.body["error"].(string), "EXECABORT") || f.data["p_app:c"] != "" {
		t.Fatalf("aborted multi-exec: %d %v", r.code, r.body)
	}
	// The connection is clean afterwards.
	if r := call(t, h, "POST", "/", tok, `["GET","b"]`, false); r.body["result"] != "x" {
		t.Fatalf("after abort: %v", r.body)
	}
}

// TestRESTScan lists the project's own keys: SCAN runs as the admin with
// MATCH held to the prefix, and the prefix is stripped.
func TestRESTScan(t *testing.T) {
	h, f := newTestREST(t)
	tok, ro := restTokens("app", "pw")
	sock := f.serve(t)
	h.admin = func(ctx context.Context) (*Client, error) { return Dial(ctx, "unix", sock, "tiffin", "adminpw") }
	f.data["p_app:user:1"], f.data["p_app:user:2"], f.data["p_app:post:1"], f.data["p_other:user:9"] = "a", "b", "c", "d"

	for _, tc := range []struct {
		tok, body string
		want      []string
	}{
		{tok, `["SCAN","0"]`, []string{"post:1", "user:1", "user:2"}},
		{ro, `["SCAN","0","MATCH","user:*","COUNT","50"]`, []string{"user:1", "user:2"}},
		{tok, `["scan","0","MATCH","*9"]`, nil},
	} {
		r := call(t, h, "POST", "/", tc.tok, tc.body, false)
		res, _ := r.body["result"].([]any)
		if r.code != 200 || len(res) != 2 || res[0] != "0" {
			t.Fatalf("%s: %d %v", tc.body, r.code, r.body)
		}
		var got []string
		for _, k := range res[1].([]any) {
			got = append(got, k.(string))
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%s: keys %v, want %v", tc.body, got, tc.want)
		}
	}
	for _, bad := range []string{`["SCAN"]`, `["SCAN","x"]`, `["SCAN","0","MATCH"]`, `["SCAN","0","COUNT","0"]`, `["SCAN","0","NOPE","1"]`} {
		if r := call(t, h, "POST", "/", tok, bad, false); r.code != 400 {
			t.Fatalf("%s: %d %v", bad, r.code, r.body)
		}
	}
}

// TestRESTScripts follows @upstash/ratelimit: EVALSHA with the SHA of its own
// text, NOSCRIPT, then EVAL; its "allow-key-locking" shebang flag is dropped
// and later EVALSHA calls with the original SHA find the cleaned script.
func TestRESTScripts(t *testing.T) {
	h, f := newTestREST(t)
	tok, _ := restTokens("app", "pw")
	script := "#!lua flags=allow-key-locking\nreturn {KEYS[1], ARGV[1]}"
	sum := sha1.Sum([]byte(script))
	sha := hex.EncodeToString(sum[:])
	body := func(cmd, s string) string {
		raw, _ := json.Marshal([]any{cmd, s, 1, "rl:1", 10})
		return string(raw)
	}
	scripts.Lock()
	delete(scripts.m, sha)
	scripts.Unlock()
	if r := call(t, h, "POST", "/", tok, body("EVALSHA", sha), false); r.code != 400 || !strings.Contains(r.body["error"].(string), "NOSCRIPT") {
		t.Fatalf("first EVALSHA: %d %v", r.code, r.body)
	}
	if r := call(t, h, "POST", "/", tok, body("EVAL", script), false); r.code != 200 || !reflect.DeepEqual(r.body["result"], []any{"p_app:rl:1", "10"}) {
		t.Fatalf("EVAL: %d %v", r.code, r.body)
	}
	if r := call(t, h, "POST", "/", tok, body("EVALSHA", sha), true); r.code != 200 || len(r.body["result"].([]any)) != 2 {
		t.Fatalf("EVALSHA after EVAL: %d %v", r.code, r.body)
	}
	if got := f.got[len(f.got)-1]; got[1] == sha || got[3] != "p_app:rl:1" {
		t.Fatalf("EVALSHA args: %v", got)
	}
	// SCRIPT LOAD answers the client's own SHA.
	raw, _ := json.Marshal([]string{"SCRIPT", "LOAD", script + " "})
	sum = sha1.Sum([]byte(script + " "))
	if r := call(t, h, "POST", "/", tok, string(raw), false); r.body["result"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("SCRIPT LOAD: %v", r.body)
	}
}
