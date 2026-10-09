package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/edge/switchboard"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/budget"
	"github.com/shiptiffin/tiffin/internal/mod/postgres"
	"github.com/shiptiffin/tiffin/internal/mod/valkey"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Deploy addresses. Every production deploy of a web app has an address of
// its own, d-<short id>--<name>.<apps domain>, where name is the app's
// name under the apps domain as previews use it (previewHost) and the short
// id is the last 8 characters of the deploy ID (its random part), in lower
// case. Preview names may not start with "d-" and routes may not contain
// "--", so no preview or app can take such a name; a label past 63
// characters is cut and ends in a hash, as for previews.
//
//   - The live deploy's address is production itself: the same containers.
//   - An old version (a kept rollback target) runs only while visited: its
//     environment is <app>@d-<short id>, a sleeping environment the first
//     request wakes (the switchboard holds it until the app answers), with
//     one instance, asleep again after VersionIdle without requests. At most
//     maxAwakeVersions of a project's old versions are awake: waking another
//     puts the least recently used one to sleep. Their containers run in the
//     project's slice, so they count against its memory like previews.
//   - An old version reads production's data read-only: the project's
//     read-only database login (p_<project>__read) and Valkey user, mail to
//     the dev inbox, its own disk folders made from its image; no crons,
//     queue deliveries or workers reach it. Otherwise it has production's env
//     and secrets.
//   - A static version's address serves its files directly.
//   - A version gc cleaned up answers a small page linking to the live site
//     (the switchboard's Gone), as long as its record is kept.
//   - By default only people signed in to the dashboard may open them
//     (edge/gate.go); a project with deployAddresses: "public" opens them
//     to anyone.

const (
	versionPrefix = "d-"
	// keepVersions is how many earlier production builds an app keeps to
	// roll back to, each at its own address.
	keepVersions = 20
	// pressureKeep is what gc keeps instead while the data disk is past the
	// disk guard's warning level.
	pressureKeep = 3
	// versionIdle: an old version sleeps after this long without requests.
	versionIdle = 5 * time.Minute
	// maxAwakeVersions is how many of a project's old versions run at once.
	maxAwakeVersions = 2
)

// Retention of a production deploy's build.
const (
	RetentionKept    = "kept"
	RetentionCleaned = "cleaned"
)

// shortID is the part of a deploy ID its address carries.
func shortID(id string) string {
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	return strings.ToLower(id)
}

// versionEnv names the environment an old version wakes in.
func versionEnv(id string) string { return versionPrefix + shortID(id) }

// isVersionEnv reports whether an environment's "preview" is an old version.
func isVersionEnv(preview string) bool { return strings.HasPrefix(preview, versionPrefix) }

// deployHost is a production deploy's own host.
func deployHost(id, project, app string, spec *manifest.App, domain string) string {
	return previewHost(versionEnv(id), project, app, spec, domain)
}

// ownURL is a deploy's own address: a production deploy's d-<id>--<name>,
// a preview's preview address; none for workers.
func (r *rt) ownURL(d *Deploy, spec *manifest.App) string {
	if spec.Role == manifest.RoleWorker {
		return ""
	}
	if d.Preview != "" {
		return r.deployURL(d, spec)
	}
	return r.p.URL(deployHost(d.ID, d.Project, d.App, spec, r.p.AppsDomain()))
}

// addressed reports whether a production deploy has (or had) an address:
// it went live at some point.
func addressed(d *Deploy) bool {
	switch d.Status {
	case StatusLive, StatusSuperseded, StatusRolledBack, StatusStopped:
		return d.Preview == ""
	}
	return false
}

// kept reports whether an addressed deploy's build is still there.
func kept(d *Deploy) bool {
	return d.Status == StatusLive || d.Image != "" || d.StaticRoot != ""
}

