package api

// Idempotency keys make the few create operations a client may have to send
// twice safe to send twice. A connection can drop after the box has done the
// work but before the answer arrives (the box reloads its edge when routes
// change, and the edge closes old connections). The client cannot tell
// whether its request ran, so it sends an Idempotency-Key with it: the box
// remembers the first successful answer for that key and gives it again,
// without doing the work twice, and GET /v1/idempotency-keys/{key} says what
// became of it without sending the request (and its upload) again.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// IdempotencyHeader carries a client's key for one create request.
const IdempotencyHeader = "Idempotency-Key"

// IdempotencyStatusHeader is set on an answer given for a key: "replayed"
// when it is the stored answer of an earlier request, "running" (with 409)
// while the first request with the key is still being handled.
const IdempotencyStatusHeader = "Idempotency-Status"

// ExtIdempotent marks operations that honour an Idempotency-Key.
const ExtIdempotent = "x-tiffin-idempotent"

// Idempotent marks an operation as honouring an Idempotency-Key: a second
// request with the same key gets the first one's answer.
func Idempotent(o huma.Operation) huma.Operation {
	o.Extensions[ExtIdempotent] = true
	return o
}

// IsIdempotent reports whether an operation honours an Idempotency-Key.
func IsIdempotent(o *huma.Operation) bool {
	v, _ := o.Extensions[ExtIdempotent].(bool)
	return v
}

const (
	idemNS      = "idempotency"
	idemTTL     = 24 * time.Hour
	idemMaxBody = 1 << 20
)

var idemKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// idemRecord is a stored answer.
type idemRecord struct {
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Status      int       `json:"status"`
	ContentType string    `json:"contentType,omitempty"`
	Body        []byte    `json:"body"`
	CreatedAt   time.Time `json:"createdAt"`
}

// idemState is what runs now (in this process: a restart forgets it, as it
// ends the requests too) and when expired answers were last cleared.
type idemState struct {
	mu        sync.Mutex
	running   map[string]bool
	lastPurge time.Time
}

func (s *idemState) begin(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *idemState) end(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

func (s *idemState) isRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

// idemID scopes a key to the token that sent it.
func idemID(p *tokens.Principal, key string) string {
	h := sha256.Sum256([]byte(p.TokenID + "\x00" + key))
	return hex.EncodeToString(h[:])
}

func (a *API) idemLoad(ctx context.Context, id string) (*idemRecord, error) {
	raw, ok, err := a.deps.DB.KVGet(ctx, idemNS, id)
	if err != nil || !ok {
		return nil, err
	}
	var rec idemRecord
	if json.Unmarshal(raw, &rec) != nil || time.Since(rec.CreatedAt) > idemTTL {
		return nil, nil
	}
	return &rec, nil
}

func (a *API) idemStore(ctx context.Context, id string, rec *idemRecord) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = a.deps.DB.KVPut(ctx, idemNS, id, raw)
	a.idem.mu.Lock()
	purge := time.Since(a.idem.lastPurge) > time.Hour
	if purge {
		a.idem.lastPurge = time.Now()
	}
	a.idem.mu.Unlock()
	if !purge {
		return
	}
	all, err := a.deps.DB.KVList(ctx, idemNS)
	if err != nil {
		return
	}
	for k, v := range all {
		var r idemRecord
		if json.Unmarshal(v, &r) != nil || time.Since(r.CreatedAt) > idemTTL {
			_ = a.deps.DB.KVDelete(ctx, idemNS, k)
		}
	}
}

// idempotent is the middleware behind Idempotency-Key: on an operation
// marked Idempotent, a key seen before gets the stored answer (the request
// body is not read), a key still running gets 409, and a new key's first
// successful (2xx) answer is stored for 24 hours. Failures are not stored:
// the request may be sent again with the same key.
func (a *API) idempotent(ctx huma.Context, next func(huma.Context)) {
	key := strings.TrimSpace(ctx.Header(IdempotencyHeader))
	p := PrincipalFrom(ctx.Context())
	if key == "" || a.deps.DB == nil || p == nil || !IsIdempotent(ctx.Operation()) {
		next(ctx)
		return
	}
	if !idemKeyRE.MatchString(key) {
		writeProblem(ctx, problem(http.StatusUnprocessableEntity, "validation", "Idempotency-Key must be 16 to 128 letters, digits, - or _"), nil)
		return
	}
	id := idemID(p, key)
	method, path := ctx.Method(), ctx.URL().Path
	if !a.idem.begin(id) {
		out := problem(http.StatusConflict, "precondition", "a request with this Idempotency-Key is still running")
		out.Hint = "wait for it: GET /v1/idempotency-keys/" + key + " says when it is done, and its answer"
		writeProblem(ctx, out, map[string]string{IdempotencyStatusHeader: "running", "Retry-After": "1"})
		return
	}
	defer a.idem.end(id)
	rec, err := a.idemLoad(ctx.Context(), id)
	if err != nil {
		writeProblem(ctx, problem(http.StatusInternalServerError, "internal", "internal error"), nil)
		return
	}
	if rec != nil {
		if rec.Method != method || rec.Path != path {
			writeProblem(ctx, problem(http.StatusUnprocessableEntity, "validation",
				"this Idempotency-Key was used for another request ("+rec.Method+" "+rec.Path+"); send a new key for a new request"), nil)
			return
		}
		ctx.SetHeader("Content-Type", orDefault(rec.ContentType, "application/json"))
		ctx.SetHeader(IdempotencyStatusHeader, "replayed")
		ctx.SetStatus(rec.Status)
		_, _ = ctx.BodyWriter().Write(rec.Body)
		return
	}
	rc := &recordingContext{innerContext: ctx}
	next(rc)
	if rc.status >= 200 && rc.status < 300 && !rc.over {
		// Stored even when the client has gone: that is when it is needed.
		a.idemStore(context.WithoutCancel(ctx.Context()), id, &idemRecord{Method: method, Path: path, Status: rc.status,
			ContentType: rc.contentType, Body: rc.body.Bytes(), CreatedAt: time.Now().UTC()})
	}
}

