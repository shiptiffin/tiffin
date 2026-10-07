package observe

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/klauspost/compress/zstd"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Traces are OTLP spans from apps, sampled on arrival and kept in their own
// SQLite file (observe/traces.db, not backed up) for a few days, within a
// size cap per project. Sampling happens on the box, after the spans are
// seen: every trace with an error or a span slower than the threshold is
// kept, and a fixed share of the rest, chosen by trace ID so every part of
// a trace gets the same answer.

// Trace defaults (changed with `tiffin observe settings set`).
const (
	DefaultTraceSample    = 0.1
	DefaultTraceSlow      = time.Second
	DefaultTraceRetention = "3d"
	DefaultTraceMaxMB     = 64

	SettingTraceSample    = "traces.sampleRate"
	SettingTraceRetention = "traces.retention"
	SettingTraceMaxMB     = "traces.maxMB"
)

// Limits on what one span keeps.
const (
	maxSpanAttrs   = 48
	maxSpanEvents  = 8
	maxAttrValue   = 1024
	maxStackValue  = 4096
	maxSpanName    = 200
	maxPendingSpan = 20000            // spans held while their trace's fate is open
	pendingFor     = time.Minute      // how long they are held
	keptFor        = 10 * time.Minute // late spans of a kept trace are still kept
	traceRowBytes  = 200              // a trace row's own cost, roughly
)

// span is one stored span (compact JSON inside a zstd chunk).
type span struct {
	ID      string         `json:"i"`
	Parent  string         `json:"p,omitempty"`
	Name    string         `json:"n"`
	Kind    int32          `json:"k,omitempty"`
	App     string         `json:"a,omitempty"`
	Service string         `json:"sv,omitempty"`
	Start   int64          `json:"s"`
	End     int64          `json:"e"`
	Status  int32          `json:"st,omitempty"`
	Msg     string         `json:"m,omitempty"`
	Attrs   map[string]any `json:"at,omitempty"`
	Events  []spanEvent    `json:"ev,omitempty"`
}

type spanEvent struct {
	Name  string         `json:"n"`
	Time  int64          `json:"t"`
	Attrs map[string]any `json:"at,omitempty"`
}

func (s *span) dur() time.Duration { return time.Duration(s.End - s.Start) }

func (s *span) failed() bool {
	if s.Status == int32(tracepb.Status_STATUS_CODE_ERROR) {
		return true
	}
	return s.httpStatus() >= 500 && (s.Kind == int32(tracepb.Span_SPAN_KIND_SERVER) || s.Parent == "")
}

