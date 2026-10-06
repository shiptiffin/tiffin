package valkey

// An Upstash-compatible REST endpoint, so apps written for @upstash/redis,
// @upstash/ratelimit or @vercel/kv run against the project's Valkey with only
// env vars (UPSTASH_REDIS_REST_URL/TOKEN, KV_REST_API_*). It speaks the subset
// those clients use: one command as a JSON array (POST /) or in the path
// (/set/k/v), /pipeline, /multi-exec, and "Upstash-Encoding: base64".
//
// Every command runs over RESP as the project's own ACL user, so its key
// prefix, the commands it may run and its cache limit apply unchanged. The
// endpoint adds the project's prefix to every key argument (found from the
// server's own key specs, COMMAND) and to PUBLISH channels, and strips it from
// the few replies that name keys (BLPOP, XREAD...), so apps see clean keys:
// "user:1" over REST is "p_shop:user:1" to REDIS_URL clients. Lua scripts get
// prefixed KEYS; a script that builds key names itself is refused by the ACL.
// It listens on 127.0.0.1 and the runtime's host IP only, like the queue's
// app endpoints; the token is derived from the project's Valkey password.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
)

// RESTPort is where the Upstash-compatible endpoint listens.
const RESTPort = "7076"

const (
	restMaxBody  = 16 << 20 // one request
	restMaxConns = 64       // Valkey connections per project
	restIdle     = 8        // kept open per project
)

// RESTEnv is the Upstash and Vercel KV env for a project's apps.
func RESTEnv(ctx context.Context, p *platform.Platform, project string) (map[string]string, error) {
	pw, err := datakit.EnsureSecret(ctx, p, nsPassword, project)
	if err != nil {
		return nil, err
	}
	u := os.Getenv("TIFFIN_KV_REST_URL")
	if u == "" {
		u = "http://" + net.JoinHostPort(hostIP(ctx, p), RESTPort)
	}
	tok, ro := restTokens(project, pw)
	return map[string]string{"UPSTASH_REDIS_REST_URL": u, "UPSTASH_REDIS_REST_TOKEN": tok,
		"KV_REST_API_URL": u, "KV_REST_API_TOKEN": tok, "KV_REST_API_READ_ONLY_TOKEN": ro}, nil
}

// restTokens derives a project's REST tokens from its Valkey password: no
// second secret to store, and they go away with the service.
func restTokens(project, pw string) (full, readOnly string) {
	mac := func(purpose string) string {
		h := hmac.New(sha256.New, []byte(pw))
		h.Write([]byte(purpose))
		return hex.EncodeToString(h.Sum(nil))[:40]
	}
	return "tvk_" + project + "_" + mac("upstash-rest"), "tvkro_" + project + "_" + mac("upstash-rest-ro")
}

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// hostIP is the address app containers reach box services on (KV
// runtime/host-ip; 127.0.0.1 until the runtime publishes it).
func hostIP(ctx context.Context, p *platform.Platform) string {
	if p != nil && p.DB != nil {
		if raw, ok, _ := p.DB.KVGet(ctx, "runtime", "host-ip"); ok && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimSpace(string(raw))
		}
	}
	return "127.0.0.1"
}

