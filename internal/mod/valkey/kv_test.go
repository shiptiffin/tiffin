package valkey

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/datakit"
)

// memValkey is a small in-memory Valkey for the write API: the six types,
// expiries, MULTI/EXEC, DUMP, SCAN, and ACL users that only reach their own
// prefix ("p_app:", "p_other:"; "tiffin" reaches everything). A held user
// has its writes refused, as the cache limit does.
type memValkey struct {
	mu   sync.Mutex
	keys map[string]*memEntry
	held map[string]bool
	seq  int64
	log  [][]string
	ver  map[string]int64 // key → writes to it, for WATCH
	// beforeExec runs (unlocked) when an EXEC arrives, before it runs: a
	// test's chance to write in between.
	beforeExec func()
}

type memEntry struct {
	typ    string
	str    string
	hash   map[string]string
	list   []string
	set    map[string]bool
	zset   map[string]float64
	stream [][]string // id, field, value...
	exp    int64      // unix ms
}

// memCommands: name → write?, first key, last key (-1: to the end), step.
var memCommands = map[string][4]int{
	"get": {0, 1, 1, 1}, "set": {1, 1, 1, 1}, "getrange": {0, 1, 1, 1}, "strlen": {0, 1, 1, 1},
	"hset": {1, 1, 1, 1}, "hdel": {1, 1, 1, 1}, "hgetall": {0, 1, 1, 1}, "hlen": {0, 1, 1, 1}, "hscan": {0, 1, 1, 1},
	"lpush": {1, 1, 1, 1}, "rpush": {1, 1, 1, 1}, "lset": {1, 1, 1, 1}, "lrem": {1, 1, 1, 1}, "lrange": {0, 1, 1, 1}, "llen": {0, 1, 1, 1},
	"sadd": {1, 1, 1, 1}, "srem": {1, 1, 1, 1}, "smembers": {0, 1, 1, 1}, "scard": {0, 1, 1, 1}, "sscan": {0, 1, 1, 1},
	"zadd": {1, 1, 1, 1}, "zrem": {1, 1, 1, 1}, "zincrby": {1, 1, 1, 1}, "zrange": {0, 1, 1, 1}, "zcard": {0, 1, 1, 1}, "zscan": {0, 1, 1, 1},
	"xadd": {1, 1, 1, 1}, "xtrim": {1, 1, 1, 1}, "xdel": {1, 1, 1, 1}, "xrange": {0, 1, 1, 1}, "xrevrange": {0, 1, 1, 1}, "xlen": {0, 1, 1, 1},
	"xinfo": {0, 2, 2, 1}, "memory": {0, 2, 2, 1}, "type": {0, 1, 1, 1}, "pttl": {0, 1, 1, 1}, "pexpiretime": {0, 1, 1, 1}, "dump": {0, 1, 1, 1},
	"pexpire": {1, 1, 1, 1}, "pexpireat": {1, 1, 1, 1}, "persist": {1, 1, 1, 1}, "renamenx": {1, 1, 2, 1},
	"del": {1, 1, -1, 1}, "unlink": {1, 1, -1, 1}, "exists": {0, 1, -1, 1},
	"flushall": {1, 0, 0, 0}, "ping": {0, 0, 0, 0}, "scan": {0, 0, 0, 0}, "command": {0, 0, 0, 0},
}

// commandReply describes memCommands in COMMAND's shape, key specs included.
func (*memValkey) commandReply() []any {
	var out []any
	for name, c := range memCommands {
		flags := []any{"readonly"}
		if c[0] == 1 {
			flags = []any{"write"}
		}
		cats := []any{"@keyspace"}
		if name == "flushall" {
			cats = []any{"@keyspace", "@write", "@dangerous"}
		}
		var specs []any
		if c[1] > 0 {
			last := int64(c[2] - c[1])
			if c[2] < 0 {
				last = -1
			}
			specs = []any{[]any{"flags", []any{}, "begin_search", []any{"type", "index", "spec", []any{"index", int64(c[1])}},
				"find_keys", []any{"type", "range", "spec", []any{"lastkey", last, "keystep", int64(c[3]), "limit", int64(0)}}}}
		}
		out = append(out, []any{name, int64(-1), flags, int64(0), int64(0), int64(0), cats, []any{}, specs, []any{}})
	}
	return out
}

func (m *memValkey) get(k string, typ string) (*memEntry, error) {
	e := m.keys[k]
	if e != nil && e.exp > 0 && e.exp <= time.Now().UnixMilli() {
		delete(m.keys, k)
		e = nil
	}
	if e != nil && typ != "" && e.typ != typ {
		return nil, errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	}
	return e, nil
}

func (m *memValkey) make(k, typ string) *memEntry {
	e, _ := m.get(k, "")
	if e == nil {
		e = &memEntry{typ: typ, hash: map[string]string{}, set: map[string]bool{}, zset: map[string]float64{}}
		m.keys[k] = e
	}
	return e
}

// cleanup drops a collection that became empty, as Valkey does.
func (m *memValkey) cleanup(k string) {
	if e := m.keys[k]; e != nil && e.typ != "string" && e.typ != "stream" && len(e.hash)+len(e.list)+len(e.set)+len(e.zset) == 0 {
		delete(m.keys, k)
	}
}

func (e *memEntry) dump() string {
	var parts []string
	switch e.typ {
	case "string":
		parts = []string{e.str}
	case "hash":
		for f, v := range e.hash {
			parts = append(parts, f+"="+v)
		}
		sort.Strings(parts)
	case "list":
		parts = e.list
	case "set":
		for s := range e.set {
			parts = append(parts, s)
		}
		sort.Strings(parts)
	case "zset":
		for s, sc := range e.zset {
			parts = append(parts, fmt.Sprint(s, sc))
		}
		sort.Strings(parts)
	case "stream":
		for _, en := range e.stream {
			parts = append(parts, strings.Join(en, ","))
		}
	}
	return e.typ + ":" + strings.Join(parts, "|")
}

func (e *memEntry) zsorted() []string {
	var ms []string
	for s := range e.zset {
		ms = append(ms, s)
	}
	sort.Slice(ms, func(i, j int) bool {
		if e.zset[ms[i]] != e.zset[ms[j]] {
			return e.zset[ms[i]] < e.zset[ms[j]]
		}
		return ms[i] < ms[j]
	})
	return ms
}