func writeProblem(ctx huma.Context, p *Problem, headers map[string]string) {
	for k, v := range headers {
		ctx.SetHeader(k, v)
	}
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(p.Status)
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(p)
}

// innerContext lets recordingContext embed a huma.Context (whose own
// Context method would clash with the field name).
type innerContext huma.Context

// recordingContext keeps a copy of the answer written through it.
type recordingContext struct {
	innerContext
	status      int
	contentType string
	body        bytes.Buffer
	over        bool // too large to keep
}

func (c *recordingContext) Unwrap() huma.Context { return c.innerContext }

func (c *recordingContext) SetStatus(code int) {
	c.status = code
	c.innerContext.SetStatus(code)
}

func (c *recordingContext) SetHeader(name, value string) {
	if strings.EqualFold(name, "Content-Type") {
		c.contentType = value
	}
	c.innerContext.SetHeader(name, value)
}

func (c *recordingContext) BodyWriter() io.Writer {
	return io.MultiWriter(c.innerContext.BodyWriter(), (*recordingBody)(c))
}

type recordingBody recordingContext

func (b *recordingBody) Write(p []byte) (int, error) {
	if !b.over {
		if b.body.Len()+len(p) > idemMaxBody {
			b.over = true
			b.body.Reset()
		} else {
			b.body.Write(p)
		}
	}
	return len(p), nil
}

// IdempotentRequest is what became of a request sent with an Idempotency-Key.
type IdempotentRequest struct {
	Key            string     `json:"key"`
	Status         string     `json:"status" enum:"none,running,done" doc:"none: no request with this key succeeded (it never arrived, or failed); send it again with the same key. running: it is still being handled. done: it succeeded; response is its answer"`
	Method         string     `json:"method,omitempty"`
	Path           string     `json:"path,omitempty"`
	ResponseStatus int        `json:"responseStatus,omitempty" doc:"The HTTP status it got"`
	Response       any        `json:"response,omitempty" doc:"The answer it got (JSON)"`
	CreatedAt      *time.Time `json:"createdAt,omitempty"`
}

func (a *API) registerIdempotency() {
	o := op("idempotency-key-get", http.MethodGet, "/v1/idempotency-keys/{key}", "-", RiskRead,
		"Look up a request by its Idempotency-Key",
		"What became of a create request sent with an Idempotency-Key (deploys, project imports, duplicates and exports), "+
			"for a client whose connection dropped before the answer came: the stored answer if it succeeded, whether it still "+
			"runs, or none (then send it again with the same key). Only the key's own token sees it; answers are kept 24 hours.", "system")
	huma.Register(a.api, o, wrap(func(ctx context.Context, in *struct {
		Key string `path:"key" pattern:"^[A-Za-z0-9_-]{16,128}$" doc:"The Idempotency-Key the request was sent with"`
	}) (*struct{ Body IdempotentRequest }, error) {
		p := PrincipalFrom(ctx) // any key may look up its own requests, whatever it reaches
		if p == nil {
			return nil, tokens.ErrUnauthenticated
		}
		out := IdempotentRequest{Key: in.Key, Status: "none"}
		if a.deps.DB == nil {
			return &struct{ Body IdempotentRequest }{out}, nil
		}
		id := idemID(p, in.Key)
		if a.idem.isRunning(id) {
			out.Status = "running"
			return &struct{ Body IdempotentRequest }{out}, nil
		}
		rec, err := a.idemLoad(ctx, id)
		if err != nil {
			return nil, err
		}
		if rec != nil {
			out.Status, out.Method, out.Path, out.ResponseStatus, out.CreatedAt = "done", rec.Method, rec.Path, rec.Status, &rec.CreatedAt
			if json.Valid(rec.Body) {
				out.Response = json.RawMessage(rec.Body)
			}
		}
		return &struct{ Body IdempotentRequest }{out}, nil
	}))
}
