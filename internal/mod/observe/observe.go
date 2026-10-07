// Package observe is the Tiffin observe module: box metrics, logs (the
// box's services, apps and the edge), OTLP ingest, request traces,
// Sentry-compatible error tracking and alerts. Metrics live in
// VictoriaMetrics and logs in VictoriaLogs (both Apache-2.0, pinned, on
// loopback); issues and alerts live in observe's own SQLite file, sampled
// traces in another (traces.db).
package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/mod/backup"
	"github.com/btahir/tiffin/internal/mod/observe/edgelog"
	"github.com/btahir/tiffin/internal/mod/observe/logtail"
	"github.com/btahir/tiffin/internal/platform"
)

func init() {
	platform.Register(&Module{})
	// Issues, alert history and settings go into every backup set. The
	// metric and log stores are not backed up: they are rebuilt from now on.
	backup.Include("observe", DataDir+"/observe.db")
}

// Module implements the observe module.
type Module struct {
	p         *platform.Platform
	store     *Store
	vic       *Victoria
	batch     *LogBatcher
	sites     *edgelog.Sites
	collector *Collector
	alerter   *Alerter
	red       *RED
	traces    *TraceStore
	sampler   *Sampler
	history   historyCache

	pushErr    atomic.Value // string: last metrics push error
	journalErr atomic.Value // string
	ingestErr  atomic.Value // string
	started    atomic.Bool
}

func (*Module) Name() string { return "observe" }
func (*Module) Order() int   { return 15 }

// Hosts the edge publishes for ingest.
func errorsHost(p *platform.Platform) string { return p.Host("errors") }
func otelHost(p *platform.Platform) string   { return p.Host("otel") }

// Start opens the store and runs the collectors, shippers, ingest server
// and alert loop.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	root := p.DataRoot
	if root == "" {
		root = "/var/lib/tiffin"
	}
	if err := m.setup(ctx, p, root, &Victoria{VM: "http://" + VMAddr, VL: "http://" + VLAddr, Password: victoriaPassword()}); err != nil {
		return err
	}
	for _, addr := range ListenAddrs(ctx, p, IngestPort) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			m.ingestErr.Store(err.Error())
			p.Log.Error("observe ingest listen", "addr", addr, "err", err)
			continue
		}
		srv := &http.Server{Handler: m.ingestHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		go func() { _ = srv.Serve(ln) }()
		go func() { <-ctx.Done(); _ = srv.Close() }()
	}
	go m.batch.Run(ctx)
	go m.metricsLoop(ctx)
	go m.alertLoop(ctx)
	go m.traceLoop(ctx)
	go m.runJournal(ctx)
	go m.runAppLogs(ctx, filepath.Join(root, "logs", "apps"))
	go m.accessTailer(ctx, filepath.Join(root, "logs", "access.log")).Run(ctx)
	m.started.Store(true)
	return nil
}

// setup wires the module without starting loops (tests drive the parts).
func (m *Module) setup(ctx context.Context, p *platform.Platform, root string, vic *Victoria) error {
	m.p = p
	st, err := OpenStore(filepath.Join(root, "observe", "observe.db"))
	if err != nil {
		return err
	}
	m.store = st
	if m.traces, err = OpenTraceStore(filepath.Join(root, "observe", "traces.db")); err != nil {
		return err
	}
	m.sampler = NewSampler(m.traceConfig(ctx).SampleRate, DefaultTraceSlow)
	m.vic = vic
	m.batch = &LogBatcher{V: m.vic}
	m.sites = &edgelog.Sites{DB: p.DB, Domain: p.AppsDomain()}
	m.collector = &Collector{Mounts: []string{"/", root}}
	m.red = &RED{}
	m.alerter = &Alerter{Store: st, Collector: m.collector, Victoria: m.vic, Platform: p,
		CertDir: filepath.Join(p.Home, "edge"), BackupDir: filepath.Join(root, "backups"), Box: p.Domain}
	for _, v := range []*atomic.Value{&m.pushErr, &m.journalErr, &m.ingestErr} {
		v.Store("")
	}
	return st.EnsureDefaultRules(ctx)
}

func (m *Module) accessTailer(ctx context.Context, path string) *logtail.Tailer {
	return &logtail.Tailer{Path: path, FromStart: true,
		Load: func() (logtail.Position, bool) { return m.store.loadPos("observe:access") },
		Save: func(pos logtail.Position) { m.store.savePos("observe:access", pos) },
		Line: func(line []byte) { m.handleAccess(ctx, line) }}
}

