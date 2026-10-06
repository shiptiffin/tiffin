package queue

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Live progress in the browser. A server action starts a job or run and
// mints a subscribe token for it (SubscribeToken; tiffin-sdk's
// subscribeToken computes the same with the project's signing secret). The
// browser opens GET /_tiffin/runs/{id}/events on the app's own host: the
// runtime hands that path to ServeLive before it could reach the app.
//
// The stream (text/event-stream) starts with the output chunks after
// Last-Event-ID and the current state, then pushes every change until the
// job or run finishes. Event IDs are output chunk IDs, so a reconnect gets
// the chunks it missed and the state as it is now. One LISTEN connection
// feeds every stream: triggers (schema.go) notify "job_<n>" or the run ID on
// each change, and the hub reads the change once per job or run and fans it
// out in memory.

// LivePath is where browsers subscribe, on every app host.
const LivePath = "/_tiffin/runs/"

// maxLivePerProject bounds the streams one project may hold open.
var maxLivePerProject = 200

const (
	liveChannel   = "tiffin_live"
	maxTokenTTL   = 7 * 24 * time.Hour
	liveHeartbeat = 15 * time.Second
	liveBuffer    = 256 // updates queued per stream before a slow reader is cut off
)

// ---- tokens ----

// SubscribeToken returns a token that lets its holder watch one job or run
// ("job_42", "run_...") until exp: "live1.<project>.<id>.<exp>.<sig>", sig
// being the hex HMAC-SHA256 of "tiffin-live:<project>:<id>:<exp>" keyed with
// the project's signing secret.
func SubscribeToken(secret, project, id string, exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return "live1." + project + "." + id + "." + e + "." + liveMAC(secret, project, id, e)
}

func liveMAC(secret, project, id, exp string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("tiffin-live:" + project + ":" + id + ":" + exp))
	return hex.EncodeToString(m.Sum(nil))
}

func unauthenticated(msg string) error {
	return &Error{Status: 401, Code: "unauthenticated", Msg: msg, Hint: "mint a token on the server with subscribeToken(id) and pass it to the browser"}
}

// checkToken returns the project a token for id belongs to.
func (e *Engine) checkToken(ctx context.Context, token, id string, now time.Time) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "live1" {
		return "", unauthenticated("missing or malformed subscribe token")
	}
	project, tokID, exp, sig := parts[1], parts[2], parts[3], parts[4]
	_, secret, err := e.cfg.Keys.Get(ctx, project)
	if err != nil {
		return "", err
	}
	if !hmac.Equal([]byte(sig), []byte(liveMAC(secret, project, tokID, exp))) {
		return "", unauthenticated("the subscribe token's signature does not match")
	}
	if tokID != id {
		return "", &Error{Status: 403, Code: "forbidden", Msg: "this token is for a different job or run",
			Hint: "each token watches the one job or run it was minted for"}
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return "", unauthenticated("malformed subscribe token")
	}
	at := time.Unix(n, 0)
	if !at.After(now) {
		return "", unauthenticated("the subscribe token expired at " + at.UTC().Format(time.RFC3339))
	}
	if at.Sub(now) > maxTokenTTL+time.Minute {
		return "", unauthenticated("subscribe tokens last at most 7 days")
	}
	return project, nil
}

// ---- state ----

// LiveState is what a browser sees of a job or run.
type LiveState struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"` // job | run
	Name       string          `json:"name"` // queue or workflow
	Status     string          `json:"status"`
	Done       bool            `json:"done"`
	Progress   json.RawMessage `json:"progress"`
	Output     json.RawMessage `json:"output"`
	Error      string          `json:"error,omitempty"`
	Attempt    int             `json:"attempt,omitempty"`
	WaitingFor string          `json:"waitingFor,omitempty"`
	Steps      []LiveStep      `json:"steps,omitempty"`
}

// LiveStep is a workflow step without its result (results stay on the server).
type LiveStep struct {
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	WaitUntil  *time.Time `json:"waitUntil,omitempty"`
}