// serveREST listens on loopback and the host IP (re-checked every 10 s).
func serveREST(ctx context.Context, p *platform.Platform) {
	h := conns(p)
	open := map[string]*http.Server{}
	defer func() {
		for _, s := range open {
			_ = s.Close()
		}
	}()
	for {
		for _, ip := range []string{"127.0.0.1", hostIP(ctx, p)} {
			addr := net.JoinHostPort(ip, RESTPort)
			if open[addr] != nil {
				continue
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				p.Log.Warn("valkey: REST listener", "addr", addr, "err", err)
				continue
			}
			srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
			open[addr] = srv
			go func() { _ = srv.Serve(ln) }()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

type restCred struct {
	pw, token, ro string
	at            time.Time
}

type restProject struct {
	slots chan struct{}
	mu    sync.Mutex
	idle  []*Client
}

type rest struct {
	secret func(ctx context.Context, project string) (string, bool, error)
	dial   func(ctx context.Context, user, pw string) (*Client, error)
	// admin connects as the box's admin user, for the reads a project user
	// may not make (SCAN across the server, for its own prefix).
	admin func(ctx context.Context) (*Client, error)

	mu    sync.Mutex
	creds map[string]restCred
	pools map[string]*restProject
	cmds  map[string]*cmdInfo
}

func newREST(secret func(context.Context, string) (string, bool, error), dial func(context.Context, string, string) (*Client, error)) *rest {
	return &rest{secret: secret, dial: dial, creds: map[string]restCred{}, pools: map[string]*restProject{}}
}

// restError is a reply the client gets as {"error": msg} with status code.
type restError struct {
	code int
	msg  string
}

func (e *restError) Error() string { return e.msg }

func badRequest(format string, a ...any) error {
	return &restError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

func (h *rest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	out, err := h.serve(r)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		code := http.StatusServiceUnavailable
		var re *restError
		if errors.As(err, &re) {
			code = re.code
		}
		out = map[string]string{"error": err.Error()}
		w.WriteHeader(code)
	}
	raw, _ := json.Marshal(out)
	_, _ = w.Write(raw)
}

func (h *rest) serve(r *http.Request) (any, error) {
	project, readOnly, err := h.auth(r)
	if err != nil {
		return nil, err
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		return nil, &restError{http.StatusMethodNotAllowed, "ERR use GET or POST"}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, restMaxBody))
	if err != nil {
		return nil, &restError{http.StatusRequestEntityTooLarge, "ERR the request is over 16 MB"}
	}
	b64 := strings.EqualFold(r.Header.Get("Upstash-Encoding"), "base64")
	path := strings.Trim(r.URL.EscapedPath(), "/")
	ctx := r.Context()
	switch path {
	case "pipeline", "multi-exec":
		var cmds [][]any
		if err := decodeJSON(raw, &cmds); err != nil || len(cmds) == 0 {
			return nil, badRequest("ERR the body must be a JSON array of commands")
		}
		args := make([][]string, len(cmds))
		for i, c := range cmds {
			if args[i], err = toArgs(c); err != nil {
				return nil, err
			}
		}
		return h.batch(ctx, project, readOnly, args, path == "multi-exec", b64)
	}
	var args []string
	if path == "" {
		var c []any
		if err := decodeJSON(raw, &c); err != nil {
			return nil, badRequest("ERR the body must be a JSON array, e.g. [\"SET\", \"key\", \"value\"]")
		}
		if args, err = toArgs(c); err != nil {
			return nil, err
		}
	} else {
		// Path style: /set/key/value, query parameters as more arguments
		// (?EX=60), a POST body as the last one.
		for _, seg := range strings.Split(path, "/") {
			s, err := url.PathUnescape(seg)
			if err != nil {
				return nil, badRequest("ERR bad path")
			}
			args = append(args, s)
		}
		for k, vs := range r.URL.Query() {
			if k == "_token" {
				continue
			}
			for _, v := range vs {
				args = append(args, k)
				if v != "" {
					args = append(args, v)
				}
			}
		}
		if len(raw) > 0 {
			args = append(args, string(raw))
		}
	}
	res, err := h.run(ctx, project, readOnly, args)
	if err != nil {
		return nil, err
	}
	if re, ok := res.(RedisError); ok {
		return nil, badRequest("%s", string(re))
	}
	return map[string]any{"result": encodeReply(unprefixReply(args, project, res), b64, true)}, nil
}

// decodeJSON keeps numbers as written (no float64 rounding).
func decodeJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

// toArgs turns a JSON command into its arguments. @upstash/redis sends
// strings, numbers and booleans.
func toArgs(c []any) ([]string, error) {
	if len(c) == 0 {
		return nil, badRequest("ERR empty command")
	}
	out := make([]string, len(c))
	for i, v := range c {
		switch x := v.(type) {
		case string:
			out[i] = x
		case json.Number:
			out[i] = x.String()
		case bool:
			out[i] = strconv.FormatBool(x)
		case nil:
			out[i] = ""
		default:
			return nil, badRequest("ERR argument %d is not a string or number", i+1)
		}
	}
	return out, nil
}

// auth checks "Authorization: Bearer <token>" (or ?_token=) and returns the
// project and whether the token is the read-only one.
func (h *rest) auth(r *http.Request) (string, bool, error) {
	unauthorized := &restError{http.StatusUnauthorized, "Unauthorized"}
	tok := r.URL.Query().Get("_token")
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		tok = strings.TrimSpace(a[7:])
	}
	rest, ro := strings.CutPrefix(tok, "tvkro_")
	if !ro {
		var ok bool
		if rest, ok = strings.CutPrefix(tok, "tvk_"); !ok {
			return "", false, unauthorized
		}
	}
	i := strings.LastIndexByte(rest, '_')
	if i < 0 || !slugRe.MatchString(rest[:i]) {
		return "", false, unauthorized
	}
	project := rest[:i]
	c, err := h.cred(r.Context(), project)
	if err != nil {
		return "", false, err
	}
	want := c.token
	if ro {
		want = c.ro
	}
	if c.pw == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		return "", false, unauthorized
	}
	return project, ro, nil
}