// pushMetrics samples the box and pushes everything to VictoriaMetrics.
func (m *Module) pushMetrics(ctx context.Context) error {
	snap := m.collector.Sample(ctx)
	m.labelContainers(ctx, snap)
	var buf bytes.Buffer
	snap.Prometheus(&buf)
	m.red.Prometheus(&buf)
	for _, mod := range platform.Modules() {
		if r, ok := mod.(platform.MetricsReporter); ok {
			r.Metrics(ctx, &buf)
		}
	}
	sent, dropped := m.batch.Stats()
	fmt.Fprintf(&buf, "tiffin_observe_logs_shipped_total %d\ntiffin_observe_logs_dropped_total %d\n", sent, dropped)
	err := m.vic.PushPrometheus(ctx, buf.Bytes())
	if err != nil {
		m.pushErr.Store(err.Error())
	} else {
		m.pushErr.Store("")
	}
	return err
}

func (m *Module) metricsLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		_ = m.pushMetrics(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Module) alertLoop(ctx context.Context) {
	// Give the collector one sample first.
	sleepCtx(ctx, 5*time.Second)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		if err := m.alerter.Evaluate(ctx); err != nil && ctx.Err() == nil {
			m.p.Log.Error("alerts", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Routes publishes the ingest endpoints for browsers and outside senders.
// Apps on the box use the loopback address directly (see Env).
func (m *Module) Routes(ctx context.Context, p *platform.Platform) ([]edge.Route, error) {
	return []edge.Route{
		{Host: errorsHost(p), Upstream: IngestAddr},
		{Host: otelHost(p), Upstream: IngestAddr},
	}, nil
}

// Env gives every app its error and telemetry endpoints. Apps run with host
// networking, so they reach the ingest server on loopback.
func (m *Module) Env(ctx context.Context, p *platform.Platform, project, app string) (map[string]string, error) {
	if m.store == nil {
		return nil, nil
	}
	k, err := m.store.KeyFor(ctx, project, app)
	if err != nil {
		return nil, err
	}
	id := strconv.FormatInt(k.ID, 10)
	internal := net.JoinHostPort(AppHost(ctx, p), IngestPort)
	public := p.URL(errorsHost(p))
	pubHost := public[len("https://"):]
	return map[string]string{
		"SENTRY_DSN":                  "http://" + k.Key + "@" + internal + "/" + id,
		"TIFFIN_PUBLIC_SENTRY_DSN":    "https://" + k.Key + "@" + pubHost + "/" + id,
		"SENTRY_ENVIRONMENT":          "production",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://" + IngestAddr,
		"OTEL_EXPORTER_OTLP_HEADERS":  "x-tiffin-key=" + k.Key,
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_TRACES_EXPORTER":        "otlp",
		"OTEL_SERVICE_NAME":           app,
		"OTEL_RESOURCE_ATTRIBUTES":    "service.namespace=" + project + ",deployment.environment=production",
		"TIFFIN_OTLP_PUBLIC_ENDPOINT": p.URL(otelHost(p)),
	}, nil
}

// Checks reports the stores, the shippers and the ingest server.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	if !m.started.Load() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out := []platform.Check{}
	add := func(name string, err error, okDetail string) {
		if err != nil {
			out = append(out, platform.Check{Name: name, OK: false, Detail: err.Error()})
		} else {
			out = append(out, platform.Check{Name: name, OK: true, Detail: okDetail})
		}
	}
	add("observe.metrics", m.vic.Healthy(ctx, m.vic.VM), "VictoriaMetrics "+vmVersion+" on "+VMAddr)
	add("observe.logs", m.vic.Healthy(ctx, m.vic.VL), "VictoriaLogs "+vlVersion+" on "+VLAddr)
	add("observe.ingest", errString(m.ingestErr.Load()), "errors and OTLP on "+IngestAddr)
	if e := errString(m.pushErr.Load()); e != nil {
		add("observe.push", fmt.Errorf("pushing box metrics: %w", e), "")
	}
	if e := errString(m.journalErr.Load()); e != nil {
		add("observe.journal", fmt.Errorf("following the journal: %w", e), "")
	}
	return out
}

// AppHost is the address app containers reach box services on: the
// runtime publishes it in KV runtime/host-ip when apps are not on the host
// network; otherwise loopback.
func AppHost(ctx context.Context, p *platform.Platform) string {
	if p != nil && p.DB != nil {
		if raw, ok, _ := p.DB.KVGet(ctx, "runtime", "host-ip"); ok {
			if ip := strings.TrimSpace(string(raw)); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return "127.0.0.1"
}

// ListenAddrs are loopback plus the app host address when it differs.
func ListenAddrs(ctx context.Context, p *platform.Platform, port string) []string {
	out := []string{net.JoinHostPort("127.0.0.1", port)}
	if h := AppHost(ctx, p); h != "127.0.0.1" {
		out = append(out, net.JoinHostPort(h, port))
	}
	return out
}

func errString(v any) error {
	if s, _ := v.(string); s != "" {
		return errors.New(s)
	}
	return nil
}
