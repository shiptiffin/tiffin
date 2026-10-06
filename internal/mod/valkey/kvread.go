package valkey

// Reading for the key browser: one level of keys at a time (grouped by ":"
// like folders, with counts) and a key's value a page at a time. These run
// as the box's admin user (a project user may not SCAN), always under the
// project's own prefix.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
)

// KVGroup is the keys further down one branch of the tree.
type KVGroup struct {
	Prefix string `json:"prefix" doc:"The branch, as apps name keys, ending in the delimiter (\"session:\")"`
	Keys   int64  `json:"keys" doc:"Keys under it, at every depth"`
}

// KVTree is one level of a project's keys.
type KVTree struct {
	Prefix  string      `json:"prefix" doc:"The level shown (\"\" for the top)"`
	Groups  []KVGroup   `json:"groups" doc:"Branches below this level, by name"`
	Keys    []KVKeyInfo `json:"keys" doc:"One page of the keys at this level, by name (key includes the project prefix)"`
	Total   int64       `json:"total" doc:"Keys at this level, on every page"`
	Next    int         `json:"next,omitempty" doc:"Offset of the next page; absent on the last"`
	Scanned int64       `json:"scanned" doc:"Keys under this level that matched"`
	Partial bool        `json:"partial" doc:"There were too many keys to look at them all in time: counts are at least these"`
}

type treeQuery struct {
	prefix, delimiter, match, typ, expiry string
	offset, limit                         int
}

// tree lists one level of the project's keys: branches with counts, then a
// page of the keys themselves with their type and expiry.
func tree(ctx context.Context, c *Client, project string, q treeQuery) (*KVTree, error) {
	pre := Prefix(project)
	out := &KVTree{Prefix: q.prefix, Groups: []KVGroup{}, Keys: []KVKeyInfo{}}
	groups := map[string]int64{}
	seen := map[string]bool{}
	var leaves []string
	err := scanAll(ctx, c, pre+globEscape(q.prefix)+"*", q.typ, 2_000_000, func(keys []string) error {
		var shorts []string
		for _, k := range keys {
			s := strings.TrimPrefix(k, pre)
			if !seen[s] && (q.match == "" || globMatch(q.match, s)) {
				seen[s] = true
				shorts = append(shorts, s)
			}
		}
		if q.expiry != "" && len(shorts) > 0 {
			cmds := make([][]string, len(shorts))
			for i, s := range shorts {
				cmds[i] = []string{"PTTL", pre + s}
			}
			r, err := c.Pipe(ctx, cmds...)
			if err != nil {
				return err
			}
			kept := shorts[:0]
			for i, s := range shorts {
				ttl, _ := r[i].(int64)
				if ttl == -2 || (q.expiry == "kept") != (ttl == -1) {
					continue
				}
				kept = append(kept, s)
			}
			shorts = kept
		}
		for _, s := range shorts {
			out.Scanned++
			rest := s[len(q.prefix):]
			if i := strings.Index(rest, q.delimiter); q.delimiter != "" && i >= 0 {
				groups[q.prefix+rest[:i+len(q.delimiter)]]++
				continue
			}
			leaves = append(leaves, s)
		}
		return nil
	}, &out.Partial)
	if err != nil {
		return nil, err
	}
	for g, n := range groups {
		out.Groups = append(out.Groups, KVGroup{Prefix: g, Keys: n})
	}
	sort.Slice(out.Groups, func(i, j int) bool { return natLess(out.Groups[i].Prefix, out.Groups[j].Prefix) })
	sort.Slice(leaves, func(i, j int) bool { return natLess(leaves[i], leaves[j]) })
	out.Total = int64(len(leaves))
	if q.limit <= 0 {
		q.limit = 500
	}
	if q.offset > len(leaves) {
		q.offset = len(leaves)
	}
	page := leaves[q.offset:min(len(leaves), q.offset+q.limit)]
	if q.offset+len(page) < len(leaves) {
		out.Next = q.offset + len(page)
	}
	cmds := make([][]string, 0, 2*len(page))
	for _, s := range page {
		cmds = append(cmds, []string{"TYPE", pre + s}, []string{"PTTL", pre + s})
	}
	r, err := c.Pipe(ctx, cmds...)
	if err != nil {
		return nil, err
	}
	for i, s := range page {
		typ, _ := r[2*i].(string)
		ttl, _ := r[2*i+1].(int64)
		if typ == "none" {
			continue // expired meanwhile
		}
		out.Keys = append(out.Keys, KVKeyInfo{Key: pre + s, Type: typ, TTLMs: ttl})
	}
	return out, nil
}

// scanAll calls fn with each page of keys matching pattern (and of type typ,
// when given), until the scan ends, max keys were seen or 5 seconds passed
// (partial is then set).
func scanAll(ctx context.Context, c *Client, pattern, typ string, max int64, fn func([]string) error, partial *bool) error {
	cursor := "0"
	var n int64
	deadline := time.Now().Add(5 * time.Second)
	for {
		args := []string{"SCAN", cursor, "MATCH", pattern, "COUNT", "1000"}
		if typ != "" {
			args = append(args, "TYPE", typ)
		}
		v, err := c.Do(ctx, args...)
		if err != nil {
			return err
		}
		next, keys := scanReply(v)
		cursor = next
		n += int64(len(keys))
		if err := fn(keys); err != nil {
			return err
		}
		if cursor == "0" {
			return nil
		}
		if n >= max || time.Now().After(deadline) {
			if partial != nil {
				*partial = true
			}
			return nil
		}
	}
}

type valueQuery struct {
	cursor, match string
	count         int
}