type liveChunk struct {
	ID   int64
	Data json.RawMessage
}

func nullRaw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

// liveState loads a job's or run's state ("" project: any).
func (e *Engine) liveState(ctx context.Context, project, id string) (*LiveState, error) {
	st := &LiveState{ID: id}
	var owner string
	var progress, output []byte
	var errText *string
	if strings.HasPrefix(id, "job_") {
		n, err := ParseJobID(id)
		if err != nil {
			return nil, err
		}
		st.Type = "job"
		err = e.pool.QueryRow(ctx, `SELECT project, queue, state, progress, output, last_error, attempt FROM tq_jobs WHERE id = $1`, n).
			Scan(&owner, &st.Name, &st.Status, &progress, &output, &errText, &st.Attempt)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && project != "" && owner != project) {
			return nil, notFound("no job " + id)
		}
		if err != nil {
			return nil, err
		}
		st.Done = st.Status == stateCompleted || st.Status == stateDead || st.Status == stateCancelled
	} else {
		st.Type = "run"
		err := e.pool.QueryRow(ctx, `SELECT project, workflow, state, progress, output, error FROM wf_runs WHERE id = $1`, id).
			Scan(&owner, &st.Name, &st.Status, &progress, &output, &errText)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && project != "" && owner != project) {
			return nil, notFound("no workflow run " + id)
		}
		if err != nil {
			return nil, err
		}
		st.Done = finalRun(st.Status)
		rows, err := e.pool.Query(ctx, `SELECT name, kind, state, started_at, finished_at, wait_until FROM wf_steps
			WHERE run_id = $1 AND kind <> 'patch' ORDER BY seq, id`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s LiveStep
			if err := rows.Scan(&s.Name, &s.Kind, &s.State, &s.StartedAt, &s.FinishedAt, &s.WaitUntil); err != nil {
				rows.Close()
				return nil, err
			}
			st.Steps = append(st.Steps, s)
		}
		rows.Close()
		if st.Status == runWaiting {
			st.WaitingFor = e.waitingSummary(ctx, id)
		}
	}
	st.Progress, st.Output, st.Error = nullRaw(progress), nullRaw(output), deref(errText)
	return st, nil
}

// liveChunks returns up to limit output chunks of a job or run after a chunk ID.
func (e *Engine) liveChunks(ctx context.Context, id string, after int64, limit int) ([]liveChunk, error) {
	var rows pgx.Rows
	var err error
	if strings.HasPrefix(id, "job_") {
		n, perr := ParseJobID(id)
		if perr != nil {
			return nil, perr
		}
		rows, err = e.pool.Query(ctx, `SELECT id, data FROM tq_output WHERE job_id = $1 AND id > $2 ORDER BY id LIMIT $3`, n, after, limit)
	} else {
		rows, err = e.pool.Query(ctx, `SELECT id, data FROM tq_output WHERE run_id = $1 AND id > $2 ORDER BY id LIMIT $3`, id, after, limit)
	}
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (liveChunk, error) {
		var c liveChunk
		return c, r.Scan(&c.ID, &c.Data)
	})
}

func (e *Engine) lastChunk(ctx context.Context, id string) (int64, error) {
	var n int64
	var err error
	if strings.HasPrefix(id, "job_") {
		j, perr := ParseJobID(id)
		if perr != nil {
			return 0, perr
		}
		err = e.pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM tq_output WHERE job_id = $1`, j).Scan(&n)
	} else {
		err = e.pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM tq_output WHERE run_id = $1`, id).Scan(&n)
	}
	return n, err
}

// ---- the hub: one LISTEN, fan-out in memory ----

type liveUpdate struct {
	gen    int64 // reads of a key are numbered
	state  *LiveState
	chunks []liveChunk
}

type liveSub struct {
	key, project string
	since        int64 // the read under way when the stream joined
	ch           chan liveUpdate
	cut          chan struct{} // closed when the stream fell too far behind
	once         sync.Once
}

