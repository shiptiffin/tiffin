package observe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/mod/observe/edgelog"
	"github.com/btahir/tiffin/internal/mod/observe/logtail"
	"github.com/btahir/tiffin/internal/platform"
)

// activityNoter is the runtime's: every request to an app's host counts as
// the app's activity (an app whose project lets it sleep sleeps after a
// while without any), those the box answers itself too.
type activityNoter interface {
	NoteActivity(project, app string, at time.Time)
}

// runtimeNoter finds the runtime once (modules register at init).
var runtimeNoter = sync.OnceValue(func() activityNoter {
	for _, mod := range platform.Modules() {
		if n, ok := mod.(activityNoter); ok {
			return n
		}
	}
	return nil
})

// noteActivity tells the runtime, if there is one, about a request to an app.
func noteActivity(project, app string, at time.Time) {
	if n := runtimeNoter(); n != nil {
		n.NoteActivity(project, app, at)
	}
}

// ---- journald: the box's own services, tiffin included ----

var priorities = []string{"emerg", "alert", "crit", "error", "warning", "notice", "info", "debug"}

// journalRecord converts one `journalctl -o json` line to a log record.
func journalRecord(line []byte) (map[string]any, string, bool) {
	var j map[string]any
	if err := json.Unmarshal(line, &j); err != nil {
		return nil, "", false
	}
	cursor, _ := j["__CURSOR"].(string)
	msg := journalString(j["MESSAGE"])
	if msg == "" {
		return nil, cursor, false
	}
	rec := map[string]any{"_msg": msg}
	if us, err := strconv.ParseInt(journalString(j["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
		rec["_time"] = time.UnixMicro(us).UTC().Format(time.RFC3339Nano)
	}
	unit := journalString(j["_SYSTEMD_UNIT"])
	if unit == "" {
		unit = journalString(j["SYSLOG_IDENTIFIER"])
	}
	if unit == "" {
		unit = "kernel"
	}
	rec["unit"] = unit
	if p, err := strconv.Atoi(journalString(j["PRIORITY"])); err == nil && p >= 0 && p < len(priorities) {
		rec["level"] = priorities[p]
	}
	if pid := journalString(j["_PID"]); pid != "" {
		rec["pid"] = pid
	}
	// tiffin and many services log JSON: lift level and msg so queries can use them.
	if strings.HasPrefix(msg, "{") {
		var inner map[string]any
		if json.Unmarshal([]byte(msg), &inner) == nil {
			if l, ok := inner["level"].(string); ok {
				rec["level"] = strings.ToLower(l)
			}
			for k, v := range inner {
				if k == "msg" || k == "time" || k == "level" || strings.HasPrefix(k, "_") {
					continue
				}
				if len(rec) < 40 {
					rec["f."+k] = fmt.Sprint(v)
				}
			}
			if m, ok := inner["msg"].(string); ok && m != "" {
				rec["_msg"] = m
			}
		}
	}
	return rec, cursor, true
}

func journalString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any: // non-UTF-8 messages come as byte arrays
		b := make([]byte, 0, len(x))
		for _, c := range x {
			if f, ok := c.(float64); ok {
				b = append(b, byte(f))
			}
		}
		return strings.ToValidUTF8(string(b), "?")
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// runJournal follows the journal and ships it to the box tenant, resuming
// after the last shipped entry across restarts.
func (m *Module) runJournal(ctx context.Context) {
	for ctx.Err() == nil {
		args := []string{"-o", "json", "--follow", "--no-pager",
			"--output-fields=MESSAGE,PRIORITY,_SYSTEMD_UNIT,SYSLOG_IDENTIFIER,_PID"}
		if c := m.store.Setting(ctx, "journal.cursor"); c != "" {
			args = append(args, "--after-cursor="+c)
		} else {
			args = append(args, "--lines=500")
		}
		cmd := exec.CommandContext(ctx, "journalctl", args...)
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			m.journalErr.Store(err.Error())
			sleepCtx(ctx, 10*time.Second)
			continue
		}
		m.journalErr.Store("")
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		var cursor string
		lastSave := time.Now()
		for sc.Scan() {
			rec, c, ok := journalRecord(sc.Bytes())
			if c != "" {
				cursor = c
			}
			if ok {
				m.batch.Add(0, "unit", rec)
			}
			if time.Since(lastSave) > 2*time.Second && cursor != "" {
				_ = m.store.SetSetting(ctx, "journal.cursor", cursor)
				lastSave = time.Now()
			}
		}
		if cursor != "" {
			_ = m.store.SetSetting(context.Background(), "journal.cursor", cursor)
		}
		_ = cmd.Wait()
		sleepCtx(ctx, 3*time.Second)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ---- app logs: /var/lib/tiffin/logs/apps/<project>/<app>/<env>/<deploy>.<instance>.log ----

// AppLog are the labels of one app log file, from its path under the apps
// dir. The runtime writes <project>/<app>/<env>/<deploy>.<instance>.log (env
// is prod or pr-<preview>); shallower layouts (<project>/<app>.log,
// <project>/<app>/<deploy>.log) work too. Build output goes to
// <project>/<app>/build/<deploy>.log (Build is set): "build" is never an
// env folder, which are prod or pr-<preview>.
type AppLog struct {
	Project, App, Env, Deploy, Instance string
	Build                               bool
}

// BuildLogDir is the folder under an app's log folder that holds its build
// output for the shipper, one <deploy>.log of JSON lines per deploy. The
// runtime writes it (see runtime.buildLog).
const BuildLogDir = "build"

func appLogLabels(root, path string) (AppLog, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || !strings.HasSuffix(rel, ".log") {
		return AppLog{}, false
	}
	parts := strings.Split(strings.TrimSuffix(rel, ".log"), string(filepath.Separator))
	switch len(parts) {
	case 2:
		return AppLog{Project: parts[0], App: parts[1]}, true
	case 3:
		return AppLog{Project: parts[0], App: parts[1], Deploy: parts[2]}, true
	case 4:
		if parts[2] == BuildLogDir {
			return AppLog{Project: parts[0], App: parts[1], Deploy: parts[3], Build: true}, true
		}
		l := AppLog{Project: parts[0], App: parts[1], Env: parts[2], Deploy: parts[3]}
		if d, n, ok := strings.Cut(parts[3], "."); ok {
			l.Deploy, l.Instance = d, n
		}
		return l, true
	}
	return AppLog{}, false
}

// appLogRecord turns one app log line into a record. JSON lines keep their
// fields (msg/message/level lifted); anything else is the message.
func appLogRecord(line []byte, l AppLog) map[string]any {
	rec := map[string]any{"app": l.App, "source": "app"}
	for k, v := range map[string]string{"deploy": l.Deploy, "env": l.Env, "instance": l.Instance} {
		if v != "" {
			rec[k] = v
		}
	}
	trim := bytes.TrimSpace(line)
	// Container json-file logs wrap each line: {"log":"...\n","stream":"stdout","time":"..."}.
	if len(trim) > 0 && trim[0] == '{' && bytes.Contains(trim, []byte(`"log"`)) && bytes.Contains(trim, []byte(`"stream"`)) {
		var d struct {
			Log    *string `json:"log"`
			Stream string  `json:"stream"`
			Time   string  `json:"time"`
		}
		if json.Unmarshal(trim, &d) == nil && d.Log != nil {
			inner := appLogRecord([]byte(strings.TrimRight(*d.Log, "\r\n")), l)
			if d.Stream != "" {
				inner["stream"] = d.Stream
			}
			if _, ok := inner["_time"]; !ok {
				if t, err := time.Parse(time.RFC3339Nano, d.Time); err == nil {
					inner["_time"] = t.UTC().Format(time.RFC3339Nano)
				}
			}
			return inner
		}
	}
	if len(trim) > 0 && trim[0] == '{' {
		var j map[string]any
		if json.Unmarshal(trim, &j) == nil {
			for k, v := range j {
				switch k {
				case "msg", "message":
					rec["_msg"] = fmt.Sprint(v)
				case "level", "severity", "lvl":
					rec["level"] = jsonLevel(v)
				case "time", "timestamp", "ts", "_time":
					if s, ok := v.(string); ok {
						if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
							rec["_time"] = t.UTC().Format(time.RFC3339Nano)
						}
					}
				case "app", "deploy", "env", "instance", "project", "source", "_msg", "_stream":
					// labels come from the path, never from the app
				default:
					if len(rec) < 50 {
						if s, ok := v.(string); ok {
							rec[k] = s
						} else {
							b, _ := json.Marshal(v)
							rec[k] = string(b)
						}
					}
				}
			}
			if _, ok := rec["_msg"]; !ok {
				rec["_msg"] = string(trim)
			}
			return withLevel(rec)
		}
	}
	rec["_msg"] = string(line)
	return withLevel(rec)
}

// jsonLevel is a JSON line's level field: lowercased as written, with
// pino's numbers (30 info, 50 error, …) named.
func jsonLevel(v any) string {
	s := strings.ToLower(fmt.Sprint(v))
	if _, err := strconv.Atoi(s); err == nil {
		return levelName(s)
	}
	return s
}

// withLevel infers a level from the message when the line has no level field.
func withLevel(rec map[string]any) map[string]any {
	if l, _ := rec["level"].(string); l != "" {
		return rec
	}
	msg, _ := rec["_msg"].(string)
	if l := inferLevel(msg); l != "" {
		rec["level"] = l
	}
	return rec
}

// runAppLogs discovers app log files and tails each one.
func (m *Module) runAppLogs(ctx context.Context, root string) {
	running := map[string]context.CancelFunc{}
	defer func() {
		for _, c := range running {
			c()
		}
	}()
	for ctx.Err() == nil {
		var paths []string
		for _, pat := range []string{"*/*.log", "*/*/*.log", "*/*/*/*.log"} {
			got, _ := filepath.Glob(filepath.Join(root, pat))
			paths = append(paths, got...)
		}
		want := map[string]bool{}
		for _, path := range paths {
			l, ok := appLogLabels(root, path)
			if !ok {
				continue
			}
			want[path] = true
			if running[path] != nil {
				continue
			}
			tctx, cancel := context.WithCancel(ctx)
			running[path] = cancel
			path := path
			tl := &logtail.Tailer{Path: path, FromStart: true,
				Load: func() (logtail.Position, bool) { return m.store.loadPos(path) },
				Save: func(p logtail.Position) { m.store.savePos(path, p) },
				Line: func(line []byte) {
					t, err := m.store.TenantFor(ctx, l.Project)
					if err != nil {
						return
					}
					if l.Build {
						m.batch.Add(t, "source,app,env", buildLogRecord(line, l))
						return
					}
					m.batch.Add(t, "source,app,env", appLogRecord(line, l))
				}}
			go tl.Run(tctx)
		}
		for path, cancel := range running {
			if !want[path] {
				cancel()
				delete(running, path)
				// Pruned for good (a rotated file reappears under a new
				// inode anyway): forget where its tail stopped.
				if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
					m.store.deletePos(path)
				}
			}
		}
		sleepCtx(ctx, 5*time.Second)
	}
}

// ---- edge access log: request metrics and logs ----

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type reqKey struct{ project, app, host, code string }
type histKey struct{ project, app string }

type histogram struct {
	counts []float64 // cumulative per bucket
	sum    float64
	count  float64
}

// RED counts requests, errors and latency per app from the access log.
type RED struct {
	mu   sync.Mutex
	reqs map[reqKey]float64
	hist map[histKey]*histogram
}

// Observe records one request.
func (r *RED) Observe(project, app, host string, status int, seconds float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reqs == nil {
		r.reqs, r.hist = map[reqKey]float64{}, map[histKey]*histogram{}
	}
	r.reqs[reqKey{project, app, host, strconv.Itoa(status/100) + "xx"}]++
	hk := histKey{project, app}
	h := r.hist[hk]
	if h == nil {
		h = &histogram{counts: make([]float64, len(latencyBuckets))}
		r.hist[hk] = h
	}
	for i, b := range latencyBuckets {
		if seconds <= b {
			h.counts[i]++
		}
	}
	h.sum += seconds
	h.count++
}

// Prometheus renders the counters.
func (r *RED) Prometheus(w *bytes.Buffer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]reqKey, 0, len(r.reqs))
	for k := range r.reqs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		fmt.Fprintf(w, "tiffin_http_requests_total{project=%q,app=%q,host=%q,code=%q} %g\n", k.project, k.app, k.host, k.code, r.reqs[k])
	}
	for k, h := range r.hist {
		for i, b := range latencyBuckets {
			fmt.Fprintf(w, "tiffin_http_request_duration_seconds_bucket{project=%q,app=%q,le=%q} %g\n", k.project, k.app, strconv.FormatFloat(b, 'g', -1, 64), h.counts[i])
		}
		fmt.Fprintf(w, "tiffin_http_request_duration_seconds_bucket{project=%q,app=%q,le=\"+Inf\"} %g\n", k.project, k.app, h.count)
		fmt.Fprintf(w, "tiffin_http_request_duration_seconds_sum{project=%q,app=%q} %g\n", k.project, k.app, h.sum)
		fmt.Fprintf(w, "tiffin_http_request_duration_seconds_count{project=%q,app=%q} %g\n", k.project, k.app, h.count)
	}
}

