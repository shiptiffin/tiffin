package email

import "sync"

// hub fans out message events to dashboard streams, per project.
type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan Summary]bool
}

func newHub() *hub { return &hub{subs: map[string]map[chan Summary]bool{}} }

func (h *hub) subscribe(project string) (<-chan Summary, func()) {
	ch := make(chan Summary, 32)
	h.mu.Lock()
	if h.subs[project] == nil {
		h.subs[project] = map[chan Summary]bool{}
	}
	h.subs[project][ch] = true
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[project], ch)
		h.mu.Unlock()
	}
}

// publish never blocks: a slow subscriber misses events (it can re-list).
func (h *hub) publish(project string, s Summary) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[project] {
		select {
		case ch <- s:
		default:
		}
	}
}
