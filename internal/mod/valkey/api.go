package valkey

import (
	"context"
	"fmt"
	"net/http"
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
		AOFEnabled  bool   `json:"aofEnabled"`
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

// KeyValue is a key with a preview of its value.
type KVKeyValue struct {
	KVKeyInfo
	Length      int64 `json:"length" doc:"String length in bytes, or number of fields/elements/members/entries"`
	MemoryBytes int64 `json:"memoryBytes"`
	Value       any   `json:"value" doc:"Preview: a string (first 4 KB), an object of fields, an array of elements, or an array of [member, score] / [id, fields]"`
	Truncated   bool  `json:"truncated" doc:"The preview shows only part of the value"`
}

// Connection is a project's Valkey URL. Owner only.
type KVConnection struct {
	RedisURL  string `json:"redisUrl" doc:"redis:// URL with the project's password (127.0.0.1, inside the box)"`
	SocketURL string `json:"socketUrl" doc:"The same over the unix socket"`
	Prefix    string `json:"prefix"`
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
		pr := api.NewProblem(409, "precondition", "project "+project+" has no valkey service")
		pr.Hint = "add services.valkey to tiffin.config.ts and apply"
		return pr
	}
	return nil
}

// RegisterAPI adds the KV operations.
func (*Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "valkey"
	st := api.Op("kv-stats", http.MethodGet, "/v1/projects/{project}/kv/stats", "kv stats", api.RiskRead,
		"Show a project's KV usage", "Keys and memory under the project's prefix, against its maxMemoryMB, plus server-wide numbers.", tag)
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

	kg := api.Op("kv-get", http.MethodGet, "/v1/projects/{project}/kv/key", "kv get", api.RiskRead,
		"Show a key's value", "A key's type, TTL, size and a preview of its value. The key may be given with or without the project prefix.", tag)
	kg.Errors = append(kg.Errors, 404, 409)
	huma.Register(a, api.Untrusted(kg), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Key     string `query:"key" required:"true" minLength:"1" maxLength:"1024" doc:"The key"`
	}) (*struct{ Body *KVKeyValue }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		out, err := GetKey(ctx, in.Project, in.Key)
		return &struct{ Body *KVKeyValue }{out}, err
	}))

	cn := api.Op("kv-connection", http.MethodGet, "/v1/projects/{project}/kv/connection", "kv connection", api.RiskRead,
		"Show the KV connection URL", "The project's REDIS_URL, including its password, for valkey-cli or a client inside the box. Box owner only; every reveal is audited.", tag)
	cn.Errors = append(cn.Errors, 409)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body *KVConnection }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: revealing KV passwords needs the box owner's token", tokens.ErrForbidden)
		}
		if err := ready(ctx, p, in.Project); err != nil {
			return nil, err
		}
		tcp, err := ConnEnv(ctx, p, in.Project, false)
		if err != nil {
			return nil, err
		}
		sock, _ := ConnEnv(ctx, p, in.Project, true)
		_ = p.DB.Audit(ctx, pr.TokenID, "valkey.connection_reveal", in.Project, map[string]any{"session": pr.Session})
		return &struct{ Body *KVConnection }{&KVConnection{RedisURL: tcp["REDIS_URL"], SocketURL: sock["REDIS_URL"], Prefix: Prefix(in.Project)}}, nil
	}))
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
	if out.Keys, out.MemoryBytes, out.Approximate, err = prefixUsage(ctx, c, out.Prefix); err != nil {
		return nil, err
	}
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
		for _, k := range keys {
			typ, _ := c.String(ctx, "TYPE", k)
			ttl, _ := c.Int(ctx, "PTTL", k)
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

// GetKey previews a key's value.
func GetKey(ctx context.Context, project, key string) (*KVKeyValue, error) {
	prefix := Prefix(project)
	if !strings.HasPrefix(key, prefix) {
		key = prefix + key
	}
	c, err := Admin(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	typ, err := c.String(ctx, "TYPE", key)
	if err != nil {
		return nil, err
	}
	if typ == "none" {
		return nil, api.NewProblem(404, "not_found", "no key "+key)
	}
	out := &KVKeyValue{KVKeyInfo: KVKeyInfo{Key: key, Type: typ}}
	out.TTLMs, _ = c.Int(ctx, "PTTL", key)
	out.MemoryBytes, _ = c.Int(ctx, "MEMORY", "USAGE", key)
	const n = 50
	switch typ {
	case "string":
		out.Length, _ = c.Int(ctx, "STRLEN", key)
		v, _ := c.String(ctx, "GETRANGE", key, "0", "4095")
		out.Value, out.Truncated = v, out.Length > 4096
	case "hash":
		out.Length, _ = c.Int(ctx, "HLEN", key)
		v, err := c.Do(ctx, "HSCAN", key, "0", "COUNT", strconv.Itoa(n))
		if err != nil {
			return nil, err
		}
		_, items := scanReply(v)
		m := map[string]string{}
		for i := 0; i+1 < len(items); i += 2 {
			m[items[i]] = items[i+1]
		}
		out.Value, out.Truncated = m, int64(len(m)) < out.Length
	case "list":
		out.Length, _ = c.Int(ctx, "LLEN", key)
		v, err := c.Do(ctx, "LRANGE", key, "0", strconv.Itoa(n-1))
		if err != nil {
			return nil, err
		}
		out.Value, out.Truncated = v, out.Length > n
	case "set":
		out.Length, _ = c.Int(ctx, "SCARD", key)
		v, err := c.Do(ctx, "SSCAN", key, "0", "COUNT", strconv.Itoa(n))
		if err != nil {
			return nil, err
		}
		_, items := scanReply(v)
		out.Value, out.Truncated = items, int64(len(items)) < out.Length
	case "zset":
		out.Length, _ = c.Int(ctx, "ZCARD", key)
		v, err := c.Do(ctx, "ZRANGE", key, "0", strconv.Itoa(n-1), "WITHSCORES")
		if err != nil {
			return nil, err
		}
		arr, _ := v.([]any)
		pairs := [][2]any{}
		for i := 0; i+1 < len(arr); i += 2 {
			score, _ := strconv.ParseFloat(fmt.Sprint(arr[i+1]), 64)
			pairs = append(pairs, [2]any{arr[i], score})
		}
		out.Value, out.Truncated = pairs, out.Length > n
	case "stream":
		out.Length, _ = c.Int(ctx, "XLEN", key)
		v, err := c.Do(ctx, "XREVRANGE", key, "+", "-", "COUNT", "10")
		if err != nil {
			return nil, err
		}
		out.Value, out.Truncated = v, out.Length > 10
	}
	return out, nil
}
