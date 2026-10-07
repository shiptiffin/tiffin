package observe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
)

// Pinned upstream releases (Apache-2.0). Bump version and both checksums
// together; checksums come from the GitHub release asset digests.
const (
	vmVersion = "v1.153.0"
	vlVersion = "v1.53.0"
)

var (
	vmSHA = map[string]string{
		"arm64": "3d5f965b7a75f713742a7f37195c1d845f47d9dc9d096a2cce29e693f08fd641",
		"amd64": "1b495bde563825cf83dc7c0425a9d8fa03e7214858e0949f9177efc6bb1f8bfc",
	}
	vlSHA = map[string]string{
		"arm64": "301b7ef12ff9fac7f70b155191affe07ac6212755514174461d775548699ef96",
		"amd64": "55feba89713cafa952673f91b8b38e7701bb0290989263da671b47a0de16edd3",
	}
)

// Local addresses. Both stores listen on loopback only; the API is the only
// way in from outside. Apps share the host's network, so loopback alone
// does not keep them out: both stores also want a password (authFile).
const (
	VMAddr = "127.0.0.1:8428"
	VLAddr = "127.0.0.1:9428"
)

// Paths on the data disk.
const (
	DataDir      = "/var/lib/tiffin/observe"
	binDir       = "/var/lib/tiffin/observe/bin"
	settingsFile = "/var/lib/tiffin/observe/retention.env"
	// authFile holds the stores' password (root only), as environment
	// variables they read with -envflag.enable.
	authFile   = "/var/lib/tiffin/observe/auth.env"
	AppLogsDir = "/var/lib/tiffin/logs/apps"
)

// Default retention, overridable with the observe-settings-set operation.
const (
	DefaultMetricsRetention = "30d"
	DefaultLogsRetention    = "30d"
)

func vmURL(v string) string {
	return fmt.Sprintf("https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/%s/victoria-metrics-linux-%s-%s.tar.gz", v, runtime.GOARCH, v)
}

func vlURL(v string) string {
	return fmt.Sprintf("https://github.com/VictoriaMetrics/VictoriaLogs/releases/download/%s/victoria-logs-linux-%s-%s.tar.gz", v, runtime.GOARCH, v)
}

const vmUnit = `[Unit]
Description=Tiffin metrics store (VictoriaMetrics %s)
After=network.target local-fs.target

[Service]
User=tiffin-observe
Group=tiffin-observe
Environment=METRICS_RETENTION=30d
EnvironmentFile=-` + settingsFile + `
EnvironmentFile=` + authFile + `
ExecStart=` + binDir + `/victoria-metrics-%s -envflag.enable -storageDataPath=` + DataDir + `/metrics -retentionPeriod=${METRICS_RETENTION} -httpListenAddr=` + VMAddr + ` -search.latencyOffset=5s -memory.allowedPercent=20 -loggerLevel=WARN
Restart=always
RestartSec=2
LimitNOFILE=65536
MemoryHigh=25%%

[Install]
WantedBy=multi-user.target
`

const vlUnit = `[Unit]
Description=Tiffin log store (VictoriaLogs %s)
After=network.target local-fs.target

[Service]
User=tiffin-observe
Group=tiffin-observe
Environment=LOGS_RETENTION=30d
EnvironmentFile=-` + settingsFile + `
EnvironmentFile=` + authFile + `
ExecStart=` + binDir + `/victoria-logs-%s -envflag.enable -storageDataPath=` + DataDir + `/logs -retentionPeriod=${LOGS_RETENTION} -retention.maxDiskUsagePercent=80 -httpListenAddr=` + VLAddr + ` -memory.allowedPercent=15 -loggerLevel=WARN
Restart=always
RestartSec=2
LimitNOFILE=65536
MemoryHigh=20%%

[Install]
WantedBy=multi-user.target
`

