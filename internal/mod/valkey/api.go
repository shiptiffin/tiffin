package valkey

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Stats is a project's Valkey usage.
type KVStats struct {
	Prefix      string `json:"prefix" doc:"Every key and pub/sub channel of the project starts with this"`
	Keys        int64  `json:"keys" doc:"Keys under the prefix"`
	KeptKeys    int64  `json:"keptKeys" doc:"Of those, keys without an expiry: kept until deleted, never dropped to make room"`
	CacheKeys   int64  `json:"cacheKeys" doc:"Of those, keys with an expiry: cache, dropped first when memory runs short"`
	MemoryBytes int64  `json:"memoryBytes" doc:"Memory used by those keys (MEMORY USAGE)"`
	Approximate bool   `json:"approximate" doc:"True when there were too many keys to measure each; memory is extrapolated from a sample"`
	MaxMemoryMB int    `json:"maxMemoryMB" doc:"The project's cap from tiffin.config.ts. Enforced while the project has a limit (see enforcedBytes); otherwise only reported."`
	OverCap     bool   `json:"overCap" doc:"Usage is above maxMemoryMB"`
	// EnforcedBytes and WritesRefused: the cache limit the box holds the
	// project to while it has a limit (see limits.go).
	EnforcedBytes int64 `json:"enforcedBytes,omitempty" doc:"While the project has a limit: the cache limit the box holds it to (the smaller of maxMemoryMB and its share of Valkey's memory). Over it, keys with an expiry are cleared first, then writes are refused."`
	WritesRefused bool  `json:"writesRefused,omitempty" doc:"True while its cache is over that limit and writes (but not deletes) are refused"`
	Server        struct {
		Version     string `json:"version"`
		UsedBytes   int64  `json:"usedBytes"`
		MaxBytes    int64  `json:"maxBytes" doc:"Server-wide limit"`
		Policy      string `json:"policy" doc:"Eviction policy"`
		Clients     int64  `json:"clients"`
		LastSaveAt  string `json:"lastSaveAt" doc:"Last RDB snapshot"`
		AOFEnabled  bool   `json:"aofEnabled" doc:"Every write is appended to disk (fsync every second)"`
		TotalKeys   int64  `json:"totalKeys" doc:"Keys in the whole server (all projects)"`
		UptimeHours int64  `json:"uptimeHours"`
	} `json:"server"`
}

// KeyInfo is one key in the browser.
type KVKeyInfo struct {
	Key   string `json:"key"`
	Type  string `json:"type" doc:"string, hash, list, set, zset, stream"`
	TTLMs int64  `json:"ttlMs" doc:"Milliseconds until it expires; -1 when it never does"`
}

// KeyPage is one page of a key scan.
type KVKeyPage struct {
	Cursor string      `json:"cursor" doc:"Pass back as cursor for the next page; \"0\" means the scan is complete"`
	Keys   []KVKeyInfo `json:"keys"`
}

// KeyValue is a key with one page of its value.
type KVKeyValue struct {
	KVKeyInfo
	Length      int64  `json:"length" doc:"String length in bytes, or number of fields/elements/members/entries"`
	MemoryBytes int64  `json:"memoryBytes"`
	Value       any    `json:"value" doc:"One page: a string (up to 1 MB), an object of fields, an array of elements, [member, score] pairs (highest score first) or [id, fields] entries (newest first)"`
	Truncated   bool   `json:"truncated" doc:"This page is not the whole value"`
	Cursor      string `json:"cursor,omitempty" doc:"Pass back as cursor for the next page; absent on the last"`
}

// KVEnv is one variable the project's apps get.
type KVEnv struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty" doc:"Absent for a secret unless revealed"`
	Secret bool   `json:"secret"`
}

// Connection is how to reach a project's KV.
type KVConnection struct {
	Prefix    string  `json:"prefix" doc:"Every key of the project starts with this on REDIS_URL; the REST endpoint adds it for you"`
	User      string  `json:"user" doc:"The project's own user on the server"`
	Host      string  `json:"host"`
	Port      int     `json:"port"`
	RedisURL  string  `json:"redisUrl" doc:"redis:// URL (127.0.0.1, inside the box); the password shows as *** unless revealed"`
	SocketURL string  `json:"socketUrl" doc:"The same over the unix socket"`
	RestURL   string  `json:"restUrl" doc:"The Upstash-compatible REST endpoint, for @upstash/redis and @vercel/kv"`
	Revealed  bool    `json:"revealed" doc:"Passwords and tokens are included"`
	Env       []KVEnv `json:"env" doc:"What every app in the project gets, already set"`
}

type projectIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
}

func ready(ctx context.Context, p *platform.Platform, project string) error {
	if p == nil || p.DB == nil {
		return api.NewProblem(501, "internal", datakit.ErrOffBox.Error())
	}
	ok, err := HasService(ctx, p, project)
	if err != nil {
		return err
	}
	if !ok {
		pr := api.NewProblem(409, "precondition", "project "+project+"'s KV is not set up yet")
		pr.Hint = "every project has KV; it is set up with the project's next apply (or within a minute): check `tiffin projects get " + project + "`"
		return pr
	}
	return nil
}

// reading checks read access and the service, then runs fn on an admin connection.
func reading[O any](ctx context.Context, p *platform.Platform, project string, fn func(c *Client) (O, error)) (*struct{ Body O }, error) {
	if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, project); err != nil {
		return nil, err
	}
	if err := ready(ctx, p, project); err != nil {
		return nil, err
	}
	c, err := Admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out, err := fn(c)
	return &struct{ Body O }{out}, err
}

// RegisterAPI adds the KV operations.
func (*Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "valkey"
	st := api.Op("kv-stats", http.MethodGet, "/v1/projects/{project}/kv/stats", "kv stats", api.RiskRead,
		"Show a project's KV usage", "Keys (kept and cache) and memory under the project's prefix, against its maxMemoryMB, plus server-wide numbers.", tag)
	st.Errors = append(st.Errors, 409)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body *KVStats }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		out, err := GetStats(ctx, p, in.Project)
		return &struct{ Body *KVStats }{out}, err
	}))

	ks := api.Op("kv-keys", http.MethodGet, "/v1/projects/{project}/kv/keys", "kv keys", api.RiskRead,
		"Browse a project's keys", "Scans keys under the project's prefix, a page at a time, with type and TTL.", tag)
	ks.Errors = append(ks.Errors, 409)
	huma.Register(a, api.Untrusted(ks), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Match   string `query:"match" maxLength:"256" doc:"Glob after the prefix, e.g. user:* (default *)"`
		Cursor  string `query:"cursor" pattern:"^[0-9]*$" doc:"From the previous page (default 0)"`
		Count   int    `query:"count" minimum:"1" maximum:"500" default:"100" doc:"Keys per page (approximate)"`
	}) (*struct{ Body *KVKeyPage }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		out, err := ScanKeys(ctx, in.Project, in.Match, in.Cursor, in.Count)
		return &struct{ Body *KVKeyPage }{out}, err
	}))

	kt := api.Op("kv-tree", http.MethodGet, "/v1/projects/{project}/kv/tree", "kv tree", api.RiskRead,
		"Browse a project's keys like folders",
		"One level of the project's keys: the branches below it (keys grouped by their next \":\" part, with counts) and a page of the keys at this level "+
			"with type and TTL, in natural order. Filter by a glob, a type, or kept (no expiry) versus cache (with one).", tag)
	kt.Errors = append(kt.Errors, 409)
	huma.Register(a, api.Untrusted(kt), api.Wrap(func(ctx context.Context, in *struct {
		Project   string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Prefix    string `query:"prefix" maxLength:"1024" doc:"The level to list, as apps name keys (\"session:\"); empty for the top"`
		Delimiter string `query:"delimiter" maxLength:"8" default:":" doc:"What separates levels; \"none\" lists every key flat"`
		Match     string `query:"match" maxLength:"256" doc:"Only keys matching this glob (whole key, e.g. *u_20*)"`
		Type      string `query:"type" enum:"string,hash,list,set,zset,stream," doc:"Only keys of this type"`
		Expiry    string `query:"expiry" enum:"kept,cache," doc:"kept: only keys without an expiry; cache: only keys with one"`
		Offset    int    `query:"offset" minimum:"0" doc:"Where this page of keys starts"`
		Limit     int    `query:"limit" minimum:"1" maximum:"2000" default:"500" doc:"Keys per page"`
	}) (*struct{ Body *KVTree }, error) {
		d := in.Delimiter
		if d == "none" {
			d = ""
		}
		return reading(ctx, p, in.Project, func(c *Client) (*KVTree, error) {
			return tree(ctx, c, in.Project, treeQuery{prefix: in.Prefix, delimiter: d, match: in.Match, typ: in.Type, expiry: in.Expiry, offset: in.Offset, limit: in.Limit})
		})
	}))

	kg := api.Op("kv-get", http.MethodGet, "/v1/projects/{project}/kv/key", "kv get", api.RiskRead,
		"Show a key's value",
		"A key's type, TTL, size and one page of its value: sorted sets highest score first, streams newest first, lists from the head. "+
			"Pass the returned cursor for the next page. The key may be given with or without the project prefix.", tag)
	kg.Errors = append(kg.Errors, 404, 409)
	huma.Register(a, api.Untrusted(kg), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Key     string `query:"key" required:"true" minLength:"1" maxLength:"1024" doc:"The key"`
		Cursor  string `query:"cursor" maxLength:"64" doc:"From the previous page"`
		Count   int    `query:"count" minimum:"1" maximum:"1000" default:"50" doc:"Items per page"`
		Match   string `query:"match" maxLength:"256" doc:"Hashes, sets and sorted sets: only fields or members matching this glob"`
	}) (*struct{ Body *KVKeyValue }, error) {
		return reading(ctx, p, in.Project, func(c *Client) (*KVKeyValue, error) {
			return getKey(ctx, c, in.Project, in.Key, valueQuery{cursor: in.Cursor, count: in.Count, match: in.Match})
		})
	}))

	cn := api.Op("kv-connection", http.MethodGet, "/v1/projects/{project}/kv/connection", "kv connection", api.RiskRead,
		"Show how to connect to a project's KV",
		"The env every app in the project already has (REDIS_URL, VALKEY_PREFIX, the Upstash REST URL and tokens), the project's user and key prefix. "+
			"Secrets show only with reveal=true, which needs full access to the project; every reveal is audited. From your computer: tiffin kv tunnel.", tag)
	cn.Errors = append(cn.Errors, 409)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Reveal  bool   `query:"reveal" doc:"Include the password and tokens"`
	}) (*struct{ Body *KVConnection }, error) {
		pr := api.PrincipalFrom(ctx)
		need := tokens.ScopeRead
		if in.Reveal {
			need = tokens.ScopeApplyIrreversible
		}
		if err := pr.Require(need, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		out, err := connection(ctx, p, in.Project, in.Reveal)
		if err == nil && in.Reveal {
			_ = p.DB.Audit(ctx, pr.TokenID, "valkey.connection_reveal", in.Project, map[string]any{"session": pr.Session})
		}
		return &struct{ Body *KVConnection }{out}, err
	}))

	registerWrites(a, p, tag)
}

