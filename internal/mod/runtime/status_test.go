package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/platform"
)

// TestReportStatus: an app whose config is applied but whose first deploy
// failed reads failed, naming the deploy; a never deployed one stays ready
// with release none; a failed deploy beside a live one changes nothing.
func TestReportStatus(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, app := range []string{"api", "site", "jobs"} {
		_ = h.p.DB.SetResourceStatus(ctx, "shop", "app/"+app, platform.StateReady, "")
	}
	_ = h.p.DB.SetResourceStatus(ctx, "shop", "service/postgres", platform.StateReady, "")
	bad := h.deploy("api", "", map[string]string{"index.ts": "v1", "FAIL": ""})
	v1 := h.deploy("jobs", "", map[string]string{"index.ts": "v1"})
	h.deploy("jobs", "", map[string]string{"index.ts": "v2", "FAIL": ""})
	h.deploy("api", "pr-1", map[string]string{"index.ts": "v1"}) // a preview is not production

	st, err := h.p.ResourceStatuses(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if s := st["app/api"]; s.State != platform.StateFailed || s.Release != "failed" ||
		!strings.Contains(s.Message, "not live: its last deploy failed ("+bad.ID+")") || !strings.Contains(s.Message, "tiffin deploys build-log shop api "+bad.ID) {
		t.Errorf("first deploy failed: %+v", s)
	}
	if s := st["app/site"]; s.State != platform.StateReady || s.Release != "none" || s.Message != "not deployed yet" {
		t.Errorf("never deployed: %+v", s)
	}
	if s := st["app/jobs"]; s.State != platform.StateReady || s.Release != "live" || s.Message != "" {
		t.Errorf("live, then a failed deploy: %+v (live %s)", s, v1.ID)
	}
	if s := st["service/postgres"]; s.Release != "" {
		t.Errorf("not an app: %+v", s)
	}
	// Stored statuses are untouched: apply's convergence check reads them.
	if failed := h.p.Failed(ctx, "shop"); len(failed) != 0 {
		t.Errorf("stored statuses changed: %v", failed)
	}

	// A good deploy makes it live.
	h.deploy("api", "", map[string]string{"index.ts": "v2"})
	st, _ = h.p.ResourceStatuses(ctx, "shop")
	if s := st["app/api"]; s.State != platform.StateReady || s.Release != "live" || s.Message != "" {
		t.Errorf("deployed again: %+v", s)
	}
}