func (s *span) httpStatus() int {
	for _, k := range []string{"http.response.status_code", "http.status_code"} {
		switch v := s.Attrs[k].(type) {
		case int64:
			return int(v)
		case float64:
			return int(v)
		case string:
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

func (s *span) attr(keys ...string) string {
	for _, k := range keys {
		if v, ok := s.Attrs[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// rank orders root candidates: a span without a parent, then a server
// span (its parent may be the edge or another service), then anything.
func (s *span) rank() int {
	switch {
	case s.Parent == "":
		return 2
	case s.Kind == int32(tracepb.Span_SPAN_KIND_SERVER) || s.Kind == int32(tracepb.Span_SPAN_KIND_CONSUMER):
		return 1
	}
	return 0
}

// ---- decoding OTLP ----

// decodeTraces reads an OTLP/HTTP trace export (protobuf or JSON) into
// spans grouped by trace ID.
func decodeTraces(raw []byte, contentType, app string) (map[string][]span, int, error) {
	req := &coltrace.ExportTraceServiceRequest{}
	if strings.Contains(contentType, "json") {
		fixed, err := otlpJSONIDs(raw)
		if err != nil {
			return nil, 0, err
		}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(fixed, req); err != nil {
			return nil, 0, err
		}
	} else if err := proto.Unmarshal(raw, req); err != nil {
		return nil, 0, err
	}
	out := map[string][]span{}
	n := 0
	for _, rs := range req.GetResourceSpans() {
		service := ""
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				// Clipped: it is copied into every span of the resource.
				service = clip(Redact(kv.GetValue().GetStringValue()), maxSpanName)
			}
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				if len(sp.GetTraceId()) != 16 || len(sp.GetSpanId()) != 8 {
					continue
				}
				s := span{ID: hex.EncodeToString(sp.GetSpanId()), Name: clip(sp.GetName(), maxSpanName), Kind: int32(sp.GetKind()),
					App: app, Service: service, Start: int64(sp.GetStartTimeUnixNano()), End: int64(sp.GetEndTimeUnixNano()),
					Status: int32(sp.GetStatus().GetCode()), Msg: clip(Redact(sp.GetStatus().GetMessage()), maxAttrValue),
					Attrs: attrMap(sp.GetAttributes(), maxSpanAttrs)}
				if p := sp.GetParentSpanId(); len(p) == 8 {
					s.Parent = hex.EncodeToString(p)
				}
				if s.End < s.Start {
					s.End = s.Start
				}
				evs := sp.GetEvents()
				// Exceptions first: they are what a person opens a trace for.
				sort.SliceStable(evs, func(i, j int) bool { return evs[i].GetName() == "exception" && evs[j].GetName() != "exception" })
				for _, ev := range evs {
					if len(s.Events) == maxSpanEvents {
						break
					}
					s.Events = append(s.Events, spanEvent{Name: clip(ev.GetName(), maxSpanName), Time: int64(ev.GetTimeUnixNano()), Attrs: attrMap(ev.GetAttributes(), 16)})
				}
				id := hex.EncodeToString(sp.GetTraceId())
				out[id] = append(out[id], s)
				n++
			}
		}
	}
	return out, n, nil
}

// otlpJSONIDs rewrites the hex trace and span IDs of OTLP/JSON into the
// base64 protojson expects.
func otlpJSONIDs(raw []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	fix := func(m map[string]any, keys ...string) {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && s != "" {
				if b, err := hex.DecodeString(s); err == nil {
					m[k] = base64.StdEncoding.EncodeToString(b)
				}
			}
		}
	}
	each := func(v any, f func(map[string]any)) {
		list, _ := v.([]any)
		for _, x := range list {
			if m, ok := x.(map[string]any); ok {
				f(m)
			}
		}
	}
	each(doc["resourceSpans"], func(rs map[string]any) {
		each(rs["scopeSpans"], func(ss map[string]any) {
			each(ss["spans"], func(sp map[string]any) {
				fix(sp, "traceId", "spanId", "parentSpanId")
				each(sp["links"], func(l map[string]any) { fix(l, "traceId", "spanId") })
			})
		})
	})
	return json.Marshal(doc)
}

func attrMap(kvs []*commonpb.KeyValue, limit int) map[string]any {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string]any, min(len(kvs), limit))
	for _, kv := range kvs {
		if len(out) == limit {
			break
		}
		max := maxAttrValue
		if strings.HasSuffix(kv.GetKey(), "stacktrace") {
			max = maxStackValue
		}
		out[clip(kv.GetKey(), 120)] = anyValue(kv.GetValue(), max, 0)
	}
	return out
}

func anyValue(v *commonpb.AnyValue, max, depth int) any {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return clip(Redact(x.StringValue), max)
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		if math.IsNaN(x.DoubleValue) || math.IsInf(x.DoubleValue, 0) {
			return nil
		}
		return x.DoubleValue
	case *commonpb.AnyValue_ArrayValue:
		if depth > 2 {
			return nil
		}
		var out []any
		for _, e := range x.ArrayValue.GetValues() {
			if len(out) == 16 {
				break
			}
			out = append(out, anyValue(e, max, depth+1))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		if depth > 2 {
			return nil
		}
		return attrMap(x.KvlistValue.GetValues(), 16)
	case *commonpb.AnyValue_BytesValue:
		return clip(base64.StdEncoding.EncodeToString(x.BytesValue), max)
	}
	return nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 { // not mid-rune
		cut--
	}
	return s[:cut] + "…"
}