// connection describes how to reach the project's KV, secrets only when revealed.
func connection(ctx context.Context, p *platform.Platform, project string, reveal bool) (*KVConnection, error) {
	tcp, err := ConnEnv(ctx, p, project, false)
	if err != nil {
		return nil, err
	}
	sock, err := ConnEnv(ctx, p, project, true)
	if err != nil {
		return nil, err
	}
	rest, err := RESTEnv(ctx, p, project)
	if err != nil {
		return nil, err
	}
	hide := func(raw string) string {
		if reveal {
			return raw
		}
		if u, err := url.Parse(raw); err == nil && u.User != nil {
			return strings.Replace(raw, u.User.String()+"@", u.User.Username()+":***@", 1)
		}
		return raw
	}
	out := &KVConnection{Prefix: Prefix(project), User: User(project), Host: "127.0.0.1", Port: Port, Revealed: reveal,
		RedisURL: hide(tcp["REDIS_URL"]), SocketURL: hide(sock["REDIS_URL"]), RestURL: rest["UPSTASH_REDIS_REST_URL"]}
	secret := map[string]bool{"REDIS_URL": true, "VALKEY_URL": true, "UPSTASH_REDIS_REST_TOKEN": true, "KV_REST_API_TOKEN": true, "KV_REST_API_READ_ONLY_TOKEN": true}
	all := map[string]string{}
	for k, v := range tcp {
		all[k] = v
	}
	for k, v := range rest {
		all[k] = v
	}
	for _, name := range []string{"REDIS_URL", "VALKEY_URL", "VALKEY_PREFIX", "UPSTASH_REDIS_REST_URL", "UPSTASH_REDIS_REST_TOKEN",
		"KV_REST_API_URL", "KV_REST_API_TOKEN", "KV_REST_API_READ_ONLY_TOKEN"} {
		e := KVEnv{Name: name, Secret: secret[name], Value: all[name]}
		if e.Secret && !reveal {
			e.Value = ""
		}
		out.Env = append(out.Env, e)
	}
	return out, nil
}

