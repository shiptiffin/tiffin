package observe

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/mod/observe/sentry"
	"github.com/btahir/tiffin/internal/tokens"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// IngestAddr is where apps send errors (Sentry protocol), metrics and logs
// (OTLP/HTTP). The edge publishes it as errors.<domain> and otel.<domain>.
const (
	IngestPort = "4318"
	IngestAddr = "127.0.0.1:" + IngestPort
)

const maxIngestBody = 10 << 20

// ingestHandler serves the Sentry envelope/store endpoints and OTLP/HTTP.
func (m *Module) ingestHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", m.sentryHTTP)
	mux.HandleFunc("/v1/metrics", m.otlpHTTP("metrics"))
	mux.HandleFunc("/v1/logs", m.otlpHTTP("logs"))
	mux.HandleFunc("/v1/traces", m.otlpTraces)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "unknown ingest path " + r.URL.Path,
			"endpoints": []string{
				"POST /api/<id>/envelope/  (Sentry SDKs; use the app's SENTRY_DSN)",
				"POST /api/<id>/store/     (legacy Sentry SDKs)",
				"POST /v1/metrics, /v1/logs, /v1/traces (OTLP/HTTP protobuf or JSON; Authorization: Bearer <key>)",
			},
		})
	})
	return mux
}

func cors(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "content-type, content-encoding, x-sentry-auth, authorization, sentry-trace, baggage")
	h.Set("Access-Control-Expose-Headers", "x-sentry-error, x-sentry-rate-limits, retry-after")
	h.Set("Access-Control-Max-Age", "86400")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sentryErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("X-Sentry-Error", msg)
	writeJSON(w, status, map[string]string{"detail": msg})
}

// body returns the request body, decompressed, capped at maxIngestBody.
func body(r *http.Request) ([]byte, error) {
	var rd io.Reader = http.MaxBytesReader(nil, r.Body, maxIngestBody)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(rd)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		rd = gz
	case "deflate":
		b, err := io.ReadAll(rd)
		if err != nil {
			return nil, err
		}
		if zr, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
			defer zr.Close()
			rd = zr
		} else {
			rd = flate.NewReader(bytes.NewReader(b))
		}
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q (use gzip, deflate or none)", r.Header.Get("Content-Encoding"))
	}
	return io.ReadAll(io.LimitReader(rd, 4*maxIngestBody))
}

// sentryHTTP handles /api/<id>/envelope/ and /api/<id>/store/.
func (m *Module) sentryHTTP(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		sentryErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api, <id>, envelope|store
	if len(parts) != 3 || (parts[2] != "envelope" && parts[2] != "store") {
		sentryErr(w, http.StatusNotFound, "use /api/<id>/envelope/ or /api/<id>/store/")
		return
	}
	pid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		sentryErr(w, http.StatusNotFound, "project id in the DSN path must be a number")
		return
	}
	raw, err := body(r)
	if err != nil {
		sentryErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var payloads [][]byte
	dsn := ""
	if parts[2] == "envelope" {
		env, err := sentry.ParseEnvelope(bytes.NewReader(raw))
		if err != nil {
			sentryErr(w, http.StatusBadRequest, err.Error())
			return
		}
		payloads, dsn = env.Events, env.DSN
	} else {
		payloads = [][]byte{raw}
	}
	key := sentry.AuthKey(r.Header.Get("X-Sentry-Auth"), r.URL.Query().Get("sentry_key"), dsn)
	if key == "" {
		if u, err := url.Parse(dsn); err == nil && u.User != nil {
			key = u.User.Username()
		}
	}
	k, ok := m.store.LookupKey(r.Context(), key)
	if !ok || k.ID != pid {
		sentryErr(w, http.StatusUnauthorized, "unknown DSN key: use the SENTRY_DSN Tiffin gives the app")
		return
	}
	var lastID string
	for _, p := range payloads {
		ev, err := sentry.ParseEvent(p)
		if err != nil {
			sentryErr(w, http.StatusBadRequest, err.Error())
			return
		}
		is, _, err := m.store.RecordEvent(r.Context(), k.Project, k.App, ev)
		if err != nil {
			sentryErr(w, http.StatusInternalServerError, "could not store the event")
			return
		}
		lastID = ev.EventID
		if t, err := m.store.TenantFor(r.Context(), k.Project); err == nil {
			m.batch.Add(t, "source,app", map[string]any{"_msg": is.Title, "_time": ev.Timestamp.Format("2006-01-02T15:04:05.000000Z07:00"),
				"level": ev.Level, "app": k.App, "issue": is.ID, "culprit": is.Culprit, "source": "errors", "release": ev.Release})
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": lastID})
}

// otlpKey reads the app key from Authorization: Bearer <key> (what
// OTEL_EXPORTER_OTLP_HEADERS sets) or X-Tiffin-Key.
func otlpKey(r *http.Request) string {
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(r.Header.Get("X-Tiffin-Key"))
}

// otlpHTTP forwards OTLP/HTTP to VictoriaMetrics or VictoriaLogs, stamping
// the sending app's project and app so apps cannot write as each other.
func (m *Module) otlpHTTP(signal string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		k, ok := m.store.LookupKey(r.Context(), otlpKey(r))
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown key: send Authorization: Bearer <key> (Tiffin sets OTEL_EXPORTER_OTLP_HEADERS for your apps)"})
			return
		}
		in := r.Header
		var raw []byte
		var err error
		if signal == "logs" {
			// Logs are masked like every other line the box keeps: decode
			// the body, mask credentials, send it on uncompressed.
			if raw, err = body(r); err == nil {
				raw, err = maskOTLPLogs(raw, r.Header.Get("Content-Type"))
			}
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			in = http.Header{"Content-Type": r.Header.Values("Content-Type")}
		} else if raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxIngestBody)); err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
			return
		}
		var target string
		hdr := http.Header{}
		switch signal {
		case "metrics":
			q := url.Values{}
			q.Add("extra_label", "project="+k.Project)
			q.Add("extra_label", "app="+k.App)
			target = m.vic.VM + "/opentelemetry/v1/metrics?" + q.Encode()
		case "logs":
			t, err := m.store.TenantFor(r.Context(), k.Project)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "tenant"})
				return
			}
			q := url.Values{}
			q.Set("extra_fields", "app="+k.App+",source=otlp")
			target = m.vic.VL + "/insert/opentelemetry/v1/logs?" + q.Encode()
			hdr.Set("AccountID", strconv.FormatUint(uint64(t), 10))
			hdr.Set("ProjectID", "0")
		}
		res, err := m.vic.forward(r.Context(), target, in, hdr, raw)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the " + signal + " store is not reachable; try again shortly"})
			return
		}
		defer res.Body.Close()
		for _, h := range []string{"Content-Type"} {
			if v := res.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(res.Body, 1<<20))
	}
}

