package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/ids"
)

// TestDeployVersionsAndProjectList: production deploys are numbered per app
// (old records backfilled by creation order), and the project-wide list
// merges every app, newest first, with its filters and pages.
func TestDeployVersionsAndProjectList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.r.st
	// Records from before versions existed.
	put := func(app, preview, status, ref string) *Deploy {
		d := &Deploy{ID: ids.New("dep"), Project: "shop", App: app, Preview: preview, Status: status, Source: SourceGit, Ref: ref, CreatedAt: time.Now().UTC()}
		if err := st.putDeploy(ctx, d); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // IDs order by time
		return d
	}
	a1 := put("api", "", StatusSuperseded, "main")
	put("api", "pr-1", StatusLive, "feat")
	a2 := put("api", "", StatusFailed, "main")
	w1 := put("web", "", StatusLive, "main")
	a3 := put("api", "", StatusLive, "main")
	// Another project's deploys never leak in (shop2 shares the "shop" prefix).
	if err := st.putDeploy(ctx, &Deploy{ID: ids.New("dep"), Project: "shop2", App: "api", Status: StatusLive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// A new production deploy numbers the old ones first, then takes the next.
	if v, err := st.nextVersion(ctx, "shop", "api"); err != nil || v != 4 {
		t.Fatalf("next version = %d, %v; want 4", v, err)
	}
	for want, d := range map[int]*Deploy{1: a1, 2: a2, 3: a3} {
		got, _ := st.getDeploy(ctx, "shop", "api", d.ID)
		if got.Version != want {
			t.Fatalf("%s: version %d, want %d", d.ID, got.Version, want)
		}
	}

	owner, _, err := h.p.Tokens.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(api.Deps{DB: h.p.DB, Engine: h.p.Engine, Tokens: h.p.Tokens, Platform: h.p}).Handler())
	defer srv.Close()
	list := func(q string) ProjectDeployList {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/v1/projects/shop/deploys"+q, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %s", q, res.StatusCode, b)
		}
		var l ProjectDeployList
		if err := json.Unmarshal(b, &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	all := list("")
	if len(all.Deploys) != 5 || all.Deploys[0].ID != a3.ID || all.Deploys[4].ID != a1.ID {
		t.Fatalf("all: %d deploys, want 5 newest first", len(all.Deploys))
	}
	// The web app's legacy record is numbered on read.
	if all.Deploys[1].ID != w1.ID || all.Deploys[1].Version != 1 {
		t.Fatalf("web: %+v", all.Deploys[1])
	}
	if l := list("?env=preview"); len(l.Deploys) != 1 || l.Deploys[0].Preview != "pr-1" || l.Deploys[0].Version != 0 {
		t.Fatalf("previews: %+v", l.Deploys)
	}
	if l := list("?app=api&env=production&status=live,failed"); len(l.Deploys) != 2 {
		t.Fatalf("filtered: %d", len(l.Deploys))
	}
	if l := list("?branch=feat"); len(l.Deploys) != 1 {
		t.Fatalf("branch: %d", len(l.Deploys))
	}
	page := list("?limit=2")
	if len(page.Deploys) != 2 || page.Next != page.Deploys[1].ID {
		t.Fatalf("page: %d next %q", len(page.Deploys), page.Next)
	}
	if rest := list("?limit=10&before=" + page.Next); len(rest.Deploys) != 3 || rest.Next != "" {
		t.Fatalf("rest: %d next %q", len(rest.Deploys), rest.Next)
	}
}
