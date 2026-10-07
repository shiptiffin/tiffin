//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFastAPI deploys the FastAPI starter on a fresh box, through the CLI:
//
//	templates list → the fastapi starter's fragment → plan/apply → deploy the
//	template (Railpack's Python provider, uv) → Alembic migrates in the
//	release, before the instances start → the API answers over HTTPS, with
//	https URLs and the visitor's address from the edge's proxy headers → an
//	unhandled error's lines are stored as errors → a request's trace reaches
//	the box → its memory → a redeploy and a rollback under load fail no
//	request.
func TestFastAPI(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "fastapi", "pynotes")
	phase("up", start)

	// ---- the starter's fragment → plan/apply ----
	p := time.Now()
	var tl struct {
		Templates []struct {
			ID, Framework, Kind, Preset, App string
			Fragment                         map[string]any
		}
	}
	_, out := b.run("templates", "list")
	if err := json.Unmarshal([]byte(out), &tl); err != nil {
		t.Fatalf("templates list: %v\n%s", err, out)
	}
	var frag map[string]any
	for _, tp := range tl.Templates {
		if tp.ID == "fastapi" {
			if tp.Framework != "fastapi" || tp.Kind != "api" || tp.Preset != "fastapi" || tp.App != "api" {
				t.Fatalf("the fastapi starter: %+v", tp)
			}
			frag = tp.Fragment
		}
	}
	if frag == nil {
		t.Fatalf("no fastapi starter in templates list:\n%s", out)
	}
	frag["project"] = "pynotes"
	raw, _ := json.Marshal(frag)
	b.apply("pynotes", string(raw))
	b.waitReady("service/postgres", "app/api")
	phase("plan+apply", p)

	// ---- deploy the template: build, release (Alembic), start ----
	p = time.Now()
	deployTemplate := func() map[string]any {
		t.Helper()
		d := b.ok("deploys", "template", "pynotes", "api", "--template", "fastapi")
		return waitDeploy(t, b, "pynotes", "api", d["id"].(string), 15*time.Minute)
	}
	d1 := deployTemplate()
	id1 := d1["id"].(string)
	phase("first deploy", p)
	t.Logf("first deploy: build %vs, total %vs (cold: Python, uv and wheels downloaded)", orZero(d1["buildSeconds"]), orZero(d1["durationSeconds"]))
	log := fmt.Sprint(b.ok("deploys", "build-log", "pynotes", "api", id1)["text"])
	t.Logf("build log (selected):\n%s", grepLines(log, "==> ", "python", "Python", "uv sync", "alembic", "Running upgrade"))
	for _, want := range []string{
		"==> FastAPI: app.main:app ([tool.fastapi] entrypoint in pyproject.toml), one Uvicorn process on $PORT",
		"uv sync --locked --no-dev",
		"Running upgrade  -> 0001, Create notes.",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("the build log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "warning: uvicorn") {
		t.Fatalf("the starter has uvicorn:\n%s", log)
	}
	if i, j := strings.Index(log, "Running upgrade"), strings.Index(log, "==> starting"); j >= 0 && i > j {
		t.Fatalf("the release must run before the instances start:\n%s", log)
	}
	ps := b.inBox(`ps -eo args | grep -v tini | grep -m1 '[u]vicorn app.main:app'`)
	if !strings.Contains(ps, "--timeout-keep-alive 75") || !strings.Contains(ps, "--forwarded-allow-ips 127.0.0.1") {
		t.Fatalf("the app's process: %s", ps)
	}
	t.Logf("process: %s", ps)
	// Dependencies are compiled to bytecode at build time (uv: UV_COMPILE_BYTECODE).
	t.Logf("bytecode in the image: %s", b.inBox(`c=$(sudo /usr/local/bin/nerdctl --namespace tiffin ps --format '{{.Names}}' | grep pynotes | head -1)
sudo /usr/local/bin/nerdctl --namespace tiffin exec $c sh -c 'echo "$(find /app/.venv -name "*.pyc" | wc -l) .pyc files for $(find /app/.venv -name "*.py" | wc -l) .py in .venv; python $(python -c "import sys; print(sys.version.split()[0])")"'`))

	// ---- the API over HTTPS ----
	c := b.https()
	site := b.url("pynotes")
	code, hdr, body := b.get(c, "GET", site+"/", nil)
	if code != 200 || !strings.Contains(body, `"name":"notes"`) || !strings.Contains(body, id1) ||
		!strings.Contains(body, `"docs":"https://pynotes.tiffin.localhost:`) {
		t.Fatalf("GET /: %d %s", code, body)
	}
	t.Logf("GET / → %s (server %q)", body, hdr.Get("Server"))
	send := func(method, u, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, u, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, u, err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	code, body = send("POST", site+"/notes", `{"text":"  from e2e  "}`)
	var note struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	if err := json.Unmarshal([]byte(body), &note); code != 201 || err != nil || note.Text != "from e2e" || note.ID == 0 {
		t.Fatalf("POST /notes: %d %s", code, body)
	}
	if code, body = send("PATCH", site+"/notes/"+strconv.Itoa(note.ID), `{"done":true}`); code != 200 || !strings.Contains(body, `"done":true`) {
		t.Fatalf("PATCH: %d %s", code, body)
	}
	if code, _, body = b.get(c, "GET", site+"/notes?done=true", nil); code != 200 || !strings.Contains(body, `"text":"from e2e"`) {
		t.Fatalf("GET /notes: %d %s", code, body)
	}
	if code, body = send("POST", site+"/notes", `{"text":""}`); code != 422 || !strings.Contains(body, "string_too_short") {
		t.Fatalf("validation: %d %s", code, body)
	}
	if code, _, body = b.get(c, "GET", site+"/docs", nil); code != 200 || !strings.Contains(body, "swagger-ui") {
		t.Fatalf("/docs: %d %.300s", code, body)
	}
	if code, _, body = b.get(c, "GET", site+"/openapi.json", nil); code != 200 || !strings.Contains(body, `"/notes/{id}"`) {
		t.Fatalf("/openapi.json: %d %.300s", code, body)
	}
	if code, _, body = b.get(c, "GET", site+"/me", nil); code != 404 || !strings.Contains(body, "services.auth") {
		t.Fatalf("/me without sign-in: %d %s", code, body)
	}
	got := b.ok("sql", "pynotes", "--body", `{"sql":"select version_num from alembic_version"}`)
	if !strings.Contains(fmt.Sprint(got["results"]), "0001") {
		t.Fatalf("alembic_version: %v", got)
	}

	// ---- an unhandled error: its lines are errors in the logs, its trace is kept ----
	b.ok("sql", "write", "pynotes", "--body", `{"sql":"alter table notes rename to notes_away"}`)
	code, hdr, body = b.get(c, "GET", site+"/notes", nil)
	b.ok("sql", "write", "pynotes", "--body", `{"sql":"alter table notes_away rename to notes"}`)
	reqID := hdr.Get("X-Request-Id") // "" when the edge does not echo it
	if code != 500 {
		t.Fatalf("GET /notes without its table: %d %s", code, body)
	}
	levels := map[string]string{}
	eventually := func(what string, d time.Duration, f func() bool) {
		t.Helper()
		for end := time.Now().Add(d); !f(); time.Sleep(2 * time.Second) {
			if time.Now().After(end) {
				t.Fatalf("%s: not in %s", what, d)
			}
		}
	}
	eventually("the error's log lines", 2*time.Minute, func() bool {
		res := b.ok("logs", "query", "--project", "pynotes", "--query", "source:app", "--since", "15m", "--limit", "500")
		rows, _ := res["rows"].([]any)
		for _, r := range rows {
			m, _ := r.(map[string]any)
			msg, lvl := fmt.Sprint(m["_msg"]), fmt.Sprint(m["level"])
			switch {
			case strings.HasPrefix(msg, "ERROR:") && strings.Contains(msg, "Exception in ASGI application"):
				levels["uvicorn error"] = lvl
			case strings.Contains(msg, "UndefinedTable") && !strings.HasPrefix(msg, " "):
				levels["exception line"] = lvl
			case strings.Contains(msg, `"POST /notes HTTP/1.1" 201`):
				levels["access"] = lvl
				levels["access line"] = msg
			case strings.Contains(msg, "notes API ready"):
				levels["startup"] = lvl
			}
		}
		return levels["uvicorn error"] != "" && levels["exception line"] != "" && levels["access"] != ""
	})
	t.Logf("levels: %v", levels)
	if levels["uvicorn error"] != "error" || levels["exception line"] != "error" || (levels["access"] != "<nil>" && levels["access"] != "info" && levels["access"] != "") {
		t.Fatalf("levels: %v", levels)
	}
	if strings.Contains(levels["access line"], "127.0.0.1:") {
		t.Errorf("the access log shows the edge, not the visitor: %s", levels["access line"])
	}
	// FastAPI exports its own spans (fastapi[opentelemetry]) to the box: the failed request is kept.
	var trace map[string]any
	eventually("the failed request's trace", 2*time.Minute, func() bool {
		for _, x := range b.list("traces", "list", "--project", "pynotes", "--since", "1h") {
			if strings.Contains(fmt.Sprint(x["name"]), "/notes") && (reqID == "" || x["traceId"] == strings.ReplaceAll(reqID, "-", "")) {
				trace = x
				return true
			}
		}
		return false
	})
	t.Logf("trace: %v", trace)
	spans, _ := b.ok("traces", "get", fmt.Sprint(trace["traceId"]), "--project", "pynotes")["spans"].([]any)
	var tree []string
	for _, sp := range spans {
		m := sp.(map[string]any)
		tree = append(tree, fmt.Sprintf("%s%s [%s %.1fms %v]", strings.Repeat("  ", int(m["depth"].(float64))), m["name"], m["kind"], m["durationMs"], m["status"]))
	}
	t.Logf("spans:\n%s", strings.Join(tree, "\n"))
	if len(spans) == 0 {
		t.Fatalf("the trace has no spans: %v", trace)
	}
	phase("api, logs, trace", p)

	// ---- memory: one Uvicorn process ----
	rss := func() string {
		return b.inBox(`for p in $(pgrep -f '[/]bin/uvicorn app.main:app'); do ps -o rss= -p $p; done | awk '{printf "%.1f MB ", $1/1024}'`)
	}
	idle := rss()

	// ---- redeploy and roll back under load: no failed request ----
	p = time.Now()
	b.ok("protect", "set", "--body", `{"limits":{"app":{"requests":0,"windowSeconds":10}}}`)
	b.inBox(`sudo apt-get install -y -qq hey >/dev/null 2>&1 || true; command -v hey >/dev/null`)
	b.inBox(`sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt
rm -f /tmp/hey.out /tmp/curl.out
nohup hey -z 150s -c 8 -host pynotes.tiffin.localhost "https://pynotes.tiffin.localhost:8443/notes?limit=20" > /tmp/hey.out 2>&1 &
nohup bash -c 'end=$((SECONDS+150)); while [ $SECONDS -lt $end ]; do curl -s -o /dev/null -w "%{http_code} %{errormsg}\n" --max-time 10 --cacert /tmp/ca.crt -X POST -H "content-type: application/json" -d "{\"text\":\"load\"}" https://pynotes.tiffin.localhost:8443/notes | sed "s/^/$(date +%T.%N) /"; done > /tmp/curl.out' >/dev/null 2>&1 &
echo started`)
	time.Sleep(10 * time.Second)
	loaded := rss()
	d2 := deployTemplate()
	id2 := d2["id"].(string)
	t.Logf("redeploy under load: build %vs, total %vs", orZero(d2["buildSeconds"]), orZero(d2["durationSeconds"]))
	if _, _, body := b.get(c, "GET", site+"/", nil); !strings.Contains(body, id2) {
		t.Fatalf("after the redeploy: %s", body)
	}
	time.Sleep(5 * time.Second)
	rolled := b.ok("rollback", "api", "--project", "pynotes")
	if rolled["id"] != id1 || rolled["status"] != "live" {
		t.Fatalf("rollback: %v", rolled)
	}
	if _, _, body := b.get(c, "GET", site+"/", nil); !strings.Contains(body, id1) {
		t.Fatalf("after the rollback the first deploy must serve: %s", body)
	}
	for i := 0; i < 200 && !strings.Contains(b.inBox("cat /tmp/hey.out"), "Status code distribution"); i++ {
		time.Sleep(time.Second)
	}
	time.Sleep(12 * time.Second) // the curl loop ends with hey
	hey := b.inBox("cat /tmp/hey.out")
	curls := b.inBox("cut -d' ' -f2- /tmp/curl.out | sort | uniq -c")
	t.Logf("hey (GET /notes, keep-alive):\n%s\ncurl loop (POST /notes, a new connection each):\n%s",
		section(hey, "Summary:", "Response time histogram:")+section(hey, "Status code distribution:", ""), curls)
	if strings.Contains(hey, "Error distribution") || !regexp.MustCompile(`\[200\]\s+\d+ responses`).MatchString(hey) ||
		regexp.MustCompile(`\[[13-9]\d\d\]`).MatchString(section(hey, "Status code distribution:", "")) {
		t.Fatalf("hey saw failures during the redeploy and rollback:\n%s", hey)
	}
	for _, line := range strings.Split(curls, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[1] != "201" {
			t.Fatalf("the POST loop saw failures:\n%s\n%s", curls, b.inBox(`grep -v " 201 $" /tmp/curl.out | head -20`))
		}
	}
	b.ok("protect", "set", "--body", `{"limits":{"app":{"requests":300,"windowSeconds":10}}}`)
	phase("redeploy+rollback under load", p)
	t.Logf("memory (RSS of the one Uvicorn process): %s idle after start and a few requests, %s under load (hey -c 8), %s after the rollback",
		idle, loaded, rss())
	t.Logf("shutdown of the replaced release:\n%s", b.inBox(`sudo grep -h -E "Shutting down|Waiting for|shutdown complete|Finished server" /var/lib/tiffin/logs/apps/pynotes/api/prod/`+id2+`.*.log | tail -6 || true`))
}
