package observe

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func intAttr(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func mustHex(s string) []byte { b, _ := hex.DecodeString(s); return b }

// nextExport is what Next.js with registerOTel sends for one request: a
// server span (parent: the edge) with a render span and a fetch under it.
func nextExport(traceID string, at time.Time, total time.Duration, status int64) *coltrace.ExportTraceServiceRequest {
	ns := func(d time.Duration) uint64 { return uint64(at.Add(d).UnixNano()) }
	spans := []*tracepb.Span{
		{TraceId: mustHex(traceID), SpanId: mustHex("0000000000000002"), ParentSpanId: mustHex("0000000000000001"), Name: "GET /api/slow",
			Kind: tracepb.Span_SPAN_KIND_SERVER, StartTimeUnixNano: ns(0), EndTimeUnixNano: ns(total),
			Attributes: []*commonpb.KeyValue{strAttr("http.method", "GET"), strAttr("http.route", "/api/slow"), intAttr("http.status_code", status),
				strAttr("next.span_type", "BaseServer.handleRequest"), strAttr("note", "token tfn_abcdefghijklmnop0123 leaked")}},
		{TraceId: mustHex(traceID), SpanId: mustHex("0000000000000004"), ParentSpanId: mustHex("0000000000000003"), Name: "fetch GET http://127.0.0.1/api/ping",
			Kind: tracepb.Span_SPAN_KIND_CLIENT, StartTimeUnixNano: ns(total / 4), EndTimeUnixNano: ns(total / 2)},
		{TraceId: mustHex(traceID), SpanId: mustHex("0000000000000003"), ParentSpanId: mustHex("0000000000000002"), Name: "executing api route (app) /api/slow",
			Kind: tracepb.Span_SPAN_KIND_INTERNAL, StartTimeUnixNano: ns(time.Millisecond), EndTimeUnixNano: ns(total - time.Millisecond)},
	}
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", "web")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
}

func TestTraceIngestSamplingAndAPI(t *testing.T) {
	h := newHarness(t, &Victoria{VM: "http://127.0.0.1:1", VL: "http://127.0.0.1:1"})
	ctx := context.Background()
	env, err := h.m.Env(ctx, h.m.p, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	if env["OTEL_TRACES_EXPORTER"] != "otlp" {
		t.Fatalf("traces exporter: %v", env)
	}
	key := strings.TrimPrefix(env["OTEL_EXPORTER_OTLP_HEADERS"], "x-tiffin-key=")
	h.m.sampler.SetRate(0) // only errors and slow requests
	send := func(req *coltrace.ExportTraceServiceRequest, gz bool, auth string) int {
		t.Helper()
		raw, _ := proto.Marshal(req)
		r, _ := http.NewRequest("POST", h.ingest.URL+"/v1/traces", bytes.NewReader(raw))
		if gz {
			var b bytes.Buffer
			w := gzip.NewWriter(&b)
			w.Write(raw)
			w.Close()
			r, _ = http.NewRequest("POST", h.ingest.URL+"/v1/traces", &b)
			r.Header.Set("Content-Encoding", "gzip")
		}
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set("X-Tiffin-Key", auth)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	now := time.Now().Add(-time.Minute)
	slowID, fastID, errID := strings.Repeat("ab", 16), strings.Repeat("cd", 16), strings.Repeat("ef", 16)
	if c := send(nextExport(slowID, now, 1500*time.Millisecond, 200), true, key); c != 200 {
		t.Fatalf("slow export: %d", c)
	}
	if c := send(nextExport(fastID, now, 20*time.Millisecond, 200), false, key); c != 200 {
		t.Fatalf("fast export: %d", c)
	}
	if c := send(nextExport(errID, now, 30*time.Millisecond, 503), false, key); c != 200 {
		t.Fatalf("error export: %d", c)
	}
	if c := send(nextExport(fastID, now, time.Millisecond, 200), false, "nope"); c != 401 {
		t.Fatalf("bad key: %d", c)
	}

	code, _, list := h.call(h.owner, "GET", "/v1/observe/traces?project=shop", nil)
	if code != 200 || len(list) != 2 {
		t.Fatalf("list: %d %v", code, list)
	}
	first := list[0].(map[string]any)
	if first["traceId"] != slowID || first["kept"] != "slow" || first["durationMs"].(float64) != 1500 || first["name"] != "GET /api/slow" ||
		first["route"] != "/api/slow" || first["status"].(float64) != 200 || first["spans"].(float64) != 3 || first["app"] != "web" {
		t.Fatalf("slow trace summary: %v", first)
	}
	if second := list[1].(map[string]any); second["traceId"] != errID || second["kept"] != "error" || second["error"] != true {
		t.Fatalf("error trace summary: %v", second)
	}
	if _, _, l := h.call(h.owner, "GET", "/v1/observe/traces?project=shop&errors=true", nil); len(l) != 1 {
		t.Fatalf("errors only: %v", l)
	}
	if _, _, l := h.call(h.owner, "GET", "/v1/observe/traces?project=shop&minMs=1000", nil); len(l) != 1 {
		t.Fatalf("min duration: %v", l)
	}

	// The request ID form (with dashes) finds the same trace.
	reqID := slowID[:8] + "-" + slowID[8:12] + "-" + slowID[12:16] + "-" + slowID[16:20] + "-" + slowID[20:]
	code, d, _ := h.call(h.owner, "GET", "/v1/observe/traces/"+reqID+"?project=shop", nil)
	if code != 200 || d["logsQuery"] != "trace_id:"+slowID {
		t.Fatalf("get: %d %v", code, d)
	}
	spans := d["spans"].([]any)
	var names []string
	for _, s := range spans {
		m := s.(map[string]any)
		names = append(names, fmt.Sprintf("%v:%v", m["depth"], m["name"]))
	}
	if got := strings.Join(names, " | "); got != "0:GET /api/slow | 1:executing api route (app) /api/slow | 2:fetch GET http://127.0.0.1/api/ping" {
		t.Fatalf("tree order: %s", got)
	}
	root := spans[0].(map[string]any)
	if root["kind"] != "server" || root["offsetMs"].(float64) != 0 || !strings.Contains(fmt.Sprint(root["attributes"]), "tfn_[redacted]") {
		t.Fatalf("root span: %v", root)
	}
	if fetch := spans[2].(map[string]any); fetch["offsetMs"].(float64) != 375 || fetch["durationMs"].(float64) != 375 || fetch["kind"] != "client" {
		t.Fatalf("fetch span: %v", fetch)
	}
	// Another project's agent sees nothing.
	other := h.agent("other")
	if code, _, _ := h.call(other, "GET", "/v1/observe/traces/"+slowID+"?project=shop", nil); code != 403 && code != 404 {
		t.Fatalf("other project's agent got %d", code)
	}
	if code, _, _ := h.call(h.owner, "GET", "/v1/observe/traces/"+fastID+"?project=shop", nil); code != 404 {
		t.Fatalf("unsampled trace: %d", code)
	}

	// OTLP/JSON (hex IDs) works too.
	jsonID := strings.Repeat("12", 16)
	body := fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"web"}}]},
		"scopeSpans":[{"spans":[{"traceId":%q,"spanId":"00000000000000aa","name":"GET /json","kind":2,
		"startTimeUnixNano":"%d","endTimeUnixNano":"%d","status":{"code":2,"message":"boom"}}]}]}]}`,
		jsonID, now.UnixNano(), now.Add(5*time.Millisecond).UnixNano())
	r, _ := http.NewRequest("POST", h.ingest.URL+"/v1/traces", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(r)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("json export: %v %v", err, res)
	}
	res.Body.Close()
	if code, d, _ := h.call(h.owner, "GET", "/v1/observe/traces/"+jsonID+"?project=shop", nil); code != 200 || d["error"] != true {
		t.Fatalf("json trace: %d %v", code, d)
	}

	// Settings: sample rate and retention.
	code, s, _ := h.call(h.owner, "PUT", "/v1/observe/settings", map[string]any{"tracesSampleRate": 1, "tracesRetention": "7d", "tracesMaxMB": 10})
	if code != 200 || s["tracesSampleRate"].(float64) != 1 || s["tracesRetention"] != "7d" || s["tracesMaxMB"].(float64) != 10 {
		t.Fatalf("settings: %d %v", code, s)
	}
	send(nextExport(fastID, now, 20*time.Millisecond, 200), false, key)
	if code, _, _ := h.call(h.owner, "GET", "/v1/observe/traces/"+fastID+"?project=shop", nil); code != 200 {
		t.Fatalf("sampled at 100%%: %d", code)
	}

	if err := h.m.ProjectDeleted(ctx, h.m.p, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, _, l := h.call(h.owner, "GET", "/v1/observe/traces?project=shop", nil); len(l) != 0 {
		t.Fatalf("after delete: %v", l)
	}
}

func TestSamplerHoldsChildrenUntilTheTraceIsDecided(t *testing.T) {
	s := NewSampler(0, time.Second)
	now := time.Now()
	child := span{ID: "2", Parent: "1", Start: 0, End: int64(10 * time.Millisecond)}
	root := span{ID: "1", Start: 0, End: int64(1200 * time.Millisecond)}
	id := strings.Repeat("ff", 16)
	if keep, _ := s.Offer("p/"+id, id, []span{child}, now); keep != nil {
		t.Fatalf("a fast child alone is undecided: %v", keep)
	}
	keep, why := s.Offer("p/"+id, id, []span{root}, now)
	if why != "slow" || len(keep) != 2 {
		t.Fatalf("slow root keeps the held child: %s %v", why, keep)
	}
	// A late span of a kept trace is kept.
	if keep, why := s.Offer("p/"+id, id, []span{{ID: "3", Parent: "1"}}, now); why != "slow" || len(keep) != 1 {
		t.Fatalf("late span: %s %v", why, keep)
	}
	// Undecided traces are forgotten after a minute.
	other := strings.Repeat("ee", 16)
	s.Offer("p/"+other, other, []span{child}, now)
	s.Sweep(now.Add(2 * time.Minute))
	if s.held != 0 || len(s.pending) != 0 || s.Dropped.Load() != 1 {
		t.Fatalf("sweep: held %d pending %d dropped %d", s.held, len(s.pending), s.Dropped.Load())
	}

	// The sampled share is decided by the trace ID alone, close to the rate.
	n := 0
	for i := 0; i < 20000; i++ {
		if sampledID(fmt.Sprintf("%016x%016x", i, uint64(i)*0x9E3779B97F4A7C15), 0.1) {
			n++
		}
	}
	if n < 1800 || n > 2200 {
		t.Fatalf("10%% of 20000 trace IDs sampled: %d", n)
	}
	if !sampledID(strings.Repeat("00", 16), 0.1) || sampledID(strings.Repeat("ff", 16), 0.99) {
		t.Fatal("sampled share edges")
	}
}

func TestTraceRetentionAndSizeCap(t *testing.T) {
	st, err := OpenTraceStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	add := func(project, id string, at time.Time) {
		t.Helper()
		s := span{ID: "0000000000000001", Name: "GET /", Kind: 2, App: "web", Start: at.UnixNano(), End: at.Add(time.Millisecond).UnixNano(),
			Attrs: map[string]any{"pad": strings.Repeat(id, 200)}}
		if err := st.Add(ctx, project, id, "sampled", []span{s}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	add("shop", fmt.Sprintf("%032x", 1), now.Add(-4*24*time.Hour)) // past retention
	for i := 2; i < 42; i++ {
		add("shop", fmt.Sprintf("%032x", i), now.Add(time.Duration(i)*time.Second-time.Hour))
	}
	add("other", fmt.Sprintf("%032x", 99), now)
	u, _ := st.Usage(ctx)
	per := u["shop"][1] / u["shop"][0]
	// Cap shop at about 20 traces: retention removes 1, the cap the 21+ oldest of the rest.
	n, err := st.Cleanup(ctx, 3*24*time.Hour, per*20)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = st.Usage(ctx)
	if u["shop"][1] > per*20 || u["shop"][0] < 15 || u["other"][0] != 1 || n != 41-u["shop"][0] {
		t.Fatalf("after cleanup: removed %d, usage %v (per trace %d)", n, u, per)
	}
	left, _ := st.ListTraces(ctx, TraceFilter{Project: "shop", From: now.Add(-48 * time.Hour), Sort: "recent", Limit: 500})
	if left[0].TraceID != fmt.Sprintf("%032x", 41) {
		t.Fatalf("the newest traces stay: %v", left[0])
	}
	var chunks int
	st.db.QueryRow(`SELECT COUNT(*) FROM trace_chunks`).Scan(&chunks)
	if chunks != int(u["shop"][0]+u["other"][0]) {
		t.Fatalf("chunks of deleted traces remain: %d", chunks)
	}
}
