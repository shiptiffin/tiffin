package analytics

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// Settings are a project's analytics options the collector needs per hit.
// Attribution is off unless the project lists services.analytics.attribution.
type Settings struct {
	Attribution      bool   `json:"attribution"`
	Campaigns        bool   `json:"campaigns"`        // keep utm_* and ref
	Signups          bool   `json:"signups"`          // save each new account's source
	FirstVisitCookie string `json:"firstVisitCookie"` // off, outside-eu or ask-eu
}

// parseSettings reads Settings from a services.analytics spec.
func parseSettings(spec json.RawMessage) Settings {
	var s struct {
		Attribution *manifest.Attribution `json:"attribution"`
	}
	_ = json.Unmarshal(spec, &s)
	a := s.Attribution
	if a == nil {
		return Settings{FirstVisitCookie: manifest.FirstVisitOff}
	}
	out := Settings{Attribution: true, Campaigns: true, Signups: true, FirstVisitCookie: a.FirstVisitCookie}
	if a.Campaigns != nil {
		out.Campaigns = *a.Campaigns
	}
	if a.Signups != nil {
		out.Signups = *a.Signups
	}
	if out.FirstVisitCookie == "" {
		out.FirstVisitCookie = manifest.FirstVisitOff
	}
	return out
}

// settingsCache keeps each project's Settings; Reconcile refreshes a project's.
type settingsCache struct {
	mu   sync.RWMutex
	byID map[string]Settings
	load func(ctx context.Context, project string) Settings
}

func (c *settingsCache) get(ctx context.Context, project string) Settings {
	c.mu.RLock()
	s, ok := c.byID[project]
	c.mu.RUnlock()
	if ok || c.load == nil {
		return s
	}
	s = c.load(ctx, project)
	c.set(project, s)
	return s
}

func (c *settingsCache) set(project string, s Settings) {
	c.mu.Lock()
	if c.byID == nil {
		c.byID = map[string]Settings{}
	}
	c.byID[project] = s
	c.mu.Unlock()
}

func (c *settingsCache) forget(project string) {
	c.mu.Lock()
	delete(c.byID, project)
	c.mu.Unlock()
}

// Settings returns project's analytics settings.
func (m *Module) Settings(ctx context.Context, project string) Settings {
	return m.settings.get(ctx, project)
}

func (m *Module) loadSettings(ctx context.Context, project string) Settings {
	_, res, err := m.p.DB.Load(ctx, project)
	if err != nil {
		return Settings{FirstVisitCookie: manifest.FirstVisitOff}
	}
	r, ok := res["service/analytics"]
	if !ok {
		return Settings{FirstVisitCookie: manifest.FirstVisitOff}
	}
	return parseSettings(r.Spec)
}
