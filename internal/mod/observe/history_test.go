package observe

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A fake metrics store: answers every range query with the step it was
// asked for, a NaN where a series divides by zero, nothing for limits.
func fakeVM(t *testing.T) (*Victoria, func() []map[string]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := map[string]string{"path": r.URL.Path, "query": r.Form.Get("query"), "step": r.Form.Get("step"), "extra": r.Form.Get("extra_label")}
		mu.Lock()
		seen = append(seen, q)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q["query"], "limit"):
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		case strings.Contains(q["query"], `code="5xx"`):
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1000,"NaN"],[1060,"0.25"]]}]}}`)
		default:
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1000,"1.23456"],[1060,"2"]]}]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return &Victoria{VM: srv.URL, VL: srv.URL}, func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = nil
		return out
	}
}

func TestUsageHistory(t *testing.T) {
	vic, seen := fakeVM(t)
	h := newHarness(t, vic)
	shop := h.agent("shop")

	code, out, _ := h.call(shop, "GET", "/v1/projects/shop/usage/history?range=7d", nil)
	if code != 200 || out["stepSeconds"].(float64) != 3600 || out["range"] != "7d" {
		t.Fatalf("%d %v", code, out)
	}
	series := out["series"].(map[string]any)
	for _, name := range []string{"memory", "cpu", "requests", "p50", "p95", "errors", "database", "files", "kv", "connections"} {
		if _, ok := series[name]; !ok {
			t.Errorf("no %s series: %v", name, series)
		}
	}
	if _, ok := series["memoryLimit"]; ok {
		t.Error("a limit series without points must be left out")
	}
	if pts := series["errors"].([]any); len(pts) != 1 || pts[0].([]any)[1].(float64) != 0.25 {
		t.Errorf("NaN steps are dropped: %v", pts)
	}
	if v := series["memory"].([]any)[0].([]any)[1].(float64); v != 1.235 {
		t.Errorf("rounded to 3 places: %v", v)
	}
	qs := seen()
	if len(qs) != 12 {
		t.Fatalf("%d queries", len(qs))
	}
	for _, q := range qs {
		if q["path"] != "/api/v1/query_range" || q["step"] != "3600s" || q["extra"] != "project=shop" || !strings.Contains(q["query"], `project="shop"`) {
			t.Errorf("query %v", q)
		}
	}

	// Asked again soon: from the cache, no new queries.
	if code, _, _ := h.call(shop, "GET", "/v1/projects/shop/usage/history?range=7d", nil); code != 200 || len(seen()) != 0 {
		t.Fatalf("not cached: %d", code)
	}

	// One app: its containers and its requests, no project-wide data.
	code, out, _ = h.call(shop, "GET", "/v1/projects/shop/usage/history?range=1h&app=web", nil)
	if code != 200 || out["stepSeconds"].(float64) != 30 || out["app"] != "web" {
		t.Fatalf("%d %v", code, out)
	}
	if _, ok := out["series"].(map[string]any)["database"]; ok {
		t.Error("an app has no database series")
	}
	for _, q := range seen() {
		if !strings.Contains(q["query"], `project="shop",app="web"`) || strings.Contains(q["query"], "tiffin_project_") {
			t.Errorf("app query %v", q)
		}
	}

	// Another project's key cannot read it.
	other := h.agent("other")
	if code, _, _ := h.call(other, "GET", "/v1/projects/shop/usage/history", nil); code != 403 {
		t.Fatalf("other project's key: %d", code)
	}
	if code, _, _ := h.call(shop, "GET", "/v1/projects/shop/usage/history?range=2y", nil); code != 422 {
		t.Fatalf("bad range: %d", code)
	}
}