// ---- sampling ----

// Sampler decides which traces to keep. A trace is kept when its ID falls
// in the sampled share, or when any of its spans failed or ran longer than
// Slow. Spans of undecided traces wait in memory (bounded) for a minute, so
// a slow parent that ends after its children still keeps them.
type Sampler struct {
	mu      sync.Mutex
	rate    float64
	slow    time.Duration
	pending map[string]*pendingTrace
	held    int
	kept    map[string]keptTrace
	Dropped atomic.Uint64 // spans not kept
}

type pendingTrace struct {
	spans []span
	since time.Time
}

type keptTrace struct {
	why string
	at  time.Time
}

// NewSampler returns a sampler keeping rate (0-1) of ordinary traces.
func NewSampler(rate float64, slow time.Duration) *Sampler {
	return &Sampler{rate: rate, slow: slow, pending: map[string]*pendingTrace{}, kept: map[string]keptTrace{}}
}

// SetRate changes the sampled share.
func (s *Sampler) SetRate(rate float64) {
	s.mu.Lock()
	s.rate = rate
	s.mu.Unlock()
}

// sampledID reports whether a trace ID falls in the sampled share: the
// last 8 bytes (the random part in W3C trace context) against the rate.
func sampledID(traceID string, rate float64) bool {
	if rate >= 1 {
		return true
	}
	b, err := hex.DecodeString(traceID)
	if err != nil || len(b) != 16 || rate <= 0 {
		return false
	}
	return float64(binary.BigEndian.Uint64(b[8:])>>11)/float64(1<<53) < rate
}

// Offer returns the spans to store now for one trace (possibly with spans
// held earlier) and why the trace is kept; nothing while it is undecided.
func (s *Sampler) Offer(key, traceID string, spans []span, now time.Time) ([]span, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.kept[key]; ok {
		return spans, k.why
	}
	why := ""
	if sampledID(traceID, s.rate) {
		why = "sampled"
	}
	for i := range spans {
		if spans[i].failed() {
			why = "error"
			break
		}
		if spans[i].dur() >= s.slow && why == "" {
			why = "slow"
		}
	}
	if why == "" {
		if s.held+len(spans) > maxPendingSpan {
			s.Dropped.Add(uint64(len(spans)))
			return nil, ""
		}
		p := s.pending[key]
		if p == nil {
			p = &pendingTrace{since: now}
			s.pending[key] = p
		}
		p.spans = append(p.spans, spans...)
		s.held += len(spans)
		return nil, ""
	}
	if why != "sampled" { // sampled traces need no memory: the ID decides again
		s.kept[key] = keptTrace{why: why, at: now}
	}
	if p := s.pending[key]; p != nil {
		spans = append(p.spans, spans...)
		s.held -= len(p.spans)
		delete(s.pending, key)
	}
	return spans, why
}

// Sweep forgets undecided traces held too long and old decisions.
func (s *Sampler) Sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.pending {
		if now.Sub(p.since) > pendingFor {
			s.held -= len(p.spans)
			s.Dropped.Add(uint64(len(p.spans)))
			delete(s.pending, k)
		}
	}
	for k, v := range s.kept {
		if now.Sub(v.at) > keptFor {
			delete(s.kept, k)
		}
	}
}

// ---- storage ----

// TraceStore is the traces SQLite file: one row per trace (the summary
// lists and sorts by) and zstd-compressed chunks of its spans.
type TraceStore struct {
	db  *sql.DB
	mu  sync.Mutex
	enc *zstd.Encoder
	dec *zstd.Decoder
}

