package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/tokens"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// run executes the CLI like an agent would: no TTY, env from the map.
func run(t *testing.T, env map[string]string, args ...string) (int, []byte, string) {
	t.Helper()
	var out, errb bytes.Buffer
	no := false
	code := Execute(context.Background(), args, IO{Out: &out, Err: &errb, TTY: &no, In: strings.NewReader(""),
		Env: func(k string) string { return env[k] }})
	return code, out.Bytes(), errb.String()
}

func newEnv(t *testing.T) map[string]string {
	return map[string]string{"TIFFIN_HOME": filepath.Join(t.TempDir(), "box"), "HOME": t.TempDir()}
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, raw)
	}
	return m
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (UPDATE_GOLDEN=1 to create): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden %s differs:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// The core acceptance path: config → JSON → plan → apply → state, all golden.
func TestGoldenConfigToState(t *testing.T) {
	env := newEnv(t)
	code, out, _ := run(t, env, "plan", "testdata")
	if code != ExitOK {
		t.Fatalf("plan exit %d: %s", code, out)
	}
	golden(t, "plan.json", out)
	plan := decode(t, out)
	hash := plan["hash"].(string)

	code, out, _ = run(t, env, "apply", "testdata", "--confirm", hash[:12], "-m", "golden")
	if code != ExitOK || decode(t, out)["applied"] != true {
		t.Fatalf("apply exit %d: %s", code, out)
	}
	code, out, _ = run(t, env, "projects", "get", "hello")
	if code != ExitOK {
		t.Fatalf("projects get: %d %s", code, out)
	}
	golden(t, "state.json", out)

	// Re-planning the same config is a no-op.
	_, out, _ = run(t, env, "plan", "testdata")
	if p := decode(t, out); len(p["ops"].([]any)) != 0 || p["summary"] != "no changes" {
		t.Fatalf("replan: %s", out)
	}
}

