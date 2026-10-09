package runtime

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/ids"
	"github.com/shiptiffin/tiffin/internal/manifest"
)

// Deploy addresses are d-<last 8 of the ID>--<the app's name under the apps
// domain>, at most one DNS label, and can't be taken by a preview or a route.
func TestDeployHostNaming(t *testing.T) {
	id := "dep_01JA2B3C4D5E6F7G8H9J0KMNPQ"
	web := &manifest.App{Routes: []string{"shop"}}
	if got := deployHost(id, "shop", "web", web, "example.app"); got != "d-9j0kmnpq--shop.example.app" {
		t.Fatalf("host %q", got)
	}
	// An app served on a path or its own domain uses <project>-<app>, as previews do.
	api := &manifest.App{Routes: []string{"shop/api", "example.com"}}
	if got := deployHost(id, "shop", "api", api, "example.app"); got != "d-9j0kmnpq--shop-api.example.app" {
		t.Fatalf("host %q", got)
	}
	if !isVersionEnv(versionEnv(id)) || isVersionEnv("pr-12") || isVersionEnv("dev") {
		t.Fatal("version environments are named d-<short id>, and only they")
	}
	// Long names are cut to one label and stay unique (a hash of the whole).
	long := strings.Repeat("a", 40)
	spec := &manifest.App{Routes: []string{long + "-" + strings.Repeat("b", 20)}}
	h1 := deployHost(id, long, "b", spec, "example.app")
	h2 := deployHost("dep_01JA2B3C4D5E6F7G8H9J0KMNPR", long, "b", spec, "example.app")
	label, _, _ := strings.Cut(h1, ".")
	if len(label) > 63 || h1 == h2 || !strings.HasPrefix(label, "d-9j0kmnpq--") {
		t.Fatalf("long: %q (%d) vs %q", h1, len(label), h2)
	}
	// Deploys made in the same millisecond still get their own addresses.
	now := time.Now()
	a, b := shortID(ids.NewAt("dep", now)), shortID(ids.NewAt("dep", now))
	if a == b || len(a) != 8 {
		t.Fatalf("short ids %q %q", a, b)
	}
	// No preview can be named like one, and no route has "--" in its first label.
	h := newHarness(t)
	if _, p := h.r.checkDeployable(context.Background(), "shop", "api", "d-9j0kmnpq", false); p == nil || !strings.Contains(p.Detail, `"d-"`) {
		t.Fatalf("preview named d-…: %v", p)
	}
	if err := checkPreviewNames(map[string]string{"d-9j0kmnpq--shop.tiffin.localhost": "web"}); err == nil {
		t.Fatal("a route shaped like a deploy address was accepted")
	}
	if previewHost("pr-1", "shop", "web", web, "example.app") == deployHost(id, "shop", "web", web, "example.app") {
		t.Fatal("a preview and a deploy share an address")
	}
}