// handleAccess processes one access log line: request metrics plus a log
// record in the serving project's tenant (the box tenant for platform hosts).
func (m *Module) handleAccess(ctx context.Context, line []byte) {
	e, ok := edgelog.Parse(line)
	if !ok {
		return
	}
	path, _, _ := strings.Cut(e.URI, "?")
	site, mapped := m.sites.Lookup(ctx, e.Host, path)
	if !strings.HasPrefix(path, "/_tiffin/") { // the box answers these, not the app
		m.red.Observe(site.Project, site.App, e.Host, e.Status, e.Duration)
	}
	rec := map[string]any{
		"_msg":        fmt.Sprintf("%s %s %d %.0fms", e.Method, path, e.Status, e.Duration*1000),
		"_time":       e.Time.Format(time.RFC3339Nano),
		"host":        e.Host,
		"method":      e.Method,
		"path":        path,
		"status":      strconv.Itoa(e.Status),
		"duration_ms": strconv.FormatFloat(e.Duration*1000, 'f', 1, 64),
		"size":        strconv.FormatInt(e.Size, 10),
		"user_agent":  e.Header("User-Agent"),
		"client_ip":   e.ClientIP,
		"source":      "edge",
	}
	if id := strings.ReplaceAll(e.RequestID, "-", ""); id != "" {
		rec["trace_id"] = id // tiffin traces get <id> opens the request's trace, when the app sent one
	}
	if e.Status >= 500 {
		rec["level"] = "error"
	} else if e.Status >= 400 {
		rec["level"] = "warning"
	} else {
		rec["level"] = "info"
	}
	var t Tenant
	stream := "source,host"
	if mapped {
		noteActivity(site.Project, site.App, e.Time)
		rec["app"] = site.App
		stream = "source,app"
		var err error
		if t, err = m.store.TenantFor(ctx, site.Project); err != nil {
			return
		}
	}
	m.batch.Add(t, stream, rec)
}