// Provision installs VictoriaMetrics and VictoriaLogs as systemd units.
func (m *Module) Provision(ctx context.Context, s *platform.System) error {
	arch := runtime.GOARCH
	if vmSHA[arch] == "" {
		return fmt.Errorf("observe: no pinned VictoriaMetrics build for %s", arch)
	}
	if err := s.User(ctx, "tiffin-observe", DataDir); err != nil {
		return err
	}
	for _, d := range []string{binDir, DataDir + "/metrics", DataDir + "/logs", AppLogsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if _, err := s.Run(ctx, "chown", "tiffin-observe:tiffin-observe", DataDir+"/metrics", DataDir+"/logs"); err != nil {
		return err
	}
	if _, err := os.Stat(authFile); err != nil {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		env := "httpAuth_username=" + authUser + "\nhttpAuth_password=" + hex.EncodeToString(b) + "\n"
		if err := os.WriteFile(authFile, []byte(env), 0o600); err != nil {
			return err
		}
	}
	install := func(url, sum, member, dest string) error {
		if fi, err := os.Stat(dest); err == nil && fi.Mode().IsRegular() {
			return nil
		}
		archive, err := s.Fetch(ctx, url, sum)
		if err != nil {
			return err
		}
		tmp, err := os.MkdirTemp(s.CacheDir, "observe-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err := s.Untar(ctx, archive, tmp, 0); err != nil {
			return err
		}
		return os.Rename(filepath.Join(tmp, member), dest)
	}
	if err := install(vmURL(vmVersion), vmSHA[arch], "victoria-metrics-prod", filepath.Join(binDir, "victoria-metrics-"+vmVersion)); err != nil {
		return fmt.Errorf("install VictoriaMetrics: %w", err)
	}
	if err := install(vlURL(vlVersion), vlSHA[arch], "victoria-logs-prod", filepath.Join(binDir, "victoria-logs-"+vlVersion)); err != nil {
		return fmt.Errorf("install VictoriaLogs: %w", err)
	}
	if err := s.Unit(ctx, "tiffin-metrics.service", fmt.Sprintf(vmUnit, vmVersion, vmVersion)); err != nil {
		return err
	}
	if err := s.Unit(ctx, "tiffin-logs.service", fmt.Sprintf(vlUnit, vlVersion, vlVersion)); err != nil {
		return err
	}
	if err := s.WaitTCP(ctx, VMAddr, 30*time.Second); err != nil {
		return fmt.Errorf("VictoriaMetrics did not start: %w", err)
	}
	return s.WaitTCP(ctx, VLAddr, 30*time.Second)
}

// ---- clients ----

var httpc = &http.Client{Timeout: 30 * time.Second}

// doRetry sends req, retrying for a few seconds while the store refuses
// connections (it restarts when its retention settings change).
func doRetry(req *http.Request) (*http.Response, error) {
	var err error
	for attempt := 0; attempt < 16; attempt++ {
		if attempt > 0 {
			if req.GetBody != nil {
				body, berr := req.GetBody()
				if berr != nil {
					return nil, berr
				}
				req.Body = body
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		var res *http.Response
		res, err = httpc.Do(req)
		if err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
			return res, err
		}
	}
	return nil, err
}

// Victoria talks to the local stores.
type Victoria struct {
	VM, VL   string // base URLs, e.g. http://127.0.0.1:8428
	Password string // the stores' password ("" for none)
}

const authUser = "tiffin"

// victoriaPassword reads the password Provision gave the stores.
func victoriaPassword() string {
	raw, _ := os.ReadFile(authFile)
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "httpAuth_password="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// newReq is a request to one of the stores, with their password.
func (v *Victoria) newReq(ctx context.Context, method, url string, body io.Reader) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, method, url, body)
	if v.Password != "" {
		req.SetBasicAuth(authUser, v.Password)
	}
	return req
}

// PushPrometheus imports Prometheus text exposition lines into VictoriaMetrics.
func (v *Victoria) PushPrometheus(ctx context.Context, body []byte) error {
	req := v.newReq(ctx, http.MethodPost, v.VM+"/api/v1/import/prometheus", bytes.NewReader(body))
	return do(req)
}

func do(req *http.Request) error {
	res, err := doRetry(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		return fmt.Errorf("%s %s: HTTP %d: %s", req.Method, req.URL.Path, res.StatusCode, strings.TrimSpace(string(b)))
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

// Tenant is a VictoriaLogs tenant: box logs live in 0, each project gets its own.
type Tenant uint32

// PushLogs sends JSON lines to VictoriaLogs for one tenant. Each line must
// have _msg; _time is optional. streamFields names the stream fields.
func (v *Victoria) PushLogs(ctx context.Context, t Tenant, streamFields string, lines []byte) error {
	q := url.Values{}
	q.Set("_stream_fields", streamFields)
	req := v.newReq(ctx, http.MethodPost, v.VL+"/insert/jsonline?"+q.Encode(), bytes.NewReader(lines))
	req.Header.Set("Content-Type", "application/stream+json")
	req.Header.Set("AccountID", strconv.FormatUint(uint64(t), 10))
	req.Header.Set("ProjectID", "0")
	return do(req)
}

// QueryLogs runs a LogsQL query in one tenant and returns the result rows.
func (v *Victoria) QueryLogs(ctx context.Context, t Tenant, query string, start, end time.Time, limit int) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))
	if !start.IsZero() {
		q.Set("start", start.UTC().Format(time.RFC3339Nano))
	}
	if !end.IsZero() {
		q.Set("end", end.UTC().Format(time.RFC3339Nano))
	}
	req := v.newReq(ctx, http.MethodPost, v.VL+"/select/logsql/query", strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("AccountID", strconv.FormatUint(uint64(t), 10))
	req.Header.Set("ProjectID", "0")
	res, err := doRetry(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4000))
		return nil, &QueryError{Status: res.StatusCode, Msg: strings.TrimSpace(string(b))}
	}
	out := []map[string]any{}
	dec := json.NewDecoder(io.LimitReader(res.Body, 32<<20))
	for dec.More() {
		var row map[string]any
		if err := dec.Decode(&row); err != nil {
			return out, err
		}
		out = append(out, row)
	}
	return out, nil
}

