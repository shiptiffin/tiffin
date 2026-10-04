package budget

import "time"

// Memory pressure, as /usage reports it.
const (
	PressureNone = "none" // no memory limit was reached in the last hour
	PressureSome = "some" // the project was held back (reached a limit, waited for memory, or has memory swapped out); nothing was killed
	PressureOOM  = "oom"  // an app copy was killed for memory in the last hour
)

// stallSome is how long a project's tasks may wait for memory within the
// hour before it counts as pressure: 100 ms.
const stallSome = 100_000

// eventMark is a slice's memory.events counters at one moment.
type eventMark struct {
	at                             time.Time
	high, max, oom, oomKill, stall uint64
}

// recordLocked keeps a sample of a slice's counters, at most one a minute
// and an hour's worth (the sync loop calls it every 15 seconds).
func (m *Module) recordLocked(project string, st Stats, now time.Time) {
	h := m.history[project]
	if n := len(h); n > 0 && now.Sub(h[n-1].at) < time.Minute {
		return
	}
	h = append(h, eventMark{now, st.High, st.Max, st.OOM, st.OOMKill, st.StallMicros})
	i := 0
	for i < len(h)-1 && now.Sub(h[i+1].at) >= historyKeep {
		i++ // keep one mark at or before the hour boundary as the baseline
	}
	m.history[project] = h[i:]
}

// pressureLocked compares a fresh reading with the oldest mark of the last
// hour. Counters that went down mean the slice was recreated: everything
// it counts since happened inside the window. Events from before Tiffin
// started watching are not counted.
func (m *Module) pressureLocked(project string, st Stats, now time.Time) string {
	h := m.history[project]
	if len(h) == 0 {
		return PressureNone
	}
	base := h[0]
	delta := func(cur, old uint64) uint64 {
		if cur < old {
			return cur
		}
		return cur - old
	}
	switch {
	case delta(st.OOMKill, base.oomKill) > 0:
		return PressureOOM
	case delta(st.OOM, base.oom) > 0 || delta(st.Max, base.max) > 0 || delta(st.High, base.high) > 0 ||
		delta(st.StallMicros, base.stall) > stallSome || st.SwapBytes > 0:
		return PressureSome
	}
	return PressureNone
}
