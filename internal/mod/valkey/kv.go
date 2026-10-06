package valkey

// Writes from the dashboard, the CLI and agents (kv-set, kv-hash-set, ...,
// kv-command). Every write runs as the project's own ACL user over the same
// pooled connections as the REST endpoint, so its key prefix, the commands
// it may run and its cache limit apply exactly as they do to its apps.
//
// Undo: before a write, the keys it touches are copied (up to 1 MB in all);
// afterwards each key's DUMP is fingerprinted. kv-undo checks the keys still
// match those fingerprints (nothing wrote to them since), then puts the
// copies back with ordinary commands in one MULTI/EXEC (RESTORE is not a
// project's to run). Undo is itself a write, so it can be undone too. A
// write too big to copy (or a stream with consumer groups, which ordinary
// commands can't rebuild) needs a confirm instead. Copies live in memory
// for an hour, 64 MB at most across projects.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
)

const (
	undoMaxBytes = 1 << 20 // copied per write, at most
	undoMaxKeys  = 5000    // keys copied per write, at most
	undoKeep     = time.Hour
	undoTotal    = 64 << 20 // every project's copies together
)

// KVWriteResult is what a write did.
type KVWriteResult struct {
	Keys    []string `json:"keys" doc:"The keys it touched, as your apps name them (without the project prefix)"`
	Undo    string   `json:"undo,omitempty" doc:"Pass to kv-undo within an hour to put back what this write changed. Empty when the value was too big to keep a copy of."`
	Replies []any    `json:"replies,omitempty" doc:"Valkey's reply to each command it ran"`
}

// kvSnap is a key's whole value, enough to put it back with ordinary commands.
type kvSnap struct {
	key      string
	typ      string // "none" when the key did not exist
	expireAt int64  // unix ms; 0 when it never expires
	str      string
	items    []string   // hash: field, value...; list and set: items; zset: member, score...
	entries  [][]string // stream: id, field, value...
	size     int64
}

// snapshot copies key. ok is false when the copy would be over limit bytes
// or can't be put back with ordinary commands; the type is known either way.
func snapshot(ctx context.Context, c *Client, key string, limit int64) (s kvSnap, ok bool, err error) {
	s.key = key
	r, err := c.Pipe(ctx, []string{"TYPE", key}, []string{"PTTL", key}, []string{"MEMORY", "USAGE", key})
	if err != nil {
		return s, false, err
	}
	if e, isErr := r[0].(RedisError); isErr {
		return s, false, e
	}
	s.typ, _ = r[0].(string)
	if s.typ == "none" {
		return s, true, nil
	}
	if ttl, _ := r[1].(int64); ttl > 0 {
		s.expireAt = time.Now().UnixMilli() + ttl
	}
	if n, _ := r[2].(int64); n > limit {
		return s, false, nil
	}
	var v any
	switch s.typ {
	case "string":
		v, err = c.Do(ctx, "GET", key)
	case "hash":
		v, err = c.Do(ctx, "HGETALL", key)
	case "list":
		v, err = c.Do(ctx, "LRANGE", key, "0", "-1")
	case "set":
		v, err = c.Do(ctx, "SMEMBERS", key)
	case "zset":
		v, err = c.Do(ctx, "ZRANGE", key, "0", "-1", "WITHSCORES")
	case "stream":
		var info any
		if info, err = c.Do(ctx, "XINFO", "STREAM", key); err != nil {
			return s, false, err
		}
		if num(pairs(info)["groups"]) > 0 {
			return s, false, nil
		}
		v, err = c.Do(ctx, "XRANGE", key, "-", "+")
	default:
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	if s.typ == "string" {
		s.str, _ = v.(string)
		s.size = int64(len(s.str))
		return s, s.size <= limit, nil
	}
	arr, _ := v.([]any)
	for _, x := range arr {
		if s.typ != "stream" {
			str := fmt.Sprint(x)
			s.items = append(s.items, str)
			s.size += int64(len(str))
			continue
		}
		e, _ := x.([]any)
		if len(e) != 2 {
			continue
		}
		entry := []string{fmt.Sprint(e[0])}
		fields, _ := e[1].([]any)
		for _, f := range fields {
			entry = append(entry, fmt.Sprint(f))
		}
		for _, f := range entry {
			s.size += int64(len(f))
		}
		s.entries = append(s.entries, entry)
	}
	return s, s.size <= limit, nil
}

// restore returns the commands that put s back as it was.
func (s kvSnap) restore() [][]string {
	out := [][]string{{"DEL", s.key}}
	chunk := func(cmd string, items []string, step int) {
		const per = 1000
		for i := 0; i < len(items); i += per * step {
			end := min(len(items), i+per*step)
			out = append(out, append([]string{cmd, s.key}, items[i:end]...))
		}
	}
	switch s.typ {
	case "none":
		return out
	case "string":
		out = append(out, []string{"SET", s.key, s.str})
	case "hash":
		chunk("HSET", s.items, 2)
	case "list":
		chunk("RPUSH", s.items, 1)
	case "set":
		chunk("SADD", s.items, 1)
	case "zset":
		swapped := make([]string, 0, len(s.items))
		for i := 0; i+1 < len(s.items); i += 2 {
			swapped = append(swapped, s.items[i+1], s.items[i])
		}
		chunk("ZADD", swapped, 2)
	case "stream":
		for _, e := range s.entries {
			out = append(out, append([]string{"XADD", s.key}, e...))
		}
	}
	if s.expireAt > 0 {
		// An expiry that has passed meanwhile deletes the key again, as it should.
		out = append(out, []string{"PEXPIREAT", s.key, strconv.FormatInt(s.expireAt, 10)})
	}
	return out
}

// fingerprints hashes each key's DUMP ("" for a key that does not exist).
func fingerprints(ctx context.Context, c *Client, keys []string) ([]string, error) {
	cmds := make([][]string, len(keys))
	for i, k := range keys {
		cmds[i] = []string{"DUMP", k}
	}
	r, err := c.Pipe(ctx, cmds...)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, v := range r {
		switch x := v.(type) {
		case RedisError:
			return nil, x
		case string:
			sum := sha256.Sum256([]byte(x))
			out[i] = hex.EncodeToString(sum[:8])
		}
	}
	return out, nil
}

// tx runs cmds, in one MULTI/EXEC when there is more than one, and returns
// their replies. The first error reply is the error.
func tx(ctx context.Context, c *Client, cmds [][]string) ([]any, error) {
	if len(cmds) == 1 {
		v, err := c.Do(ctx, cmds[0]...)
		return []any{v}, err
	}
	all := append(append([][]string{{"MULTI"}}, cmds...), []string{"EXEC"})
	r, err := c.Pipe(ctx, all...)
	if err != nil {
		return nil, err
	}
	for _, v := range r[1 : len(r)-1] {
		if e, ok := v.(RedisError); ok {
			return nil, e // queued with an error: EXEC aborted the lot
		}
	}
	if e, ok := r[len(r)-1].(RedisError); ok {
		return nil, e
	}
	out, _ := r[len(r)-1].([]any)
	for _, v := range out {
		if e, ok := v.(RedisError); ok {
			return out, e
		}
	}
	return out, nil
}

// ---- undo records ----

type kvUndo struct {
	id, project string
	at          time.Time
	snaps       []kvSnap
	after       []string // fingerprint of each key right after the write
	size        int64
}

type undoStore struct {
	mu    sync.Mutex
	m     map[string]*kvUndo
	order []string
	size  int64
}

var undos = &undoStore{m: map[string]*kvUndo{}}

func (u *undoStore) put(r *kvUndo) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.m[r.id] = r
	u.order = append(u.order, r.id)
	u.size += r.size
	for len(u.order) > 0 {
		old := u.m[u.order[0]]
		if old != nil && u.size <= undoTotal && time.Since(old.at) < undoKeep {
			break
		}
		if old != nil {
			u.size -= old.size
			delete(u.m, old.id)
		}
		u.order = u.order[1:]
	}
}

