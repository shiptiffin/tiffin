package change

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

// MemStore is an in-memory Store for tests.
type MemStore struct {
	mu       sync.Mutex
	versions map[string]int64
	state    map[string]map[string]Resource
	changes  []*Change
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{versions: map[string]int64{}, state: map[string]map[string]Resource{}}
}

func (m *MemStore) Load(_ context.Context, project string) (int64, map[string]Resource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := map[string]Resource{}
	for k, v := range m.state[project] {
		cur[k] = v
	}
	return m.versions[project], cur, nil
}

func (m *MemStore) Commit(_ context.Context, c *Change) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.versions[c.Project] != c.Plan.BaseVersion {
		return ErrConflict
	}
	cur := m.state[c.Project]
	if err := CheckPreconditions(cur, c.Plan.Ops); err != nil {
		return err
	}
	var undone *Change
	if c.UndoOf != "" {
		undone = m.find(c.UndoOf)
		if undone == nil {
			return ErrNotFound
		}
		if undone.UndoneBy != "" {
			return &PreconditionError{Address: c.UndoOf, Detail: "already undone by " + undone.UndoneBy}
		}
	}
	m.state[c.Project] = ApplyOps(cur, c.Plan.Ops)
	m.versions[c.Project]++
	c.Version = m.versions[c.Project]
	if undone != nil {
		undone.UndoneBy = c.ID
	}
	m.changes = append(m.changes, clone(c))
	return nil
}

func (m *MemStore) find(id string) *Change {
	for _, c := range m.changes {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (m *MemStore) GetChange(_ context.Context, id string) (*Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.find(id); c != nil {
		return clone(c), nil
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListChanges(_ context.Context, f ListFilter) ([]*Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	var out []*Change
	for i := len(m.changes) - 1; i >= 0 && len(out) < limit; i-- {
		c := m.changes[i]
		seq := int64(i + 1)
		if f.Project != "" && c.Project != f.Project {
			continue
		}
		if f.Before > 0 && seq >= f.Before {
			continue
		}
		out = append(out, clone(c))
	}
	return out, nil
}

func (m *MemStore) ListProjects(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p, s := range m.state {
		if len(s) > 0 {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func clone(c *Change) *Change {
	b, _ := json.Marshal(c)
	var out Change
	_ = json.Unmarshal(b, &out)
	return &out
}
