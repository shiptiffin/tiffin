package budget

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Limit events: moments a project's limit held it back. They show on the
// project's Usage and History pages. Each kind is recorded at most once an
// hour per project, so a project that sits at its limit is not a flood.
const (
	EventMemory      = "memory"      // an app ran out of the memory it may use and was restarted
	EventConnections = "connections" // the database used every connection it may open; more were refused
	EventCacheFull   = "cache-full"  // the cache reached its limit: writes refused until it is under it
	EventCacheFreed  = "cache-freed" // the cache reached its limit: keys with an expiry were cleared early
)

// Event is one moment a limit held a project back.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind" enum:"memory,connections,cache-full,cache-freed" doc:"memory: an app ran out of the memory it may use and was restarted; connections: its database used every connection it may open and more were refused; cache-full: its cache reached its limit and writes were refused until it was under it; cache-freed: keys with an expiry were cleared early to keep its cache under its limit"`
	Message string    `json:"message" doc:"What happened and what to do, in plain words"`
}

const (
	kvEvents   = "budget.events" // project → JSON []Event, newest last
	eventGap   = time.Hour
	eventsKeep = 30 * 24 * time.Hour
	eventsMax  = 100
)

var eventsMu sync.Mutex

// RecordEvent notes that a limit held project back, unless the same kind
// was noted within the hour. Off a box (budget not started) it does nothing.
func RecordEvent(ctx context.Context, project, kind, message string) {
	mod.mu.Lock()
	p := mod.p
	mod.mu.Unlock()
	if p == nil {
		return
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	now := time.Now().UTC()
	list := readEvents(ctx, project)
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Kind == kind && now.Sub(list[i].At) < eventGap {
			return
		}
	}
	list = append(list, Event{At: now, Kind: kind, Message: message})
	for len(list) > 0 && (len(list) > eventsMax || now.Sub(list[0].At) > eventsKeep) {
		list = list[1:]
	}
	raw, _ := json.Marshal(list)
	if err := p.DB.KVPut(ctx, kvEvents, project, raw); err != nil {
		p.Log.Warn("budget: record limit event", "project", project, "err", err)
	}
}

// Events returns a project's limit events of the last 30 days, newest first.
func Events(ctx context.Context, project string) []Event {
	eventsMu.Lock()
	list := readEvents(ctx, project)
	eventsMu.Unlock()
	out := make([]Event, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		if time.Since(list[i].At) <= eventsKeep {
			out = append(out, list[i])
		}
	}
	return out
}

func readEvents(ctx context.Context, project string) []Event {
	mod.mu.Lock()
	p := mod.p
	mod.mu.Unlock()
	if p == nil {
		return nil
	}
	var list []Event
	if raw, ok, _ := p.DB.KVGet(ctx, kvEvents, project); ok {
		_ = json.Unmarshal(raw, &list)
	}
	return list
}

func forgetEvents(ctx context.Context, project string) {
	mod.mu.Lock()
	p := mod.p
	mod.mu.Unlock()
	if p != nil {
		_ = p.DB.KVDelete(ctx, kvEvents, project)
	}
}