func (s *liveSub) cutOff() { s.once.Do(func() { close(s.cut) }) }

type liveKey struct {
	subs        map[*liveSub]struct{}
	cursor      int64 // last chunk sent to subscribers
	gen         int64
	busy, dirty bool
}

type hub struct {
	e          *Engine
	ctx        context.Context
	mu         sync.Mutex
	keys       map[string]*liveKey
	perProject map[string]int
	listening  bool
}

func newHub(ctx context.Context, e *Engine) *hub {
	return &hub{e: e, ctx: ctx, keys: map[string]*liveKey{}, perProject: map[string]int{}}
}

// subscribe registers a stream for key. It starts the listener on first use.
func (h *hub) subscribe(ctx context.Context, project, key string) (*liveSub, error) {
	cursor, err := h.e.lastChunk(ctx, key)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.perProject[project] >= maxLivePerProject {
		return nil, &Error{Status: 429, Code: "rate_limited", Msg: fmt.Sprintf("project %s already has %d live streams open", project, maxLivePerProject),
			Hint: "close streams of finished runs; try again shortly"}
	}
	if !h.listening {
		h.listening = true
		go h.listen()
	}
	k := h.keys[key]
	if k == nil {
		k = &liveKey{subs: map[*liveSub]struct{}{}, cursor: cursor}
		h.keys[key] = k
	}
	s := &liveSub{key: key, project: project, since: k.gen, ch: make(chan liveUpdate, liveBuffer), cut: make(chan struct{})}
	k.subs[s] = struct{}{}
	h.perProject[project]++
	return s, nil
}

func (h *hub) unsubscribe(s *liveSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if k := h.keys[s.key]; k != nil {
		delete(k.subs, s)
		if len(k.subs) == 0 && !k.busy {
			delete(h.keys, s.key)
		}
	}
	if h.perProject[s.project]--; h.perProject[s.project] <= 0 {
		delete(h.perProject, s.project)
	}
}

// poke says key changed: subscribers get a fresh read. Reads of one key never
// overlap, so updates reach subscribers in order; pokes during a read
// coalesce into one more read.
func (h *hub) poke(key string) {
	h.mu.Lock()
	k := h.keys[key]
	if k == nil {
		h.mu.Unlock()
		return
	}
	if k.busy {
		k.dirty = true
		h.mu.Unlock()
		return
	}
	k.busy = true
	h.mu.Unlock()
	go h.refresh(key, k)
}

func (h *hub) pokeAll() {
	h.mu.Lock()
	keys := make([]string, 0, len(h.keys))
	for k := range h.keys {
		keys = append(keys, k)
	}
	h.mu.Unlock()
	for _, k := range keys {
		h.poke(k)
	}
}

func (h *hub) refresh(key string, k *liveKey) {
	for {
		h.mu.Lock()
		cursor := k.cursor
		k.gen++
		gen := k.gen
		h.mu.Unlock()
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		st, err := h.e.liveState(ctx, "", key)
		var chunks []liveChunk
		if err == nil {
			chunks, err = h.e.liveChunks(ctx, key, cursor, maxChunks)
		}
		cancel()
		h.mu.Lock()
		if err == nil {
			if n := len(chunks); n > 0 {
				k.cursor = chunks[n-1].ID
			}
			u := liveUpdate{gen: gen, state: st, chunks: chunks}
			for s := range k.subs {
				select {
				case s.ch <- u:
				default:
					s.cutOff() // it reconnects with Last-Event-ID and reads what it missed
				}
			}
		} else if h.ctx.Err() == nil {
			h.e.log.Warn("queue: live update", "key", key, "err", err)
		}
		if !k.dirty || h.ctx.Err() != nil {
			k.busy = false
			if len(k.subs) == 0 && h.keys[key] == k {
				delete(h.keys, key)
			}
			h.mu.Unlock()
			return
		}
		k.dirty = false
		h.mu.Unlock()
	}
}