func (u *undoStore) get(project, id string) (*kvUndo, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	r, ok := u.m[id]
	if !ok || r.project != project || time.Since(r.at) > undoKeep {
		return nil, false
	}
	return r, true
}

func (u *undoStore) drop(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if r, ok := u.m[id]; ok {
		u.size -= r.size
		delete(u.m, id)
	}
}

func newUndoID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "kvu_" + hex.EncodeToString(b[:])
}

// ---- the write ----

// kvWrite is one write: the keys it touches and the commands that do it.
type kvWrite struct {
	op      string     // for the audit log and the confirm value
	keys    []string   // full keys (with the project prefix)
	cmds    [][]string // full commands
	create  bool       // keys[0] must not exist yet
	exist   bool       // keys[0] must exist
	confirm string     // the caller's confirm value, for a write that can't be undone
	// confirmed: the caller already had it confirmed (kv-delete-prefix).
	confirmed bool
	noUndo    bool // don't copy the keys: too many
	raw       bool // return Valkey's own error replies (the console)
	// check sees what is there before anything changes (undo: nothing wrote since).
	check func(ctx context.Context, c *Client, snaps []kvSnap) error
	// done checks the replies, e.g. that the key existed.
	done func(replies []any) error
}

// write runs w as the project's user. allow says whether the caller may make
// a write that can (true) or cannot (false) be undone.
func (h *rest) write(ctx context.Context, project string, w kvWrite, allow func(undoable bool) error) (*KVWriteResult, error) {
	var res *KVWriteResult
	err := h.with(ctx, project, func(c *Client) error {
		undoable := !w.noUndo && len(w.keys) <= undoMaxKeys
		var snaps []kvSnap
		var total int64
		for i, k := range w.keys {
			if !undoable && i > 0 && w.check == nil {
				break // too much to copy: no need to look at the rest
			}
			limit := int64(undoMaxBytes) - total
			if !undoable {
				limit = 0
			}
			s, ok, err := snapshot(ctx, c, k, limit)
			if err != nil {
				return err
			}
			undoable = undoable && ok
			total += s.size
			snaps = append(snaps, s)
		}
		if w.create && len(snaps) > 0 && snaps[0].typ != "none" {
			return api.NewProblem(409, "conflict", "a key named "+strings.TrimPrefix(w.keys[0], Prefix(project))+" already exists")
		}
		if w.exist && len(snaps) > 0 && snaps[0].typ == "none" {
			return api.NewProblem(404, "not_found", "no key "+strings.TrimPrefix(w.keys[0], Prefix(project)))
		}
		if w.check != nil {
			if err := w.check(ctx, c, snaps); err != nil {
				return err
			}
		}
		if allow != nil {
			if err := allow(undoable); err != nil {
				return err
			}
		}
		if !undoable && !w.confirmed {
			preview := map[string]any{"keys": shortKeys(project, w.keys[:min(5, len(w.keys))]), "undo": false,
				"why": fmt.Sprintf("this changes more than %s of data, too much to keep a copy for Undo", mbWords(undoMaxBytes))}
			if err := datakit.RequireConfirm(w.confirm, []any{w.op, w.keys}, preview); err != nil {
				return err
			}
		}
		replies, err := tx(ctx, c, w.cmds)
		if err != nil {
			return err
		}
		if w.done != nil {
			if err := w.done(replies); err != nil {
				return err
			}
		}
		res = &KVWriteResult{Keys: shortKeys(project, w.keys), Replies: replies}
		if !undoable {
			return nil
		}
		after, err := fingerprints(ctx, c, w.keys)
		if err != nil {
			return err
		}
		r := &kvUndo{id: newUndoID(), project: project, at: time.Now(), snaps: snaps, after: after, size: total}
		undos.put(r)
		res.Undo = r.id
		return nil
	})
	if err != nil {
		if w.raw {
			return nil, err
		}
		return nil, kvErr(project, err)
	}
	return res, nil
}

