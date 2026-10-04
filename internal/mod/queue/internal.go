package queue

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// App-facing endpoints, used by tiffin-sdk inside apps. They authenticate
// with their app key (TIFFIN_QUEUE_KEY, "Bearer tqk_...", see AppKey), not with
// Tiffin tokens, and are not part of the OpenAPI document, so they are not
// CLI commands or MCP tools. /v1/hooks/{token} is public: the token is the
// secret.

type internalRoute struct {
	method, path string
	public       bool
	h            func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error)
}

func internalRoutes() []internalRoute {
	return []internalRoute{
		{method: "POST", path: "/v1/queue-internal/send", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			var b struct {
				sendBody
				FromApp string `json:"fromApp"`
			}
			if err := decode(r, &b); err != nil {
				return nil, err
			}
			from := c.app(b.FromApp)
			return e.Send(r.Context(), c.project, b.request(c.by(from), from))
		}},
		{method: "POST", path: "/v1/queue-internal/jobs/{id}/heartbeat", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			var b struct {
				AttemptID int `json:"attemptId"`
			}
			if err := decode(r, &b); err != nil {
				return nil, err
			}
			id, err := ParseJobID(r.PathValue("id"))
			if err != nil {
				return nil, err
			}
			until, err := e.Heartbeat(r.Context(), c.project, id, b.AttemptID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"leaseUntil": until}, nil
		}},
		{method: "POST", path: "/v1/queue-internal/workflows/start", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			var b struct {
				Workflow string          `json:"workflow"`
				Input    json.RawMessage `json:"input"`
				ID       string          `json:"id"`
				App      string          `json:"app"`
				Path     string          `json:"path"`
				FromApp  string          `json:"fromApp"`
			}
			if err := decode(r, &b); err != nil {
				return nil, err
			}
			run, created, err := e.StartRun(r.Context(), c.project, StartRequest{Workflow: b.Workflow, Input: b.Input, ID: b.ID, App: b.App,
				Path: b.Path, By: c.by(c.app(b.FromApp)), FromApp: c.app(b.FromApp)})
			if err != nil {
				return nil, err
			}
			return map[string]any{"run": run, "created": created}, nil
		}},
		{method: "GET", path: "/v1/queue-internal/workflows/runs/{id}", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			return e.GetRun(r.Context(), c.project, r.PathValue("id"), false)
		}},
		{method: "POST", path: "/v1/queue-internal/workflows/runs/{id}/steps", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			var b StepRecord
			if err := decode(r, &b); err != nil {
				return nil, err
			}
			return e.RecordStep(r.Context(), c.project, r.PathValue("id"), b)
		}},
		{method: "POST", path: "/v1/queue-internal/workflows/runs/{id}/waits", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			var b WaitRequest
			if err := decode(r, &b); err != nil {
				return nil, err
			}
			return e.Wait(r.Context(), c.project, r.PathValue("id"), b)
		}},
		{method: "POST", path: "/v1/queue-internal/workflows/events", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			var b struct {
				Name    string          `json:"name"`
				Payload json.RawMessage `json:"payload"`
				FromApp string          `json:"fromApp"`
			}
			if err := decode(r, &b); err != nil {
				return nil, err
			}
			return e.Emit(r.Context(), c.project, b.Name, b.Payload, c.by(c.app(b.FromApp)))
		}},
		{method: "POST", path: "/v1/queue-internal/outbox/kick", h: func(e *Engine, c caller, w http.ResponseWriter, r *http.Request) (any, error) {
			e.KickOutbox()
			return map[string]bool{"ok": true}, nil
		}},
		{method: "POST", path: "/v1/hooks/{token}", public: true, h: func(e *Engine, _ caller, w http.ResponseWriter, r *http.Request) (any, error) {
			raw, err := io.ReadAll(io.LimitReader(r.Body, maxPayload+1))
			if err != nil {
				return nil, err
			}
			if len(raw) > maxPayload {
				return nil, invalid("the webhook body is over 1 MB", "")
			}
			payload := json.RawMessage(raw)
			if len(raw) == 0 {
				payload = json.RawMessage("null")
			} else if !json.Valid(raw) {
				payload, _ = json.Marshal(map[string]string{"body": string(raw), "contentType": r.Header.Get("Content-Type")})
			}
			res, err := e.Hook(r.Context(), r.PathValue("token"), payload)
			if err != nil {
				return nil, err
			}
			w.WriteHeader(http.StatusAccepted)
			return map[string]any{"accepted": res.Accepted, "message": res.Message}, nil
		}},
	}
}

// caller is who called an app-facing endpoint: the project its key belongs
// to and, for an app's own key, the app.
type caller struct{ project, keyApp string }

// app is the calling app: the one its key names, else the one the request
// says (keys from before apps had their own).
func (c caller) app(claimed string) string { return orDefault(c.keyApp, claimed) }

// by is how History and job records name the calling app.
func (c caller) by(app string) string {
	if app == "" {
		return "app:" + c.project
	}
	return "app:" + c.project + "/" + app
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return invalid("the request body is not valid JSON: "+err.Error(), "")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var qe *Error
	if errors.As(err, &qe) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(qe.Status)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": qe.Status, "code": qe.Code, "detail": qe.Msg, "hint": qe.Hint})
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(500)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": 500, "code": "internal", "detail": err.Error()})
}

type statusRecorder struct {
	http.ResponseWriter
	wrote bool
}

func (s *statusRecorder) WriteHeader(code int) { s.wrote = true; s.ResponseWriter.WriteHeader(code) }

func serveInternal(rt internalRoute, getEngine func() *Engine, keys func() Keys, w http.ResponseWriter, r *http.Request) {
	e := getEngine()
	if e == nil {
		writeErr(w, &Error{Status: 503, Code: "precondition", Msg: "the queue is not running yet", Hint: "retry shortly"})
		return
	}
	var c caller
	if !rt.public {
		k := keys()
		key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		var okKey bool
		if k != nil {
			c.project, c.keyApp, okKey = CheckKey(r.Context(), k, key)
		}
		if !okKey {
			writeErr(w, &Error{Status: 401, Code: "unauthenticated", Msg: "missing or wrong queue key", Hint: "send Authorization: Bearer $TIFFIN_QUEUE_KEY"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sr := &statusRecorder{ResponseWriter: w}
	res, err := rt.h(e, c, sr, r.WithContext(ctx))
	if err != nil {
		writeErr(w, err)
		return
	}
	if sr.wrote {
		_ = json.NewEncoder(w).Encode(res)
		return
	}
	writeJSON(w, 0, res)
}

func (m *Module) registerInternal(a huma.API) {
	for _, rt := range internalRoutes() {
		a.Adapter().Handle(&huma.Operation{Method: rt.method, Path: rt.path}, func(hc huma.Context) {
			r, w := humago.Unwrap(hc)
			serveInternal(rt, m.engine, func() Keys {
				m.mu.RLock()
				defer m.mu.RUnlock()
				return m.keys
			}, w, r)
		})
	}
}

// InternalHandler serves the app-facing endpoints for one engine (tests and
// embedding).
func (e *Engine) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range internalRoutes() {
		mux.HandleFunc(rt.method+" "+rt.path, func(w http.ResponseWriter, r *http.Request) {
			serveInternal(rt, func() *Engine { return e }, func() Keys { return e.cfg.Keys }, w, r)
		})
	}
	return mux
}

// Keys returns the engine's key store.
func (e *Engine) Keys() Keys { return e.cfg.Keys }