// QueryError is a query the store rejected (usually a syntax error).
type QueryError struct {
	Status int
	Msg    string
}

func (e *QueryError) Error() string { return e.Msg }

// PromResult is a Prometheus API query result.
type PromResult struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

// QueryMetrics runs PromQL. With step == 0 it is an instant query at end.
// extraLabels are enforced on every series selector (VictoriaMetrics extra_label).
func (v *Victoria) QueryMetrics(ctx context.Context, query string, start, end time.Time, step time.Duration, extraLabels ...string) (*PromResult, error) {
	q := url.Values{}
	q.Set("query", query)
	for _, l := range extraLabels {
		q.Add("extra_label", l)
	}
	path := "/api/v1/query"
	if step > 0 {
		path = "/api/v1/query_range"
		q.Set("start", strconv.FormatInt(start.Unix(), 10))
		q.Set("end", strconv.FormatInt(end.Unix(), 10))
		q.Set("step", strconv.FormatInt(int64(step/time.Second), 10)+"s")
	} else if !end.IsZero() {
		q.Set("time", strconv.FormatInt(end.Unix(), 10))
	}
	q.Set("timeout", "10s")
	req := v.newReq(ctx, http.MethodPost, v.VM+path, strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := doRetry(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var body struct {
		Status string     `json:"status"`
		Data   PromResult `json:"data"`
		Error  string     `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("metrics store: HTTP %d", res.StatusCode)
	}
	if body.Status != "success" {
		return nil, &QueryError{Status: res.StatusCode, Msg: body.Error}
	}
	return &body.Data, nil
}

// Healthy checks both stores.
func (v *Victoria) Healthy(ctx context.Context, base string) error {
	req := v.newReq(ctx, http.MethodGet, base+"/health", nil)
	return do(req)
}

// ---- log batching ----

// LogBatcher buffers JSON log lines per tenant and stream-field set and
// flushes them every second. Everything it holds, queued or being sent,
// stays within maxLogBuffer: while the store is down, new lines are
// dropped and counted rather than grown.
type LogBatcher struct {
	V    *Victoria
	mu   sync.Mutex
	bufs map[batchKey]*bytes.Buffer
	held int // bytes queued or in flight
	// Dropped counts lines dropped because the store was unreachable.
	Dropped uint64
	Sent    uint64
}

// maxLogBuffer is how much log data the box holds while the log store is
// unreachable (all tenants together).
const maxLogBuffer = 32 << 20

type batchKey struct {
	t      Tenant
	stream string
}

// Redact masks Tiffin credentials in s (tokens.Redact), so they never
// reach the log store, even if a process prints one.
func Redact(s string) string { return tokens.Redact(s) }

// Add queues one log record (marshalled as a JSON line).
func (b *LogBatcher) Add(t Tenant, streamFields string, rec map[string]any) {
	for k, v := range rec {
		if s, ok := v.(string); ok {
			rec[k] = Redact(s)
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held+len(line)+1 > maxLogBuffer { // store down for a while: drop rather than grow
		b.Dropped++
		return
	}
	if b.bufs == nil {
		b.bufs = map[batchKey]*bytes.Buffer{}
	}
	k := batchKey{t, streamFields}
	buf := b.bufs[k]
	if buf == nil {
		buf = &bytes.Buffer{}
		b.bufs[k] = buf
	}
	buf.Write(line)
	buf.WriteByte('\n')
	b.held += len(line) + 1
}

// Flush sends everything queued.
func (b *LogBatcher) Flush(ctx context.Context) {
	b.mu.Lock()
	bufs := b.bufs
	b.bufs = nil
	b.mu.Unlock()
	for k, buf := range bufs {
		n := uint64(bytes.Count(buf.Bytes(), []byte{'\n'}))
		if err := b.V.PushLogs(ctx, k.t, k.stream, buf.Bytes()); err != nil {
			// Put it back, ahead of what came in meanwhile: held already
			// counts both, so the cap in Add bounds the merge too.
			b.mu.Lock()
			if b.bufs == nil {
				b.bufs = map[batchKey]*bytes.Buffer{}
			}
			if cur := b.bufs[k]; cur != nil {
				buf.Write(cur.Bytes())
			}
			b.bufs[k] = buf
			b.mu.Unlock()
			continue
		}
		b.mu.Lock()
		b.Sent += n
		b.held -= buf.Len()
		b.mu.Unlock()
	}
}

// Run flushes every second until ctx ends.
func (b *LogBatcher) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			b.Flush(fctx)
			cancel()
			return
		case <-t.C:
			b.Flush(ctx)
		}
	}
}

// Stats returns lines shipped and dropped so far.
func (b *LogBatcher) Stats() (sent, dropped uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Sent, b.Dropped
}