func streamID(s string) (int64, int64) {
	a, b, _ := strings.Cut(s, "-")
	x, _ := strconv.ParseInt(a, 10, 64)
	y, _ := strconv.ParseInt(b, 10, 64)
	return x, y
}

func idLess(a, b string) bool {
	a1, a2 := streamID(a)
	b1, b2 := streamID(b)
	return a1 < b1 || (a1 == b1 && a2 < b2)
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func entryReply(en []string) any { return []any{en[0], toAny(en[1:])} }

func (m *memValkey) exec(user string, args []string) any {
	m.log = append(m.log, args)
	name := strings.ToLower(args[0])
	c, known := memCommands[name]
	if !known {
		return RedisError("ERR unknown command '" + args[0] + "'")
	}
	if user != "tiffin" {
		if name == "flushall" || name == "scan" {
			return RedisError("NOPERM User " + user + " has no permissions to run the '" + name + "' command")
		}
		if c[0] == 1 && m.held[user] && name != "del" && name != "unlink" && name != "pexpire" {
			return RedisError("NOPERM User " + user + " has no permissions to run the '" + name + "' command")
		}
		if c[1] > 0 {
			last := c[2]
			if last < 0 {
				last = len(args) - 1
			}
			for i := c[1]; i <= last && i < len(args); i += c[3] {
				if !strings.HasPrefix(args[i], user+":") {
					return RedisError("NOPERM No permissions to access a key")
				}
			}
		}
	}
	reply, err := m.run(name, args)
	if err != nil {
		return RedisError(err.Error())
	}
	if c[0] == 1 {
		if m.ver == nil {
			m.ver = map[string]int64{}
		}
		last := c[2]
		if last < 0 {
			last = len(args) - 1
		}
		for i := c[1]; i > 0 && i <= last && i < len(args); i += c[3] {
			m.ver[args[i]]++
		}
		if name == "flushall" {
			for k := range m.ver {
				m.ver[k]++
			}
		}
	}
	return reply
}

func (m *memValkey) run(name string, a []string) (any, error) {
	now := time.Now().UnixMilli()
	k := ""
	if len(a) > 1 {
		k = a[1]
	}
	switch name {
	case "ping":
		return "PONG", nil
	case "command":
		return m.commandReply(), nil
	case "flushall":
		m.keys = map[string]*memEntry{}
		return "OK", nil
	case "type":
		e, _ := m.get(k, "")
		if e == nil {
			return "none", nil
		}
		return e.typ, nil
	case "pttl":
		e, _ := m.get(k, "")
		switch {
		case e == nil:
			return int64(-2), nil
		case e.exp == 0:
			return int64(-1), nil
		}
		return e.exp - now, nil
	case "pexpiretime":
		e, _ := m.get(k, "")
		switch {
		case e == nil:
			return int64(-2), nil
		case e.exp == 0:
			return int64(-1), nil
		}
		return e.exp, nil
	case "memory":
		e, _ := m.get(a[2], "")
		if e == nil {
			return nil, nil
		}
		return int64(len(e.dump()) + 50), nil
	case "dump":
		e, _ := m.get(k, "")
		if e == nil {
			return nil, nil
		}
		return e.dump(), nil
	case "exists":
		n := int64(0)
		for _, x := range a[1:] {
			if e, _ := m.get(x, ""); e != nil {
				n++
			}
		}
		return n, nil
	case "del", "unlink":
		n := int64(0)
		for _, x := range a[1:] {
			if e, _ := m.get(x, ""); e != nil {
				delete(m.keys, x)
				n++
			}
		}
		return n, nil
	case "pexpire", "pexpireat":
		e, _ := m.get(k, "")
		if e == nil {
			return int64(0), nil
		}
		v, _ := strconv.ParseInt(a[2], 10, 64)
		if name == "pexpire" {
			v += now
		}
		e.exp = v
		m.get(k, "") // an expiry in the past deletes it now
		return int64(1), nil
	case "persist":
		e, _ := m.get(k, "")
		if e == nil || e.exp == 0 {
			return int64(0), nil
		}
		e.exp = 0
		return int64(1), nil
	case "renamenx":
		e, _ := m.get(k, "")
		if e == nil {
			return nil, errors.New("ERR no such key")
		}
		if t, _ := m.get(a[2], ""); t != nil {
			return int64(0), nil
		}
		m.keys[a[2]] = e
		delete(m.keys, k)
		return int64(1), nil
	case "scan":
		var out []string
		match, typ := "*", ""
		for i := 2; i+1 < len(a); i += 2 {
			switch strings.ToLower(a[i]) {
			case "match":
				match = a[i+1]
			case "type":
				typ = a[i+1]
			}
		}
		for key := range m.keys {
			if e, _ := m.get(key, ""); e != nil && globMatch(match, key) && (typ == "" || e.typ == typ) {
				out = append(out, key)
			}
		}
		sort.Strings(out)
		return []any{"0", toAny(out)}, nil
	}

	var e *memEntry
	var err error
	typeOf := map[byte]string{'g': "string", 's': "string", 'h': "hash", 'l': "list", 'r': "list", 'z': "zset", 'x': "stream"}
	typ := typeOf[name[0]]
	if strings.HasPrefix(name, "s") && name != "set" && name != "strlen" {
		typ = "set"
	}
	if e, err = m.get(k, typ); err != nil {
		return nil, err
	}
	switch name {
	case "set":
		nx, keep, px := false, false, int64(0)
		for i := 3; i < len(a); i++ {
			switch strings.ToUpper(a[i]) {
			case "NX":
				nx = true
			case "KEEPTTL":
				keep = true
			case "PX":
				px, _ = strconv.ParseInt(a[i+1], 10, 64)
				i++
			}
		}
		old, _ := m.get(k, "")
		if nx && old != nil {
			return nil, nil
		}
		exp := int64(0)
		if keep && old != nil {
			exp = old.exp
		}
		if px > 0 {
			exp = now + px
		}
		m.keys[k] = &memEntry{typ: "string", str: a[2], exp: exp}
		return "OK", nil
	case "get":
		if e == nil {
			return nil, nil
		}
		return e.str, nil
	case "getrange":
		if e == nil {
			return "", nil
		}
		hi, _ := strconv.Atoi(a[3])
		return e.str[:min(len(e.str), hi+1)], nil
	case "strlen":
		if e == nil {
			return int64(0), nil
		}
		return int64(len(e.str)), nil
	case "hset":
		e = m.make(k, "hash")
		n := int64(0)
		for i := 2; i+1 < len(a); i += 2 {
			if _, ok := e.hash[a[i]]; !ok {
				n++
			}
			e.hash[a[i]] = a[i+1]
		}
		return n, nil
	case "hdel":
		n := int64(0)
		if e != nil {
			for _, f := range a[2:] {
				if _, ok := e.hash[f]; ok {
					delete(e.hash, f)
					n++
				}
			}
			m.cleanup(k)
		}
		return n, nil
	case "hgetall", "hscan":
		out := []string{}
		if e != nil {
			var fs []string
			for f := range e.hash {
				fs = append(fs, f)
			}
			sort.Strings(fs)
			for _, f := range fs {
				out = append(out, f, e.hash[f])
			}
		}
		if name == "hscan" {
			return []any{"0", toAny(out)}, nil
		}
		return toAny(out), nil
	case "hlen":
		if e == nil {
			return int64(0), nil
		}
		return int64(len(e.hash)), nil
	case "lpush", "rpush":
		e = m.make(k, "list")
		for _, v := range a[2:] {
			if name == "lpush" {
				e.list = append([]string{v}, e.list...)
			} else {
				e.list = append(e.list, v)
			}
		}
		return int64(len(e.list)), nil
	case "lset":
		if e == nil {
			return nil, errors.New("ERR no such key")
		}
		i, _ := strconv.Atoi(a[2])
		if i < 0 {
			i += len(e.list)
		}
		if i < 0 || i >= len(e.list) {
			return nil, errors.New("ERR index out of range")
		}
		e.list[i] = a[3]
		return "OK", nil
	case "lrem":
		if e == nil {
			return int64(0), nil
		}
		n, _ := strconv.Atoi(a[2])
		removed := int64(0)
		var keep []string
		for _, v := range e.list {
			if v == a[3] && (n == 0 || removed < int64(n)) {
				removed++
				continue
			}
			keep = append(keep, v)
		}
		e.list = keep
		m.cleanup(k)
		return removed, nil
	case "lrange":
		if e == nil {
			return []any{}, nil
		}
		lo, _ := strconv.Atoi(a[2])
		hi, _ := strconv.Atoi(a[3])
		if hi < 0 {
			hi += len(e.list)
		}
		hi = min(hi, len(e.list)-1)
		if lo > hi {
			return []any{}, nil
		}
		return toAny(e.list[lo : hi+1]), nil
	case "llen":
		if e == nil {
			return int64(0), nil
		}
		return int64(len(e.list)), nil
	case "sadd":
		e = m.make(k, "set")
		n := int64(0)
		for _, v := range a[2:] {
			if !e.set[v] {
				e.set[v] = true
				n++
			}
		}
		return n, nil
	case "srem":
		n := int64(0)
		if e != nil {
			for _, v := range a[2:] {
				if e.set[v] {
					delete(e.set, v)
					n++
				}
			}
			m.cleanup(k)
		}
		return n, nil
	case "smembers", "sscan":
		out := []string{}
		if e != nil {
			for v := range e.set {
				out = append(out, v)
			}
			sort.Strings(out)
		}
		if name == "sscan" {
			return []any{"0", toAny(out)}, nil
		}
		return toAny(out), nil
	case "scard":
		if e == nil {
			return int64(0), nil
		}
		return int64(len(e.set)), nil
	case "zadd":
		e = m.make(k, "zset")
		n := int64(0)
		for i := 2; i+1 < len(a); i += 2 {
			sc, err := strconv.ParseFloat(a[i], 64)
			if err != nil {
				return nil, errors.New("ERR value is not a valid float")
			}
			if _, ok := e.zset[a[i+1]]; !ok {
				n++
			}
			e.zset[a[i+1]] = sc
		}
		return n, nil
	case "zrem":
		n := int64(0)
		if e != nil {
			for _, v := range a[2:] {
				if _, ok := e.zset[v]; ok {
					delete(e.zset, v)
					n++
				}
			}
			m.cleanup(k)
		}
		return n, nil
	case "zincrby":
		e = m.make(k, "zset")
		by, _ := strconv.ParseFloat(a[2], 64)
		e.zset[a[3]] += by
		return score(e.zset[a[3]]), nil
	case "zcard":
		if e == nil {
			return int64(0), nil
		}
		return int64(len(e.zset)), nil
	case "zrange", "zscan":
		if e == nil {
			return []any{}, nil
		}
		ms := e.zsorted()
		if name == "zscan" {
			var out []string
			for _, s := range ms {
				out = append(out, s, score(e.zset[s]))
			}
			return []any{"0", toAny(out)}, nil
		}
		rev, with := false, false
		for _, x := range a[4:] {
			rev = rev || strings.EqualFold(x, "REV")
			with = with || strings.EqualFold(x, "WITHSCORES")
		}
		if rev {
			slices.Reverse(ms)
		}
		lo, _ := strconv.Atoi(a[2])
		hi, _ := strconv.Atoi(a[3])
		if hi < 0 {
			hi += len(ms)
		}
		hi = min(hi, len(ms)-1)
		var out []any
		for i := lo; i <= hi; i++ {
			out = append(out, ms[i])
			if with {
				out = append(out, score(e.zset[ms[i]]))
			}
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case "xadd":
		id := a[2]
		var last string
		if e != nil && len(e.stream) > 0 {
			last = e.stream[len(e.stream)-1][0]
		}
		if id == "*" {
			m.seq++
			id = fmt.Sprintf("%d-0", 1700000000000+m.seq)
		}
		if last != "" && !idLess(last, id) {
			return nil, errors.New("ERR The ID specified in XADD is equal or smaller than the target stream top item")
		}
		e = m.make(k, "stream")
		e.stream = append(e.stream, append([]string{id}, a[3:]...))
		return id, nil
	case "xtrim":
		if e == nil {
			return int64(0), nil
		}
		n, _ := strconv.Atoi(a[len(a)-1])
		drop := max(0, len(e.stream)-n)
		e.stream = e.stream[drop:]
		return int64(drop), nil
	case "xdel":
		if e == nil {
			return int64(0), nil
		}
		n := int64(0)
		e.stream = slices.DeleteFunc(e.stream, func(en []string) bool {
			if slices.Contains(a[2:], en[0]) {
				n++
				return true
			}
			return false
		})
		return n, nil
	case "xlen":
		if e == nil {
			return int64(0), nil
		}
		return int64(len(e.stream)), nil
	case "xinfo":
		e, _ = m.get(a[2], "stream")
		if e == nil {
			return nil, errors.New("ERR no such key")
		}
		return []any{"length", int64(len(e.stream)), "groups", int64(0)}, nil
	case "xrange", "xrevrange":
		out := []any{}
		if e == nil {
			return out, nil
		}
		hi, lo := a[2], a[3]
		if name == "xrange" {
			lo, hi = a[2], a[3]
		}
		count := len(e.stream)
		if len(a) > 5 {
			count, _ = strconv.Atoi(a[5])
		}
		in := func(id string) bool {
			if lo != "-" && idLess(id, lo) {
				return false
			}
			if strings.HasPrefix(hi, "(") {
				return idLess(id, hi[1:])
			}
			return hi == "+" || !idLess(hi, id)
		}
		list := slices.Clone(e.stream)
		if name == "xrevrange" {
			slices.Reverse(list)
		}
		for _, en := range list {
			if in(en[0]) && len(out) < count {
				out = append(out, entryReply(en))
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("ERR fake: %s", name)
}

// nilArray is RESP's null array, EXEC's reply when a watched key changed.
type nilArray struct{}

func (m *memValkey) serve(t *testing.T) string {
	sock := filepath.Join(t.TempDir(), "kv.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				user := ""
				var queue [][]string
				inTx, abort := false, false
				watched := map[string]int64{}
				for {
					args, err := readCommand(r)
					if err != nil {
						return
					}
					if strings.EqualFold(args[0], "exec") && m.beforeExec != nil {
						m.beforeExec()
					}
					m.mu.Lock()
					var reply any
					switch strings.ToLower(args[0]) {
					case "auth":
						user, reply = args[1], "OK"
					case "watch":
						for _, k := range args[1:] {
							watched[k] = m.ver[k]
						}
						reply = "OK"
					case "unwatch":
						watched, reply = map[string]int64{}, "OK"
					case "multi":
						inTx, abort, queue, reply = true, false, nil, "OK"
					case "exec":
						raced := false
						for k, v := range watched {
							raced = raced || m.ver[k] != v
						}
						watched = map[string]int64{}
						if abort {
							reply = RedisError("EXECABORT Transaction discarded because of previous errors.")
						} else if raced {
							reply = nilArray{}
						} else {
							out := []any{}
							for _, q := range queue {
								out = append(out, m.exec(user, q))
							}
							reply = out
						}
						inTx = false
					default:
						if inTx {
							if _, ok := memCommands[strings.ToLower(args[0])]; !ok {
								abort, reply = true, RedisError("ERR unknown command")
							} else {
								queue, reply = append(queue, args), "QUEUED"
							}
						} else {
							reply = m.exec(user, args)
						}
					}
					m.mu.Unlock()
					var s string
					if _, ok := reply.(nilArray); ok {
						s = "*-1\r\n"
					} else {
						s = respEncode(reply)
					}
					if x, ok := reply.(string); ok && (x == "OK" || x == "QUEUED" || x == "PONG") {
						s = "+" + x + "\r\n"
					}
					if _, err := io.WriteString(c, s); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return sock
}

func newKV(t *testing.T) (*rest, *memValkey) {
	m := &memValkey{keys: map[string]*memEntry{}, held: map[string]bool{}}
	sock := m.serve(t)
	h := newREST(func(_ context.Context, project string) (string, bool, error) {
		return "pw", project == "app" || project == "other", nil
	}, func(ctx context.Context, user, pw string) (*Client, error) {
		return Dial(ctx, "unix", sock, user, pw)
	})
	h.admin = func(ctx context.Context) (*Client, error) { return Dial(ctx, "unix", sock, "tiffin", "pw") }
	return h, m
}

// value reads a key back through the browser's read path.
func value(t *testing.T, h *rest, key string) any {
	t.Helper()
	c, err := h.admin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	v, err := getKey(context.Background(), c, "app", key, valueQuery{count: 100})
	var pr *api.Problem
	if errors.As(err, &pr) && pr.Status == 404 {
		return nil
	}
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v.Value
}

func run(t *testing.T, h *rest, w kvWrite, err error) *KVWriteResult {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.write(context.Background(), "app", w, nil)
	if err != nil {
		t.Fatalf("%s: %v", w.op, err)
	}
	return res
}

func undo(t *testing.T, h *rest, id string) {
	t.Helper()
	if _, err := h.undo(context.Background(), "app", id, "", nil); err != nil {
		t.Fatalf("undo: %v", err)
	}
}

// Every write op does what it says to the right key, and Undo puts the key
// back exactly as it was (value and expiry).
func TestKVWriteOps(t *testing.T) {
	h, m := newKV(t)
	ctx := context.Background()
	same := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %#v, want %#v", what, got, want)
		}
	}

	// Strings: create (refused when it exists), change keeps the expiry, undo.
	w, err := buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "greeting"}, KVNewKey: KVNewKey{Create: true, TTLSeconds: 60}, Value: "hello"})
	first := run(t, h, w, err)
	same("set", value(t, h, "greeting"), "hello")
	if e := m.keys["p_app:greeting"]; e == nil || e.exp == 0 {
		t.Fatal("ttl not set")
	}
	if _, err := h.write(ctx, "app", w, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create twice: %v", err)
	}
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "greeting"}, Value: `{"a":1}`})
	res := run(t, h, w, nil)
	if m.keys["p_app:greeting"].exp == 0 {
		t.Fatal("changing the value dropped its expiry")
	}
	undo(t, h, res.Undo)
	same("undo set", value(t, h, "greeting"), "hello")
	undo(t, h, first.Undo) // the key did not exist before
	same("undo create", value(t, h, "greeting"), nil)

	// Hashes.
	w, _ = buildHashSet("app", &hashSetBody{KVKeyArg: KVKeyArg{Key: "session:1"}, Fields: map[string]string{"plan": "pro", "theme": "dark"}})
	run(t, h, w, nil)
	m.keys["p_app:session:1"].exp = time.Now().UnixMilli() + 3_600_000
	w, _ = buildHashSet("app", &hashSetBody{KVKeyArg: KVKeyArg{Key: "session:1"}, Fields: map[string]string{"plan": "free"}})
	res = run(t, h, w, nil)
	same("hset", value(t, h, "session:1"), map[string]string{"plan": "free", "theme": "dark"})
	undo(t, h, res.Undo)
	same("undo hset", value(t, h, "session:1"), map[string]string{"plan": "pro", "theme": "dark"})
	w, _ = buildHashDel("app", &hashDelBody{KVKeyArg: KVKeyArg{Key: "session:1"}, Fields: []string{"plan", "theme"}})
	res = run(t, h, w, nil)
	same("hdel last field drops the key", value(t, h, "session:1"), nil)
	undo(t, h, res.Undo)
	same("undo hdel", value(t, h, "session:1"), map[string]string{"plan": "pro", "theme": "dark"})
	if e := m.keys["p_app:session:1"]; e.exp == 0 {
		t.Fatal("undo lost the expiry")
	}
	w, _ = buildHashDel("app", &hashDelBody{KVKeyArg: KVKeyArg{Key: "nope"}, Fields: []string{"x"}})
	if _, err := h.write(ctx, "app", w, nil); err == nil || !strings.Contains(err.Error(), "no key nope") {
		t.Fatalf("delete from a missing key: %v", err)
	}

	// Lists: push at either end in order, change, remove by position.
	w, _ = buildListPush("app", &listPushBody{KVKeyArg: KVKeyArg{Key: "q"}, Values: []string{"b", "c"}})
	run(t, h, w, nil)
	w, _ = buildListPush("app", &listPushBody{KVKeyArg: KVKeyArg{Key: "q"}, Values: []string{"x", "a"}, Head: true})
	res = run(t, h, w, nil)
	same("push", value(t, h, "q"), []any{"x", "a", "b", "c"})
	undo(t, h, res.Undo)
	same("undo push", value(t, h, "q"), []any{"b", "c"})
	w, _ = buildListSet("app", &listSetBody{KVKeyArg: KVKeyArg{Key: "q"}, Index: -1, Value: "C"})
	run(t, h, w, nil)
	w, _ = buildListRemove("app", &listRemoveBody{KVKeyArg: KVKeyArg{Key: "q"}, Index: 0})
	res = run(t, h, w, nil)
	same("lset + remove", value(t, h, "q"), []any{"C"})
	undo(t, h, res.Undo)
	same("undo remove", value(t, h, "q"), []any{"b", "C"})
	w, _ = buildListSet("app", &listSetBody{KVKeyArg: KVKeyArg{Key: "q"}, Index: 9, Value: "z"})
	if _, err := h.write(ctx, "app", w, nil); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("lset out of range: %v", err)
	}

	// Sets.
	w, _ = buildSetAdd("app", &setAddBody{KVKeyArg: KVKeyArg{Key: "tags"}, Members: []string{"a", "b"}})
	run(t, h, w, nil)
	w, _ = buildSetRemove("app", &setRemoveBody{KVKeyArg: KVKeyArg{Key: "tags"}, Members: []string{"a"}})
	res = run(t, h, w, nil)
	same("srem", value(t, h, "tags"), []string{"b"})
	undo(t, h, res.Undo)
	same("undo srem", value(t, h, "tags"), []string{"a", "b"})

	// Sorted sets: add, increment, remove; undo.
	w, _ = buildZsetAdd("app", &zsetAddBody{KVKeyArg: KVKeyArg{Key: "lb"}, Members: []KVScored{{"ann", 10}, {"bo", 2.5}}})
	run(t, h, w, nil)
	w, _ = buildZsetIncr("app", &zsetIncrBody{KVKeyArg: KVKeyArg{Key: "lb"}, Member: "bo", By: 20})
	res = run(t, h, w, nil)
	same("zincrby", value(t, h, "lb"), [][2]any{{"bo", 22.5}, {"ann", 10.0}})
	undo(t, h, res.Undo)
	w, _ = buildZsetRemove("app", &zsetRemoveBody{KVKeyArg: KVKeyArg{Key: "lb"}, Members: []string{"ann"}})
	res = run(t, h, w, nil)
	same("zrem", value(t, h, "lb"), [][2]any{{"bo", 2.5}})
	undo(t, h, res.Undo)
	same("undo zrem", value(t, h, "lb"), [][2]any{{"ann", 10.0}, {"bo", 2.5}})

	// Streams: add, delete an entry, trim; undo rebuilds with the same ids.
	for _, v := range []string{"1", "2", "3"} {
		w, _ = buildStreamAdd("app", &streamAddBody{KVKeyArg: KVKeyArg{Key: "events"}, Fields: []KVField{{"n", v}, {"by", "x"}}})
		run(t, h, w, nil)
	}
	before := value(t, h, "events")
	if l := before.([]any); len(l) != 3 || !reflect.DeepEqual(l[0].([]any)[1], []any{"n", "3", "by", "x"}) {
		t.Fatalf("stream newest first: %v", before)
	}
	w, _ = buildStreamTrim("app", &streamTrimBody{KVKeyArg: KVKeyArg{Key: "events"}, MaxLen: 1})
	res = run(t, h, w, nil)
	if l := value(t, h, "events").([]any); len(l) != 1 {
		t.Fatalf("trim: %v", l)
	}
	undo(t, h, res.Undo)
	same("undo trim", value(t, h, "events"), before)
	id := before.([]any)[1].([]any)[0].(string)
	w, _ = buildStreamDel("app", &streamDelBody{KVKeyArg: KVKeyArg{Key: "events"}, IDs: []string{id}})
	res = run(t, h, w, nil)
	undo(t, h, res.Undo)
	same("undo xdel", value(t, h, "events"), before)

	// Expiry: set one, keep forever, undo.
	w, _ = buildExpire("app", &expireBody{KVKeyArg: KVKeyArg{Key: "tags"}, TTLSeconds: 30})
	run(t, h, w, nil)
	if m.keys["p_app:tags"].exp == 0 {
		t.Fatal("expire")
	}
	w, _ = buildExpire("app", &expireBody{KVKeyArg: KVKeyArg{Key: "tags"}})
	keep := run(t, h, w, nil)
	if m.keys["p_app:tags"].exp != 0 {
		t.Fatal("keep forever")
	}
	undo(t, h, keep.Undo)
	if m.keys["p_app:tags"].exp == 0 {
		t.Fatal("undo keep forever")
	}

	// Rename: refused onto an existing key; undo puts the old name back.
	w, _ = buildRename("app", &renameBody{KVKeyArg: KVKeyArg{Key: "tags"}, To: "q"})
	if _, err := h.write(ctx, "app", w, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("rename onto q: %v", err)
	}
	w, _ = buildRename("app", &renameBody{KVKeyArg: KVKeyArg{Key: "tags"}, To: "labels"})
	res = run(t, h, w, nil)
	same("renamed", value(t, h, "labels"), []string{"a", "b"})
	undo(t, h, res.Undo)
	same("undo rename", value(t, h, "tags"), []string{"a", "b"})
	same("undo rename leaves no copy", value(t, h, "labels"), nil)

	// Delete keys, then undo all of them.
	w, _ = buildDel("app", &delBody{Keys: []string{"q", "lb", "events"}})
	res = run(t, h, w, nil)
	for _, k := range []string{"q", "lb", "events"} {
		same("deleted "+k, value(t, h, k), nil)
	}
	undo(t, h, res.Undo)
	same("undo delete", value(t, h, "events"), before)
	same("undo delete zset", value(t, h, "lb"), [][2]any{{"ann", 10.0}, {"bo", 2.5}})

	// A wrong type says so.
	w, _ = buildHashSet("app", &hashSetBody{KVKeyArg: KVKeyArg{Key: "q"}, Fields: map[string]string{"a": "b"}})
	if _, err := h.write(ctx, "app", w, nil); err == nil || !strings.Contains(err.Error(), "different type") {
		t.Fatalf("wrong type: %v", err)
	}
}

