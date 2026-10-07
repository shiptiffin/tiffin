// Package queue is the Tiffin queue module: push queues, topics, cron and
// durable workflows, on River (MPL-2.0, unmodified) in the platform
// Postgres, database tiffin_queue.
//
// Apps never poll. The box POSTs each job to an app route over plain HTTP on
// the box, signed with the project's signing secret; a 2xx acks it, status
// 489 (or the Tiffin-Non-Retryable header) sends it to the dead-letter queue,
// anything else retries with exponential backoff and jitter. Long jobs hold a
// lease the app extends with heartbeats. Limits (queue concurrency, per-key
// concurrency, per-key rate, FIFO groups) are enforced in Postgres by the
// delivery worker. Workflows are checkpointed functions run by @shiptiffin/sdk
// inside the app; see workflow.go.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/dnskit"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

func init() { platform.Register(&Module{}) }

// Module implements the queue module.
type Module struct {
	mu     sync.RWMutex
	eng    *Engine
	status string
	broken bool
	keys   Keys
}

func (*Module) Name() string { return "queue" }
func (*Module) Order() int   { return 30 }

// Kinds: the manifest's queues, topics and crons.
func (*Module) Kinds() []string {
	return []string{change.KindQueue, change.KindTopic, change.KindCron}
}

func (m *Module) engine() *Engine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eng
}

func (m *Module) setStatus(s string, broken bool) {
	m.mu.Lock()
	m.status, m.broken = s, broken
	m.mu.Unlock()
}

// Start connects in the background (Postgres may come up after us).
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	m.mu.Lock()
	m.keys = &kvKeys{db: p.DB}
	m.status = "starting"
	m.mu.Unlock()
	go m.connect(ctx, p)
	return nil
}

func (m *Module) dsn(ctx context.Context, p *platform.Platform) (string, error) {
	if v := os.Getenv("TIFFIN_QUEUE_DATABASE_URL"); v != "" {
		return v, nil
	}
	if sd, ok := findModule[SystemDatabases](); ok {
		return sd.SystemDatabase(ctx, p, "tiffin_queue")
	}
	return "", nil
}