// The live deploy's address is production itself; an earlier one wakes in
// its own environment on its first request, with production's data read
// only, sleeps when idle, and gives way to production when rolled back to.
func TestOldVersionAtItsAddress(t *testing.T) {
	h := newHarness(t)
	h.withPostgres("")
	ctx := context.Background()
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	host := func(d *Deploy) string { return deployHost(d.ID, "shop", "api", spec, h.p.AppsDomain()) }
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	v2 := h.deploy("api", "", map[string]string{"index.ts": "v2"})
	if v2.URL != "https://"+host(v2)+":8443" {
		t.Fatalf("v2 url %q", v2.URL)
	}

	runs := h.eng.runs
	if code, body := h.get(host(v2), "/x"); code != 200 || !strings.Contains(body, strings.ToLower(v2.ID)) || h.eng.runs != runs {
		t.Fatalf("live deploy's address: %d %s (runs %d → %d: no second copy)", code, body, runs, h.eng.runs)
	}

	env := versionEnv(v1.ID)
	code, body := h.get(host(v1), "/x")
	if code != 200 || !strings.Contains(body, strings.ToLower(v1.ID)) {
		t.Fatalf("old version: %d %s", code, body)
	}
	st := h.state("api", env)
	if st.Live != v1.ID || st.Sleeping || len(st.Instances) != 1 {
		t.Fatalf("old version's environment: %+v", st)
	}
	if cur, _ := h.r.st.getDeploy(ctx, "shop", "api", v1.ID); cur.Status != StatusSuperseded || cur.Preview != "" {
		t.Fatalf("waking an old version changed its record: %s %q", cur.Status, cur.Preview)
	}
	if prod := h.state("api", ""); prod.Live != v2.ID || len(prod.Instances) != 2 {
		t.Fatalf("production moved: %+v", prod)
	}
	// Read-only: the project's read login, its own address, no preview identity.
	ie := h.instanceEnv("api", env)
	if !strings.Contains(ie["DATABASE_URL"], "p_shop__read:") || ie["PGUSER"] != "p_shop__read" || ie["TIFFIN_READ_ONLY"] != "1" {
		t.Fatalf("old version's database: %s %s %q", ie["DATABASE_URL"], ie["PGUSER"], ie["TIFFIN_READ_ONLY"])
	}
	if ie["TIFFIN_URL"] != v1.URL || ie["TIFFIN_PREVIEW"] != "" || ie["GREETING"] != "hello" {
		t.Fatalf("old version's env: TIFFIN_URL %q, preview %q, GREETING %q", ie["TIFFIN_URL"], ie["TIFFIN_PREVIEW"], ie["GREETING"])
	}
	if pe := h.instanceEnv("api", ""); strings.Contains(pe["DATABASE_URL"], "__read") || pe["TIFFIN_READ_ONLY"] != "" {
		t.Fatalf("production lost its writable database: %s", pe["DATABASE_URL"])
	}
	// Not a preview: not listed with them.
	if rt, err := h.r.appRuntime(ctx, "shop", "api"); err != nil || len(rt.Previews) != 0 {
		t.Fatalf("runtime lists %d previews: %v", len(rt.Previews), err)
	}

	// Idle → asleep; the next request wakes it again.
	h.r.opt.VersionIdle = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	h.r.sleepIdle(ctx)
	if st := h.state("api", env); !st.Sleeping || len(st.Instances) != 0 {
		t.Fatalf("not asleep: %+v", st)
	}
	h.r.opt.VersionIdle = time.Hour
	if code, body := h.get(host(v1), "/again"); code != 200 || !strings.Contains(body, strings.ToLower(v1.ID)) {
		t.Fatalf("wake: %d %s", code, body)
	}

	// Rolled back to: its address is production's, and its own copy goes.
	if _, err := h.r.rollback(ctx, "shop", "api", v1.ID); err != nil {
		t.Fatal(err)
	}
	if st := h.state("api", env); !st.UpdatedAt.IsZero() {
		t.Fatalf("the old version's environment outlived the rollback: %+v", st)
	}
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.preview"] == env {
			t.Fatalf("its container still runs: %s", c.spec.Name)
		}
	}
	if code, body := h.get(host(v1), "/x"); code != 200 || !strings.Contains(body, strings.ToLower(v1.ID)) || len(h.state("api", "").Instances) != 2 {
		t.Fatalf("v1's address after the rollback: %d %s", code, body)
	}
	// v2 is the old version now.
	if code, body := h.get(host(v2), "/x"); code != 200 || !strings.Contains(body, strings.ToLower(v2.ID)) || h.state("api", versionEnv(v2.ID)).Live != v2.ID {
		t.Fatalf("v2's address after the rollback: %d %s", code, body)
	}
}