// Undo is refused once the key changed again; it works once, and can be redone.
func TestKVUndoGuards(t *testing.T) {
	h, _ := newKV(t)
	ctx := context.Background()
	w, _ := buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "k"}, Value: "v1"})
	run(t, h, w, nil)
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "k"}, Value: "v2"})
	res := run(t, h, w, nil)
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "k"}, Value: "v3"})
	run(t, h, w, nil)
	if _, err := h.undo(ctx, "app", res.Undo, "", nil); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("undo after a newer write: %v", err)
	}
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "k"}, Value: "v4"})
	res = run(t, h, w, nil)
	redo, err := h.undo(ctx, "app", res.Undo, "", nil)
	if err != nil || value(t, h, "k") != "v3" {
		t.Fatalf("undo: %v %v", err, value(t, h, "k"))
	}
	if _, err := h.undo(ctx, "app", res.Undo, "", nil); err == nil || !strings.Contains(err.Error(), "nothing to undo") {
		t.Fatalf("second undo: %v", err)
	}
	if _, err := h.undo(ctx, "other", redo.Undo, "", nil); err == nil {
		t.Fatal("another project used this project's undo")
	}
	undo(t, h, redo.Undo)
	if value(t, h, "k") != "v4" {
		t.Fatal("redo")
	}
}