var traceSchema = []string{
	`PRAGMA auto_vacuum = INCREMENTAL`, // before the first table: deleted traces give their space back
	`CREATE TABLE IF NOT EXISTS traces (
		project TEXT NOT NULL, trace_id TEXT NOT NULL, app TEXT NOT NULL, name TEXT NOT NULL,
		rank INTEGER NOT NULL, root_start INTEGER NOT NULL, method TEXT NOT NULL, route TEXT NOT NULL, status INTEGER NOT NULL,
		start_ns INTEGER NOT NULL, end_ns INTEGER NOT NULL, dur_ns INTEGER NOT NULL, spans INTEGER NOT NULL,
		error INTEGER NOT NULL, why TEXT NOT NULL, bytes INTEGER NOT NULL,
		PRIMARY KEY(project, trace_id))`,
	`CREATE INDEX IF NOT EXISTS traces_start ON traces(project, start_ns)`,
	`CREATE INDEX IF NOT EXISTS traces_dur ON traces(project, dur_ns)`,
	`CREATE TABLE IF NOT EXISTS trace_chunks (
		id INTEGER PRIMARY KEY, project TEXT NOT NULL, trace_id TEXT NOT NULL, data BLOB NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS trace_chunks_trace ON trace_chunks(project, trace_id)`,
}

// OpenTraceStore opens (creating) the traces database; ":memory:" for tests.
func OpenTraceStore(path string) (*TraceStore, error) {
	dsn := "file::memory:"
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath()
	}
	dsn += "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=synchronous(normal)&_pragma=journal_mode(wal)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	for _, s := range traceSchema {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("traces db: %w", err)
		}
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	dec, _ := zstd.NewReader(nil)
	return &TraceStore{db: db, enc: enc, dec: dec}, nil
}

// Close closes the database.
func (t *TraceStore) Close() error { return t.db.Close() }

