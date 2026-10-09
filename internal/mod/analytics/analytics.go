// Package analytics is the Tiffin analytics module: first-party,
// cookieless web analytics for every app on the box.
//
// Pageviews come from the edge's access log, so they need no JavaScript and
// cannot be blocked. The ~1.6 KB tracker (t.<domain>/script.js) adds
// single-page navigations, custom events, outbound clicks and downloads;
// server code adds events with @shiptiffin/sdk/analytics track(). Visitors are a
// daily-salted hash of project, IP and user agent: no cookies, no storage,
// and raw IPs and user agents are never stored.
//
// Storage today: one SQLite file (/var/lib/tiffin/analytics/analytics.db)
// with raw events and daily rollups, behind the Store interface; realtime
// is an in-memory minute ring rebuilt from the store on start.
package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/mod/analytics/enrich"
	"github.com/shiptiffin/tiffin/internal/mod/backup"
	"github.com/shiptiffin/tiffin/internal/mod/observe"
	"github.com/shiptiffin/tiffin/internal/mod/observe/edgelog"
	"github.com/shiptiffin/tiffin/internal/mod/observe/logtail"
	"github.com/shiptiffin/tiffin/internal/platform"
)

func init() {
	platform.Register(&Module{})
	backup.Include("analytics", dataDir)
}

// Module implements the analytics module.
type Module struct {
	p       *platform.Platform
	store   Store
	pipe    *Pipeline
	rt      *Realtime
	sites   *edgelog.Sites
	geo     *enrich.Geo
	vit     *Vitals
	started atomic.Bool
	listErr atomic.Value
}

func (*Module) Name() string { return "analytics" }
func (*Module) Order() int   { return 35 }

// DB-IP Lite country database (CC-BY-4.0, "IP Geolocation by DB-IP",
// https://db-ip.com), pinned through the immutable npm package that
// republishes it monthly. Bump version and checksum together.
const (
	geoVersion = "2.3.2026060120"
	geoURL     = "https://registry.npmjs.org/@ip-location-db/dbip-country-mmdb/-/dbip-country-mmdb-" + geoVersion + ".tgz"
	geoSHA     = "5340f28d08f4e666473e0c5dc1c40d9db457ad00603d1f725fb4d8621c624239"
	dataDir    = "/var/lib/tiffin/analytics"
)

func geoPath(root string) string {
	return filepath.Join(root, "analytics", "dbip-country-"+geoVersion+".mmdb")
}

// Provision downloads the pinned country database. Analytics works without
// it (countries show as unknown), so a failed download is reported, not fatal.
func (m *Module) Provision(ctx context.Context, s *platform.System) error {
	dest := geoPath("/var/lib/tiffin")
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}
	archive, err := s.Fetch(ctx, geoURL, geoSHA)
	if err != nil {
		s.Log("analytics: country database not downloaded (countries will show as unknown): " + err.Error())
		return nil
	}
	tmp, err := os.MkdirTemp(s.CacheDir, "dbip-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := s.Untar(ctx, archive, tmp, 1); err != nil {
		return err
	}
	return os.Rename(filepath.Join(tmp, "dbip-country.mmdb"), dest)
}

// Start opens the store, restores realtime and starts the collector, the
// edge log reader and the rollup/retention loops.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	root := p.DataRoot
	if root == "" {
		root = "/var/lib/tiffin"
	}
	st, err := OpenSQLite(filepath.Join(root, "analytics", "analytics.db"))
	if err != nil {
		return err
	}
	geo, _ := enrich.OpenGeo(geoPath(root))
	m.setup(p, st, geo)
	if geo.Loaded() { // "New sign-in" emails name the country a sign-in came from
		api.SetLocator(func(ip string) string { return enrich.CountryName(geo.Country(ip)) })
	}
	if err := m.pipe.Restore(ctx); err != nil {
		p.Log.Error("analytics restore", "err", err)
	}
	m.listErr.Store("")
	for _, addr := range observe.ListenAddrs(ctx, p, CollectorPort) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			m.listErr.Store(err.Error())
			p.Log.Error("analytics collector listen", "addr", addr, "err", err)
			continue
		}
		srv := &http.Server{Handler: m.collectorHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		go func() { _ = srv.Serve(ln) }()
		go func() { <-ctx.Done(); _ = srv.Close() }()
	}
	go m.pipe.Run(ctx)
	go m.vit.Run(ctx)
	tl := &logtail.Tailer{Path: filepath.Join(root, "logs", "access.log"), FromStart: true,
		Load: func() (logtail.Position, bool) {
			b, ok, err := p.DB.KVGet(ctx, "analytics", "access-log")
			var pos logtail.Position
			if err != nil || !ok || json.Unmarshal(b, &pos) != nil {
				return pos, false
			}
			return pos, true
		},
		Save: func(pos logtail.Position) {
			b, _ := json.Marshal(pos)
			_ = p.DB.KVPut(ctx, "analytics", "access-log", b)
		},
		Line: func(line []byte) { m.handleEdge(ctx, line) }}
	go tl.Run(ctx)
	go m.retentionLoop(ctx)
	m.started.Store(true)
	return nil
}