func TestExitCodes(t *testing.T) {
	env := newEnv(t)
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "tiffin.config.ts"), []byte(`export default { project: "Bad Name!" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, _ := run(t, env, "plan", "testdata")
	hash := decode(t, out)["hash"].(string)

	cases := []struct {
		name string
		env  map[string]string
		args []string
		want int
	}{
		{"version", nil, []string{"version"}, ExitOK},
		{"apply needs confirm", nil, []string{"apply", "testdata"}, ExitConfirm},
		{"apply wrong hash", nil, []string{"apply", "testdata", "--confirm", "deadbeefdead"}, ExitConfirm},
		{"apply short hash", nil, []string{"apply", "testdata", "--confirm", hash[:6]}, ExitConfirm},
		{"invalid config", nil, []string{"plan", bad}, ExitInvalid},
		{"missing config", nil, []string{"plan", t.TempDir()}, ExitInvalid},
		{"unknown command", nil, []string{"frobnicate"}, ExitInvalid},
		{"bad flag", nil, []string{"plan", "--nope"}, ExitInvalid},
		{"bad token", map[string]string{"TIFFIN_TOKEN": "tfn_wrong"}, []string{"whoami"}, ExitAuth},
		{"bad id", nil, []string{"changes", "get", "nope"}, ExitInvalid},
		{"missing change", nil, []string{"changes", "get", "chg_01M3ZEFRQ7TQDNS6CY55958PN7"}, ExitError},
		{"remote without token", map[string]string{"TIFFIN_URL": "http://127.0.0.1:1", "TIFFIN_TOKEN": ""}, []string{"whoami"}, ExitAuth},
		{"remote unreachable", map[string]string{"TIFFIN_URL": "http://127.0.0.1:1", "TIFFIN_TOKEN": "tfn_x"}, []string{"whoami"}, ExitError},
		{"wrong arg count", nil, []string{"undo"}, ExitInvalid},
		{"boolean query flag without a value", nil, []string{"tokens", "list", "--revoked"}, ExitOK},
		{"unusable home is an error, not invalid input", map[string]string{"TIFFIN_HOME": filepath.Join(bad, "tiffin.config.ts", "box")}, []string{"whoami"}, ExitError},
		{"apply ok", nil, []string{"apply", "testdata", "--confirm", hash}, ExitOK},
		{"stale confirm after apply", nil, []string{"apply", "testdata", "--confirm", hash}, ExitOK}, // empty plan: nothing to confirm
	}
	for _, c := range cases {
		e := map[string]string{}
		for k, v := range env {
			e[k] = v
		}
		for k, v := range c.env {
			e[k] = v
		}
		code, out, errOut := run(t, e, c.args...)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d\nstdout: %s\nstderr: %s", c.name, code, c.want, out, errOut)
		}
		// Non-TTY output is always one JSON document on stdout.
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Errorf("%s: stdout is not JSON: %q", c.name, out)
		}
	}
}

func TestReadKeyViaCLI(t *testing.T) {
	env := newEnv(t)
	_, out, _ := run(t, env, "tokens", "create", "--name", "claude", "--projects", "hello", "--access", "read", "--expires-in-days", "30")
	created := decode(t, out)
	secret, _ := created["secret"].(string)
	key, _ := created["key"].(map[string]any)
	if !strings.HasPrefix(secret, "tfn_") || key["access"] != "read" || fmt.Sprint(key["projects"]) != "[hello]" || key["expiresAt"] == nil {
		t.Fatalf("key create: %s", out)
	}
	agent := map[string]string{"TIFFIN_HOME": env["TIFFIN_HOME"], "TIFFIN_TOKEN": secret, "TIFFIN_SESSION": "s1"}
	code, out, _ := run(t, agent, "plan", "testdata")
	if code != ExitOK {
		t.Fatalf("agent plan: %d %s", code, out)
	}
	hash := decode(t, out)["hash"].(string)
	code, out, _ = run(t, agent, "apply", "testdata", "--confirm", hash)
	if p := decode(t, out); code != ExitAuth || p["code"] != "forbidden" || !strings.Contains(p["detail"].(string), "read only") {
		t.Fatalf("read key must not apply: %d %s", code, out)
	}
	// --projects all sends "all".
	_, out, _ = run(t, env, "tokens", "create", "--name", "ci", "--projects", "all", "--access", "full")
	if k, _ := decode(t, out)["key"].(map[string]any); k["projects"] != "all" || k["admin"] != true || k["expiresAt"] == nil {
		t.Fatalf("all-projects key: %s", out)
	}
	// --expires-in-days 0: never.
	_, out, _ = run(t, env, "tokens", "create", "--name", "forever", "--projects", "all", "--access", "read", "--expires-in-days", "0")
	if k, _ := decode(t, out)["key"].(map[string]any); k["name"] != "forever" || k["expiresAt"] != nil {
		t.Fatalf("never-expiring key: %s", out)
	}
	if code, _, _ := run(t, agent, "tokens", "list"); code != ExitAuth {
		t.Fatalf("agent token list: %d", code)
	}
}

func TestInitWritesAWorkingConfig(t *testing.T) {
	env := newEnv(t)
	dir := filepath.Join(t.TempDir(), "My Cool App")
	code, out, _ := run(t, env, "init", dir)
	if code != ExitOK || decode(t, out)["project"] != "my-cool-app" {
		t.Fatalf("init: %d %s", code, out)
	}
	if code, out, _ := run(t, env, "init", dir); code != ExitInvalid {
		t.Fatalf("second init must refuse: %d %s", code, out)
	}
	if code, out, _ := run(t, env, "plan", dir); code != ExitOK {
		t.Fatalf("plan of init config: %d %s", code, out)
	}
}

// The edge check names the CA the certificates really come from.
func TestCAName(t *testing.T) {
	for ca, want := range map[string]string{"": "Let's Encrypt", edge.LetsEncryptStaging: "Let's Encrypt staging",
		edge.ZeroSSL: "ZeroSSL", "https://pebble:14000/dir": "pebble:14000"} {
		if got := caName(&edge.ACME{CA: ca}); got != want {
			t.Errorf("%q: %q, want %q", ca, got, want)
		}
	}
}

func TestDoctor(t *testing.T) {
	env := newEnv(t)
	code, out, _ := run(t, env, "doctor")
	d := decode(t, out)
	if code != ExitOK || d["ok"] != true {
		t.Fatalf("doctor: %d %s", code, out)
	}
	// A source build is named by its build, not as "tiffin dev"; the box's
	// own checks are included.
	if s := string(out); !strings.Contains(s, "tiffin answering (") || strings.Contains(s, "tiffin dev") || !strings.Contains(s, `"box checks"`) {
		t.Fatalf("doctor: %s", out)
	}
	// A world-readable home is flagged.
	if err := os.Chmod(env["TIFFIN_HOME"], 0o755); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(t, env, "doctor"); code != ExitError || decode(t, out)["ok"] != false {
		t.Fatalf("doctor should flag mode 755: %d %s", code, out)
	}
}

// The served box: CLI in remote mode and MCP over streamable HTTP.
func TestServeRemoteCLIAndMCPOverHTTP(t *testing.T) {
	home := filepath.Join(t.TempDir(), "box")
	b, owner, err := openBox(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	srv := httptest.NewServer(serveMux(b))
	defer srv.Close()

	remote := map[string]string{"TIFFIN_URL": srv.URL, "TIFFIN_TOKEN": owner, "TIFFIN_HOME": filepath.Join(t.TempDir(), "unused")}
	code, out, _ := run(t, remote, "plan", "testdata")
	if code != ExitOK {
		t.Fatalf("remote plan: %d %s", code, out)
	}
	hash := decode(t, out)["hash"].(string)
	if code, out, _ := run(t, remote, "apply", "testdata", "--confirm", hash); code != ExitOK {
		t.Fatalf("remote apply: %d %s", code, out)
	}
	if _, err := os.Stat(remote["TIFFIN_HOME"]); !os.IsNotExist(err) {
		t.Fatalf("remote mode must not create a local box")
	}

	connect := func(token string) (*sdk.ClientSession, error) {
		hc := &http.Client{Transport: headerRT{token}}
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).
			Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: hc}, nil)
		if err == nil {
			t.Cleanup(func() { cs.Close() })
		}
		return cs, err
	}
	cs, err := connect(owner)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) < 12 {
		t.Fatalf("tools over HTTP: %v %d", err, len(tools.Tools))
	}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "project_get", Arguments: map[string]any{"project": "hello"}})
	if err != nil || res.IsError {
		t.Fatalf("project_get over HTTP: %v %+v", err, res)
	}
	if _, err := connect(""); err == nil {
		t.Fatal("MCP without a token must not connect")
	}
}

// HTTP MCP checks the key before any tool runs: anonymous or bad-key calls
// to "run" get a 401 and the code is never evaluated.
func TestMCPOverHTTPNeedsAKey(t *testing.T) {
	b, owner, err := openBox(t.Context(), filepath.Join(t.TempDir(), "box"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	call := func(token string) *httptest.ResponseRecorder {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run","arguments":{"code":"return 'ran-' + (40 + 2);"}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		// As on a box: the service listens on loopback, the edge forwards the public name.
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7070}))
		req.Host = "dashboard.example.com"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		serveMux(b).ServeHTTP(rec, req)
		return rec
	}
	for _, token := range []string{"", "tfn_not-a-real-key"} {
		rec := call(token)
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "ran-42") {
			t.Fatalf("token %q: %d %s", token, rec.Code, rec.Body)
		}
		if rec.Header().Get("WWW-Authenticate") == "" || decode(t, rec.Body.Bytes())["code"] != "unauthenticated" {
			t.Fatalf("token %q: want a Bearer challenge and a problem body: %v %s", token, rec.Header(), rec.Body)
		}
	}
	if rec := call(owner); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ran-42") {
		t.Fatalf("with a key: %d %s", rec.Code, rec.Body)
	}
}

type headerRT struct{ token string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+h.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{"My Cool App": "my-cool-app", "123": "app-123", "--x--": "x", "Ünïcode Ok": "n-code-ok"} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// "tokens revoke" acts immediately; its help must not promise a dry run.
func TestOnlyConfirmableCommandsPromiseDryRun(t *testing.T) {
	env := newEnv(t)
	_, out, _ := run(t, env, "tokens", "revoke", "--help")
	if strings.Contains(string(out), "Without --confirm nothing changes") {
		t.Errorf("tokens revoke help claims a dry run:\n%s", out)
	}
	_, out, _ = run(t, env, "changes", "undo", "--help")
	if !strings.Contains(string(out), "Without --confirm nothing changes") {
		t.Errorf("changes undo help lost its dry-run note:\n%s", out)
	}
}

// Every op with an intent takes -m for it, as apply does, and destroy's help
// talks about deleting, not undoing.
func TestIntentShorthandEverywhere(t *testing.T) {
	env := newEnv(t)
	for _, args := range [][]string{{"apply"}, {"undo"}, {"changes", "undo"}, {"projects", "destroy"}} {
		_, out, _ := run(t, env, append(args, "--help")...)
		if !strings.Contains(string(out), "-m, --intent") {
			t.Errorf("%s help has no -m for --intent:\n%s", strings.Join(args, " "), out)
		}
	}
	_, out, _ := run(t, env, "projects", "destroy", "--help")
	if strings.Contains(string(out), "undo plan") || !strings.Contains(string(out), "deletes the project") {
		t.Errorf("projects destroy --confirm help is wrong:\n%s", out)
	}
}

// The local agent key has full access to all projects (Claude Code asks
// the person before destructive tools; Tiffin records and can undo), but
// it is not the owner token and it expires.
func TestLocalAgentKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), "box")
	b, _, err := openBox(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s1, err := b.agentToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.tokens.Authenticate(t.Context(), s1)
	if err != nil || p.Kind != "agent" || !p.BoxAdmin() || p.Access != "full" || p.ExpiresAt == nil {
		t.Fatalf("local agent key: %+v %v", p, err)
	}
	if s1 == readOwnerToken(home) {
		t.Fatal("agent token is the owner token")
	}
	if s2, _ := b.agentToken(t.Context()); s2 != s1 {
		t.Fatal("agent token should be reused while valid")
	}
	if fi, _ := os.Stat(filepath.Join(home, agentTokenFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent-token mode %o", fi.Mode().Perm())
	}
	// A narrower token left by an older version is replaced.
	owner, _ := b.tokens.Authenticate(t.Context(), readOwnerToken(home))
	old, _, err := b.tokens.Create(t.Context(), owner, tokens.CreateRequest{Name: "local-agent", Kind: tokens.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, agentTokenFile), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s3, _ := b.agentToken(t.Context()); s3 == old {
		t.Fatal("a narrower agent token was kept")
	}
}

func TestTiffinEnvNeverReachesConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := `export default { project: "leak", env: { T: process.env.TIFFIN_TOKEN ?? "none", H: process.env.HOME_MARKER ?? "none" } }`
	if err := os.WriteFile(filepath.Join(dir, "tiffin.config.ts"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIFFIN_TOKEN", "tfn_secret")
	t.Setenv("HOME_MARKER", "visible")
	a := &app{}
	raw, _, err := a.loadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tfn_secret") || !strings.Contains(string(raw), "visible") {
		t.Fatalf("env filtering wrong: %s", raw)
	}
}

// The edge routes by Host, so the MCP proxy must send the box's host, not
// whatever the in-process request carried.
func TestProxySendsTargetHost(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Host }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	req := httptest.NewRequest("GET", "http://example.com/v1/health", nil)
	proxyTo(u, nil).ServeHTTP(httptest.NewRecorder(), req)
	if got != u.Host {
		t.Fatalf("proxy sent Host %q, want %q", got, u.Host)
	}
}

// Inside an agent's shell the CLI acts as the box's agent key (as tiffin mcp
// does), so History shows the agent; an explicit TIFFIN_TOKEN still wins.
func TestAgentShellUsesAgentKey(t *testing.T) {
	cfg := t.TempDir()
	tokenFor := func(env map[string]string) string {
		env["TIFFIN_CONFIG_DIR"] = cfg
		a := &app{io: IO{Env: func(k string) string { return env[k] }}, token: env["TIFFIN_TOKEN"]}
		if err := a.saveBoxes(&boxesFile{Current: "local", Boxes: map[string]*boxConfig{"local": {Provider: "local",
			URL: "https://dashboard.tiffin.localhost:8443", Token: "tfn_owner", AgentToken: "tfn_agent"}}}); err != nil {
			t.Fatal(err)
		}
		c, err := a.client(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return c.token
	}
	if got := tokenFor(map[string]string{}); got != "tfn_owner" {
		t.Fatalf("a person's shell: %q", got)
	}
	if got := tokenFor(map[string]string{"CLAUDECODE": "1"}); got != "tfn_agent" {
		t.Fatalf("Claude Code: %q", got)
	}
	if got := tokenFor(map[string]string{"TIFFIN_AGENT": "1", "TIFFIN_TOKEN": "tfn_mine"}); got != "tfn_mine" {
		t.Fatalf("explicit token: %q", got)
	}
}

// A wrong call says how the command is used, and a group refuses a
// subcommand it does not have instead of printing its help with exit 0.
func TestUsageErrorsHint(t *testing.T) {
	env := newEnv(t)
	code, out, _ := run(t, env, "changes", "list", "extra")
	if p := decode(t, out); code != ExitInvalid || p["code"] != "cli" || p["hint"] != "usage: tiffin changes list [flags]; example: tiffin changes list" {
		t.Fatalf("extra argument: %d %s", code, out)
	}
	code, out, _ = run(t, env, "changes", "bogus")
	if p := decode(t, out); code != ExitInvalid || !strings.Contains(p["detail"].(string), `unknown command "bogus"`) || !strings.Contains(fmt.Sprint(p["hint"]), "one of: get, list, undo") {
		t.Fatalf("unknown subcommand: %d %s", code, out)
	}
	if code, out, _ = run(t, env, "changes"); code != ExitOK || !strings.Contains(string(out), "Available Commands") {
		t.Fatalf("a group alone prints its help: %d %s", code, out)
	}
}

// Deploying a preview says what it shares with production.
func TestPreviewNote(t *testing.T) {
	var out bytes.Buffer
	no := false
	a := &app{io: IO{Out: &out, TTY: &no}}
	a.printDeploys([]*rtDeploy{{App: "web", Preview: "fix", Status: "live"}}, nil)
	if p := decode(t, out.Bytes()); p["note"] != "Previews use this project's live data. Email goes to the dev inbox." {
		t.Fatalf("preview: %s", out.String())
	}
	out.Reset()
	a.printDeploys([]*rtDeploy{{App: "web", Status: "live"}}, nil)
	if p := decode(t, out.Bytes()); p["note"] != nil {
		t.Fatalf("production: %s", out.String())
	}
}