// Add stores spans of one trace and updates its summary.
func (t *TraceStore) Add(ctx context.Context, project, traceID, why string, spans []span) error {
	if len(spans) == 0 {
		return nil
	}
	raw, err := json.Marshal(spans)
	if err != nil {
		return err
	}
	chunk := t.enc.EncodeAll(raw, nil)
	root := &spans[0]
	start, end, failed := spans[0].Start, spans[0].End, false
	for i := range spans {
		s := &spans[i]
		if r, rr := s.rank(), root.rank(); r > rr || (r == rr && s.Start < root.Start) {
			root = s
		}
		start, end = min(start, s.Start), max(end, s.End)
		failed = failed || s.failed()
	}
	route := root.attr("http.route", "next.route", "url.path", "http.target")
	if i := strings.IndexByte(route, '?'); i >= 0 {
		route = route[:i]
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO trace_chunks(project, trace_id, data) VALUES (?, ?, ?)`, project, traceID, chunk); err != nil {
		return err
	}
	// In an upsert's SET, plain column names are the stored row's values.
	newRoot := `(excluded.rank > rank OR (excluded.rank = rank AND excluded.root_start < root_start))`
	_, err = tx.ExecContext(ctx, `INSERT INTO traces(project, trace_id, app, name, rank, root_start, method, route, status,
			start_ns, end_ns, dur_ns, spans, error, why, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project, trace_id) DO UPDATE SET
			app = CASE WHEN `+newRoot+` THEN excluded.app ELSE app END,
			name = CASE WHEN `+newRoot+` THEN excluded.name ELSE name END,
			method = CASE WHEN `+newRoot+` THEN excluded.method ELSE method END,
			route = CASE WHEN `+newRoot+` THEN excluded.route ELSE route END,
			status = CASE WHEN `+newRoot+` THEN excluded.status ELSE status END,
			root_start = CASE WHEN `+newRoot+` THEN excluded.root_start ELSE root_start END,
			rank = MAX(rank, excluded.rank),
			start_ns = MIN(start_ns, excluded.start_ns), end_ns = MAX(end_ns, excluded.end_ns),
			dur_ns = MAX(end_ns, excluded.end_ns) - MIN(start_ns, excluded.start_ns),
			spans = spans + excluded.spans, error = MAX(error, excluded.error),
			why = CASE WHEN excluded.why = 'error' OR why = '' THEN excluded.why ELSE why END,
			bytes = bytes + excluded.bytes`,
		project, traceID, root.App, root.Name, root.rank(), root.Start, root.attr("http.request.method", "http.method"), clip(route, 300), root.httpStatus(),
		start, end, end-start, len(spans), failed, why, len(chunk)+traceRowBytes)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// TraceSummary is one kept trace, as listed.
type TraceSummary struct {
	TraceID    string    `json:"traceId"`
	Project    string    `json:"project"`
	App        string    `json:"app"`
	Name       string    `json:"name" doc:"The root span's name, e.g. GET /api/orders"`
	Method     string    `json:"method,omitempty"`
	Route      string    `json:"route,omitempty" doc:"The route or path the request matched"`
	Status     int       `json:"status,omitempty" doc:"HTTP status of the root span, when it has one"`
	Start      time.Time `json:"start"`
	DurationMS float64   `json:"durationMs" doc:"From the first span's start to the last span's end"`
	Spans      int       `json:"spans"`
	Error      bool      `json:"error" doc:"A span failed (error status, or a 5xx response)"`
	Kept       string    `json:"kept" enum:"sampled,error,slow" doc:"Why the box kept it: in the sampled share, a span failed, or a span was slow"`
}

// TraceFilter narrows ListTraces.
type TraceFilter struct {
	Project string
	App     string
	From    time.Time
	MinDur  time.Duration
	Errors  bool
	Sort    string // slowest | recent
	Limit   int
}

const traceCols = `trace_id, project, app, name, method, route, status, start_ns, dur_ns, spans, error, why`

func scanTrace(sc interface{ Scan(...any) error }) (TraceSummary, error) {
	var s TraceSummary
	var start, dur int64
	err := sc.Scan(&s.TraceID, &s.Project, &s.App, &s.Name, &s.Method, &s.Route, &s.Status, &start, &dur, &s.Spans, &s.Error, &s.Kept)
	s.Start = time.Unix(0, start).UTC()
	s.DurationMS = math.Round(float64(dur)/1e3) / 1e3
	return s, err
}

// ListTraces lists a project's traces, slowest or most recent first.
func (t *TraceStore) ListTraces(ctx context.Context, f TraceFilter) ([]TraceSummary, error) {
	q := `SELECT ` + traceCols + ` FROM traces WHERE project = ? AND start_ns >= ? AND dur_ns >= ?`
	args := []any{f.Project, f.From.UnixNano(), int64(f.MinDur)}
	if f.App != "" {
		q += ` AND app = ?`
		args = append(args, f.App)
	}
	if f.Errors {
		q += ` AND error = 1`
	}
	if f.Sort == "recent" {
		q += ` ORDER BY start_ns DESC`
	} else {
		q += ` ORDER BY dur_ns DESC`
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` LIMIT ?`
	args = append(args, f.Limit)
	rows, err := t.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TraceSummary{}
	for rows.Next() {
		s, err := scanTrace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Span is one span of a trace, in tree order.
type Span struct {
	SpanID        string         `json:"spanId"`
	ParentID      string         `json:"parentId,omitempty"`
	Depth         int            `json:"depth" doc:"Nesting under the root (0)"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind" enum:"server,client,internal,producer,consumer,unspecified"`
	App           string         `json:"app"`
	Service       string         `json:"service,omitempty"`
	OffsetMS      float64        `json:"offsetMs" doc:"Start, from the start of the trace"`
	DurationMS    float64        `json:"durationMs"`
	Error         bool           `json:"error"`
	StatusMessage string         `json:"statusMessage,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Events        []SpanEvent    `json:"events,omitempty" doc:"Exceptions first"`
}

// SpanEvent is an event recorded on a span (an exception, a log).
type SpanEvent struct {
	Name       string         `json:"name"`
	OffsetMS   float64        `json:"offsetMs"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// TraceDetail is a trace with its spans.
type TraceDetail struct {
	TraceSummary
	LogsQuery string `json:"logsQuery" doc:"LogsQuery for this request's edge line: requests reach Next.js and other OpenTelemetry apps with the edge's request ID as their trace ID"`
	Spans     []Span `json:"spans"`
}

// ErrTraceNotFound is returned for unknown trace IDs.
var ErrTraceNotFound = errors.New("trace not found")

var kindNames = map[int32]string{0: "unspecified", 1: "internal", 2: "server", 3: "client", 4: "producer", 5: "consumer"}

// GetTrace returns one trace with its spans as a tree (depth-first, by
// start time).
func (t *TraceStore) GetTrace(ctx context.Context, project, traceID string) (*TraceDetail, error) {
	s, err := scanTrace(t.db.QueryRowContext(ctx, `SELECT `+traceCols+` FROM traces WHERE project = ? AND trace_id = ?`, project, traceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTraceNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := t.db.QueryContext(ctx, `SELECT data FROM trace_chunks WHERE project = ? AND trace_id = ? ORDER BY id`, project, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []span
	seen := map[string]bool{}
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			return nil, err
		}
		raw, err := t.dec.DecodeAll(chunk, nil)
		if err != nil {
			continue
		}
		var ss []span
		if json.Unmarshal(raw, &ss) != nil {
			continue
		}
		for _, x := range ss {
			if !seen[x.ID] { // a retried export sends the same spans again
				seen[x.ID] = true
				all = append(all, x)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d := &TraceDetail{TraceSummary: s, LogsQuery: "trace_id:" + traceID, Spans: treeOrder(all)}
	return d, nil
}

// treeOrder lays spans out depth-first under their parents, children by
// start time; spans whose parent is not stored (the edge, another box)
// are roots.
func treeOrder(all []span) []Span {
	sort.Slice(all, func(i, j int) bool { return all[i].Start < all[j].Start })
	var t0 int64
	if len(all) > 0 {
		t0 = all[0].Start
	}
	byID := map[string]bool{}
	for _, s := range all {
		byID[s.ID] = true
	}
	children := map[string][]int{}
	var roots []int
	for i, s := range all {
		if s.Parent != "" && byID[s.Parent] && s.Parent != s.ID {
			children[s.Parent] = append(children[s.Parent], i)
		} else {
			roots = append(roots, i)
		}
	}
	ms := func(ns int64) float64 { return math.Round(float64(ns)/1e3) / 1e3 }
	out := make([]Span, 0, len(all))
	done := make([]bool, len(all))
	var walk func(i, depth int)
	walk = func(i, depth int) {
		if done[i] {
			return
		}
		done[i] = true
		s := all[i]
		v := Span{SpanID: s.ID, ParentID: s.Parent, Depth: depth, Name: s.Name, Kind: kindNames[s.Kind], App: s.App, Service: s.Service,
			OffsetMS: ms(s.Start - t0), DurationMS: ms(s.End - s.Start), Error: s.failed(), StatusMessage: s.Msg, Attributes: s.Attrs}
		if v.Kind == "" {
			v.Kind = "unspecified"
		}
		for _, e := range s.Events {
			v.Events = append(v.Events, SpanEvent{Name: e.Name, OffsetMS: ms(e.Time - t0), Attributes: e.Attrs})
		}
		out = append(out, v)
		for _, c := range children[s.ID] {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return out
}

// Cleanup deletes traces older than retention, then the oldest traces of
// any project over maxBytes, and returns how many traces went.
func (t *TraceStore) Cleanup(ctx context.Context, retention time.Duration, maxBytes int64) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var n int64
	cut := time.Now().Add(-retention).UnixNano()
	res, err := t.db.ExecContext(ctx, `DELETE FROM traces WHERE start_ns < ?`, cut)
	if err != nil {
		return 0, err
	}
	d, _ := res.RowsAffected()
	n += d
	rows, err := t.db.QueryContext(ctx, `SELECT project, SUM(bytes) FROM traces GROUP BY project HAVING SUM(bytes) > ?`, maxBytes)
	if err != nil {
		return n, err
	}
	over := map[string]int64{}
	for rows.Next() {
		var p string
		var b int64
		if err := rows.Scan(&p, &b); err != nil {
			rows.Close()
			return n, err
		}
		over[p] = b
	}
	rows.Close()
	for p, total := range over {
		// Down to 90% of the cap, so the next few traces do not trigger it again.
		free := total - maxBytes*9/10
		var startCut int64
		err := t.db.QueryRowContext(ctx, `SELECT start_ns FROM (SELECT start_ns, SUM(bytes) OVER (ORDER BY start_ns, trace_id) AS run
			FROM traces WHERE project = ?) WHERE run >= ? ORDER BY start_ns LIMIT 1`, p, free).Scan(&startCut)
		if err != nil {
			continue
		}
		res, err := t.db.ExecContext(ctx, `DELETE FROM traces WHERE project = ? AND start_ns <= ?`, p, startCut)
		if err != nil {
			return n, err
		}
		d, _ := res.RowsAffected()
		n += d
	}
	if n > 0 {
		if _, err := t.db.ExecContext(ctx, `DELETE FROM trace_chunks WHERE NOT EXISTS
			(SELECT 1 FROM traces WHERE traces.project = trace_chunks.project AND traces.trace_id = trace_chunks.trace_id)`); err != nil {
			return n, err
		}
		_, _ = t.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	}
	return n, nil
}

// Usage returns each project's stored traces and their bytes.
func (t *TraceStore) Usage(ctx context.Context) (map[string][2]int64, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT project, COUNT(*), SUM(bytes) FROM traces GROUP BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int64{}
	for rows.Next() {
		var p string
		var n, b int64
		if err := rows.Scan(&p, &n, &b); err != nil {
			return nil, err
		}
		out[p] = [2]int64{n, b}
	}
	return out, rows.Err()
}

// DeleteProject removes a project's traces.
func (t *TraceStore) DeleteProject(ctx context.Context, project string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, q := range []string{`DELETE FROM trace_chunks WHERE project = ?`, `DELETE FROM traces WHERE project = ?`} {
		if _, err := t.db.ExecContext(ctx, q, project); err != nil {
			return err
		}
	}
	return nil
}

// ---- wiring ----

// TraceConfig is the box's trace settings.
type TraceConfig struct {
	SampleRate float64
	Retention  time.Duration
	MaxBytes   int64
}

func (m *Module) traceConfig(ctx context.Context) TraceConfig {
	c := TraceConfig{SampleRate: DefaultTraceSample, MaxBytes: DefaultTraceMaxMB << 20}
	c.Retention, _ = parseDur(DefaultTraceRetention)
	if v, err := strconv.ParseFloat(m.store.Setting(ctx, SettingTraceSample), 64); err == nil && v >= 0 && v <= 1 {
		c.SampleRate = v
	}
	if d, err := parseDur(m.store.Setting(ctx, SettingTraceRetention)); err == nil && d > 0 {
		c.Retention = d
	}
	if n, err := strconv.ParseInt(m.store.Setting(ctx, SettingTraceMaxMB), 10, 64); err == nil && n > 0 {
		c.MaxBytes = n << 20
	}
	return c
}

// keepTraces stores the spans of one export that the sampler keeps.
func (m *Module) keepTraces(ctx context.Context, project string, byTrace map[string][]span) error {
	now := time.Now()
	for id, spans := range byTrace {
		keep, why := m.sampler.Offer(project+"/"+id, id, spans, now)
		if len(keep) == 0 {
			continue
		}
		if err := m.traces.Add(ctx, project, id, why, keep); err != nil {
			return err
		}
	}
	return nil
}

// traceLoop sweeps the sampler and applies retention and the size cap.
func (m *Module) traceLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for i := 0; ; i++ {
		m.sampler.Sweep(time.Now())
		if i%20 == 0 { // every 5 minutes
			c := m.traceConfig(ctx)
			m.sampler.SetRate(c.SampleRate)
			if _, err := m.traces.Cleanup(ctx, c.Retention, c.MaxBytes); err != nil && ctx.Err() == nil {
				m.p.Log.Error("traces cleanup", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- API ----

var traceIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (m *Module) registerTraceAPI(a huma.API) {
	huma.Register(a, api.Untrusted(api.Op("traces-list", http.MethodGet, "/v1/observe/traces", "traces list", api.RiskRead,
		"List request traces",
		"Traces your apps sent over OpenTelemetry (Next.js with instrumentation.ts, or any OTel SDK), slowest first. "+
			"The box keeps every trace with an error or a span of a second or more, and a sample of the rest (10% by default), for 3 days."+untrusted, "observe")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `query:"project" required:"true" doc:"Project"`
			App     string `query:"app" doc:"Only traces whose root span came from this app"`
			Since   string `query:"since" doc:"How far back, e.g. 1h, 24h, 3d. Default 24h."`
			MinMS   int    `query:"minMs" minimum:"0" doc:"Only traces at least this long, in milliseconds"`
			Errors  bool   `query:"errors" doc:"Only traces with a failed span"`
			Sort    string `query:"sort" enum:"slowest,recent" default:"slowest"`
			Limit   int    `query:"limit" minimum:"1" maximum:"500" default:"50"`
		}) (*struct{ Body []TraceSummary }, error) {
			if err := m.ready(); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			from, _, err := window(in.Since, "", "", 24*time.Hour)
			if err != nil {
				return nil, err
			}
			out, err := m.traces.ListTraces(ctx, TraceFilter{Project: in.Project, App: in.App, From: from,
				MinDur: time.Duration(in.MinMS) * time.Millisecond, Errors: in.Errors, Sort: in.Sort, Limit: in.Limit})
			if err != nil {
				return nil, err
			}
			return &struct{ Body []TraceSummary }{out}, nil
		}))

	g := api.Op("trace-get", http.MethodGet, "/v1/observe/traces/{id}", "traces get", api.RiskRead,
		"Get a request trace",
		"One trace with its spans in tree order (depth, offset and duration in milliseconds, attributes, exceptions) and the logs query for its edge request."+untrusted, "observe")
	g.Errors = append(g.Errors, 404)
	huma.Register(a, api.Untrusted(g), api.Wrap(func(ctx context.Context, in *struct {
		ID      string `path:"id" doc:"Trace ID (32 hex characters); an edge request ID works too"`
		Project string `query:"project" required:"true" doc:"Project"`
	}) (*struct{ Body *TraceDetail }, error) {
		if err := m.ready(); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		id := strings.ToLower(strings.ReplaceAll(in.ID, "-", ""))
		if !traceIDRe.MatchString(id) {
			return nil, api.NewProblem(422, "validation", "a trace ID is 32 hex characters")
		}
		d, err := m.traces.GetTrace(ctx, in.Project, id)
		if errors.Is(err, ErrTraceNotFound) {
			p := api.NewProblem(404, "not_found", "no trace "+id+" in project "+in.Project)
			p.Hint = "the box keeps errors, requests of a second or more and a sample of the rest for 3 days; tiffin traces list shows what it has"
			return nil, p
		}
		if err != nil {
			return nil, err
		}
		return &struct{ Body *TraceDetail }{d}, nil
	}))
}