func (m *Module) connect(ctx context.Context, p *platform.Platform) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		dsn, err := m.dsn(ctx, p)
		switch {
		case err != nil:
			m.setStatus("cannot get the queue database: "+err.Error(), true)
		case dsn == "":
			m.setStatus("waiting for the platform Postgres", false)
		default:
			e, err := Open(ctx, m.config(p, dsn))
			if err == nil {
				m.mu.Lock()
				m.eng, m.status, m.broken = e, "running", false
				m.mu.Unlock()
				e.Start(ctx)
				go m.serveApps(ctx, p, e)
				go func() {
					select {
					case <-e.Ready():
					case <-ctx.Done():
						return
					}
					// Queues, topics and crons that failed to reconcile before we connected.
					if projects, err := p.DB.ListProjects(ctx); err == nil {
						for _, pr := range projects {
							p.ReconcileProject(pr)
						}
					}
				}()
				<-ctx.Done()
				e.Close()
				return
			}
			m.setStatus("cannot open the queue database: "+err.Error(), true)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

// selfIPs are the box's own public addresses, which URL targets may not call.
func selfIPs(p *platform.Platform) func() []netip.Addr {
	return func() []netip.Addr {
		return append(slices.Clone(p.Reach.PublicIPs), dnskit.LocalPublicIPs()...)
	}
}

// urlRate is the cap on one project's calls to URLs outside the box per
// minute: TIFFIN_QUEUE_URL_RATE, default 600.
func urlRate() int {
	if n, err := strconv.Atoi(os.Getenv("TIFFIN_QUEUE_URL_RATE")); err == nil && n >= 0 {
		return n
	}
	return 600
}

// projectConcurrency is the cap on one project's deliveries in flight:
// TIFFIN_QUEUE_PROJECT_CONCURRENCY, default a quarter of the workers (50).
func projectConcurrency() int {
	n, _ := strconv.Atoi(os.Getenv("TIFFIN_QUEUE_PROJECT_CONCURRENCY"))
	return n
}

// CheckPlan refuses crons and queues whose url is plainly inside the box or
// a private network (an address literal or localhost); host names are
// checked against what they resolve to when each call is made.
func (m *Module) CheckPlan(ctx context.Context, p *platform.Platform, project string, desired map[string]change.Resource) error {
	g := &guard{allow: allowNets(), self: selfIPs(p)}
	addrs := make([]string, 0, len(desired))
	for a := range desired {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, a := range addrs {
		kind := change.Kind(a)
		if kind != change.KindCron && kind != change.KindQueue {
			continue
		}
		var t struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(desired[a].Spec, &t) != nil || t.URL == "" {
			continue
		}
		if err := g.checkURL(t.URL); err != nil {
			prob := api.NewProblem(422, "validation", kind+" "+change.Name(a)+": "+err.Error())
			prob.Hint = "point it at a public address (https://…), or at an app of the project with app and path"
			prob.Errors = append(prob.Errors, api.FieldError{Path: "/" + kind + "s/" + change.Name(a) + "/url", Message: err.Error()})
			return prob
		}
	}
	return nil
}

func (m *Module) config(p *platform.Platform, dsn string) Config {
	return Config{
		DSN:                dsn,
		Keys:               m.keys,
		PublicURL:          p.PublicURL,
		Log:                p.Log,
		AllowNets:          allowNets(),
		SelfIPs:            selfIPs(p),
		URLRatePerMinute:   urlRate(),
		ProjectConcurrency: projectConcurrency(),
		Endpoint: func(ctx context.Context, project, app, release string) (string, error) {
			if up, ok := findModule[AppUpstreams](); ok {
				return up.AppEndpoint(ctx, p, project, app, release)
			}
			return runtimeStateEndpoint(ctx, p, project, app, release)
		},
		CurrentRelease: func(ctx context.Context, project, app string) (string, error) {
			if up, ok := findModule[AppUpstreams](); ok {
				return up.CurrentRelease(ctx, p, project, app)
			}
			st, err := runtimeState(ctx, p, project, app)
			if err != nil || st == nil {
				return "", err
			}
			return st.Live, nil
		},
		Outbox: func(ctx context.Context) (map[string]string, error) { return projectDatabases(ctx, p) },
		AppEnv: func(ctx context.Context, project, app string) (map[string]string, error) {
			return p.ProjectEnv(ctx, project, app)
		},
	}
}

// projectDatabases maps each project that has the postgres service to its
// app DATABASE_URL (from the postgres module), for draining sendTx outboxes.
func projectDatabases(ctx context.Context, p *platform.Platform) (map[string]string, error) {
	out := map[string]string{}
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var pg platform.EnvProvider
	for _, mod := range platform.Modules() {
		if ep, ok := mod.(platform.EnvProvider); ok && mod.Name() == "postgres" {
			pg = ep
		}
	}
	if pg == nil {
		return out, nil
	}
	for _, pr := range projects {
		_, res, err := p.DB.Load(ctx, pr)
		if err != nil {
			continue
		}
		if _, ok := res[change.KindService+"/postgres"]; !ok {
			continue
		}
		app := ""
		for addr := range res {
			if change.Kind(addr) == change.KindApp {
				app = change.Name(addr)
				break
			}
		}
		env, err := pg.Env(ctx, p, pr, app)
		if err == nil && env["DATABASE_URL"] != "" {
			out[pr] = env["DATABASE_URL"]
		}
	}
	return out, nil
}

// Reconcile stores the manifest's queues, topics and crons; the engine runs
// them. While the engine is down it fails, so the platform marks the resource
// failed and converges again when the queue connects (see connect).
func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	e := m.engine()
	if e == nil {
		m.mu.RLock()
		st := m.status
		m.mu.RUnlock()
		return fmt.Errorf("the queue is not running yet (%s); queues, topics and crons are stored as soon as it is", st)
	}
	name := change.Name(address)
	switch change.Kind(address) {
	case change.KindQueue:
		return e.ReconcileQueue(ctx, project, name, spec)
	case change.KindTopic:
		return e.ReconcileTopic(ctx, project, name, spec)
	}
	return e.ReconcileCron(ctx, project, name, spec)
}

// SetAppCrons replaces the crons an app's own files declare (origin names
// the file, e.g. vercel.json), for the runtime. They are called with GET,
// as Vercel calls them; a manifest cron of the same name or app and path
// wins.
func (m *Module) SetAppCrons(ctx context.Context, p *platform.Platform, project, app, origin string, crons map[string]manifest.Cron) error {
	e := m.engine()
	if e == nil {
		return errors.New("the queue is not running yet")
	}
	return e.SetFileCrons(ctx, project, app, origin, crons)
}

var _ platform.ProjectStopper = (*Module)(nil)

// ProjectStopped holds a stopped project's jobs and crons, and lets them go
// when it starts (see stop.go).
func (m *Module) ProjectStopped(ctx context.Context, p *platform.Platform, project string, stopped bool) error {
	e := m.engine()
	if e == nil {
		return errors.New("the queue is not running yet; its jobs are held or let go as soon as it is")
	}
	return e.SetProjectStopped(ctx, project, stopped)
}

// Env gives apps what @shiptiffin/sdk needs to send jobs and verify deliveries.
func (m *Module) Env(ctx context.Context, p *platform.Platform, project, app string) (map[string]string, error) {
	m.mu.RLock()
	k := m.keys
	m.mu.RUnlock()
	if k == nil {
		k = &kvKeys{db: p.DB}
	}
	appKey, secret, err := k.Get(ctx, project)
	if err != nil {
		return nil, err
	}
	if app != "" {
		appKey = AppKey(appKey, project, app) // what it sends names it
	}
	return map[string]string{
		"TIFFIN_QUEUE_URL":            boxURLForApps(ctx, p),
		"TIFFIN_QUEUE_KEY":            appKey,
		"TIFFIN_QUEUE_SIGNING_SECRET": secret,
	}, nil
}

// AppPort is where the queue serves the app-facing endpoints (@shiptiffin/sdk's
// send, heartbeat and workflow calls): on 127.0.0.1 for the box itself and
// on the runtime's host IP, which app containers reach.
const AppPort = "7075"

// hostIP is the address app containers reach box services on (published by
// the runtime as KV runtime/host-ip; 127.0.0.1 until then).
func hostIP(ctx context.Context, p *platform.Platform) string {
	if p != nil && p.DB != nil {
		if raw, ok, _ := p.DB.KVGet(ctx, "runtime", "host-ip"); ok && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimSpace(string(raw))
		}
	}
	return "127.0.0.1"
}