func (m *Module) setup(p *platform.Platform, st Store, geo *enrich.Geo) {
	m.p, m.store, m.geo = p, st, geo
	m.rt = &Realtime{}
	m.sites = &edgelog.Sites{DB: p.DB, Domain: p.AppsDomain()}
	m.pipe = &Pipeline{Store: st, Bots: enrich.NewBots(), Agents: enrich.NewAgents(), Geo: geo, RT: m.rt, Own: m.ownPage}
	m.vit = &Vitals{Store: st}
}

// retentionLoop deletes raw events past each project's retention daily.
func (m *Module) retentionLoop(ctx context.Context) {
	for {
		m.applyRetention(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

func (m *Module) applyRetention(ctx context.Context) {
	_ = m.store.ForgetSalts(ctx, time.Now().Add(-48*time.Hour))
	projects, err := m.p.DB.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, pr := range projects {
		if spec, ok := m.spec(ctx, pr); ok {
			_, _ = m.store.Purge(ctx, pr, time.Now().AddDate(0, 0, -spec.RetentionDays))
		}
	}
}

type serviceSpec struct {
	RetentionDays int `json:"retentionDays"`
}

func (m *Module) spec(ctx context.Context, project string) (serviceSpec, bool) {
	_, res, err := m.p.DB.Load(ctx, project)
	if err != nil {
		return serviceSpec{}, false
	}
	r, ok := res[change.KindService+"/analytics"]
	if !ok {
		return serviceSpec{}, false
	}
	var s serviceSpec
	_ = json.Unmarshal(r.Spec, &s)
	if s.RetentionDays <= 0 {
		s.RetentionDays = 365
	}
	return s, true
}

// Kinds: analytics is enabled per project with services.analytics.
func (*Module) Kinds() []string { return []string{"service/analytics"} }

// Reconcile enables analytics (and applies retention) or, when the service
// is removed, deletes the project's analytics data.
func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	if m.store == nil {
		return errors.New("analytics is not running on this server")
	}
	m.sites.Invalidate()
	if spec == nil {
		return m.deleteProject(ctx, project)
	}
	m.pipe.Revive(project)
	m.vit.Revive(project)
	var s serviceSpec
	if err := json.Unmarshal(spec, &s); err != nil {
		return fmt.Errorf("analytics spec: %w", err)
	}
	if s.RetentionDays <= 0 {
		s.RetentionDays = 365
	}
	_, err := m.store.Purge(ctx, project, time.Now().AddDate(0, 0, -s.RetentionDays))
	return err
}

// deleteProject deletes a project's analytics data, after dropping what
// is pending for it: a buffered event or a flush in flight must not write
// it back.
func (m *Module) deleteProject(ctx context.Context, project string) error {
	if m.pipe != nil {
		m.pipe.Forget(project)
	}
	if m.vit != nil {
		m.vit.Forget(project)
	}
	return m.store.DeleteProject(ctx, project)
}

// Routes publishes the collector (script, beacons) at t.<domain>, and
// VitalsPath on every app host.
func (m *Module) Routes(ctx context.Context, p *platform.Platform) ([]edge.Route, error) {
	return []edge.Route{{Host: p.Host("t"), Upstream: CollectorAddr}, {PathPrefix: VitalsPath, Upstream: CollectorAddr}}, nil
}

// ScriptURL is the public URL of the tracker.
func ScriptURL(p *platform.Platform) string { return p.URL(p.Host("t")) + "/script.js" }

// Env gives apps of projects with analytics the track() endpoint and key.
func (m *Module) Env(ctx context.Context, p *platform.Platform, project, app string) (map[string]string, error) {
	if m.store == nil {
		return nil, nil
	}
	if _, ok := m.spec(ctx, project); !ok {
		return nil, nil
	}
	key, err := m.store.Key(ctx, project, app)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"TIFFIN_ANALYTICS_URL":    "http://" + net.JoinHostPort(observe.AppHost(ctx, p), CollectorPort),
		"TIFFIN_ANALYTICS_KEY":    key,
		"TIFFIN_ANALYTICS_SCRIPT": ScriptURL(p),
	}, nil
}

// Checks reports the collector and the country database.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	if !m.started.Load() {
		return nil
	}
	out := []platform.Check{}
	if e, _ := m.listErr.Load().(string); e != "" {
		out = append(out, platform.Check{Name: "analytics.collector", OK: false, Detail: e})
	} else {
		st := m.pipe.Stats()
		out = append(out, platform.Check{Name: "analytics.collector", OK: st.Failed == 0,
			Detail: fmt.Sprintf("%d events accepted, %d bots dropped, %d not counted for Global Privacy Control, %d lost since start", st.Accepted, st.Bots, st.OptedOut, st.Failed)})
	}
	if m.geo.Loaded() {
		out = append(out, platform.Check{Name: "analytics.geoip", OK: true, Detail: "DB-IP Lite " + geoVersion})
	} else {
		out = append(out, platform.Check{Name: "analytics.geoip", OK: true, Detail: "no country database: countries show as unknown (re-run tiffin up to download it)"})
	}
	return out
}