// present fills what a deploy record shows that depends on now: its own
// address (the box's domain may have changed) and, for production, whether
// its build is kept. spec nil: the app is gone.
func (r *rt) present(d *Deploy, spec *manifest.App) {
	if spec == nil || spec.Role == manifest.RoleWorker {
		d.URL, d.AppURL, d.Retention = "", "", ""
		return
	}
	d.AppURL = r.deployURL(d, spec)
	if d.Preview != "" {
		return
	}
	d.URL, d.Retention = "", ""
	switch {
	case !d.Terminal():
		d.URL = r.ownURL(d, spec) // where it will be, once live
	case !addressed(d):
	case kept(d):
		d.URL, d.Retention = r.ownURL(d, spec), RetentionKept
	default:
		d.Retention = RetentionCleaned
	}
}

// presentAll presents deploys of possibly several apps of a project.
func (r *rt) presentAll(ctx context.Context, project string, ds []*Deploy) {
	specs := map[string]*manifest.App{}
	for _, d := range ds {
		spec, ok := specs[d.App]
		if !ok {
			spec, _ = r.appSpec(ctx, project, d.App)
			specs[d.App] = spec
		}
		r.present(d, spec)
	}
}

// deployRef is the deploy an address serves.
type deployRef struct{ project, app, id string }

// deployAddrs is what routes found at deploy addresses.
type deployAddrs struct {
	hosts map[string]deployRef
	// stubs: old versions with no environment yet, as the switchboard
	// sees them (asleep: the first request wakes them).
	stubs map[string]*switchboard.Env
	gone  map[string]string // cleaned versions' hosts → the live address
}

// deployAddressesPublic reports whether a project opens its deploy
// addresses to anyone (deployAddresses: "public").
func deployAddressesPublic(res map[string]change.Resource) bool {
	var ps change.ProjectSpec
	if rs, ok := res[change.KindProject]; ok && json.Unmarshal(rs.Spec, &ps) == nil {
		return ps.DeployAddresses == manifest.DeployAddressesPublic
	}
	return false
}

// versionRoutes adds the deploy addresses of a production environment
// (st, serving live) through add, and records them in addrs.
func (r *rt) versionRoutes(ctx context.Context, st *AppState, spec *manifest.App, live *Deploy, public bool, sb string, addrs *deployAddrs,
	add func(rt edge.Route, who, env string)) error {
	ds, err := r.st.listDeploys(ctx, st.Project, st.App, "")
	if err != nil {
		return err
	}
	prodEnv := envKey(st.Project, st.App, "")
	main := r.deployURL(live, spec)
	who := st.Project + "/" + st.App
	for _, d := range ds {
		building := !d.Terminal()
		if !addressed(d) && d.ID != live.ID && !building {
			continue
		}
		host := deployHost(d.ID, st.Project, st.App, spec, r.p.AppsDomain())
		addrs.hosts[host] = deployRef{st.Project, st.App, d.ID}
		rt := edge.Route{Host: host, Gate: !public, NoIndex: true, NextCache: d.StaticRoot == "" && spec.Framework == manifest.FrameworkNext}
		if d.Vercel != nil {
			rt.Rules = d.Vercel.Rules
		}
		// A host's route is the same before and after its deploy goes live
		// (the switchboard's table says which environment it reaches), so a
		// deploy's switch never reloads the edge: its address is added
		// while it builds.
		env := ""
		switch {
		case d.StaticRoot != "":
			rt.FileRoot, rt.SPA, rt.SPAPage = d.StaticRoot, strings.HasSuffix(d.Framework, "+spa"), d.SPAPage
		case d.ID == live.ID: // also while its record still says starting
			rt.Upstream, env = sb, prodEnv
		case building:
			rt.Upstream = sb // nothing serves it until it is live
		case !kept(d):
			addrs.gone[host] = main
			rt.Upstream = sb // the same route as while kept: no edge reload
		default:
			rt.Upstream, env = sb, envKey(st.Project, st.App, versionEnv(d.ID))
			addrs.stubs[env] = &switchboard.Env{Project: st.Project, App: st.App, Preview: versionEnv(d.ID), Live: d.ID, Sleeping: true,
				Assets: r.assetsDir(st.Project, st.App, versionEnv(d.ID)), Timeout: r.requestTimeout(ctx, st.Project, st.App), Cgroup: r.peerDir(st.Project)}
		}
		add(rt, who+"#"+d.ID, env)
	}
	return nil
}

