package observe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (h *harness) reader(projects ...string) string {
	h.t.Helper()
	var ps any = projects
	if len(projects) == 1 && projects[0] == "all" {
		ps = "all"
	}
	code, out, _ := h.call(h.owner, "POST", "/v1/tokens", map[string]any{"name": "reader", "projects": ps, "access": "read"})
	if code != 200 {
		h.t.Fatalf("token: %d %v", code, out)
	}
	return out["secret"].(string)
}

// A project's developer may change error_spike rules of their project, but
// never by overwriting a box-wide rule or another project's rule.
func TestRulePutChecksTheRuleItReplaces(t *testing.T) {
	h := newHarness(t, &Victoria{VM: "http://127.0.0.1:1", VL: "http://127.0.0.1:1"})
	ctx := context.Background()
	shop := h.agent("shop")
	spike := map[string]any{"kind": "error_spike", "project": "shop", "threshold": 20, "enabled": false}
	if code, _, _ := h.call(shop, "PUT", "/v1/observe/alert-rules/disk-full", spike); code != 403 {
		t.Fatalf("overwriting a box rule: %d", code)
	}
	if code, _, _ := h.call(h.owner, "PUT", "/v1/observe/alert-rules/other-errors",
		map[string]any{"kind": "error_spike", "project": "other", "threshold": 5}); code != 200 {
		t.Fatalf("owner rule: %d", code)
	}
	if code, _, _ := h.call(shop, "PUT", "/v1/observe/alert-rules/other-errors", spike); code != 403 {
		t.Fatalf("overwriting another project's rule: %d", code)
	}
	if code, _, _ := h.call(shop, "PUT", "/v1/observe/alert-rules/shop-errors", spike); code != 200 {
		t.Fatalf("own rule: %d", code)
	}
	if code, _, _ := h.call(shop, "PUT", "/v1/observe/alert-rules/shop-errors", spike); code != 200 {
		t.Fatalf("replacing own rule: %d", code)
	}
	rules, _ := h.m.store.Rules(ctx)
	for _, r := range rules {
		if r.Name == "disk-full" && (r.Kind != KindDisk || !r.Enabled) {
			t.Fatalf("disk rule changed: %+v", r)
		}
		if r.Name == "other-errors" && r.Project != "other" {
			t.Fatalf("other's rule changed: %+v", r)
		}
	}
}

// A key for some projects sees its projects' containers, alerts and rules,
// not the rest of the box's.
func TestProjectReadersSeeOnlyTheirProjects(t *testing.T) {
	stores := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer stores.Close()
	h := newHarness(t, &Victoria{VM: stores.URL, VL: stores.URL})
	ctx := context.Background()
	h.m.collector.mu.Lock()
	h.m.collector.last = &Snapshot{Containers: []Container{{ID: "a", Project: "shop", App: "web"}, {ID: "b", Project: "other", App: "site"}, {ID: "c"}}}
	h.m.collector.mu.Unlock()
	h.m.store.mu.Lock()
	for _, subj := range []string{"project:shop", "project:other", "/var/lib/tiffin"} {
		if _, err := h.m.store.db.ExecContext(ctx, `INSERT INTO alert_state(rule, subject, firing, value, since, summary) VALUES ('r', ?, 1, 1, ?, 's')`, subj, now()); err != nil {
			t.Fatal(err)
		}
		if _, err := h.m.store.db.ExecContext(ctx, `INSERT INTO alert_history(at, rule, subject, state, value, summary, delivery) VALUES (?, 'r', ?, 'firing', 1, 's', 'd')`, now(), subj); err != nil {
			t.Fatal(err)
		}
	}
	h.m.store.mu.Unlock()
	_ = h.m.store.PutRule(ctx, Rule{Name: "other-errors", Kind: KindErrorSpike, Project: "other", Threshold: 1, Enabled: true})
	_ = h.m.store.PutRule(ctx, Rule{Name: "shop-errors", Kind: KindErrorSpike, Project: "shop", Threshold: 1, Enabled: true})

	shop := h.reader("shop")
	code, ov, _ := h.call(shop, "GET", "/v1/observe/overview", nil)
	if code != 200 {
		t.Fatalf("overview: %d %v", code, ov)
	}
	cs := ov["now"].(map[string]any)["containers"].([]any)
	if len(cs) != 1 || cs[0].(map[string]any)["project"] != "shop" {
		t.Fatalf("containers: %v", cs)
	}
	if f := ov["firing"].([]any); len(f) != 1 || f[0].(map[string]any)["subject"] != "project:shop" {
		t.Fatalf("overview alerts: %v", f)
	}
	_, view, _ := h.call(shop, "GET", "/v1/observe/alerts", nil)
	if f, hi := view["firing"].([]any), view["history"].([]any); len(f) != 1 || len(hi) != 1 || hi[0].(map[string]any)["subject"] != "project:shop" {
		t.Fatalf("alerts: %v", view)
	}
	_, _, rules := h.call(shop, "GET", "/v1/observe/alert-rules", nil)
	var names []string
	for _, r := range rules {
		names = append(names, r.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "error-spike,shop-errors" {
		t.Fatalf("rules: %v", names)
	}
	// A key for every project still sees everything.
	all := h.reader("all")
	_, ov, _ = h.call(all, "GET", "/v1/observe/overview", nil)
	if cs := ov["now"].(map[string]any)["containers"].([]any); len(cs) != 3 {
		t.Fatalf("all containers: %v", cs)
	}
	if _, _, rules := h.call(all, "GET", "/v1/observe/alert-rules", nil); len(rules) != len(DefaultRules)+2 {
		t.Fatalf("all rules: %d", len(rules))
	}
	// The collector's snapshot is not changed by filtering a copy.
	if n := len(h.m.collector.Latest().Containers); n != 3 {
		t.Fatalf("snapshot mutated: %d containers", n)
	}
}

// Delivery errors never contain the webhook URL: its path is its secret.
func TestWebhookErrorsHideTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	hook := srv.URL + "/services/T000/B000/s3cr3tPath?token=s3cr3tQuery"
	srv.Close() // connection refused
	err := postWebhook(context.Background(), hook, Notification{})
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("error %v", err)
	}
	if err := postWebhook(context.Background(), "http://bad host/s3cr3t", Notification{}); err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("bad URL error %v", err)
	}
}
