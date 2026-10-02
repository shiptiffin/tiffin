package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// The M0 acceptance path: config → JSON → plan → apply → state, all golden.
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

func TestScopedAgentTokenViaCLI(t *testing.T) {
	env := newEnv(t)
	_, out, _ := run(t, env, "tokens", "create", "--name", "claude", "--scopes", "read,plan", "--projects", "hello")
	secret, _ := decode(t, out)["secret"].(string)
	if !strings.HasPrefix(secret, "tfn_") {
		t.Fatalf("token create: %s", out)
	}
	agent := map[string]string{"TIFFIN_HOME": env["TIFFIN_HOME"], "TIFFIN_TOKEN": secret, "TIFFIN_SESSION": "s1"}
	code, out, _ := run(t, agent, "plan", "testdata")
	if code != ExitOK {
		t.Fatalf("agent plan: %d %s", code, out)
	}
	hash := decode(t, out)["hash"].(string)
	code, out, _ = run(t, agent, "apply", "testdata", "--confirm", hash)
	if code != ExitAuth || decode(t, out)["code"] != "denied" {
		t.Fatalf("plan-only token must not apply: %d %s", code, out)
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

func TestDoctor(t *testing.T) {
	env := newEnv(t)
	code, out, _ := run(t, env, "doctor")
	d := decode(t, out)
	if code != ExitOK || d["ok"] != true {
		t.Fatalf("doctor: %d %s", code, out)
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

	connect := func(token string) *sdk.ClientSession {
		hc := &http.Client{Transport: headerRT{token}}
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).
			Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: hc}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close() })
		return cs
	}
	cs := connect(owner)
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) < 12 {
		t.Fatalf("tools over HTTP: %v %d", err, len(tools.Tools))
	}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "project_get", Arguments: map[string]any{"project": "hello"}})
	if err != nil || res.IsError {
		t.Fatalf("project_get over HTTP: %v %+v", err, res)
	}
	anon := connect("")
	res, err = anon.CallTool(t.Context(), &sdk.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
	if err != nil || !res.IsError {
		t.Fatalf("MCP without a token must fail: %v %+v", err, res)
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