// versionDeploy finds the kept, not live, container build an old
// version's environment runs.
func (r *rt) versionDeploy(ctx context.Context, project, app, env string) (*Deploy, error) {
	prod, err := r.st.getState(ctx, project, app, "")
	if err != nil {
		return nil, err
	}
	ds, err := r.st.listDeploys(ctx, project, app, "")
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		if versionEnv(d.ID) != env || !addressed(d) {
			continue
		}
		switch {
		case d.ID == prod.Live:
			return nil, &stateError{"deploy " + d.ID + " is live: its address is production's", "Open it again: it serves production now."}
		case d.Image == "":
			return nil, &stateError{"deploy " + d.ID + " was cleaned up", "Deploy that version again to get it back."}
		}
		return d, nil
	}
	return nil, errNotFound
}

// prepareVersion readies an old version's environment for a wake: it
// records the environment (asleep, so the wake starts it) and, when it is
// not awake already, puts the project's least recently used old versions to
// sleep until fewer than maxAwakeVersions run. Callers hold the project's
// versions lock (versionsLock).
func (r *rt) prepareVersion(ctx context.Context, project, app, env string) error {
	d, err := r.versionDeploy(ctx, project, app, env)
	if err != nil {
		return err
	}
	key := envKey(project, app, env)
	if st, err := r.st.getState(ctx, project, app, env); err != nil {
		return err
	} else if st.Live == d.ID && len(st.Instances) > 0 && !st.Sleeping {
		return nil
	}
	r.capVersions(ctx, project, key, maxAwakeVersions-1)
	unlock := r.lock(key)
	defer unlock()
	st, err := r.st.getState(ctx, project, app, env)
	if err != nil || st.Live == d.ID {
		return err
	}
	st.Live, st.Sleeping, st.Stopped = d.ID, true, false
	return r.st.putState(ctx, st)
}

func versionsLock(project string) string { return "versions " + project }

// capVersions puts a project's least recently used old versions to sleep
// until at most n besides except are awake.
func (r *rt) capVersions(ctx context.Context, project, except string, n int) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	var awake []*AppState
	for _, s := range states {
		if s.Project == project && isVersionEnv(s.Preview) && !s.Sleeping && len(s.Instances) > 0 && envKey(s.Project, s.App, s.Preview) != except {
			awake = append(awake, s)
		}
	}
	r.pullActivity()
	sort.SliceStable(awake, func(i, j int) bool { return r.lastActive(awake[i]).Before(r.lastActive(awake[j])) })
	for len(awake) > n {
		s := awake[0]
		awake = awake[1:]
		r.p.Log.Info("old version asleep: another one woke", "project", s.Project, "app", s.App, "version", s.Preview, "max_awake", maxAwakeVersions)
		r.sleep(ctx, s.Project, s.App, s.Preview, 0)
	}
}

// dropVersionEnv removes an old version's environment: its containers,
// state, logs, assets and disk folders. Its deploy record and build stay
// (gc decides about those).
func (r *rt) dropVersionEnv(ctx context.Context, project, app, env string) error {
	if !isVersionEnv(env) {
		return fmt.Errorf("%s is not an old version's environment", env)
	}
	unlock := r.lock(envKey(project, app, env))
	defer unlock()
	st, err := r.st.getState(ctx, project, app, env)
	if err != nil {
		return err
	}
	if !st.UpdatedAt.IsZero() {
		if err := r.st.deleteState(ctx, st); err != nil {
			return err
		}
	}
	ins := slices.Concat(st.Instances, st.Parked)
	for _, ds := range st.Draining {
		ins = append(ins, ds.Instances...)
	}
	r.removeInstances(ctx, ins)
	r.removeDir(r.envLogDir(project, app, env))
	r.forgetFiles(project, app, env, false)
	if dir := r.diskDir(project, app, env); exists(dir) {
		r.removeDir(dir)
		r.quotas.forget(ctx, project+"/"+app+"/"+envDirName(env)+"/")
		forgetDiskBytes(project)
	}
	return nil
}