// A write too big to copy needs a confirm (and the caller's say-so for a
// write that can't be undone); then it goes ahead without an undo id.
func TestKVTooBigToUndo(t *testing.T) {
	h, _ := newKV(t)
	ctx := context.Background()
	big := strings.Repeat("x", undoMaxBytes+10)
	w, _ := buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "blob"}, Value: big})
	run(t, h, w, nil) // a new key: nothing to copy
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "blob"}, Value: "small"})
	_, err := h.write(ctx, "app", w, nil)
	var cp *datakit.ConfirmProblem
	if !errors.As(err, &cp) || cp.Status != 428 || cp.Confirm == "" {
		t.Fatalf("want a confirm: %v", err)
	}
	refused := errors.New("needs full access")
	if _, err := h.write(ctx, "app", w, func(undoable bool) error {
		if !undoable {
			return refused
		}
		return nil
	}); !errors.Is(err, refused) {
		t.Fatalf("allow not asked: %v", err)
	}
	w.confirm = cp.Confirm
	res, err := h.write(ctx, "app", w, nil)
	if err != nil || res.Undo != "" || value(t, h, "blob") != "small" {
		t.Fatalf("confirmed: %+v %v", res, err)
	}
}

// Writes reach only the project's own keys; a project over its cache limit
// gets a clear refusal; a key named with the full prefix is the same key.
func TestKVPrefixIsolation(t *testing.T) {
	h, m := newKV(t)
	ctx := context.Background()
	w, _ := buildSet("other", &setBody{KVKeyArg: KVKeyArg{Key: "secret"}, Value: "theirs"})
	if _, err := h.write(ctx, "other", w, nil); err != nil {
		t.Fatal(err)
	}
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "secret"}, Value: "mine"})
	run(t, h, w, nil)
	if m.keys["p_other:secret"].str != "theirs" || m.keys["p_app:secret"].str != "mine" {
		t.Fatal("projects share a key")
	}
	if fullKey("app", "p_app:secret") != "p_app:secret" || fullKey("app", "p_other:secret") != "p_app:p_other:secret" {
		t.Fatal("fullKey")
	}
	// Commands with a key outside the prefix never reach Valkey unprefixed:
	// even a hand-made command runs as the project's user, which refuses it.
	_, err := h.write(ctx, "app", kvWrite{op: "x", keys: []string{"p_other:secret"}, cmds: [][]string{{"DEL", "p_other:secret"}}}, nil)
	if err == nil || m.keys["p_other:secret"] == nil {
		t.Fatalf("reached another project: %v", err)
	}

	limits.mu.Lock()
	limits.state["app"] = CacheState{WritesRefused: true, LimitBytes: 1 << 20}
	limits.mu.Unlock()
	t.Cleanup(func() { limits.forget("app") })
	m.mu.Lock()
	m.held["p_app"] = true
	m.mu.Unlock()
	w, _ = buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "more"}, Value: "x"})
	_, err = h.write(ctx, "app", w, nil)
	var pr *api.Problem
	if !errors.As(err, &pr) || pr.Status != 409 || !strings.Contains(pr.Detail, "over its memory limit") {
		t.Fatalf("held: %v", err)
	}
	w, _ = buildDel("app", &delBody{Keys: []string{"secret"}})
	if _, err := h.write(ctx, "app", w, nil); err != nil {
		t.Fatalf("deletes still work while held: %v", err)
	}
}