func (h *rest) cred(ctx context.Context, project string) (restCred, error) {
	h.mu.Lock()
	c, ok := h.creds[project]
	h.mu.Unlock()
	if ok && time.Since(c.at) < 30*time.Second {
		return c, nil
	}
	pw, ok, err := h.secret(ctx, project)
	if err != nil {
		return c, err
	}
	c = restCred{at: time.Now()}
	if ok {
		c.pw = pw
		c.token, c.ro = restTokens(project, pw)
	}
	h.mu.Lock()
	h.creds[project] = c
	h.mu.Unlock()
	return c, nil
}

// conn takes a connection for project (an idle one when there is one).
func (h *rest) conn(ctx context.Context, project string) (*Client, *restProject, bool, error) {
	h.mu.Lock()
	pp := h.pools[project]
	if pp == nil {
		pp = &restProject{slots: make(chan struct{}, restMaxConns)}
		h.pools[project] = pp
	}
	h.mu.Unlock()
	select {
	case pp.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, false, ctx.Err()
	}
	pp.mu.Lock()
	if n := len(pp.idle); n > 0 {
		c := pp.idle[n-1]
		pp.idle = pp.idle[:n-1]
		pp.mu.Unlock()
		return c, pp, true, nil
	}
	pp.mu.Unlock()
	cr, err := h.cred(ctx, project)
	if err == nil && cr.pw == "" {
		err = &restError{http.StatusUnauthorized, "Unauthorized"}
	}
	var c *Client
	if err == nil {
		c, err = h.dial(ctx, User(project), cr.pw)
	}
	if err != nil {
		<-pp.slots
		return nil, nil, false, err
	}
	return c, pp, false, nil
}

// release returns c to the pool, or closes it after an I/O error.
func (pp *restProject) release(c *Client, broken bool) {
	pp.mu.Lock()
	if broken || len(pp.idle) >= restIdle {
		c.Close()
	} else {
		pp.idle = append(pp.idle, c)
	}
	pp.mu.Unlock()
	<-pp.slots
}

// with runs fn on a connection of project. A pooled connection the server
// closed (a restart) is retried once on a fresh one.
func (h *rest) with(ctx context.Context, project string, fn func(*Client) error) error {
	for attempt := 0; ; attempt++ {
		c, pp, reused, err := h.conn(ctx, project)
		if err != nil {
			return err
		}
		err = fn(c)
		broken := err != nil && !answered(err)
		pp.release(c, broken)
		if broken && reused && attempt == 0 && isClosed(err) {
			continue
		}
		if broken {
			err = fmt.Errorf("ERR valkey: %w", err)
		}
		return err
	}
}

// answered reports whether err is an answer (a Valkey error reply or a
// refusal of ours), not a failed connection.
func answered(err error) bool {
	var re RedisError
	var rerr *restError
	var pr *api.Problem
	var cp *datakit.ConfirmProblem
	return errors.As(err, &re) || errors.As(err, &rerr) || errors.As(err, &pr) || errors.As(err, &cp) || errors.Is(err, errWrite)
}

func isClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "broken pipe") ||
		strings.Contains(err.Error(), "connection reset")
}

// run runs one command; a Valkey error reply comes back as a RedisError value.
func (h *rest) run(ctx context.Context, project string, readOnly bool, args []string) (any, error) {
	var out any
	err := h.with(ctx, project, func(c *Client) error {
		cmds, err := h.commands(ctx, c)
		if err != nil {
			return err
		}
		prep, err := prepare(cmds, project, readOnly, args)
		if err != nil {
			return err
		}
		out, err = send(ctx, c, prep)
		return err
	})
	return out, err
}

