package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// applyManifest records a project's resources the way `tiffin apply` does.
func applyManifest(t *testing.T, db *state.DB, raw string) {
	t.Helper()
	ctx := context.Background()
	m, err := manifest.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	res, err := change.Resources(m)
	if err != nil {
		t.Fatal(err)
	}
	eng := change.NewEngine(db)
	plan, err := eng.Plan(ctx, m.Project, res)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "system", ID: "test"},
		Authorize: func(*change.Plan) error { return nil }}); err != nil {
		t.Fatal(err)
	}
}

// victoria starts the real VictoriaMetrics and VictoriaLogs binaries from
// $TIFFIN_TEST_VICTORIA (a directory with victoria-metrics-prod and
// victoria-logs-prod for this OS), or skips.
func victoria(t *testing.T) *Victoria {
	t.Helper()
	dir := os.Getenv("TIFFIN_TEST_VICTORIA")
	if dir == "" {
		t.Skip("set TIFFIN_TEST_VICTORIA to a directory with victoria-metrics-prod and victoria-logs-prod")
	}
	data := t.TempDir()
	start := func(bin string, args ...string) string {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		cmd := exec.Command(filepath.Join(dir, bin), append(args, "-httpListenAddr="+addr, "-loggerLevel=ERROR")...)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		for i := 0; i < 100; i++ {
			if res, err := http.Get("http://" + addr + "/health"); err == nil {
				res.Body.Close()
				return "http://" + addr
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s did not start", bin)
		return ""
	}
	vm := start("victoria-metrics-prod", "-storageDataPath="+filepath.Join(data, "vm"), "-search.latencyOffset=0s", "-search.disableCache")
	vl := start("victoria-logs-prod", "-storageDataPath="+filepath.Join(data, "vl"))
	return &Victoria{VM: vm, VL: vl}
}

type harness struct {
	t      *testing.T
	m      *Module
	db     *state.DB
	api    *httptest.Server
	ingest *httptest.Server
	owner  string
	tm     *tokens.Manager
}

func newHarness(t *testing.T, vic *Victoria) *harness {
	t.Helper()
	root := t.TempDir()
	db, err := state.Open(filepath.Join(root, "platform", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sec, err := platform.OpenSecrets(db, filepath.Join(root, "platform"))
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Tokens: tm, Engine: change.NewEngine(db), Secrets: sec, Home: filepath.Join(root, "platform"), DataRoot: root,
		Domain: "box.test", PublicURL: "https://dashboard.box.test:8443", Log: slog.Default()}
	var m *Module // the registered instance: the API's handlers belong to it
	for _, x := range platform.Modules() {
		if om, ok := x.(*Module); ok {
			m = om
		}
	}
	if err := m.setup(context.Background(), p, root, vic); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.store.Close(); m.traces.Close() })
	a := api.New(api.Deps{DB: db, Engine: p.Engine, Tokens: tm, Version: "test", Platform: p})
	h := &harness{t: t, m: m, db: db, tm: tm, owner: owner,
		api: httptest.NewServer(a.Handler()), ingest: httptest.NewServer(m.ingestHandler())}
	t.Cleanup(h.api.Close)
	t.Cleanup(h.ingest.Close)
	applyManifest(t, db, `{"project":"shop","apps":{"web":{"routes":["shop","example.com/api"]}}}`)
	applyManifest(t, db, `{"project":"other","apps":{"site":{}}}`)
	return h
}

func (h *harness) call(token, method, path string, body any) (int, map[string]any, []any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.api.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var obj map[string]any
	var arr []any
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &arr)
	} else {
		_ = json.Unmarshal(raw, &obj)
	}
	return res.StatusCode, obj, arr
}

func (h *harness) agent(projects ...string) string {
	h.t.Helper()
	code, out, _ := h.call(h.owner, "POST", "/v1/tokens", map[string]any{"name": "agent", "projects": projects, "access": "full"})
	if code != 200 {
		h.t.Fatalf("token: %d %v", code, out)
	}
	return out["secret"].(string)
}