// getKey returns a key's type, expiry, size and one page of its value:
// sorted sets highest score first, streams newest first.
func getKey(ctx context.Context, c *Client, project, key string, q valueQuery) (*KVKeyValue, error) {
	key = fullKey(project, key)
	n := q.count
	if n <= 0 {
		n = 50
	}
	r, err := c.Pipe(ctx, []string{"TYPE", key}, []string{"PTTL", key}, []string{"MEMORY", "USAGE", key})
	if err != nil {
		return nil, err
	}
	typ, _ := r[0].(string)
	if typ == "none" || typ == "" {
		return nil, api.NewProblem(404, "not_found", "no key "+strings.TrimPrefix(key, Prefix(project)))
	}
	out := &KVKeyValue{KVKeyInfo: KVKeyInfo{Key: key, Type: typ}}
	out.TTLMs, _ = r[1].(int64)
	out.MemoryBytes, _ = r[2].(int64)
	off, _ := strconv.Atoi(q.cursor)
	from, to := strconv.Itoa(off), strconv.Itoa(off+n-1)
	// scan pages a hash, set or (filtered) sorted set by cursor.
	scan := func(cmd string) ([]string, error) {
		cur := q.cursor
		if cur == "" {
			cur = "0"
		}
		args := []string{cmd, key, cur, "COUNT", strconv.Itoa(n)}
		if q.match != "" {
			args = append(args, "MATCH", q.match)
		}
		v, err := c.Do(ctx, args...)
		if err != nil {
			return nil, err
		}
		next, items := scanReply(v)
		if next != "0" {
			out.Cursor = next
		}
		return items, nil
	}
	switch typ {
	case "string":
		const most = 1 << 20
		out.Length, _ = c.Int(ctx, "STRLEN", key)
		v, err := c.String(ctx, "GETRANGE", key, "0", strconv.Itoa(most-1))
		if err != nil {
			return nil, err
		}
		out.Value, out.Truncated = v, out.Length > most
	case "hash":
		out.Length, _ = c.Int(ctx, "HLEN", key)
		items, err := scan("HSCAN")
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		for i := 0; i+1 < len(items); i += 2 {
			m[items[i]] = items[i+1]
		}
		out.Value = m
	case "set":
		out.Length, _ = c.Int(ctx, "SCARD", key)
		items, err := scan("SSCAN")
		if err != nil {
			return nil, err
		}
		out.Value = items
	case "list":
		out.Length, _ = c.Int(ctx, "LLEN", key)
		v, err := c.Do(ctx, "LRANGE", key, from, to)
		if err != nil {
			return nil, err
		}
		out.Value = v
		if int64(off+n) < out.Length {
			out.Cursor = strconv.Itoa(off + n)
		}
	case "zset":
		out.Length, _ = c.Int(ctx, "ZCARD", key)
		var flat []string
		if q.match != "" {
			if flat, err = scan("ZSCAN"); err != nil {
				return nil, err
			}
		} else {
			v, err := c.Do(ctx, "ZRANGE", key, from, to, "REV", "WITHSCORES")
			if err != nil {
				return nil, err
			}
			for _, x := range asList(v) {
				flat = append(flat, fmt.Sprint(x))
			}
			if int64(off+n) < out.Length {
				out.Cursor = strconv.Itoa(off + n)
			}
		}
		pairs := [][2]any{}
		for i := 0; i+1 < len(flat); i += 2 {
			score, _ := strconv.ParseFloat(flat[i+1], 64)
			pairs = append(pairs, [2]any{flat[i], score})
		}
		out.Value = pairs
	case "stream":
		out.Length, _ = c.Int(ctx, "XLEN", key)
		end := "+"
		if q.cursor != "" {
			end = "(" + q.cursor
		}
		v, err := c.Do(ctx, "XREVRANGE", key, end, "-", "COUNT", strconv.Itoa(n))
		if err != nil {
			return nil, err
		}
		entries := asList(v)
		out.Value = entries
		if len(entries) == n {
			if last, _ := entries[n-1].([]any); len(last) > 0 {
				out.Cursor = fmt.Sprint(last[0])
			}
		}
	}
	out.Truncated = out.Truncated || out.Cursor != ""
	return out, nil
}

func asList(v any) []any {
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}

// globEscape quotes s for a MATCH pattern.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[]\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// globMatch is Valkey's MATCH: * ? [abc] [^a] [a-z] and \ to quote.
func globMatch(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for len(p) > 1 && p[1] == '*' {
				p = p[1:]
			}
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if globMatch(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
			s = s[1:]
		case '[':
			if s == "" {
				return false
			}
			i, not, hit := 1, false, false
			if i < len(p) && p[i] == '^' {
				not, i = true, i+1
			}
			for ; i < len(p) && p[i] != ']'; i++ {
				switch {
				case p[i] == '\\' && i+1 < len(p):
					i++
					hit = hit || p[i] == s[0]
				case i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']':
					lo, hi := p[i], p[i+2]
					if lo > hi {
						lo, hi = hi, lo
					}
					hit = hit || (s[0] >= lo && s[0] <= hi)
					i += 2
				default:
					hit = hit || p[i] == s[0]
				}
			}
			if hit == not {
				return false
			}
			s, p = s[1:], p[min(i, len(p)-1):]
		case '\\':
			if len(p) > 1 {
				p = p[1:]
			}
			fallthrough
		default:
			if s == "" || p[0] != s[0] {
				return false
			}
			s = s[1:]
		}
		p = p[1:]
	}
	return s == ""
}

// natLess orders names the way people do: "item2" before "item10".
func natLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digits(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}
