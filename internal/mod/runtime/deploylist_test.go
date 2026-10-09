package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/ids"
	"github.com/shiptiffin/tiffin/internal/manifest"
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
	if len(page.Deploys) != 2 || page.NextCursor != encodeCursor(page.Deploys[1].ID) {
		t.Fatalf("page: %d next %q", len(page.Deploys), page.NextCursor)
	}
	if rest := list("?limit=10&cursor=" + page.NextCursor); len(rest.Deploys) != 7 || rest.NextCursor != "" {
		t.Fatalf("rest: %d next %q", len(rest.Deploys), rest.NextCursor)
	}
}

// Pages walk deploys newest first by (created, ID) with no gaps or repeats,
// also across deploys made in the same millisecond and with filters, and a
// project's page reads its index in order.
func TestDeployPagesAndTies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.r.st
	at := time.Now().UTC().Truncate(time.Millisecond)
	var all []*Deploy
	put := func(app, preview, status string, t0 time.Time) {
		d := &Deploy{ID: ids.NewAt("dep", t0), Project: "shop", App: app, Preview: preview, Status: status, Source: SourceUpload, CreatedAt: t0}
		if err := st.putDeploy(ctx, d); err != nil {
			t.Fatal(err)
		}
		all = append(all, d)
	}
	for i := range 9 { // nine at the same millisecond: ties broken by ID
		put("api", "", []string{StatusLive, StatusSuperseded, StatusFailed}[i%3], at)
	}
	for i := range 4 {
		put("web", "", StatusSuperseded, at.Add(time.Duration(i-2)*time.Millisecond))
	}
	put("web", "pr-1", StatusLive, at.Add(time.Millisecond))
	// newest first: created, then ID
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	walk := func(app string, limit int, keep func(*Deploy) bool) []string {
		t.Helper()
		var got []string
		after := ""
		for pages := 0; ; pages++ {
			ds, next, err := st.pageDeploys(ctx, "shop", app, after, limit, keep)
			if err != nil || pages > 20 {
				t.Fatalf("page %d: %v", pages, err)
			}
			if len(ds) > limit || (next != "" && len(ds) != limit) {
				t.Fatalf("page of %d (limit %d), next %q", len(ds), limit, next)
			}
			for _, d := range ds {
				got = append(got, d.ID)
			}
			if next == "" {
				return got
			}
			after = next
		}
	}
	want := func(keep func(*Deploy) bool) []string {
		var out []string
		for _, d := range all {
			if keep == nil || keep(d) {
				out = append(out, d.ID)
			}
		}
		return out
	}
	for _, limit := range []int{1, 2, 3, 9, 14, 50} {
		if got, w := walk("", limit, nil), want(nil); !slices.Equal(got, w) {
			t.Fatalf("project, %d a page:\n got %v\nwant %v", limit, got, w)
		}
		api := func(d *Deploy) bool { return d.App == "api" }
		if got, w := walk("api", limit, nil), want(api); !slices.Equal(got, w) {
			t.Fatalf("api, %d a page:\n got %v\nwant %v", limit, got, w)
		}
		live := func(d *Deploy) bool { return d.Status == StatusLive }
		if got, w := walk("", limit, live), want(live); !slices.Equal(got, w) {
			t.Fatalf("live, %d a page:\n got %v\nwant %v", limit, got, w)
		}
	}
	// The last page says so: a page exactly full of the last deploys has no next.
	if ds, next, _ := st.pageDeploys(ctx, "shop", "", "", len(all), nil); len(ds) != len(all) || next != "" {
		t.Fatalf("full last page: %d, next %q", len(ds), next)
	}
	// Cursors are opaque and checked.
	if c := encodeCursor(all[3].ID); c == all[3].ID {
		t.Fatal("cursor is the bare ID")
	} else if id, err := decodeCursor(c); err != nil || id != all[3].ID {
		t.Fatalf("cursor round trip: %q %v", id, err)
	}
	if _, err := decodeCursor("bm90LWEtY3Vyc29y"); err == nil {
		t.Fatal("a made-up cursor was taken")
	}
	// The project's page reads the kv_deploys index in order (no sort).
	rows, err := h.p.DB.SQL().QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT value FROM kv WHERE ns GLOB 'runtime/deploys/*' AND `+deployProjectExpr+` = ? AND key < ? ORDER BY key DESC`, "shop", "~")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if p := strings.Join(plan, "; "); !strings.Contains(p, "kv_deploys") || strings.Contains(p, "TEMP B-TREE") {
		t.Fatalf("query plan: %s", p)
	}
}