// syncVersionEnvs drops the old-version environments of an app whose
// deploy is gone, cleaned up or live again (its address is production's).
func (r *rt) syncVersionEnvs(ctx context.Context, project, app string) {
	states, err := r.st.statesOf(ctx, project, app)
	if err != nil {
		return
	}
	var prod *AppState
	for _, s := range states {
		if s.Preview == "" {
			prod = s
		}
	}
	for _, s := range states {
		if !isVersionEnv(s.Preview) {
			continue
		}
		d, err := r.st.getDeploy(ctx, project, app, s.Live)
		if err == nil && d.Image != "" && (prod == nil || prod.Live != d.ID) {
			continue
		}
		if err := r.dropVersionEnv(ctx, project, app, s.Preview); err != nil {
			r.p.Log.Warn("runtime: remove an old version's environment", "project", project, "app", app, "version", s.Preview, "err", err)
		}
	}
}

// convergeVersion keeps an awake old version on the app's current config:
// one started with another puts it to sleep (the next request starts it
// with the new one).
func (r *rt) convergeVersion(ctx context.Context, project, app, env string, spec *manifest.App) error {
	st, err := r.st.getState(ctx, project, app, env)
	if err != nil || st.Sleeping || len(st.Instances) == 0 {
		return err
	}
	_, hash, err := r.instanceEnv(ctx, project, app, env, spec)
	if err != nil {
		return err
	}
	if hash != st.Hash {
		r.sleep(ctx, project, app, env, 0)
	}
	return nil
}

// readOnlyServices swaps the box's database and Valkey credentials in env
// for their read-only twins (the project's p_<project>__read login and
// read-only Valkey user), each where env still holds the box's own value:
// an app that set one itself keeps it. branch "" is production's database.
func (r *rt) readOnlyServices(ctx context.Context, project, branch string, env map[string]string) error {
	if r.hasService(ctx, project, "postgres") {
		box, err := postgres.ConnEnv(ctx, r.p, project, branch, false)
		if err != nil {
			return err
		}
		if env["DATABASE_URL"] == box["DATABASE_URL"] {
			ro, err := r.opt.ReadAccess.PostgresReadEnv(ctx, r.p, project, branch)
			if err != nil {
				return err
			}
			for k, v := range ro {
				if env[k] == box[k] {
					env[k] = v
				}
			}
		}
	}
	if r.hasService(ctx, project, "valkey") {
		box, err := valkey.ConnEnv(ctx, r.p, project, false)
		if err != nil {
			return err
		}
		rest, err := valkey.RESTEnv(ctx, r.p, project)
		if err != nil {
			return err
		}
		maps.Copy(box, rest)
		ro, err := r.opt.ReadAccess.ValkeyReadEnv(ctx, r.p, project)
		if err != nil {
			return err
		}
		for k, v := range ro {
			if env[k] == box[k] {
				env[k] = v
			}
		}
	}
	return nil
}

// ---- retention under disk pressure ----

// diskPressure reports whether the data disk is past the disk guard's
// warning level: gc then keeps pressureKeep rollback targets per app.
func (r *rt) diskPressure() bool {
	used := float64(-1)
	if r.opt.DiskUsedPercent != nil {
		used = r.opt.DiskUsedPercent()
	} else if total, free := diskBytes(r.opt.DataDir), freeBytes(r.opt.DataDir); total > 0 && free >= 0 {
		used = 100 * float64(total-free) / float64(total)
	}
	return used >= float64(budget.CurrentSettings().DiskWarnPercent)
}

