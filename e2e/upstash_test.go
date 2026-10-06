//go:build e2e

package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestUpstash is the Upstash-compatibility acceptance test on a fresh box:
// an app written for @upstash/redis and @upstash/ratelimit (sliding window)
// runs unchanged with the env the box gives it → set/get, pipeline, multi
// and hashes work, and its keys sit under the project's prefix → the rate
// limit blocks after its limit → the read-only token reads but does not
// write → another project's token reaches none of its keys, not even by
// naming them in full or from a script.
func TestUpstash(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "upstash", "upstash")
	phase("up", start)

	p := time.Now()
	app := filepath.Join(b.dir, "upstash")
	if out, err := exec.Command("cp", "-R", filepath.Join(RepoRoot(), "e2e", "upstashapp"), app).CombinedOutput(); err != nil {
		t.Fatalf("copy app: %v\n%s", err, out)
	}
	_ = os.RemoveAll(filepath.Join(app, "node_modules"))
	cfg := "export default {\n  project: \"upstash\",\n  services: { valkey: {} },\n  apps: { api: { routes: [\"upstash\"] } },\n};\n"
	if err := os.WriteFile(filepath.Join(app, "tiffin.config.ts"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := b.ok("plan", app)
	b.ok("apply", app, "--confirm", plan["hash"].(string), "-m", "e2e upstash")
	b.waitReady("app/api", "service/valkey")
	deploy(t, b, app)
	phase("deploy", p)

	c := b.https()
	get := func(path string) map[string]any {
		t.Helper()
		code, _, body := b.get(c, "GET", b.url("upstash")+path, nil)
		var m map[string]any
		if code != 200 || json.Unmarshal([]byte(body), &m) != nil {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
		return m
	}

	// ---- the env, and the app's own calls ----
	p = time.Now()
	if env := get("/env"); env["url"] != "http://127.0.0.1:7076" || env["kv"] != true {
		t.Fatalf("env: %v", env)
	}
	kv := get("/kv")
	want := map[string]any{
		"got":   map[string]any{"hello": "world"},
		"piped": []any{"OK", float64(1), float64(6), float64(6)},
		"multi": []any{"OK", float64(4), "a b!"},
		"hash":  map[string]any{"a": float64(1), "b": "two"},
	}
	if !reflect.DeepEqual(kv, want) {
		t.Fatalf("kv: %v\nwant %v", kv, want)
	}
	redisURL := b.ok("kv", "connection", "upstash")["redisUrl"].(string)
	cli := func(u string, args ...string) string {
		return b.inBox("valkey-cli -u " + shq(u) + " --no-auth-warning " + strings.Join(args, " "))
	}
	if got := cli(redisURL, "get", "p_upstash:greeting"); got != `{"hello":"world"}` {
		t.Fatalf("p_upstash:greeting through REDIS_URL: %q", got)
	}

	// ---- sliding-window rate limit: 3 a minute ----
	for i := 1; i <= 4; i++ {
		r := get("/limit?id=e2e")
		if r["success"] != (i <= 3) {
			t.Fatalf("limit call %d: %v", i, r)
		}
	}
	if get("/limit?id=someone-else")["success"] != true {
		t.Fatal("another identifier was limited")
	}
	phase("app", p)

	// ---- tokens, straight to the endpoint inside the box ----
	token := func(redisURL, project, purpose, prefix string) string {
		u, err := url.Parse(redisURL)
		if err != nil {
			t.Fatal(err)
		}
		pw, _ := u.User.Password()
		h := hmac.New(sha256.New, []byte(pw))
		h.Write([]byte(purpose))
		return prefix + project + "_" + hex.EncodeToString(h.Sum(nil))[:40]
	}
	rest := func(tok, body string) string {
		return b.inBox("curl -s -w ' %{http_code}' -H " + shq("Authorization: Bearer "+tok) + " -d " + shq(body) + " http://127.0.0.1:7076/")
	}
	ro := token(redisURL, "upstash", "upstash-rest-ro", "tvkro_")
	if got := rest(ro, `["GET","greeting"]`); got != `{"result":"{\"hello\":\"world\"}"} 200` {
		t.Fatalf("read-only GET: %s", got)
	}
	if got := rest(ro, `["SET","greeting","x"]`); !strings.Contains(got, "read-only") || !strings.HasSuffix(got, " 400") {
		t.Fatalf("read-only SET: %s", got)
	}
	if got := rest("tvk_upstash_"+strings.Repeat("0", 40), `["GET","greeting"]`); got != `{"error":"Unauthorized"} 401` {
		t.Fatalf("forged token: %s", got)
	}

	b.apply("other", `{"project":"other","services":{"valkey":{}}}`)
	for i := 0; ; i++ {
		_, out := b.run("projects", "get", "other")
		var st struct {
			Status map[string]struct{ State string } `json:"status"`
		}
		_ = json.Unmarshal([]byte(out), &st)
		if st.Status["service/valkey"].State == "ready" {
			break
		}
		if i == 60 {
			t.Fatalf("other's valkey not ready: %s", out)
		}
		time.Sleep(time.Second)
	}
	other := token(b.ok("kv", "connection", "other")["redisUrl"].(string), "other", "upstash-rest", "tvk_")
	for body, wantOut := range map[string]string{
		`["GET","greeting"]`:                                           `{"result":null} 200`,
		`["GET","p_upstash:greeting"]`:                                 `{"result":null} 200`,
		`["EVAL","return redis.call('GET','p_upstash:greeting')","0"]`: "no permissions",
		`["KEYS","*"]`:                                                 "no permissions",
	} {
		if got := rest(other, body); !strings.Contains(strings.ToLower(got), wantOut) {
			t.Fatalf("other project %s: %s", body, got)
		}
	}
	phase("isolation", p)
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}

// shq quotes s for a POSIX shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