// batch runs /pipeline (one after another) or /multi-exec (MULTI ... EXEC).
func (h *rest) batch(ctx context.Context, project string, readOnly bool, all [][]string, tx, b64 bool) (any, error) {
	results := make([]map[string]any, len(all))
	item := func(i int, v any) {
		if re, ok := v.(RedisError); ok {
			results[i] = map[string]any{"error": string(re)}
		} else {
			results[i] = map[string]any{"result": encodeReply(unprefixReply(all[i], project, v), b64, true)}
		}
	}
	err := h.with(ctx, project, func(c *Client) error {
		cmds, err := h.commands(ctx, c)
		if err != nil {
			return err
		}
		preps := make([]prepared, len(all))
		for i, args := range all {
			if preps[i], err = prepare(cmds, project, readOnly, args); err != nil {
				return err
			}
		}
		if !tx {
			for i, pr := range preps {
				v, err := send(ctx, c, pr)
				if err != nil {
					return err
				}
				item(i, v)
			}
			return nil
		}
		if _, err := c.Do(ctx, "MULTI"); err != nil {
			return err
		}
		var abort error
		for _, pr := range preps {
			if _, err := c.Do(ctx, pr.args...); err != nil {
				var re RedisError
				if !errors.As(err, &re) {
					return err
				}
				abort = firstErr(abort, err)
			}
		}
		if abort != nil {
			if _, err := c.Do(ctx, "DISCARD"); err != nil {
				return err
			}
			return badRequest("EXECABORT Transaction discarded because of previous errors: %s", abort.Error())
		}
		v, err := c.Do(ctx, "EXEC")
		if err != nil {
			return err
		}
		arr, _ := v.([]any)
		if len(arr) != len(preps) {
			return badRequest("EXECABORT the transaction was aborted")
		}
		for i := range arr {
			item(i, fromScript(preps[i], arr[i]))
		}
		return nil
	})
	var re RedisError
	if errors.As(err, &re) {
		return nil, badRequest("%s", string(re))
	}
	if err != nil {
		return nil, err
	}
	return results, nil
}

func firstErr(first, next error) error {
	if first != nil {
		return first
	}
	return next
}

// prepared is a command ready for Valkey, with the original script's SHA
// when the script was rewritten (see cleanShebang).
type prepared struct {
	args      []string
	scriptSHA string
}

// Commands that would change the connection's state (it is pooled), stream
// replies, or need a session the REST protocol does not have.
var restDenied = map[string]bool{"auth": true, "hello": true, "client": true, "reset": true, "select": true, "quit": true,
	"multi": true, "exec": true, "discard": true, "watch": true, "unwatch": true, "subscribe": true, "psubscribe": true,
	"ssubscribe": true, "unsubscribe": true, "punsubscribe": true, "sunsubscribe": true, "monitor": true, "sync": true,
	"psync": true, "readonly": true, "readwrite": true, "asking": true, "replconf": true}

// prepare checks a command and adds the project prefix to its keys.
func prepare(cmds cmdTable, project string, readOnly bool, in []string) (prepared, error) {
	name := strings.ToLower(in[0])
	if restDenied[name] {
		return prepared{}, badRequest("ERR %s is not available over REST", strings.ToUpper(name))
	}
	info := cmds.lookup(in)
	if readOnly && name != "ping" && name != "echo" && (info == nil || !info.readonly) {
		return prepared{}, badRequest("NOPERM this token is read-only and %s writes", strings.ToUpper(name))
	}
	args := append([]string(nil), in...)
	out := prepared{args: args}
	switch name {
	case "eval", "eval_ro":
		if len(args) > 1 {
			if s, ok := cleanShebang(args[1]); ok {
				out.scriptSHA = remember(args[1], s)
				args[1] = s
			}
		}
	case "evalsha", "evalsha_ro":
		if len(args) > 1 {
			args[1] = scriptSHA(args[1])
		}
	case "script":
		if len(args) > 2 && strings.EqualFold(args[1], "load") {
			if s, ok := cleanShebang(args[2]); ok {
				out.scriptSHA = remember(args[2], s)
				args[2] = s
			}
		} else if len(args) > 2 && strings.EqualFold(args[1], "exists") {
			for i := 2; i < len(args); i++ {
				args[i] = scriptSHA(args[i])
			}
		}
	case "publish", "spublish":
		if len(args) > 1 {
			args[1] = Prefix(project) + args[1]
		}
	}
	if info != nil {
		for _, i := range info.keyPositions(args) {
			args[i] = Prefix(project) + args[i]
		}
	}
	return out, nil
}

// send runs a prepared command; error replies come back as RedisError values.
func send(ctx context.Context, c *Client, pr prepared) (any, error) {
	v, err := c.Do(ctx, pr.args...)
	var re RedisError
	if errors.As(err, &re) {
		return re, nil
	}
	if err != nil {
		return nil, err
	}
	return fromScript(pr, v), nil
}

