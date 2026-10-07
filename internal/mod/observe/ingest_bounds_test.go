package observe

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/btahir/tiffin/internal/mod/observe/sentry"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const leaked = "tfn_abcdefghijklmnop0123456789"

// service.name is copied into every span: a huge one must not multiply.
func TestTraceServiceNameIsClipped(t *testing.T) {
	var spans []*tracepb.Span
	for i := range 100 {
		spans = append(spans, &tracepb.Span{TraceId: mustHex("0123456789abcdef0123456789abcdef"), SpanId: mustHex(fmt.Sprintf("%016x", i+1)), Name: "s"})
	}
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", strings.Repeat("x", 1<<20))}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
	raw, _ := proto.Marshal(req)
	byTrace, n, err := decodeTraces(raw, "application/x-protobuf", "web")
	if err != nil || n != 100 {
		t.Fatal(n, err)
	}
	for _, ss := range byTrace {
		b, _ := json.Marshal(ss)
		if len(b) > 64<<10 {
			t.Fatalf("100 spans serialize to %d bytes", len(b))
		}
	}
}

// Events are clipped on the way in, credentials are masked before they
// are stored, and recording returns the summary without the kept events.
func TestSentryEventsAreBoundedAndMasked(t *testing.T) {
	st, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var frames []string
	for i := range 500 {
		frames = append(frames, fmt.Sprintf(`{"filename":%q,"function":"f%d","in_app":true}`, strings.Repeat("p", 4096), i))
	}
	big := fmt.Sprintf(`{"message":%q,"fingerprint":["fixed"],"tags":{"t":%q},"exception":{"values":[{"type":"E","value":%q,"stacktrace":{"frames":[%s]}}]}}`,
		strings.Repeat("m", 1<<20), strings.Repeat("v", 1<<20), "login code "+leaked+" "+strings.Repeat("e", 1<<20), strings.Join(frames, ","))
	var id string
	for range 25 {
		ev, err := sentry.ParseEvent([]byte(big))
		if err != nil {
			t.Fatal(err)
		}
		is, _, err := st.RecordEvent(ctx, "shop", "web", ev)
		if err != nil {
			t.Fatal(err)
		}
		id = is.ID
		if strings.Contains(is.Title, leaked) {
			t.Fatalf("title keeps the credential: %.80s", is.Title)
		}
	}
	d, err := st.GetIssue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if len(b) > 8<<20 {
		t.Fatalf("issue detail is %d bytes", len(b))
	}
	if bytes.Contains(b, []byte(leaked)) || !bytes.Contains(b, []byte("tfn_[redacted]")) {
		t.Fatal("stored event keeps the credential")
	}
}

// OTLP logs are masked before they reach the log store, gzip or not.
func TestOTLPLogsAreMasked(t *testing.T) {
	var mu sync.Mutex
	var got [][]byte
	vl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, b)
		mu.Unlock()
		if r.Header.Get("Content-Encoding") != "" {
			t.Errorf("forwarded with Content-Encoding %q", r.Header.Get("Content-Encoding"))
		}
	}))
	defer vl.Close()
	h := newHarness(t, &Victoria{VM: vl.URL, VL: vl.URL})
	k, err := h.m.store.KeyFor(context.Background(), "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	req := &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
		{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "Authorization: Bearer " + leaked}},
			Attributes: []*commonpb.KeyValue{strAttr("header", leaked)}},
	}}}}}}
	raw, _ := proto.Marshal(req)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	for _, enc := range []string{"", "gzip"} {
		body := raw
		if enc == "gzip" {
			body = gz.Bytes()
		}
		r, _ := http.NewRequest("POST", h.ingest.URL+"/v1/logs", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+k.Key)
		r.Header.Set("Content-Type", "application/x-protobuf")
		if enc != "" {
			r.Header.Set("Content-Encoding", enc)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", enc, res.StatusCode)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("forwarded %d", len(got))
	}
	for _, b := range got {
		var out collogs.ExportLogsServiceRequest
		if err := proto.Unmarshal(b, &out); err != nil {
			t.Fatalf("forwarded body is not OTLP: %v", err)
		}
		rec := out.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
		if bytes.Contains(b, []byte(leaked)) || !strings.Contains(rec.GetBody().GetStringValue(), "tfn_[redacted]") {
			t.Fatalf("not masked: %v", rec)
		}
	}
}
