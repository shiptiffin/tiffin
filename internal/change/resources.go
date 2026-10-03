package change

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/manifest"
)

// Resource address kinds.
const (
	KindProject = "project"
	KindApp     = "app"
	KindService = "service"
	KindBucket  = "bucket"
	KindEnv     = "env"
)

// Kind returns the kind part of an address ("app/web" → "app").
func Kind(address string) string {
	k, _, _ := strings.Cut(address, "/")
	return k
}

// Name returns the name part of an address ("app/web" → "web").
func Name(address string) string {
	_, n, _ := strings.Cut(address, "/")
	return n
}

// Resources flattens a normalized manifest into addressable resources.
// Every project has a "project" resource so an empty project still exists.
func Resources(m *manifest.Manifest) (map[string]Resource, error) {
	out := map[string]Resource{}
	add := func(addr string, spec any) error {
		b, err := compact(spec)
		if err != nil {
			return err
		}
		out[addr] = Resource{Address: addr, Spec: b}
		return nil
	}
	if err := add(KindProject, struct{}{}); err != nil {
		return nil, err
	}
	for name, app := range m.Apps {
		if err := add(KindApp+"/"+name, app); err != nil {
			return nil, err
		}
	}
	for k, v := range m.Env {
		if err := add(KindEnv+"/"+k, v); err != nil {
			return nil, err
		}
	}
	s := m.Services
	if s.Postgres != nil {
		if err := add(KindService+"/postgres", s.Postgres); err != nil {
			return nil, err
		}
	}
	if s.Valkey != nil {
		if err := add(KindService+"/valkey", s.Valkey); err != nil {
			return nil, err
		}
	}
	if s.Storage != nil {
		if err := add(KindService+"/storage", struct{}{}); err != nil {
			return nil, err
		}
		for name, b := range s.Storage.Buckets {
			if err := add(KindBucket+"/"+name, b); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// compact marshals v to compact JSON with sorted map keys and no HTML escaping.
func compact(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// Diff computes the ops that turn current into desired, sorted so creates of
// containers come before their contents and deletes come after.
func Diff(current, desired map[string]Resource) []Op {
	var ops []Op
	for addr, want := range desired {
		have, ok := current[addr]
		switch {
		case !ok:
			ops = append(ops, Op{Action: Create, Address: addr, After: want.Spec})
		case !jsonEqual(have.Spec, want.Spec):
			ops = append(ops, Op{Action: Update, Address: addr, Before: have.Spec, After: want.Spec, Fields: changedFields(have.Spec, want.Spec)})
		}
	}
	for addr, have := range current {
		if _, ok := desired[addr]; !ok {
			ops = append(ops, Op{Action: Delete, Address: addr, Before: have.Spec})
		}
	}
	sortOps(ops)
	for i := range ops {
		ops[i].Risk, ops[i].Reason = Classify(ops[i])
	}
	return ops
}

// order of kinds when creating; deletes run in reverse.
var kindOrder = map[string]int{KindProject: 0, KindService: 1, KindBucket: 2, KindEnv: 3, KindApp: 4}

// SortOps orders ops: creates (containers first), updates, then deletes
// (contents first).
func SortOps(ops []Op) { sortOps(ops) }

func sortOps(ops []Op) {
	actionOrder := map[Action]int{Create: 0, Update: 1, Delete: 2}
	sort.SliceStable(ops, func(i, j int) bool {
		a, b := ops[i], ops[j]
		if a.Action != b.Action {
			return actionOrder[a.Action] < actionOrder[b.Action]
		}
		ka, kb := kindOrder[Kind(a.Address)], kindOrder[Kind(b.Address)]
		if ka != kb {
			if a.Action == Delete {
				return ka > kb
			}
			return ka < kb
		}
		return a.Address < b.Address
	})
}

func jsonEqual(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ca, _ := compact(x)
	cb, _ := compact(y)
	return bytes.Equal(ca, cb)
}

func changedFields(before, after json.RawMessage) []string {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
		return nil // scalar specs (env values) have no fields
	}
	set := map[string]bool{}
	for k, v := range a {
		if w, ok := b[k]; !ok || !jsonEqual(v, w) {
			set[k] = true
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			set[k] = true
		}
	}
	fields := make([]string, 0, len(set))
	for k := range set {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	return fields
}