// fromScript makes SCRIPT LOAD of a rewritten script answer the SHA the
// client computed from its own text.
func fromScript(pr prepared, v any) any {
	if _, ok := v.(string); ok && pr.scriptSHA != "" && strings.EqualFold(pr.args[0], "script") {
		return pr.scriptSHA
	}
	return v
}

// unprefixReply strips the prefix from the replies that name keys.
func unprefixReply(args []string, project string, v any) any {
	pre := Prefix(project)
	strip := func(x any) any {
		if s, ok := x.(string); ok {
			return strings.TrimPrefix(s, pre)
		}
		return x
	}
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return v
	}
	switch strings.ToLower(args[0]) {
	case "blpop", "brpop", "bzpopmin", "bzpopmax", "lmpop", "blmpop", "zmpop", "bzmpop":
		arr[0] = strip(arr[0])
	case "xread", "xreadgroup":
		for _, s := range arr {
			if pair, ok := s.([]any); ok && len(pair) > 0 {
				pair[0] = strip(pair[0])
			}
		}
	}
	return arr
}

// encodeReply turns a RESP reply into JSON values. With b64 every string is
// base64 except a top-level "OK", as Upstash does.
func encodeReply(v any, b64, top bool) any {
	switch x := v.(type) {
	case string:
		if !b64 || (top && x == "OK") {
			return x
		}
		return base64.StdEncoding.EncodeToString([]byte(x))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = encodeReply(e, b64, false)
		}
		return out
	case RedisError:
		return string(x)
	}
	return v
}

// ---- Lua shebang flags ----

// Script flags Valkey knows. @upstash/ratelimit marks its scripts
// "#!lua flags=allow-key-locking" (a Dragonfly flag), which Valkey refuses.
var knownScriptFlags = map[string]bool{"no-writes": true, "allow-oom": true, "allow-stale": true, "no-cluster": true, "allow-cross-slot-keys": true}

// cleanShebang drops script flags Valkey does not know. ok is false when the
// script needs no change.
func cleanShebang(script string) (string, bool) {
	if !strings.HasPrefix(script, "#!") {
		return script, false
	}
	line, body, _ := strings.Cut(script, "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "#!lua" {
		return script, false
	}
	changed := false
	out := []string{"#!lua"}
	for _, f := range fields[1:] {
		list, ok := strings.CutPrefix(f, "flags=")
		if !ok {
			out = append(out, f)
			continue
		}
		var keep []string
		for _, fl := range strings.Split(list, ",") {
			if fl == "" || knownScriptFlags[fl] {
				if fl != "" {
					keep = append(keep, fl)
				}
				continue
			}
			changed = true
		}
		if len(keep) > 0 {
			out = append(out, "flags="+strings.Join(keep, ","))
		}
	}
	if !changed {
		return script, false
	}
	return strings.Join(out, " ") + "\n" + body, true
}

