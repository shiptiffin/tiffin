package valkey

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// KVKeyArg is in every one-key write.
type KVKeyArg struct {
	Key     string `json:"key" minLength:"1" maxLength:"1024" doc:"The key, as your apps name it (without the project prefix)"`
	Confirm string `json:"confirm,omitempty" doc:"Only for a write too big to undo: the confirm value from its 428 reply"`
}

// KVNewKey is in the writes that can make a key.
type KVNewKey struct {
	Create     bool  `json:"create,omitempty" doc:"Make a new key: refused when one with this name exists"`
	TTLSeconds int64 `json:"ttlSeconds,omitempty" minimum:"0" doc:"Also expire the key after this many seconds (it becomes cache). 0 leaves its expiry as it is; a new key is then kept until deleted."`
}

// KVScored is a sorted-set member and its score.
type KVScored struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

// KVField is one field of a stream entry.
type KVField struct {
	Field string `json:"field" minLength:"1"`
	Value string `json:"value"`
}

type writeIn[B any] struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Body    B
}

const undoNote = " Runs as the project's own KV user, so its prefix and cache limit apply. The reply's undo id puts the key back as it was for an hour; " +
	"a write too big to keep a copy of (over 1 MB) answers 428 first and then needs full access and the confirm value."

func ms(sec int64) string { return strconv.FormatInt(sec*1000, 10) }