func boxURLForApps(ctx context.Context, p *platform.Platform) string {
	if v := os.Getenv("TIFFIN_QUEUE_APP_URL"); v != "" {
		return v
	}
	return "http://" + net.JoinHostPort(hostIP(ctx, p), AppPort)
}

// serveApps listens for apps on loopback and, once the runtime publishes it,
// the host IP (re-checked every 10 s).
func (m *Module) serveApps(ctx context.Context, p *platform.Platform, e *Engine) {
	h := e.InternalHandler()
	open := map[string]*http.Server{}
	defer func() {
		for _, s := range open {
			_ = s.Close()
		}
	}()
	for {
		for _, ip := range []string{"127.0.0.1", hostIP(ctx, p)} {
			addr := net.JoinHostPort(ip, AppPort)
			if open[addr] != nil {
				continue
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				p.Log.Warn("queue: app listener", "addr", addr, "err", err)
				continue
			}
			srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
			open[addr] = srv
			go func() { _ = srv.Serve(ln) }()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// ServeLive streams a job or run to a browser (live.go); the runtime calls it
// for LivePath on every app host.
func (m *Module) ServeLive(w http.ResponseWriter, r *http.Request) {
	e := m.engine()
	if e == nil {
		w.Header().Set("Retry-After", "5")
		writeErr(w, &Error{Status: 503, Code: "precondition", Msg: "the queue is not running yet", Hint: "retry shortly"})
		return
	}
	e.ServeLive(w, r)
}

// Checks reports the queue's health.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	e := m.engine()
	m.mu.RLock()
	st, broken := m.status, m.broken
	m.mu.RUnlock()
	if e == nil {
		return []platform.Check{{Name: "queue", OK: !broken, Detail: st}}
	}
	var running, queued, dead int
	err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state = 'running'), count(*) FILTER (WHERE state = 'queued'),
		count(*) FILTER (WHERE state = 'dead') FROM tq_jobs WHERE state IN ('running', 'queued', 'dead')`).Scan(&running, &queued, &dead)
	if err != nil {
		return []platform.Check{{Name: "queue", OK: false, Detail: "queue database: " + err.Error()}}
	}
	select {
	case <-e.Ready():
	default:
		return []platform.Check{{Name: "queue", OK: true, Detail: "waiting for the single-owner lock (another tiffin process holds it)"}}
	}
	return []platform.Check{{Name: "queue", OK: true, Detail: fmt.Sprintf("%d running, %d queued, %d dead", running, queued, dead)}}
}

// PinnedReleases implements the runtime contract (contract.go). On error the
// runtime must keep old releases running.
func (m *Module) PinnedReleases(ctx context.Context, _ *platform.Platform, project, app string) ([]string, error) {
	e := m.engine()
	if e == nil {
		return nil, errors.New("the queue is not running yet")
	}
	return e.PinnedReleases(ctx, project, app)
}

// Delivering implements the runtime contract (contract.go): whether a job,
// cron tick or workflow turn is being delivered to the app right now.
func (m *Module) Delivering(ctx context.Context, _ *platform.Platform, project, app string) (bool, error) {
	e := m.engine()
	if e == nil {
		return false, errors.New("the queue is not running yet")
	}
	return e.Delivering(ctx, project, app)
}

// Until the runtime implements AppUpstreams, the queue reads the runtime's
// published app state (KV runtime/state, "project/app") read-only: the live
// deploy is the current release and its instances' localhost ports are the
// upstreams. Old releases are not kept running in this mode, so a pinned
// run whose release was replaced moves to the live one (on its timeline).
type runtimeAppState struct {
	Live      string `json:"live"`
	Stopped   bool   `json:"stopped"`
	Instances []struct {
		Port   int    `json:"port"`
		Deploy string `json:"deploy"`
	} `json:"instances"`
}

func runtimeState(ctx context.Context, p *platform.Platform, project, app string) (*runtimeAppState, error) {
	raw, ok, err := p.DB.KVGet(ctx, "runtime/state", project+"/"+app)
	if err != nil || !ok {
		return nil, err
	}
	var st runtimeAppState
	return &st, json.Unmarshal(raw, &st)
}

func runtimeStateEndpoint(ctx context.Context, p *platform.Platform, project, app, release string) (string, error) {
	st, err := runtimeState(ctx, p, project, app)
	if err != nil {
		return "", err
	}
	if st == nil || st.Stopped || st.Live == "" {
		return "", fmt.Errorf("app %s is not deployed (deploy it, or check `tiffin apps`)", app)
	}
	var ports []int
	for _, in := range st.Instances {
		if in.Port > 0 && (release == "" || in.Deploy == release) {
			ports = append(ports, in.Port)
		}
	}
	if len(ports) == 0 {
		if release != "" && release != st.Live {
			return "", fmt.Errorf("release %s: %w", release, ErrReleaseGone)
		}
		return "", fmt.Errorf("app %s has no running instance", app)
	}
	return "http://127.0.0.1:" + strconv.Itoa(ports[rand.IntN(len(ports))]), nil
}
