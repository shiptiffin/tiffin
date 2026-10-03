package change

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Loss is what one irreversible op would destroy, measured on the box when
// the plan was made: "18,204 rows in 12 tables · 41 MB". It is best effort:
// an op the box can't measure (or not quickly) carries no Loss at all, which
// means "not measured", never "nothing". It is not part of the plan hash.
type Loss struct {
	Bytes   int64       `json:"bytes" doc:"Bytes that would be deleted (on disk, or the stored size of what goes)"`
	Counts  []LossCount `json:"counts" doc:"What goes, counted: rows, tables, files, events, jobs"`
	Summary string      `json:"summary" doc:"The same in plain words, e.g. \"18,204 rows in 12 tables · 41 MB\""`
}

// LossCount is one counted thing an op would destroy.
type LossCount struct {
	N      int64  `json:"n"`
	Unit   string `json:"unit" enum:"row,table,file,event,job,user,database" doc:"Singular noun of what is counted"`
	Approx bool   `json:"approx,omitempty" doc:"True when N is an estimate (a large table's row count)"`
}

// Empty reports whether nothing at all would be lost.
func (l *Loss) Empty() bool {
	if l.Bytes > 0 {
		return false
	}
	for _, c := range l.Counts {
		if c.N > 0 {
			return false
		}
	}
	return true
}

// Summarize fills Summary from Bytes and Counts and returns l.
func (l *Loss) Summarize() *Loss {
	if l.Counts == nil {
		l.Counts = []LossCount{}
	}
	if l.Empty() {
		l.Summary = "nothing: it is empty"
		return l
	}
	var parts []string
	var rows, tables *LossCount
	for i := range l.Counts {
		switch l.Counts[i].Unit {
		case "row":
			rows = &l.Counts[i]
		case "table":
			tables = &l.Counts[i]
		}
	}
	for i := range l.Counts {
		c := &l.Counts[i]
		switch {
		case c == tables && rows != nil:
			continue // said with the rows: "1,180 rows in 3 tables"
		case c == rows && tables != nil:
			parts = append(parts, countWords(*rows)+" in "+countWords(*tables))
		default:
			parts = append(parts, countWords(*c))
		}
	}
	if l.Bytes > 0 {
		parts = append(parts, Bytes(l.Bytes))
	}
	l.Summary = strings.Join(parts, " · ")
	return l
}

func countWords(c LossCount) string {
	s := Thousands(c.N) + " " + c.Unit
	if c.N != 1 {
		s += "s"
	}
	if c.Approx {
		s = "about " + s
	}
	return s
}

// Thousands formats n with comma separators: 18204 → "18,204".
func Thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// Bytes formats a size the way the dashboard does (binary units): "41 MB", "1.5 KB".
func Bytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + " " + units[i]
}

// LossBudget is how long a plan waits for loss estimates. Estimates that
// take longer are left out: a plan must stay cheap.
const LossBudget = 200 * time.Millisecond

// EstimateFunc measures what one op would destroy; nil, nil means "can't say".
type EstimateFunc func(ctx context.Context, project string, op Op) (*Loss, error)

// AttachLosses measures every irreversible op of p concurrently, within
// LossBudget, and sets Op.Loss on those that answered in time without error.
func AttachLosses(ctx context.Context, p *Plan, estimate EstimateFunc) {
	if p == nil || estimate == nil {
		return
	}
	var idx []int
	for i, o := range p.Ops {
		if o.Risk == TierIrreversible {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, LossBudget)
	defer cancel()
	type result struct {
		i int
		l *Loss
	}
	ch := make(chan result, len(idx))
	var wg sync.WaitGroup
	for _, i := range idx {
		wg.Add(1)
		go func(i int, op Op) {
			defer wg.Done()
			defer func() { _ = recover() }() // an estimate never breaks a plan
			l, err := estimate(ctx, p.Project, op)
			if err != nil || l == nil {
				return
			}
			ch <- result{i, l.Summarize()}
		}(i, p.Ops[i])
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	got := map[int]*Loss{}
	for {
		select {
		case r := <-ch:
			got[r.i] = r.l
			continue
		case <-done:
		case <-ctx.Done():
		}
		break
	}
	// Drain anything that arrived alongside done.
	for {
		select {
		case r := <-ch:
			got[r.i] = r.l
			continue
		default:
		}
		break
	}
	for i, l := range got {
		p.Ops[i].Loss = l
	}
}