// A version gc cleans up loses its image and its environment; its address
// then says so and links to the live site, and its record says cleaned.
func TestCleanedVersionAddress(t *testing.T) {
	h := newHarness(t) // keeps 2 rollback targets
	ctx := context.Background()
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	var ds []*Deploy
	for i := range 3 {
		ds = append(ds, h.deploy("api", "", map[string]string{"index.ts": fmt.Sprint(i)}))
	}
	v1 := ds[0]
	host := deployHost(v1.ID, "shop", "api", spec, h.p.AppsDomain())
	if code, _ := h.get(host, "/"); code != 200 || len(h.state("api", versionEnv(v1.ID)).Instances) != 1 {
		t.Fatalf("v1 is kept but did not wake: %d", code)
	}
	h.deploy("api", "", map[string]string{"index.ts": "3"}) // v1 falls out of the window
	if st := h.state("api", versionEnv(v1.ID)); !st.UpdatedAt.IsZero() {
		t.Fatalf("a cleaned version's environment stays: %+v", st)
	}
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.preview"] == versionEnv(v1.ID) {
			t.Fatalf("a cleaned version's container runs: %s", c.spec.Name)
		}
	}
	code, body := h.get(host, "/pricing")
	if code != http.StatusGone || !strings.Contains(body, "cleaned up") || !strings.Contains(body, "https://shop.tiffin.localhost:8443/api") {
		t.Fatalf("cleaned version: %d %s", code, body)
	}
	cur, _ := h.r.st.getDeploy(ctx, "shop", "api", v1.ID)
	h.r.present(cur, spec)
	if cur.Retention != RetentionCleaned || cur.URL != "" || cur.AppURL != "https://shop.tiffin.localhost:8443/api" {
		t.Fatalf("cleaned record: retention %q url %q app %q", cur.Retention, cur.URL, cur.AppURL)
	}
	kept, _ := h.r.st.getDeploy(ctx, "shop", "api", ds[1].ID)
	h.r.present(kept, spec)
	if kept.Retention != RetentionKept || kept.URL != "https://"+deployHost(kept.ID, "shop", "api", spec, h.p.AppsDomain())+":8443" {
		t.Fatalf("kept record: %q %q", kept.Retention, kept.URL)
	}
	// Static sites' versions are served as files: no environment, no wake.
	s1 := h.deploy("site", "", map[string]string{"index.html": "<h1>one</h1>"})
	h.deploy("site", "", map[string]string{"index.html": "<h1>two</h1>"})
	sspec, _ := h.r.appSpec(ctx, "shop", "site")
	runs := h.eng.runs
	if code, body := h.get(deployHost(s1.ID, "shop", "site", sspec, h.p.AppsDomain()), "/"); code != 200 || !strings.Contains(body, "one") || h.eng.runs != runs {
		t.Fatalf("static old version: %d %s", code, body)
	}
}

// At most two of a project's old versions run at once: waking a third puts
// the least recently used one to sleep.
func TestOldVersionsAwakeCap(t *testing.T) {
	h := newHarness(t)
	h.r.opt.KeepImages = 5
	ctx := context.Background()
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	var ds []*Deploy
	for i := range 4 {
		ds = append(ds, h.deploy("api", "", map[string]string{"index.ts": fmt.Sprint(i)}))
	}
	visit := func(d *Deploy) {
		t.Helper()
		if code, body := h.get(deployHost(d.ID, "shop", "api", spec, h.p.AppsDomain()), "/"); code != 200 || !strings.Contains(body, strings.ToLower(d.ID)) {
			t.Fatalf("visit %s: %d %s", d.ID, code, body)
		}
		time.Sleep(5 * time.Millisecond) // a clear order of last requests
	}
	awake := func() []string {
		var out []string
		for _, d := range ds[:3] {
			if st := h.state("api", versionEnv(d.ID)); !st.Sleeping && len(st.Instances) > 0 {
				out = append(out, d.ID)
			}
		}
		return out
	}
	visit(ds[0])
	visit(ds[1])
	visit(ds[2])
	if a := awake(); len(a) != 2 || a[0] != ds[1].ID || a[1] != ds[2].ID {
		t.Fatalf("awake after three: %v (want the two most recent, %s and %s)", a, ds[1].ID, ds[2].ID)
	}
	if st := h.state("api", versionEnv(ds[0].ID)); !st.Sleeping || len(st.Parked) != 1 {
		t.Fatalf("the least recently used one is not asleep: %+v", st)
	}
	visit(ds[0]) // ds[1] is now the least recently used
	if a := awake(); len(a) != 2 || a[0] != ds[0].ID || a[1] != ds[2].ID {
		t.Fatalf("awake: %v", a)
	}
	// Production never counts against the cap.
	if prod := h.state("api", ""); len(prod.Instances) != 2 || prod.Sleeping {
		t.Fatalf("production: %+v", prod)
	}
}