// TrimVersions runs gc on every app's production environment, so the
// oldest kept versions go once the data disk is past the disk guard's
// warning level (the disk guard calls it then).
func (m *Module) TrimVersions(ctx context.Context) {
	r, err := m.rt()
	if err != nil {
		return
	}
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	for _, s := range states {
		if s.Preview == "" && !s.Stopped {
			r.gc(ctx, s.Project, s.App, "")
		}
	}
}

// ---- the gate ----

const nsGate = "runtime/gate"

// loadGateKey reads (or makes) the secret the deploy-address gate signs
// with, and hands it to the edge.
func (r *rt) loadGateKey(ctx context.Context) error {
	raw, ok, err := r.p.DB.KVGet(ctx, nsGate, "key")
	if err != nil {
		return err
	}
	key, _ := hex.DecodeString(string(raw))
	if !ok || len(key) < 32 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		if err := r.p.DB.KVPut(ctx, nsGate, "key", []byte(hex.EncodeToString(key))); err != nil {
			return err
		}
	}
	r.gateKey = key
	edge.SetGateSource(func() *edge.Gate { return &edge.Gate{Secret: key} })
	return nil
}

// DeployLink is a one-use link that opens a deploy address.
type DeployLink struct {
	URL       string    `json:"url" doc:"Open within a minute, once: it sets a cookie for that address (an hour) and goes on to the page"`
	ExpiresAt time.Time `json:"expiresAt"`
	Project   string    `json:"project"`
	App       string    `json:"app"`
	Deploy    string    `json:"deploy"`
}

var linkMu sync.Mutex // one routes() read per link, not per request burst

// deployLink makes a hand-off link for raw, a URL on a deploy address.
func (r *rt) deployLink(ctx context.Context, raw string) (*DeployLink, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, problem(422, "validation", "url must be a deploy's address, like https://d-1a2b3c4d--shop."+r.p.AppsDomain()+"/", "")
	}
	host := strings.ToLower(u.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	r.mu.Lock()
	ref, ok := r.addrs.hosts[host]
	r.mu.Unlock()
	if !ok {
		linkMu.Lock()
		_, _, _ = r.routes(ctx) // a deploy that just went live
		linkMu.Unlock()
		r.mu.Lock()
		ref, ok = r.addrs.hosts[host]
		r.mu.Unlock()
	}
	if !ok {
		return nil, problem(404, "not_found", host+" is not a deploy's address on this box", "Deploy addresses look like d-<id>--<app>."+r.p.AppsDomain()+": see url in tiffin deploys list.")
	}
	if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ref.project); err != nil {
		return nil, err
	}
	next := u.EscapedPath()
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	if u.RawQuery != "" {
		next += "?" + u.RawQuery
	}
	now := time.Now()
	tok := edge.HandoffToken(r.gateKey, host, now)
	link := r.p.URL(host) + edge.GatePath + "?t=" + url.QueryEscape(tok) + "&next=" + url.QueryEscape(next)
	return &DeployLink{URL: link, ExpiresAt: now.Add(edge.HandoffTTL).UTC().Truncate(time.Second), Project: ref.project, App: ref.app, Deploy: ref.id}, nil
}

func (m *Module) registerDeployLink(a huma.API) {
	op := api.Op("deploy-link", http.MethodPost, "/v1/deploy-link", "deploys link", api.RiskRead, "Get a link that opens a deploy's address",
		"Every production deploy of a web app has its own address (url in the deploy's record). Unless its project sets deployAddresses: \"public\", "+
			"only people signed in to the box's dashboard may open one: the dashboard does this for them. This returns a one-use link, "+
			"valid for a minute, that opens the address in any browser (an agent's headless browser, say) for an hour. Needs read access to the project.", "apps")
	op.Errors = append(op.Errors, 404)
	huma.Register(a, op, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			URL string `json:"url" doc:"The deploy's address, with the path to open: https://d-1a2b3c4d--shop.example.app/pricing"`
		}
	}) (*struct{ Body *DeployLink }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		l, err := r.deployLink(ctx, in.Body.URL)
		if err != nil {
			return nil, err
		}
		return &struct{ Body *DeployLink }{l}, nil
	}))
}