// maskOTLPLogs masks Tiffin credentials in an OTLP log export: in place
// for JSON (a credential ends at its string's quote), by decoding and
// re-encoding for protobuf.
func maskOTLPLogs(raw []byte, contentType string) ([]byte, error) {
	if strings.Contains(contentType, "json") {
		return tokens.RedactBytes(raw), nil
	}
	if !bytes.Contains(raw, []byte("tfn_")) && !bytes.Contains(raw, []byte("tfl_")) && !bytes.Contains(raw, []byte("tak_")) {
		return raw, nil
	}
	req := &collogs.ExportLogsServiceRequest{}
	if err := proto.Unmarshal(raw, req); err != nil {
		return nil, fmt.Errorf("not an OTLP log export: %w", err)
	}
	maskStrings(req.ProtoReflect())
	return proto.Marshal(req)
}

// maskStrings masks credentials in every string field of m, recursively.
func maskStrings(m protoreflect.Message) {
	type set struct {
		fd protoreflect.FieldDescriptor
		v  string
	}
	var sets []set
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				if fd.Kind() == protoreflect.StringKind {
					l.Set(i, protoreflect.ValueOfString(tokens.Redact(l.Get(i).String())))
				} else if fd.Message() != nil {
					maskStrings(l.Get(i).Message())
				}
			}
		case fd.Kind() == protoreflect.StringKind:
			if r := tokens.Redact(v.String()); r != v.String() {
				sets = append(sets, set{fd, r})
			}
		case fd.Message() != nil:
			maskStrings(v.Message())
		}
		return true
	})
	for _, x := range sets {
		m.Set(x.fd, protoreflect.ValueOfString(x.v))
	}
}

// otlpTraces takes OTLP/HTTP trace exports. Spans are decoded here (not
// forwarded): the sampler keeps errors, slow requests and a share of the
// rest, and the trace store keeps those for a few days.
func (m *Module) otlpTraces(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	k, ok := m.store.LookupKey(r.Context(), otlpKey(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown key: send Authorization: Bearer <key> (Tiffin sets OTEL_EXPORTER_OTLP_HEADERS for your apps)"})
		return
	}
	raw, err := body(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ct := r.Header.Get("Content-Type")
	byTrace, _, err := decodeTraces(raw, ct, k.App)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not an OTLP trace export: " + err.Error()})
		return
	}
	if err := m.keepTraces(r.Context(), k.Project, byTrace); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the trace store is busy; try again shortly"})
		return
	}
	// An empty ExportTraceServiceResponse: no bytes in protobuf, {} in JSON.
	if strings.Contains(ct, "json") {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func (v *Victoria) forward(ctx context.Context, target string, in, extra http.Header, body []byte) (*http.Response, error) {
	// The stores' password: without it they answer 401 to every export.
	req := v.newReq(ctx, http.MethodPost, target, bytes.NewReader(body))
	if req == nil {
		return nil, errors.New("bad store URL " + target)
	}
	for _, h := range []string{"Content-Type", "Content-Encoding"} {
		if v := in.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	for k, v := range extra {
		req.Header[k] = v
	}
	res, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 500 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		res.Body.Close()
		return nil, errors.New(string(b))
	}
	return res, nil
}