// Sorted sets read highest score first, a page at a time; lists and streams page too.
func TestKVPaging(t *testing.T) {
	h, _ := newKV(t)
	ctx := context.Background()
	var ms []KVScored
	for i := 1; i <= 120; i++ {
		ms = append(ms, KVScored{fmt.Sprintf("m%d", i), float64(i)})
	}
	w, _ := buildZsetAdd("app", &zsetAddBody{KVKeyArg: KVKeyArg{Key: "lb"}, Members: ms})
	run(t, h, w, nil)
	c, _ := h.admin(ctx)
	defer c.Close()
	v, err := getKey(ctx, c, "app", "lb", valueQuery{count: 50})
	if err != nil {
		t.Fatal(err)
	}
	page := v.Value.([][2]any)
	if page[0][0] != "m120" || page[0][1] != 120.0 || page[49][0] != "m71" || v.Cursor != "50" || !v.Truncated || v.Length != 120 {
		t.Fatalf("first page: %v .. %v cursor %q", page[0], page[49], v.Cursor)
	}
	v, _ = getKey(ctx, c, "app", "lb", valueQuery{count: 50, cursor: "100"})
	if page = v.Value.([][2]any); len(page) != 20 || page[19][0] != "m1" || v.Cursor != "" {
		t.Fatalf("last page: %v cursor %q", page, v.Cursor)
	}

	var vals []string
	for i := 0; i < 7; i++ {
		vals = append(vals, strconv.Itoa(i))
	}
	w, _ = buildListPush("app", &listPushBody{KVKeyArg: KVKeyArg{Key: "l"}, Values: vals})
	run(t, h, w, nil)
	v, _ = getKey(ctx, c, "app", "l", valueQuery{count: 3, cursor: "3"})
	if !reflect.DeepEqual(v.Value, []any{"3", "4", "5"}) || v.Cursor != "6" {
		t.Fatalf("list page: %v %q", v.Value, v.Cursor)
	}

	for i := 0; i < 5; i++ {
		w, _ = buildStreamAdd("app", &streamAddBody{KVKeyArg: KVKeyArg{Key: "s"}, Fields: []KVField{{"i", strconv.Itoa(i)}}})
		run(t, h, w, nil)
	}
	v, _ = getKey(ctx, c, "app", "s", valueQuery{count: 2})
	first := v.Value.([]any)
	if first[0].([]any)[1].([]any)[1] != "4" || v.Cursor == "" {
		t.Fatalf("stream page 1: %v", first)
	}
	v, _ = getKey(ctx, c, "app", "s", valueQuery{count: 2, cursor: v.Cursor})
	if second := v.Value.([]any); second[0].([]any)[1].([]any)[1] != "2" {
		t.Fatalf("stream page 2: %v", second)
	}
}