// undo puts back what write id changed, if nothing wrote to its keys since.
func (h *rest) undo(ctx context.Context, project, id, confirm string, allow func(bool) error) (*KVWriteResult, error) {
	r, ok := undos.get(project, id)
	if !ok {
		pr := api.NewProblem(404, "not_found", "there is nothing to undo with "+id)
		pr.Hint = "a write can be undone for an hour, once"
		return nil, pr
	}
	w := kvWrite{op: "undo", confirm: confirm}
	for _, s := range r.snaps {
		w.keys = append(w.keys, s.key)
		w.cmds = append(w.cmds, s.restore()...)
	}
	w.check = func(ctx context.Context, c *Client, _ []kvSnap) error {
		now, err := fingerprints(ctx, c, w.keys)
		if err != nil {
			return err
		}
		for i := range now {
			if now[i] != r.after[i] {
				pr := api.NewProblem(409, "conflict", strings.TrimPrefix(w.keys[i], Prefix(project))+" changed after that edit, so Undo would overwrite the newer value")
				pr.Hint = "reload the key and change it by hand"
				return pr
			}
		}
		return nil
	}
	res, err := h.write(ctx, project, w, allow)
	if err == nil {
		undos.drop(id)
	}
	return res, err
}

// kvErr turns a Valkey error reply into a problem that says what to do.
func kvErr(project string, err error) error {
	var re RedisError
	if !errors.As(err, &re) {
		return err
	}
	msg := string(re)
	var pr *api.Problem
	switch {
	case strings.HasPrefix(msg, "NOPERM") && limits.held(project):
		pr = api.NewProblem(409, "conflict", "writes are refused while "+project+"'s KV is over its memory limit; reads and deletes still work")
		pr.Hint = "delete keys or give them an expiry, or give the project a bigger cache limit"
	case strings.HasPrefix(msg, "NOPERM"):
		pr = api.NewProblem(403, "forbidden", "the project's KV user may not do that: "+msg)
	case strings.HasPrefix(msg, "OOM"):
		pr = api.NewProblem(409, "conflict", "the box's KV memory is full, so keys that never expire can't be added")
		pr.Hint = "delete keys or give them an expiry"
	case strings.HasPrefix(msg, "WRONGTYPE"):
		pr = api.NewProblem(409, "conflict", "that key holds a different type of value")
	default:
		pr = api.NewProblem(400, "bad_request", strings.TrimPrefix(msg, "ERR "))
	}
	return pr
}

func shortKeys(project string, keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = strings.TrimPrefix(k, Prefix(project))
	}
	return out
}

// fullKey adds the project prefix to a key as apps name it. A key that
// already starts with the prefix is taken as given (as kv-get does).
func fullKey(project, key string) string {
	if p := Prefix(project); !strings.HasPrefix(key, p) {
		return p + key
	}
	return key
}

// ---- the project connections ----

var shared struct {
	once sync.Once
	h    *rest
}

// conns is the pool of project-user connections the REST endpoint and the
// write API share.
func conns(p *platform.Platform) *rest {
	shared.once.Do(func() {
		shared.h = newREST(func(ctx context.Context, project string) (string, bool, error) {
			return datakit.GetSecret(ctx, p, nsPassword, project)
		}, func(ctx context.Context, user, pw string) (*Client, error) {
			return Dial(ctx, "unix", SocketPath, user, pw)
		})
		shared.h.admin = Admin
	})
	return shared.h
}
