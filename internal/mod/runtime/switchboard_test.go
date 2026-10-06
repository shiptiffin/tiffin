package runtime

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
)

func TestAppTimeout(t *testing.T) {
	if got := (&manifest.App{}).Timeout(); got != 15*time.Minute {
		t.Fatalf("default %s", got)
	}
	if got := (&manifest.App{TimeoutSeconds: 7200}).Timeout(); got != 2*time.Hour {
		t.Fatalf("7200 s: %s", got)
	}
}

// TestRequestTimeLimit: a request past its app's time limit gets 504, a
// stream past it is cut, and work under it (a stream included) is not;
// a new limit applies to the next request without a deploy.
func TestRequestTimeLimit(t *testing.T) {
	h := newHarness(t)
	web := h.mf.Apps["api"]
	web.TimeoutSeconds = 1
	h.mf.Apps["api"] = web
	h.apply()
	if d := h.deploy("api", "", map[string]string{"index.ts": "v1"}); d.Status != StatusLive {
		t.Fatalf("deploy: %s %s", d.Status, d.Error)
	}
	if code, body := h.get("shop.tiffin.localhost", "/api/x?sleep=1500ms"); code != http.StatusGatewayTimeout || !strings.Contains(body, "time limit for one request (1s)") {
		t.Fatalf("past the limit: %d %q", code, body)
	}
	if code, body := h.get("shop.tiffin.localhost", "/api/x?sleep=300ms"); code != 200 {
		t.Fatalf("under the limit: %d %q", code, body)
	}
	stream := func(ticks string) (string, error) {
		req, _ := http.NewRequest("GET", h.srv.URL+"/api/x?ticks="+ticks, nil)
		req.Host = "shop.tiffin.localhost"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return string(b), err
	}
	if got, err := stream("3"); err != nil || strings.Count(got, "tick") != 3 {
		t.Fatalf("a stream under the limit: %q %v", got, err)
	}
	began := time.Now()
	got, err := stream("15") // 3 s
	if err == nil || strings.Count(got, "tick") >= 15 || time.Since(began) > 2500*time.Millisecond {
		t.Fatalf("a stream past the limit must be cut at it: %q %v after %s", got, err, time.Since(began))
	}

	web.TimeoutSeconds = 3
	h.mf.Apps["api"] = web
	h.apply()
	if code, body := h.get("shop.tiffin.localhost", "/api/x?sleep=1500ms"); code != 200 {
		t.Fatalf("after raising the limit: %d %q", code, body)
	}
}