// The tree groups keys by ":" with counts, filters by glob, type and
// expiry, and pages the keys at a level in natural order.
func TestKVTree(t *testing.T) {
	h, _ := newKV(t)
	ctx := context.Background()
	for _, k := range []string{"session:u1", "session:u2", "session:u10", "cart:1", "flags", "item2", "item10"} {
		w, _ := buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: k}, KVNewKey: KVNewKey{TTLSeconds: map[bool]int64{true: 60}[strings.HasPrefix(k, "session")]}, Value: "v"})
		run(t, h, w, nil)
	}
	w, _ := buildSet("other", &setBody{KVKeyArg: KVKeyArg{Key: "session:x"}, Value: "theirs"})
	_, _ = h.write(ctx, "other", w, nil)
	c, _ := h.admin(ctx)
	defer c.Close()
	top, err := tree(ctx, c, "app", treeQuery{delimiter: ":"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(top.Groups, []KVGroup{{"cart:", 1}, {"session:", 3}}) || top.Total != 3 {
		t.Fatalf("top: %+v", top)
	}
	var names []string
	for _, k := range top.Keys {
		names = append(names, k.Key)
	}
	if !slices.Equal(names, []string{"p_app:flags", "p_app:item2", "p_app:item10"}) {
		t.Fatalf("top keys: %v", names)
	}
	lvl, _ := tree(ctx, c, "app", treeQuery{prefix: "session:", delimiter: ":", limit: 2})
	if lvl.Total != 3 || len(lvl.Keys) != 2 || lvl.Keys[1].Key != "p_app:session:u2" || lvl.Next != 2 || lvl.Keys[0].TTLMs <= 0 {
		t.Fatalf("session level: %+v", lvl)
	}
	kept, _ := tree(ctx, c, "app", treeQuery{delimiter: ":", expiry: "kept"})
	if len(kept.Groups) != 1 || kept.Groups[0].Prefix != "cart:" {
		t.Fatalf("kept only: %+v", kept)
	}
	m, _ := tree(ctx, c, "app", treeQuery{delimiter: "", match: "*u1*"})
	if m.Total != 2 {
		t.Fatalf("match: %+v", m)
	}
	for p, s := range map[string]string{"a*": "abc", "a?c": "abc", "a[bx]c": "abc", "a[^x]c": "abc", "a[a-c]c": "abc", `a\*`: "a*"} {
		if !globMatch(p, s) {
			t.Errorf("%q should match %q", p, s)
		}
	}
	for p, s := range map[string]string{"a*d": "abc", "a[^b]c": "abc", `a\*`: "ab"} {
		if globMatch(p, s) {
			t.Errorf("%q should not match %q", p, s)
		}
	}
}

// Deleting a prefix counts first (428 with the count), then deletes only
// that project's keys under it, and can be undone.
func TestKVDeletePrefix(t *testing.T) {
	h, m := newKV(t)
	ctx := context.Background()
	for _, k := range []string{"session:a", "session:b", "sessions", "cart:1"} {
		w, _ := buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: k}, Value: "v"})
		run(t, h, w, nil)
	}
	w, _ := buildSet("other", &setBody{KVKeyArg: KVKeyArg{Key: "session:a"}, Value: "theirs"})
	_, _ = h.write(ctx, "other", w, nil)
	_, err := h.deletePrefix(ctx, "app", "session:", "", nil)
	var cp *datakit.ConfirmProblem
	if !errors.As(err, &cp) || cp.Preview.(map[string]any)["count"] != 2 || cp.Preview.(map[string]any)["undo"] != true {
		t.Fatalf("count first: %v %+v", err, cp)
	}
	if len(m.keys) != 5 {
		t.Fatal("counting deleted something")
	}
	res, err := h.deletePrefix(ctx, "app", "session:", cp.Confirm, nil)
	if err != nil || len(res.Keys) != 2 || res.Undo == "" {
		t.Fatalf("delete: %+v %v", res, err)
	}
	if m.keys["p_app:session:a"] != nil || m.keys["p_app:sessions"] == nil || m.keys["p_other:session:a"] == nil {
		t.Fatalf("deleted the wrong keys: %v", m.keys)
	}
	undo(t, h, res.Undo)
	if m.keys["p_app:session:b"] == nil {
		t.Fatal("undo prefix delete")
	}
	if _, err := h.deletePrefix(ctx, "app", "nothing:", "", nil); err == nil || !strings.Contains(err.Error(), "no keys start with") {
		t.Fatalf("empty prefix: %v", err)
	}
}