// scripts maps the SHA1 of a script as the client wrote it to the SHA1 of
// the text Valkey runs, so EVALSHA with the client's own SHA finds it.
var scripts = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func sha1hex(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func remember(orig, cleaned string) string {
	o := sha1hex(orig)
	scripts.Lock()
	if len(scripts.m) > 4096 {
		scripts.m = map[string]string{}
	}
	scripts.m[o] = sha1hex(cleaned)
	scripts.Unlock()
	return o
}

func scriptSHA(sha string) string {
	scripts.Lock()
	defer scripts.Unlock()
	if s, ok := scripts.m[strings.ToLower(sha)]; ok {
		return s
	}
	return sha
}

// ---- key positions, from the server's key specs ----

type keySpec struct {
	begin                   string // "index", "keyword" or "unknown"
	index, startFrom        int
	keyword                 string
	find                    string // "range", "keynum" or "unknown"
	lastKey, keyStep, limit int
	keyNumIdx, firstKey     int
}

type cmdInfo struct {
	readonly bool
	admin    bool // in @admin or @dangerous: never for a project
	specs    []keySpec
	sub      map[string]*cmdInfo
}

type cmdTable map[string]*cmdInfo

func (t cmdTable) lookup(args []string) *cmdInfo {
	c := t[strings.ToLower(args[0])]
	if c != nil && c.sub != nil && len(args) > 1 {
		if s := c.sub[strings.ToLower(args[1])]; s != nil {
			return s
		}
	}
	return c
}

// commands loads the command table once, from COMMAND.
func (h *rest) commands(ctx context.Context, c *Client) (cmdTable, error) {
	h.mu.Lock()
	t := h.cmds
	h.mu.Unlock()
	if t != nil {
		return t, nil
	}
	v, err := c.Do(ctx, "COMMAND")
	if err != nil {
		return nil, err
	}
	t = parseCommands(v)
	h.mu.Lock()
	h.cmds = t
	h.mu.Unlock()
	return t, nil
}

// parseCommands reads a COMMAND reply: per command [name, arity, flags,
// first, last, step, categories, tips, key specs, subcommands].
func parseCommands(v any) cmdTable {
	t := cmdTable{}
	list, _ := v.([]any)
	for _, e := range list {
		if name, c := parseCommand(e); c != nil {
			t[name] = c
		}
	}
	return t
}

func parseCommand(e any) (string, *cmdInfo) {
	a, _ := e.([]any)
	if len(a) < 3 {
		return "", nil
	}
	name, _ := a[0].(string)
	name = strings.ToLower(name)
	if _, sub, ok := strings.Cut(name, "|"); ok {
		name = sub
	}
	c := &cmdInfo{}
	flags, _ := a[2].([]any)
	for _, f := range flags {
		if f == "readonly" {
			c.readonly = true
		}
	}
	if len(a) > 6 {
		cats, _ := a[6].([]any)
		for _, cat := range cats {
			if cat == "@admin" || cat == "@dangerous" {
				c.admin = true
			}
		}
	}
	if len(a) > 8 {
		specs, _ := a[8].([]any)
		for _, s := range specs {
			c.specs = append(c.specs, parseKeySpec(s))
		}
	}
	if len(a) > 9 {
		subs, _ := a[9].([]any)
		for _, s := range subs {
			if n, sc := parseCommand(s); sc != nil {
				if c.sub == nil {
					c.sub = map[string]*cmdInfo{}
				}
				c.sub[n] = sc
			}
		}
	}
	return name, c
}

func pairs(v any) map[string]any {
	a, _ := v.([]any)
	m := map[string]any{}
	for i := 0; i+1 < len(a); i += 2 {
		if k, ok := a[i].(string); ok {
			m[k] = a[i+1]
		}
	}
	return m
}

func num(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func parseKeySpec(v any) keySpec {
	m := pairs(v)
	bs, fk := pairs(m["begin_search"]), pairs(m["find_keys"])
	s := keySpec{begin: fmt.Sprint(bs["type"]), find: fmt.Sprint(fk["type"])}
	b, f := pairs(bs["spec"]), pairs(fk["spec"])
	s.index, s.startFrom = num(b["index"]), num(b["startfrom"])
	s.keyword, _ = b["keyword"].(string)
	s.lastKey, s.keyStep, s.limit = num(f["lastkey"]), num(f["keystep"]), num(f["limit"])
	s.keyNumIdx, s.firstKey = num(f["keynumidx"]), num(f["firstkey"])
	return s
}

// keyPositions returns the indexes of args that are key names, the way
// Valkey reads its key specs.
func (c *cmdInfo) keyPositions(args []string) []int {
	argc := len(args)
	seen := map[int]bool{}
	var out []int
	for _, s := range c.specs {
		first := -1
		switch s.begin {
		case "index":
			first = s.index
		case "keyword":
			if s.startFrom > 0 {
				for i := s.startFrom; i < argc; i++ {
					if strings.EqualFold(args[i], s.keyword) {
						first = i + 1
						break
					}
				}
			} else {
				for i := argc + s.startFrom; i > 0 && i < argc; i-- {
					if strings.EqualFold(args[i], s.keyword) {
						first = i + 1
						break
					}
				}
			}
		}
		if first < 1 {
			continue
		}
		step, last := max(s.keyStep, 1), 0
		switch s.find {
		case "range":
			switch {
			case s.lastKey >= 0:
				last = first + s.lastKey
			case s.limit <= 1:
				last = argc + s.lastKey
			default:
				last = first + (argc-first)/s.limit + s.lastKey
			}
		case "keynum":
			if first+s.keyNumIdx >= argc {
				continue
			}
			n, err := strconv.Atoi(args[first+s.keyNumIdx])
			if err != nil || n < 0 {
				continue
			}
			first += s.firstKey
			last = first + (n-1)*step
		default:
			continue
		}
		for i := first; i <= last && i < argc; i += step {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	return out
}