// listen holds the box's one LISTEN connection, reconnecting as needed;
// after each (re)connect every watched key is read again, in case a change
// slipped by while it was not listening.
func (h *hub) listen() {
	wait := time.Second
	for h.ctx.Err() == nil {
		conn, err := pgx.Connect(h.ctx, h.e.cfg.DSN)
		if err == nil {
			if _, err = conn.Exec(h.ctx, "LISTEN "+liveChannel); err == nil {
				wait = time.Second
				h.pokeAll()
				for {
					n, werr := conn.WaitForNotification(h.ctx)
					if werr != nil {
						err = werr
						break
					}
					h.poke(n.Payload)
				}
			}
			_ = conn.Close(context.Background())
		}
		if h.ctx.Err() != nil {
			return
		}
		h.e.log.Warn("queue: live listener", "err", err)
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

// ---- the stream ----

// liveID returns the job or run ID in /_tiffin/runs/{id}/events.
func liveID(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, LivePath)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/events")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	if strings.HasPrefix(id, "job_") {
		_, err := ParseJobID(id)
		return id, err == nil
	}
	return id, strings.HasPrefix(id, "run_") && len(id) <= 64
}

// ServeLive streams one job or run to a browser (see the top of this file).
func (e *Engine) ServeLive(w http.ResponseWriter, r *http.Request) {
	id, ok := liveID(r.URL.Path)
	if !ok {
		writeErr(w, &Error{Status: 404, Code: "not_found", Msg: "no such stream", Hint: "subscribe at GET /_tiffin/runs/<job or run id>/events?token=..."})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeErr(w, &Error{Status: 405, Code: "validation", Msg: "use GET"})
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	ctx := r.Context()
	project, err := e.checkToken(ctx, token, id, e.now())
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := e.liveState(ctx, project, id); err != nil {
		writeErr(w, err)
		return
	}
	var cursor int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		cursor, _ = strconv.ParseInt(v, 10, 64)
	}
	sub, err := e.hub.subscribe(ctx, project, id)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		writeErr(w, err)
		return
	}
	defer e.hub.unsubscribe(sub)
	// Replay after registering, so nothing falls between the two; what
	// arrives twice is skipped by chunk ID and by comparing states.
	st, err := e.liveState(ctx, project, id)
	var chunks []liveChunk
	if err == nil {
		chunks, err = e.liveChunks(ctx, id, cursor, maxChunks)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	var last []byte
	send := func(u liveUpdate) (done bool, err error) {
		var b bytes.Buffer
		for _, c := range u.chunks {
			if c.ID <= cursor {
				continue
			}
			cursor = c.ID
			fmt.Fprintf(&b, "id: %d\nevent: output\ndata: %s\n\n", c.ID, c.Data)
		}
		if u.state != nil {
			raw, _ := json.Marshal(u.state)
			if !bytes.Equal(raw, last) {
				last = raw
				fmt.Fprintf(&b, "id: %d\nevent: state\ndata: %s\n\n", cursor, raw)
			}
			if u.state.Done {
				fmt.Fprintf(&b, "event: end\ndata: {\"status\":%q}\n\n", u.state.Status)
				done = true
			}
		}
		if b.Len() == 0 {
			return done, nil
		}
		if _, err := w.Write(b.Bytes()); err != nil {
			return done, err
		}
		return done, rc.Flush()
	}
	if _, err := fmt.Fprint(w, "retry: 2000\n\n"); err != nil {
		return
	}
	if done, err := send(liveUpdate{state: st, chunks: chunks}); done || err != nil {
		return
	}
	beat := time.NewTicker(liveHeartbeat)
	defer beat.Stop()
	for {
		select {
		case u := <-sub.ch:
			if u.gen <= sub.since {
				// Read before this stream's replay: its state may be older
				// (its chunks are not: they are skipped by ID).
				u.state = nil
			}
			if done, err := send(u); done || err != nil {
				return
			}
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-sub.cut:
			return
		case <-ctx.Done():
			return
		case <-e.hub.ctx.Done():
			return
		}
	}
}