// The console: prefixing both ways, writes only when allowed (with Undo),
// SCAN/KEYS for the project's own keys, nothing administrative.
func TestKVConsole(t *testing.T) {
	h, _ := newKV(t)
	ctx := context.Background()
	runAll := func(text string, write bool) []KVCommandResult {
		t.Helper()
		cmds, err := splitCommands(text)
		if err != nil {
			t.Fatal(err)
		}
		var out []KVCommandResult
		for _, c := range cmds {
			out = append(out, h.command(ctx, "app", c, write, nil))
		}
		return out
	}
	r := runAll(`SET greeting "hello world"`, false)
	if !strings.Contains(r[0].Error, "Allow changes") || !r[0].Write {
		t.Fatalf("write without allow: %+v", r[0])
	}
	r = runAll("SET greeting \"hello world\"\nHSET user:1 name 'Ada L' plan pro\n# a comment\nGET greeting\nHGETALL user:1", true)
	if r[0].Reply != "OK" || r[0].Undo == "" || r[2].Reply != "hello world" || !reflect.DeepEqual(r[3].Reply, []any{"name", "Ada L", "plan", "pro"}) {
		t.Fatalf("console: %+v", r)
	}
	if _, err := h.undo(ctx, "app", r[0].Undo, "", nil); err != nil {
		t.Fatal(err)
	}
	if r := runAll("GET greeting", false); r[0].Reply != nil {
		t.Fatalf("undo from the console: %+v", r[0])
	}
	r = runAll("KEYS *\nSCAN 0 MATCH user:*\nDBSIZE", false)
	if !reflect.DeepEqual(r[0].Reply, []any{"user:1"}) || !reflect.DeepEqual(r[1].Reply, []any{"0", []any{"user:1"}}) || r[2].Reply != int64(1) {
		t.Fatalf("listing: %+v", r)
	}
	r = runAll("FLUSHALL\nCLIENT LIST\nMONITOR", true)
	if r[0].Error != "FLUSHALL is for the box's admin only, not a project" || !strings.Contains(r[1].Error, "isn't available") || !strings.Contains(r[2].Error, "isn't available") {
		t.Fatalf("refused: %+v", r)
	}
	if r = runAll("NOSUCH x", true); !strings.Contains(r[0].Error, "unknown command") {
		t.Fatalf("unknown: %+v", r[0])
	}

	for in, want := range map[string][][]string{
		`set k "a\"b\n" 'it\'s'`: {{"set", "k", "a\"b\n", "it's"}},
		"get a\n\n  get b  \n":   {{"get", "a"}, {"get", "b"}},
		`set k "\x41"`:           {{"set", "k", "A"}},
		"set k \"two\nlines\"":   {{"set", "k", "two\nlines"}},
		`set k ""`:               {{"set", "k", ""}},
	} {
		got, err := splitCommands(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{`get "open`, `get "a"b`, "  \n# only a comment"} {
		if _, err := splitCommands(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// Undo sees a change to a key's expiry as a change (DUMP alone does not
// cover it), and a write that lands between Undo's check and its restore
// makes it fail instead of being overwritten (WATCH). An ordinary write
// raced the same way starts over.
func TestKVUndoExpiryAndRaces(t *testing.T) {
	h, m := newKV(t)
	ctx := context.Background()
	set := func(v string) *KVWriteResult {
		w, err := buildSet("app", &setBody{KVKeyArg: KVKeyArg{Key: "k"}, Value: v})
		return run(t, h, w, err)
	}
	appWrite := func(args ...string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if r, ok := m.exec("p_app", args).(RedisError); ok {
			t.Fatal(r)
		}
	}
	set("v1")
	res := set("v2")
	appWrite("PEXPIRE", "p_app:k", "60000") // the app makes it cache
	if _, err := h.undo(ctx, "app", res.Undo, "", nil); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("undo after the expiry changed: %v", err)
	}

	res = set("v3")
	var once sync.Once
	m.beforeExec = func() { once.Do(func() { appWrite("SET", "p_app:k", "app") }) }
	if _, err := h.undo(ctx, "app", res.Undo, "", nil); err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("undo raced by a write: %v", err)
	}
	if v := value(t, h, "k"); v != "app" {
		t.Fatalf("the app's write was overwritten: %v", v)
	}

	once = sync.Once{}
	res = set("v4") // raced once: starts over, then goes through
	m.beforeExec = nil
	if v := value(t, h, "k"); v != "v4" {
		t.Fatalf("after a raced write: %v", v)
	}
	undo(t, h, res.Undo)
	if v := value(t, h, "k"); v != "app" {
		t.Fatalf("undo of the raced write puts back what it replaced: %v", v)
	}
}

// A TTL in seconds is never turned into a negative millisecond count (which
// PEXPIRE takes as "delete now"): the schema stops at 100 years and ms caps.
func TestTTLNoOverflow(t *testing.T) {
	w, _ := buildExpire("app", &expireBody{KVKeyArg: KVKeyArg{Key: "session:123"}, TTLSeconds: 9223372036854776})
	if got := w.cmds[0][2]; strings.HasPrefix(got, "-") || got != strconv.FormatInt(maxTTLSeconds*1000, 10) {
		t.Fatalf("PEXPIRE %s", got)
	}
	w, _ = buildHashSet("app", &hashSetBody{KVKeyArg: KVKeyArg{Key: "h"}, Fields: map[string]string{"a": "1"}, KVNewKey: KVNewKey{TTLSeconds: math.MaxInt64 / 999}})
	if got := w.cmds[len(w.cmds)-1][2]; strings.HasPrefix(got, "-") {
		t.Fatalf("collection PEXPIRE %s", got)
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(expireBody{}), reflect.TypeOf(KVNewKey{})} {
		f, _ := typ.FieldByName("TTLSeconds")
		if f.Tag.Get("maximum") != strconv.Itoa(maxTTLSeconds) {
			t.Errorf("%s.TTLSeconds maximum %q", typ.Name(), f.Tag.Get("maximum"))
		}
	}
}