// Production keeps a sliding window of KeepImages earlier builds (20 on a
// box), fewer (pressureKeep) once the data disk is past the disk guard's
// warning level; the disk guard trims at once.
func TestRetentionWindowAndDiskPressure(t *testing.T) {
	if o := defaultOptions(); o.KeepImages != 20 || o.VersionIdle != 5*time.Minute {
		t.Fatalf("defaults: keep %d, idle %s", o.KeepImages, o.VersionIdle)
	}
	h := newHarness(t)
	ctx := context.Background()
	used := 50.0
	h.r.opt.DiskUsedPercent = func() float64 { return used }
	h.r.opt.KeepImages = 4
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	var ds []*Deploy
	for i := range 7 {
		ds = append(ds, h.deploy("api", "", map[string]string{"index.ts": fmt.Sprint(i)}))
	}
	retention := func() string {
		var b strings.Builder
		for _, d := range ds {
			cur, _ := h.r.st.getDeploy(ctx, "shop", "api", d.ID)
			h.r.present(cur, spec)
			b.WriteString(cur.Retention[:1])
		}
		return b.String()
	}
	if got := retention(); got != "cckkkkk" {
		t.Fatalf("window of 4 + live: %s", got)
	}
	used = 90 // past the default warning level (85)
	h.m.TrimVersions(ctx)
	if got := retention(); got != "ccckkkk" || h.imageCount() != 4 {
		t.Fatalf("under pressure, 3 + live: %s, %d images", got, h.imageCount())
	}
	if _, err := h.r.rollback(ctx, "shop", "api", ds[2].ID); err == nil {
		t.Fatal("rolled back to a trimmed version")
	}
}

// deploy-link hands a signed-in caller a one-use link that the edge's gate
// turns into a cookie for that one address; public projects drop the gate.
func TestDeployLinkAndGate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	host := deployHost(v1.ID, "shop", "api", spec, h.p.AppsDomain())
	gated := func() bool {
		t.Helper()
		routes, _, err := h.r.routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range routes {
			if r.Host == host {
				return r.Gate
			}
		}
		t.Fatalf("no route for %s", host)
		return false
	}
	if !gated() {
		t.Fatal("deploy addresses must be gated by default")
	}

	owner, _, err := h.p.Tokens.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(api.Deps{DB: h.p.DB, Engine: h.p.Engine, Tokens: h.p.Tokens, Platform: h.p}).Handler())
	defer srv.Close()
	link := func(u string) (int, DeployLink) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"url": u})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/deploy-link", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+owner)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		var l DeployLink
		_ = json.Unmarshal(body, &l)
		return res.StatusCode, l
	}
	code, l := link(v1.URL + "/pricing?plan=pro")
	if code != 200 || l.Deploy != v1.ID || l.App != "api" {
		t.Fatalf("link: %d %+v", code, l)
	}
	lu, _ := url.Parse(l.URL)
	if lu.Hostname() != host || lu.Path != edge.GatePath || lu.Query().Get("next") != "/pricing?plan=pro" {
		t.Fatalf("link %s", l.URL)
	}
	if code, _ := link("https://d-00000000--shop-api." + h.p.AppsDomain() + "/"); code != 404 {
		t.Fatalf("unknown address: %d", code)
	}

	// The edge's gate, with the same secret, takes the link once.
	g := &edge.GateHandler{Secret: hex.EncodeToString(h.r.gateKey), SignIn: "https://dashboard.tiffin.localhost:8443/gate"}
	if err := g.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	serve := func(target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.Host = host
		for _, c := range cookies {
			req.AddCookie(c)
		}
		w := httptest.NewRecorder()
		_ = g.ServeHTTP(w, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			fmt.Fprint(w, "app")
			return nil
		}))
		return w
	}
	w := serve(lu.RequestURI())
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/pricing?plan=pro" || len(w.Result().Cookies()) != 1 {
		t.Fatalf("hand-off: %d %v", w.Code, w.Header())
	}
	if w := serve("/", w.Result().Cookies()[0]); w.Code != 200 || w.Body.String() != "app" {
		t.Fatalf("with the cookie: %d %s", w.Code, w.Body)
	}
	if w := serve(lu.RequestURI()); w.Code != http.StatusForbidden {
		t.Fatalf("the link worked twice: %d", w.Code)
	}

	// deployAddresses: "public" opens them.
	h.mf.DeployAddresses = manifest.DeployAddressesPublic
	h.apply()
	if gated() {
		t.Fatal("a public project's deploy addresses are still gated")
	}
}