// GetStats measures a project's keys and memory.
func GetStats(ctx context.Context, p *platform.Platform, project string) (*KVStats, error) {
	c, err := Admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out := &KVStats{Prefix: Prefix(project)}
	if raw, ok, _ := p.DB.KVGet(ctx, nsCap, project); ok {
		out.MaxMemoryMB, _ = strconv.Atoi(string(raw))
	}
	u, err := measure(ctx, c, out.Prefix, true)
	if err != nil {
		return nil, err
	}
	out.Keys, out.MemoryBytes, out.Approximate, out.KeptKeys = u.keys, u.bytes, u.approx, u.kept
	out.CacheKeys = u.keys - u.kept
	out.OverCap = out.MaxMemoryMB > 0 && out.MemoryBytes > int64(out.MaxMemoryMB)<<20
	if l, ok := CacheLimit(project); ok && l.LimitBytes > 0 {
		out.EnforcedBytes, out.WritesRefused = l.LimitBytes, l.WritesRefused
	}
	raw, err := c.String(ctx, "INFO", "everything")
	if err != nil {
		return nil, err
	}
	info := parseInfo(raw)
	s := &out.Server
	s.Version = info["valkey_version"]
	if s.Version == "" {
		s.Version = info["redis_version"]
	}
	s.UsedBytes, _ = strconv.ParseInt(info["used_memory"], 10, 64)
	s.MaxBytes, _ = strconv.ParseInt(info["maxmemory"], 10, 64)
	s.Policy = info["maxmemory_policy"]
	s.Clients, _ = strconv.ParseInt(info["connected_clients"], 10, 64)
	if ts, err := strconv.ParseInt(info["rdb_last_save_time"], 10, 64); err == nil {
		s.LastSaveAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	s.AOFEnabled = info["aof_enabled"] == "1"
	up, _ := strconv.ParseInt(info["uptime_in_seconds"], 10, 64)
	s.UptimeHours = up / 3600
	for k, v := range info {
		if strings.HasPrefix(k, "db") {
			if keys, _, ok := strings.Cut(v, ","); ok {
				n, _ := strconv.ParseInt(strings.TrimPrefix(keys, "keys="), 10, 64)
				s.TotalKeys += n
			}
		}
	}
	return out, nil
}

// ScanKeys returns one page of keys under the project's prefix.
func ScanKeys(ctx context.Context, project, match, cursor string, count int) (*KVKeyPage, error) {
	if match == "" {
		match = "*"
	}
	if cursor == "" {
		cursor = "0"
	}
	if count <= 0 {
		count = 100
	}
	c, err := Admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	pattern := Prefix(project) + strings.TrimPrefix(match, Prefix(project))
	page := &KVKeyPage{Keys: []KVKeyInfo{}}
	for i := 0; i < 50; i++ {
		v, err := c.Do(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(max(count, 100)))
		if err != nil {
			return nil, err
		}
		next, keys := scanReply(v)
		cursor = next
		cmds := make([][]string, 0, 2*len(keys))
		for _, k := range keys {
			cmds = append(cmds, []string{"TYPE", k}, []string{"PTTL", k})
		}
		r, err := c.Pipe(ctx, cmds...)
		if err != nil {
			return nil, err
		}
		for j, k := range keys {
			typ, _ := r[2*j].(string)
			ttl, _ := r[2*j+1].(int64)
			if typ == "none" {
				continue // expired between SCAN and TYPE
			}
			page.Keys = append(page.Keys, KVKeyInfo{Key: k, Type: typ, TTLMs: ttl})
		}
		if cursor == "0" || len(page.Keys) >= count {
			break
		}
	}
	page.Cursor = cursor
	return page, nil
}