func envelope(dsn, value string, line int) string {
	ev := fmt.Sprintf(`{"event_id":"%032x","platform":"javascript","exception":{"values":[{"type":"TypeError","value":%q,
"stacktrace":{"frames":[{"filename":"/app/src/cart.ts","function":"addItem","lineno":%d,"in_app":true}]}}]}}`, time.Now().UnixNano(), value, line)
	return fmt.Sprintf(`{"dsn":%q}`+"\n"+`{"type":"event","length":%d}`+"\n%s\n", dsn, len(ev), ev)
}

func TestSentryIngestAndIssuesAPI(t *testing.T) {
	h := newHarness(t, &Victoria{VM: "http://127.0.0.1:1", VL: "http://127.0.0.1:1"})
	ctx := context.Background()
	env, err := h.m.Env(ctx, h.m.p, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	dsn := env["SENTRY_DSN"]
	if !strings.HasPrefix(dsn, "http://") || !strings.HasPrefix(env["TIFFIN_PUBLIC_SENTRY_DSN"], "https://") || !strings.Contains(env["TIFFIN_PUBLIC_SENTRY_DSN"], "@errors.box.test:8443/") {
		t.Fatalf("env %v", env)
	}
	key := strings.TrimPrefix(strings.Split(dsn, "@")[0], "http://")
	id := dsn[strings.LastIndex(dsn, "/")+1:]
	post := func(path, body, auth string) int {
		req, _ := http.NewRequest("POST", h.ingest.URL+path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("X-Sentry-Auth", "Sentry sentry_version=7, sentry_key="+auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := post("/api/"+id+"/envelope/", envelope(dsn, "x is undefined", 10), ""); c != 200 {
		t.Fatalf("envelope: %d", c)
	}
	if c := post("/api/"+id+"/envelope/", envelope("", "x is undefined", 12), key); c != 200 {
		t.Fatalf("envelope with header key: %d", c)
	}
	if c := post("/api/"+id+"/store/?sentry_key="+key, `{"message":"payment 991 failed"}`, ""); c != 200 {
		t.Fatalf("store: %d", c)
	}
	if c := post("/api/"+id+"/envelope/", envelope("", "x", 1), "wrongkey"); c != 401 {
		t.Fatalf("bad key: %d", c)
	}
	if c := post("/api/999/envelope/", envelope("", "x", 1), key); c != 401 {
		t.Fatalf("key for another id: %d", c)
	}

	code, _, issues := h.call(h.owner, "GET", "/v1/observe/issues?project=shop", nil)
	if code != 200 || len(issues) != 2 {
		t.Fatalf("issues: %d %v", code, issues)
	}
	var te map[string]any
	for _, x := range issues {
		if x.(map[string]any)["title"] == "TypeError: x is undefined" {
			te = x.(map[string]any)
		}
	}
	if te == nil || te["count"].(float64) != 2 || te["culprit"] != "addItem (/app/src/cart.ts:12)" {
		t.Fatalf("grouped issue: %v", issues)
	}
	iid := te["id"].(string)
	// Another project's agent cannot see it.
	other := h.agent("other")
	if code, _, _ := h.call(other, "GET", "/v1/observe/issues/"+iid, nil); code != 404 {
		t.Fatalf("other project's agent got %d", code)
	}
	if _, _, l := h.call(other, "GET", "/v1/observe/issues", nil); len(l) != 0 {
		t.Fatalf("other agent lists %v", l)
	}
	shopAgent := h.agent("shop")
	if code, out, _ := h.call(shopAgent, "POST", "/v1/observe/issues/"+iid+"/resolve", map[string]any{}); code != 200 || out["status"] != "resolved" {
		t.Fatalf("resolve: %d %v", code, out)
	}
	// A new event reopens it.
	post("/api/"+id+"/envelope/", envelope(dsn, "x is undefined", 15), "")
	if _, out, _ := h.call(shopAgent, "GET", "/v1/observe/issues/"+iid, nil); out["status"] != "unresolved" || out["count"].(float64) != 3 || len(out["events"].([]any)) != 3 {
		t.Fatalf("regression: %v", out)
	}
	if n, _ := h.m.store.ErrorCount(ctx, 5*time.Minute); n["shop"] != 4 {
		t.Fatalf("error count %v", n)
	}
}

func TestAlertsFireAndDeliver(t *testing.T) {
	h := newHarness(t, &Victoria{VM: "http://127.0.0.1:1", VL: "http://127.0.0.1:1"})
	ctx := context.Background()
	var mu sync.Mutex
	var got []Notification
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n Notification
		_ = json.NewDecoder(r.Body).Decode(&n)
		mu.Lock()
		got = append(got, n)
		mu.Unlock()
	}))
	defer hook.Close()
	if code, _, _ := h.call(h.owner, "PUT", "/v1/observe/settings", map[string]any{"emailProject": "ops"}); code != 422 {
		t.Fatalf("email project without email service: %d", code)
	}
	applyManifest(t, h.db, `{"project":"ops","services":{"email":{}}}`)
	for _, x := range platform.Modules() { // converge the email service as the box would after apply
		if rc, ok := x.(platform.Reconciler); ok && x.Name() == "email" {
			if err := rc.Reconcile(ctx, h.m.p, "ops", "service/email", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if code, out, _ := h.call(h.owner, "PUT", "/v1/observe/settings", map[string]any{"webhook": hook.URL, "emailProject": "ops"}); code != 200 || out["webhook"] != hook.URL || out["emailProject"] != "ops" {
		t.Fatalf("settings: %d %v", code, out)
	}
	agent := h.agent("shop")
	if code, _, _ := h.call(agent, "PUT", "/v1/observe/alert-rules/disk-full", map[string]any{"kind": "disk", "threshold": 0}); code != 403 {
		t.Fatalf("agent must not change box rules: %d", code)
	}
	if code, out, _ := h.call(h.owner, "PUT", "/v1/observe/alert-rules/disk-full", map[string]any{"kind": "disk", "threshold": 0.01}); code != 200 || out["enabled"] != true {
		t.Fatalf("rule put: %d %v", code, out)
	}
	if code, _, _ := h.call(h.owner, "PUT", "/v1/observe/alert-rules/bad", map[string]any{"kind": "promql", "threshold": 1}); code != 422 {
		t.Fatalf("promql without expr: %d", code)
	}
	h.m.collector.Sample(ctx)
	if err := h.m.alerter.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	_, view, _ := h.call(h.owner, "GET", "/v1/observe/alerts", nil)
	firing := view["firing"].([]any)
	if len(firing) == 0 || firing[0].(map[string]any)["rule"] != "disk-full" {
		t.Fatalf("firing: %v", view)
	}
	hist := view["history"].([]any)
	if len(hist) == 0 || !strings.HasPrefix(hist[0].(map[string]any)["delivery"].(string), "webhook ok; email to alerts@box.test captured in project ops's dev inbox") {
		t.Fatalf("history: %v", hist)
	}
	if code, out, arr := h.call(h.owner, "GET", "/v1/projects/ops/email/messages", nil); code != 200 || !strings.Contains(fmt.Sprint(out, arr), "subject:Firing: disk-full on box.test") {
		t.Fatalf("dev inbox: %d %v %v", code, out, arr)
	}
	mu.Lock()
	if len(got) == 0 || got[0].Rule != "disk-full" || got[0].State != "firing" || !strings.Contains(got[0].Text, "[FIRING] disk-full") {
		t.Fatalf("webhook got %+v", got)
	}
	n := len(got)
	mu.Unlock()
	// Evaluating again does not notify again; raising the threshold resolves.
	_ = h.m.alerter.Evaluate(ctx)
	h.call(h.owner, "PUT", "/v1/observe/alert-rules/disk-full", map[string]any{"kind": "disk", "threshold": 100})
	_ = h.m.alerter.Evaluate(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2*n || got[len(got)-1].State != "resolved" {
		t.Fatalf("want one resolve per subject after %d firings, got %+v", n, got)
	}
}

func accessLine(host, uri, ua string, status int, dur float64) string {
	b, _ := json.Marshal(map[string]any{"level": "info", "ts": float64(time.Now().UnixNano()) / 1e9, "logger": "http.log.access.access", "msg": "handled request",
		"request": map[string]any{"remote_ip": "203.0.113.9", "client_ip": "203.0.113.9", "proto": "HTTP/2.0", "method": "GET", "host": host + ":8443", "uri": uri,
			"headers": map[string][]string{"User-Agent": {ua}, "Sec-Fetch-Dest": {"document"}}},
		"duration": dur, "size": 512, "status": status})
	return string(b)
}

func TestVictoriaRoundTrip(t *testing.T) {
	vic := victoria(t)
	h := newHarness(t, vic)
	ctx := context.Background()
	// Edge traffic for shop and other; one platform request.
	for i := 0; i < 8; i++ {
		h.m.handleAccess(ctx, []byte(accessLine("shop.box.test", fmt.Sprintf("/p/%d?token=x", i), "Mozilla/5.0", 200, 0.02)))
	}
	h.m.handleAccess(ctx, []byte(accessLine("example.com", "/api/orders", "Mozilla/5.0", 502, 1.5)))
	h.m.handleAccess(ctx, []byte(accessLine("site.box.test", "/", "Mozilla/5.0", 200, 0.01)))
	h.m.handleAccess(ctx, []byte(accessLine("dashboard.box.test", "/v1/status", "tiffin", 200, 0.003)))
	h.m.batch.Flush(ctx)
	if err := h.m.pushMetrics(ctx); err != nil {
		t.Fatal(err)
	}

	// OTLP metrics from shop/web are stamped with project and app, whatever the app claims.
	env, _ := h.m.Env(ctx, h.m.p, "shop", "web")
	key := strings.TrimPrefix(env["OTEL_EXPORTER_OTLP_HEADERS"], "x-tiffin-key=")
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{Name: "orders_placed", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
			DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: uint64(time.Now().UnixNano()), Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 7},
				Attributes: []*commonpb.KeyValue{{Key: "project", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "other"}}}}}}}}}}}}}}}
	pb, _ := proto.Marshal(req)
	r, _ := http.NewRequest("POST", h.ingest.URL+"/v1/metrics", bytes.NewReader(pb))
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Header.Set("X-Tiffin-Key", key)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("otlp metrics: %d", res.StatusCode)
	}
	r, _ = http.NewRequest("POST", h.ingest.URL+"/v1/metrics", bytes.NewReader(pb))
	if res, _ := http.DefaultClient.Do(r); res.StatusCode != 401 {
		t.Fatalf("otlp without key: %d", res.StatusCode)
	}

	shop := h.agent("shop")
	other := h.agent("other")
	eventually := func(what string, f func() bool) {
		t.Helper()
		for i := 0; i < 60; i++ {
			if f() {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	// Logs: scoped by tenant. shop sees its 9 edge lines, other sees 1, box logs are admin-only.
	eventually("shop logs", func() bool {
		code, out, _ := h.call(shop, "POST", "/v1/observe/logs/query", map[string]any{"project": "shop", "query": "source:edge"})
		return code == 200 && out["count"].(float64) == 9
	})
	_, out, _ := h.call(shop, "POST", "/v1/observe/logs/query", map[string]any{"project": "shop", "query": "status:502"})
	if out["count"].(float64) != 1 || out["rows"].([]any)[0].(map[string]any)["app"] != "web" {
		t.Fatalf("502 row: %v", out)
	}
	if _, out, _ := h.call(shop, "POST", "/v1/observe/logs/query", map[string]any{"project": "shop", "query": "* | stats count() n"}); out["rows"].([]any)[0].(map[string]any)["n"] != "9" {
		t.Fatalf("stats: %v", out)
	}
	// A union pipe cannot reach another tenant.
	eventually("other's logs (twice through union, never shop's)", func() bool {
		_, out, _ := h.call(other, "POST", "/v1/observe/logs/query", map[string]any{"project": "other", "query": "* | union (*)", "limit": 50})
		return out["count"].(float64) == 2
	})
	if code, _, _ := h.call(other, "POST", "/v1/observe/logs/query", map[string]any{"project": "shop", "query": "*"}); code != 403 {
		t.Fatalf("cross-project logs: %d", code)
	}
	if code, _, _ := h.call(shop, "POST", "/v1/observe/logs/query", map[string]any{"query": "*"}); code != 403 {
		t.Fatalf("box logs as agent: %d", code)
	}
	if code, out, _ := h.call(h.owner, "POST", "/v1/observe/logs/query", map[string]any{"query": "host:dashboard.box.test"}); code != 200 || out["count"].(float64) != 1 {
		t.Fatalf("box logs as owner: %d %v", code, out)
	}
	if code, _, _ := h.call(h.owner, "POST", "/v1/observe/logs/query", map[string]any{"query": "| bogus("}); code != 422 {
		t.Fatalf("bad LogsQL: %d", code)
	}

	// Metrics: an agent's PromQL only sees its project, even when it asks for another.
	eventually("shop metrics", func() bool {
		code, out, _ := h.call(shop, "POST", "/v1/observe/metrics/query", map[string]any{"project": "shop", "query": `sum(tiffin_http_requests_total)`})
		if code != 200 {
			return false
		}
		r := out["result"].([]any)
		return len(r) == 1 && r[0].(map[string]any)["value"].([]any)[1] == "9"
	})
	_, out, _ = h.call(shop, "POST", "/v1/observe/metrics/query", map[string]any{"project": "shop", "query": `tiffin_http_requests_total{project="other"}`})
	if len(out["result"].([]any)) != 0 {
		t.Fatalf("label override leaked: %v", out)
	}
	if code, _, _ := h.call(shop, "POST", "/v1/observe/metrics/query", map[string]any{"query": `up`}); code != 403 {
		t.Fatalf("agent without project: %d", code)
	}
	eventually("otlp metric", func() bool {
		_, out, _ := h.call(shop, "POST", "/v1/observe/metrics/query", map[string]any{"project": "shop", "query": `orders_placed`})
		r, _ := out["result"].([]any)
		return len(r) == 1 && r[0].(map[string]any)["metric"].(map[string]any)["app"] == "web"
	})
	// Curated per-app view.
	eventually("app metrics", func() bool {
		code, _, apps := h.call(shop, "GET", "/v1/observe/apps?project=shop&since=15m", nil)
		if code != 200 || len(apps) != 1 {
			return false
		}
		a := apps[0].(map[string]any)
		return a["requests"].(float64) == 9 && a["errors"].(float64) == 1 && a["p95ms"].(float64) > 1000
	})
	if code, out, _ := h.call(h.owner, "GET", "/v1/observe/overview", nil); code != 200 || out["stores"].(map[string]any)["metrics"] != "ok" {
		t.Fatalf("overview: %d %v", code, out)
	}
}

// Rules added after a box was seeded are installed once there too, and
// stay deleted when the owner deletes them.
func TestLaterDefaultRules(t *testing.T) {
	ctx := context.Background()
	st, err := OpenStore(filepath.Join(t.TempDir(), "observe.db"))
	if err != nil {
		t.Fatal(err)
	}
	// A box seeded before the off-box rules existed, whose owner deleted disk-full.
	for _, r := range DefaultRules[:4] {
		if err := st.PutRule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetSetting(ctx, "rules.seeded", "2026-09-01T00:00:00Z")
	_, _ = st.DeleteRule(ctx, "disk-full")
	if err := st.EnsureDefaultRules(ctx); err != nil {
		t.Fatal(err)
	}
	names := func() string {
		rs, _ := st.Rules(ctx)
		var n []string
		for _, r := range rs {
			n = append(n, r.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(); got != "backup-stale,cert-expiring,memory-high,offsite-stale,restore-drill-failed" {
		t.Fatalf("rules: %s", got)
	}
	_, _ = st.DeleteRule(ctx, "offsite-stale")
	_ = st.EnsureDefaultRules(ctx)
	if got := names(); strings.Contains(got, "offsite-stale") || strings.Contains(got, "disk-full") {
		t.Fatalf("deleted rules came back: %s", got)
	}
}
