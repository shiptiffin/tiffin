package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/manifest"
)

// TestDeployVersionsAndProjectList: production deploys are numbered per app,
// two at once never take one number, and the project-wide list merges every
// app, newest first, with its filters and pages.
func TestDeployVersionsAndProjectList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.r.st
	put := func(app, preview, status, ref string, version int) *Deploy {
		d := &Deploy{ID: ids.New("dep"), Project: "shop", App: app, Preview: preview, Status: status, Source: SourceGit, Ref: ref,
			Version: version, CreatedAt: time.Now().UTC()}
		if err := st.putDeploy(ctx, d); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // IDs order by time
		return d
	}
	a1 := put("api", "", StatusSuperseded, "main", 1)
	put("api", "pr-1", StatusLive, "feat", 0)
	put("api", "", StatusFailed, "main", 2)
	w1 := put("web", "", StatusLive, "main", 1)
	a3 := put("api", "", StatusLive, "main", 3)
	// Another project's deploys never leak in (shop2 shares the "shop" prefix).
	if err := st.putDeploy(ctx, &Deploy{ID: ids.New("dep"), Project: "shop2", App: "api", Status: StatusLive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if v, err := st.nextVersion(ctx, "shop", "api"); err != nil || v != 4 {
		t.Fatalf("next version = %d, %v; want 4", v, err)
	}
	// Deploys made at once each take their own number.
	spec := &manifest.App{Framework: manifest.FrameworkStatic}
	var wg sync.WaitGroup
	got := make(chan int, 4)
	for range 4 {
		wg.Go(func() {
			d, err := h.r.newDeploy(ctx, "shop", "api", "", SourceUpload, "tok", spec)
			if err != nil {
				t.Error(err)
				return
			}
			got <- d.Version
		})
	}
	wg.Wait()
	close(got)
	seen := map[int]bool{}
	for v := range got {
		if seen[v] || v < 4 || v > 7 {
			t.Fatalf("versions taken at once: %v, then %d", seen, v)
		}
		seen[v] = true
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
	if len(all.Deploys) != 9 || all.Deploys[4].ID != a3.ID || all.Deploys[8].ID != a1.ID {
		t.Fatalf("all: %d deploys, want 9 newest first", len(all.Deploys))
	}
	if all.Deploys[5].ID != w1.ID || all.Deploys[5].Version != 1 {
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
	if rest := list("?limit=10&before=" + page.Next); len(rest.Deploys) != 7 || rest.Next != "" {
		t.Fatalf("rest: %d next %q", len(rest.Deploys), rest.Next)
	}
}