func score(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// expire adds the expiry a KVNewKey asks for.
func (n KVNewKey) expire(w kvWrite, k string) kvWrite {
	if n.TTLSeconds > 0 {
		w.cmds = append(w.cmds, []string{"PEXPIRE", k, ms(n.TTLSeconds)})
	}
	w.create = n.Create
	return w
}

func one(op, k string, cmds ...[]string) kvWrite {
	return kvWrite{op: op, keys: []string{k}, cmds: cmds}
}

func exists(w kvWrite) kvWrite { w.exist = true; return w }

// allowFor lets a write go ahead: one that can be undone needs
// apply:reversible (checked first), one that can't needs apply:irreversible.
func allowFor(pr *tokens.Principal, project string) func(bool) error {
	return func(undoable bool) error {
		if undoable {
			return nil
		}
		return pr.Require(tokens.ScopeApplyIrreversible, project)
	}
}

func auditWrite(ctx context.Context, p *platform.Platform, pr *tokens.Principal, project, op string, res *KVWriteResult) {
	if p == nil || p.DB == nil || res == nil {
		return
	}
	keys := res.Keys
	if len(keys) > 20 {
		keys = keys[:20]
	}
	_ = p.DB.Audit(ctx, pr.TokenID, "valkey."+op, project, map[string]any{"keys": keys, "count": len(res.Keys), "undo": res.Undo, "session": pr.Session})
}

// writeOp registers one write: build turns the body into commands.
func writeOp[B any](a huma.API, p *platform.Platform, id, path, cli, risk, summary, desc string, build func(project string, b *B) (kvWrite, error)) {
	o := api.Op(id, http.MethodPost, "/v1/projects/{project}/kv/"+path, cli, risk, summary, desc, "valkey")
	o.Errors = append(o.Errors, 404, 409, 428)
	o.MaxBodyBytes = 8 << 20
	huma.Register(a, o, api.Wrap(func(ctx context.Context, in *writeIn[B]) (*struct{ Body *KVWriteResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		w, err := build(in.Project, &in.Body)
		if err != nil {
			return nil, err
		}
		res, err := conns(p).write(ctx, in.Project, w, allowFor(pr, in.Project))
		if err != nil {
			return nil, err
		}
		auditWrite(ctx, p, pr, in.Project, w.op, res)
		return &struct{ Body *KVWriteResult }{res}, nil
	}))
}

type setBody struct {
	KVKeyArg
	KVNewKey
	Value string `json:"value" doc:"The text (JSON is text too)"`
}

type hashSetBody struct {
	KVKeyArg
	KVNewKey
	Fields map[string]string `json:"fields" minProperties:"1" doc:"Fields to set, with their values"`
}

type hashDelBody struct {
	KVKeyArg
	Fields []string `json:"fields" minItems:"1" doc:"Fields to delete"`
}

type listPushBody struct {
	KVKeyArg
	KVNewKey
	Values []string `json:"values" minItems:"1" doc:"Items to add, in order"`
	Head   bool     `json:"head,omitempty" doc:"Add at the head (left) instead of the tail"`
}

type listSetBody struct {
	KVKeyArg
	Index int64  `json:"index" doc:"Position from the head, 0 first (negative counts from the tail)"`
	Value string `json:"value"`
}

type listRemoveBody struct {
	KVKeyArg
	Index int64 `json:"index" doc:"Position from the head, 0 first (negative counts from the tail)"`
}

type setAddBody struct {
	KVKeyArg
	KVNewKey
	Members []string `json:"members" minItems:"1"`
}

type setRemoveBody struct {
	KVKeyArg
	Members []string `json:"members" minItems:"1"`
}

type zsetAddBody struct {
	KVKeyArg
	KVNewKey
	Members []KVScored `json:"members" minItems:"1" doc:"Members with their scores; an existing member gets the new score"`
}

type zsetRemoveBody struct {
	KVKeyArg
	Members []string `json:"members" minItems:"1"`
}

type zsetIncrBody struct {
	KVKeyArg
	Member string  `json:"member"`
	By     float64 `json:"by" doc:"Added to the score (negative subtracts)"`
}

type streamAddBody struct {
	KVKeyArg
	KVNewKey
	ID     string    `json:"id,omitempty" pattern:"^(\\*|[0-9]+(-[0-9*]+)?)$" doc:"Entry id; * (the default) lets the server pick the next one"`
	Fields []KVField `json:"fields" minItems:"1" doc:"The entry's fields, in order"`
}

type streamTrimBody struct {
	KVKeyArg
	MaxLen int64 `json:"maxLen" minimum:"0" doc:"Keep this many newest entries"`
}

type streamDelBody struct {
	KVKeyArg
	IDs []string `json:"ids" minItems:"1" doc:"Entry ids to delete"`
}

type expireBody struct {
	KVKeyArg
	TTLSeconds int64 `json:"ttlSeconds" minimum:"0" doc:"Expire after this many seconds (cache); 0 keeps the key until it is deleted"`
}

type renameBody struct {
	KVKeyArg
	To string `json:"to" minLength:"1" maxLength:"1024" doc:"The new name, as your apps name keys; refused when that key exists"`
}

type delBody struct {
	Keys    []string `json:"keys" minItems:"1" maxItems:"1000" doc:"Keys to delete, as your apps name them"`
	Confirm string   `json:"confirm,omitempty" doc:"Only for a delete too big to undo: the confirm value from its 428 reply"`
}

func registerWrites(a huma.API, p *platform.Platform, tag string) {
	writeOp(a, p, "kv-set", "set", "kv set", api.RiskWrite, "Set a key's text value",
		"Sets a string key (text or JSON). Changing an existing key keeps its expiry unless ttlSeconds is given."+undoNote, buildSet)

	writeOp(a, p, "kv-hash-set", "hash/set", "kv hash set", api.RiskWrite, "Set fields of a hash", "Sets one or more fields of a hash key, making it if needed."+undoNote, buildHashSet)

	writeOp(a, p, "kv-hash-delete", "hash/delete", "kv hash delete", api.RiskWrite, "Delete fields of a hash", "Deletes fields from a hash key (the key goes with its last field)."+undoNote, buildHashDel)

	writeOp(a, p, "kv-list-push", "list/push", "kv list push", api.RiskWrite, "Add items to a list", "Adds items to the tail of a list key (or the head), making it if needed."+undoNote, buildListPush)

	writeOp(a, p, "kv-list-set", "list/set", "kv list set", api.RiskWrite, "Change a list item", "Replaces the item at a position of a list key."+undoNote, buildListSet)

	writeOp(a, p, "kv-list-remove", "list/remove", "kv list remove", api.RiskWrite, "Remove a list item", "Removes the item at a position of a list key."+undoNote, buildListRemove)

	writeOp(a, p, "kv-set-add", "set/add", "kv set add", api.RiskWrite, "Add members to a set", "Adds members to a set key, making it if needed."+undoNote, buildSetAdd)

	writeOp(a, p, "kv-set-remove", "set/remove", "kv set remove", api.RiskWrite, "Remove members of a set", "Removes members from a set key."+undoNote, buildSetRemove)

	writeOp(a, p, "kv-zset-add", "zset/add", "kv zset add", api.RiskWrite, "Add or rescore sorted-set members", "Adds members to a sorted set key, or sets their score."+undoNote, buildZsetAdd)

	writeOp(a, p, "kv-zset-remove", "zset/remove", "kv zset remove", api.RiskWrite, "Remove sorted-set members", "Removes members from a sorted set key."+undoNote, buildZsetRemove)

	writeOp(a, p, "kv-zset-incr", "zset/incr", "kv zset incr", api.RiskWrite, "Add to a member's score", "Adds to a sorted-set member's score (a new member starts at 0)."+undoNote, buildZsetIncr)

	writeOp(a, p, "kv-stream-add", "stream/add", "kv stream add", api.RiskWrite, "Add a stream entry", "Appends an entry to a stream key, making it if needed."+undoNote, buildStreamAdd)

	writeOp(a, p, "kv-stream-trim", "stream/trim", "kv stream trim", api.RiskWrite, "Trim a stream", "Drops a stream key's oldest entries, keeping the newest maxLen."+undoNote, buildStreamTrim)

	writeOp(a, p, "kv-stream-delete", "stream/delete", "kv stream delete", api.RiskWrite, "Delete stream entries", "Deletes entries from a stream key."+undoNote, buildStreamDel)

	writeOp(a, p, "kv-expire", "expire", "kv expire", api.RiskWrite, "Set or clear a key's expiry",
		"Gives a key an expiry (it becomes cache, dropped first when memory runs short) or, with ttlSeconds 0, keeps it until deleted."+undoNote, buildExpire)

	writeOp(a, p, "kv-rename", "rename", "kv rename", api.RiskWrite, "Rename a key", "Renames a key within the project, keeping its value and expiry."+undoNote, buildRename)

	writeOp(a, p, "kv-delete", "delete", "kv delete", api.RiskWrite, "Delete keys", "Deletes keys and their values."+undoNote, buildDel)

	dp := api.Op("kv-delete-prefix", http.MethodPost, "/v1/projects/{project}/kv/delete-prefix", "kv delete-prefix", api.RiskDestructive,
		"Delete every key under a prefix",
		"Deletes every key whose name starts with prefix (\"session:\"). Two steps: without confirm nothing changes and the reply is 428 with the count, "+
			"a few of the keys and whether it can be undone; repeat with the confirm value to delete. Up to 1 MB and 5,000 keys can be undone; more needs full access.", tag)
	dp.Errors = append(dp.Errors, 404, 409, 428)
	dp.Extensions[api.ExtConfirm] = true
	huma.Register(a, dp, api.Wrap(func(ctx context.Context, in *writeIn[struct {
		Prefix  string `json:"prefix" minLength:"1" maxLength:"1024" doc:"Keys starting with this, as your apps name them"`
		Confirm string `json:"confirm,omitempty" doc:"The confirm value from the 428 reply"`
	}]) (*struct{ Body *KVWriteResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		res, err := conns(p).deletePrefix(ctx, in.Project, in.Body.Prefix, in.Body.Confirm, allowFor(pr, in.Project))
		if err != nil {
			return nil, err
		}
		auditWrite(ctx, p, pr, in.Project, "delete-prefix", res)
		return &struct{ Body *KVWriteResult }{res}, nil
	}))

	un := api.Op("kv-undo", http.MethodPost, "/v1/projects/{project}/kv/undo", "kv undo", api.RiskWrite,
		"Undo a KV write",
		"Puts back what a write changed, from the copy taken before it (for an hour, once). Refused when one of its keys changed since. "+
			"The reply has its own undo id, to redo.", tag)
	un.Errors = append(un.Errors, 404, 409, 428)
	huma.Register(a, un, api.Wrap(func(ctx context.Context, in *writeIn[struct {
		ID      string `json:"id" pattern:"^kvu_[0-9a-f]{20}$" doc:"The undo id from the write"`
		Confirm string `json:"confirm,omitempty" doc:"Only when the keys are now too big to keep a copy of: the confirm value from the 428 reply"`
	}]) (*struct{ Body *KVWriteResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		res, err := conns(p).undo(ctx, in.Project, in.Body.ID, in.Body.Confirm, allowFor(pr, in.Project))
		if err != nil {
			return nil, err
		}
		auditWrite(ctx, p, pr, in.Project, "undo", res)
		return &struct{ Body *KVWriteResult }{res}, nil
	}))

	registerConsole(a, p, tag)
}

func buildSet(project string, b *setBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	cmd := []string{"SET", k, b.Value}
	switch {
	case b.TTLSeconds > 0:
		cmd = append(cmd, "PX", ms(b.TTLSeconds))
	case !b.Create:
		cmd = append(cmd, "KEEPTTL")
	}
	w := one("set", k, cmd)
	w.create = b.Create
	return w, nil
}

func buildHashSet(project string, b *hashSetBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	cmd := []string{"HSET", k}
	names := make([]string, 0, len(b.Fields))
	for f := range b.Fields {
		names = append(names, f)
	}
	slices.Sort(names)
	for _, f := range names {
		cmd = append(cmd, f, b.Fields[f])
	}
	return b.expire(one("hash-set", k, cmd), k), nil
}

func buildHashDel(project string, b *hashDelBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return exists(one("hash-delete", k, append([]string{"HDEL", k}, b.Fields...))), nil
}

func buildListPush(project string, b *listPushBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	cmd := append([]string{"RPUSH", k}, b.Values...)
	if b.Head {
		// LPUSH adds one at a time, so reversed they end up in the order given.
		cmd = []string{"LPUSH", k}
		for i := len(b.Values) - 1; i >= 0; i-- {
			cmd = append(cmd, b.Values[i])
		}
	}
	return b.expire(one("list-push", k, cmd), k), nil
}

func buildListSet(project string, b *listSetBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return exists(one("list-set", k, []string{"LSET", k, strconv.FormatInt(b.Index, 10), b.Value})), nil
}

func buildListRemove(project string, b *listRemoveBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	// Lists remove by value, so the item is marked first, then the mark removed.
	mark := "\x00tiffin:removed:" + newUndoID()
	return exists(one("list-remove", k, []string{"LSET", k, strconv.FormatInt(b.Index, 10), mark}, []string{"LREM", k, "1", mark})), nil
}

func buildSetAdd(project string, b *setAddBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return b.expire(one("set-add", k, append([]string{"SADD", k}, b.Members...)), k), nil
}

func buildSetRemove(project string, b *setRemoveBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return exists(one("set-remove", k, append([]string{"SREM", k}, b.Members...))), nil
}

func buildZsetAdd(project string, b *zsetAddBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	cmd := []string{"ZADD", k}
	for _, m := range b.Members {
		cmd = append(cmd, score(m.Score), m.Member)
	}
	return b.expire(one("zset-add", k, cmd), k), nil
}

func buildZsetRemove(project string, b *zsetRemoveBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return exists(one("zset-remove", k, append([]string{"ZREM", k}, b.Members...))), nil
}

func buildZsetIncr(project string, b *zsetIncrBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return one("zset-incr", k, []string{"ZINCRBY", k, score(b.By), b.Member}), nil
}

func buildStreamAdd(project string, b *streamAddBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	id := b.ID
	if id == "" {
		id = "*"
	}
	cmd := []string{"XADD", k, id}
	for _, f := range b.Fields {
		cmd = append(cmd, f.Field, f.Value)
	}
	return b.expire(one("stream-add", k, cmd), k), nil
}

func buildStreamTrim(project string, b *streamTrimBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return exists(one("stream-trim", k, []string{"XTRIM", k, "MAXLEN", "=", strconv.FormatInt(b.MaxLen, 10)})), nil
}

func buildStreamDel(project string, b *streamDelBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	return exists(one("stream-delete", k, append([]string{"XDEL", k}, b.IDs...))), nil
}

func buildExpire(project string, b *expireBody) (kvWrite, error) {
	k := fullKey(project, b.Key)
	if b.TTLSeconds == 0 {
		return exists(one("persist", k, []string{"PERSIST", k})), nil
	}
	return exists(one("expire", k, []string{"PEXPIRE", k, ms(b.TTLSeconds)})), nil
}

func buildRename(project string, b *renameBody) (kvWrite, error) {
	k, to := fullKey(project, b.Key), fullKey(project, b.To)
	if k == to {
		return kvWrite{}, api.NewProblem(400, "bad_request", "the new name is the same as the old one")
	}
	w := exists(kvWrite{op: "rename", keys: []string{k, to}, cmds: [][]string{{"RENAMENX", k, to}}})
	w.done = func(r []any) error {
		if n, _ := r[0].(int64); n == 0 {
			return api.NewProblem(409, "conflict", "a key named "+b.To+" already exists")
		}
		return nil
	}
	return w, nil
}

func buildDel(project string, b *delBody) (kvWrite, error) {
	w := kvWrite{op: "delete", confirm: b.Confirm, cmds: [][]string{{"UNLINK"}}}
	for _, k := range b.Keys {
		w.keys = append(w.keys, fullKey(project, k))
	}
	w.cmds[0] = append(w.cmds[0], w.keys...)
	w.done = func(r []any) error {
		if n, _ := r[0].(int64); n == 0 {
			return api.NewProblem(404, "not_found", "none of those keys exist")
		}
		return nil
	}
	return w, nil
}

// deletePrefix deletes every key under prefix, after a confirm that names
// how many. It can be undone when the keys are small enough to copy.
func (h *rest) deletePrefix(ctx context.Context, project, prefix, confirm string, allow func(bool) error) (*KVWriteResult, error) {
	c, err := h.admin(ctx)
	if err != nil {
		return nil, err
	}
	var keys []string
	var bytes int64
	err = scanAll(ctx, c, Prefix(project)+globEscape(prefix)+"*", "", 1_000_000, func(ks []string) error {
		keys = append(keys, ks...)
		if len(keys) > undoMaxKeys || len(ks) == 0 {
			return nil
		}
		cmds := make([][]string, len(ks))
		for i, k := range ks {
			cmds[i] = []string{"MEMORY", "USAGE", k}
		}
		r, err := c.Pipe(ctx, cmds...)
		for _, x := range r {
			n, _ := x.(int64)
			bytes += n
		}
		return err
	}, nil)
	c.Close()
	if err != nil {
		return nil, err
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	if len(keys) == 0 {
		return nil, api.NewProblem(404, "not_found", "no keys start with "+prefix)
	}
	undoable := len(keys) <= undoMaxKeys && bytes <= undoMaxBytes
	preview := map[string]any{"prefix": prefix, "count": len(keys), "examples": shortKeys(project, keys[:min(5, len(keys))]), "undo": undoable,
		"deletes": fmt.Sprintf("%d keys starting with %q", len(keys), prefix)}
	if err := datakit.RequireConfirm(confirm, []any{"delete-prefix", project, prefix}, preview); err != nil {
		return nil, err
	}
	w := kvWrite{op: "delete-prefix", keys: keys, confirmed: true}
	for i := 0; i < len(keys); i += 1000 {
		w.cmds = append(w.cmds, append([]string{"UNLINK"}, keys[i:min(len(keys), i+1000)]...))
	}
	if !undoable {
		w.noUndo = true
	}
	return h.write(ctx, project, w, allow)
}
