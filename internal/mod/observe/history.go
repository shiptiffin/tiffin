package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// UsageHistory is a project's (or one app's) use of the box over time.
type UsageHistory struct {
	Project     string                  `json:"project"`
	App         string                  `json:"app,omitempty"`
	Range       string                  `json:"range"`
	From        time.Time               `json:"from"`
	To          time.Time               `json:"to"`
	StepSeconds int                     `json:"stepSeconds" doc:"Seconds between points; each point is the step before it (memory and data: the highest value in it)"`
	Series      map[string][][2]float64 `json:"series" doc:"[unix seconds, value] pairs, oldest first; a series is absent when nothing was measured. memory and memoryLimit (bytes), cpu and cpuLimit (percent, 100 = one core), requests (per minute), p50 and p95 (response time, ms), errors (share of requests answered 5xx, 0-1); for the whole project also database, files and kv (bytes on disk; kv is held in memory) and connections (open database connections). Limits only while the project has one."`
}

// historyRanges: each range's step keeps a chart near 150 points.
var historyRanges = map[string][2]time.Duration{
	"1h":  {time.Hour, 30 * time.Second},
	"24h": {24 * time.Hour, 10 * time.Minute},
	"7d":  {7 * 24 * time.Hour, time.Hour},
	"30d": {30 * 24 * time.Hour, 4 * time.Hour},
}

// historyQueries are the MetricsQL queries behind UsageHistory, one per
// series: gauges take the step's highest (memory) or average (CPU) value,
// counters a rate over the step (at least a minute).
func historyQueries(project, app string, step time.Duration) map[string]string {
	sel := fmt.Sprintf(`project=%q`, project)
	if app != "" {
		sel += fmt.Sprintf(`,app=%q`, app)
	}
	s := fmt.Sprintf("%ds", int(step.Seconds()))
	w := fmt.Sprintf("%ds", int(max(step, time.Minute).Seconds()))
	reqs := fmt.Sprintf(`sum(rate(tiffin_http_requests_total{%s}[%s]))`, sel, w)
	q := map[string]string{
		"requests": reqs + ` * 60`,
		"errors":   fmt.Sprintf(`sum(rate(tiffin_http_requests_total{%s,code="5xx"}[%s])) / %s`, sel, w, reqs),
		"p50":      fmt.Sprintf(`histogram_quantile(0.5, sum by (le) (rate(tiffin_http_request_duration_seconds_bucket{%s}[%s]))) * 1000`, sel, w),
		"p95":      fmt.Sprintf(`histogram_quantile(0.95, sum by (le) (rate(tiffin_http_request_duration_seconds_bucket{%s}[%s]))) * 1000`, sel, w),
	}
	if app != "" {
		q["memory"] = fmt.Sprintf(`sum(max_over_time(tiffin_container_memory_bytes{%s}[%s]))`, sel, s)
		q["cpu"] = fmt.Sprintf(`sum(rate(tiffin_container_cpu_seconds_total{%s}[%s])) * 100`, sel, w)
		return q
	}
	q["memory"] = fmt.Sprintf(`max(max_over_time(tiffin_project_memory_bytes{%s}[%s]))`, sel, s)
	q["memoryLimit"] = fmt.Sprintf(`max(max_over_time(tiffin_project_memory_limit_bytes{%s}[%s]))`, sel, s)
	q["cpu"] = fmt.Sprintf(`max(avg_over_time(tiffin_project_cpu_percent{%s}[%s]))`, sel, s)
	q["cpuLimit"] = fmt.Sprintf(`max(max_over_time(tiffin_project_cpu_limit_cores{%s}[%s])) * 100`, sel, s)
	q["connections"] = fmt.Sprintf(`max(max_over_time(tiffin_project_db_connections{%s}[%s]))`, sel, s)
	for _, part := range []string{"database", "files", "kv"} {
		q[part] = fmt.Sprintf(`max(max_over_time(tiffin_project_data_bytes{%s,part=%q}[%s]))`, sel, part, s)
	}
	return q
}

type historyKey struct{ project, app, rng string }

// historyCache keeps answers for a few seconds: a page polls, and every
// answer is about a dozen range queries.
type historyCache struct {
	mu sync.Mutex
	m  map[historyKey]*UsageHistory
}

const historyFresh = 20 * time.Second

func (c *historyCache) get(k historyKey, now time.Time) *UsageHistory {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.m[k]; h != nil && now.Sub(h.To) < historyFresh {
		return h
	}
	return nil
}

func (c *historyCache) put(k historyKey, h *UsageHistory) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 500 {
		c.m = map[historyKey]*UsageHistory{}
	}
	c.m[k] = h
}

// usageHistory runs the queries at once and keeps every series that
// returned points.
func (m *Module) usageHistory(ctx context.Context, project, app, rng string, now time.Time) (*UsageHistory, error) {
	k := historyKey{project, app, rng}
	if h := m.history.get(k, now); h != nil {
		return h, nil
	}
	r := historyRanges[rng]
	span, step := r[0], r[1]
	end := now.Truncate(step).Add(step)
	start := end.Add(-span)
	out := &UsageHistory{Project: project, App: app, Range: rng, From: start, To: now, StepSeconds: int(step.Seconds()), Series: map[string][][2]float64{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	for name, q := range historyQueries(project, app, step) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := m.vic.QueryMetrics(ctx, q, start, end, step, "project="+project)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if pts := matrixPoints(res); len(pts) > 0 {
				out.Series[name] = pts
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	m.history.put(k, out)
	return out, nil
}

// matrixPoints reads the first series of a range result as numbers,
// leaving out steps without a value.
func matrixPoints(r *PromResult) [][2]float64 {
	var mat []struct {
		Values [][2]any `json:"values"`
	}
	if json.Unmarshal(r.Result, &mat) != nil || len(mat) == 0 {
		return nil
	}
	out := make([][2]float64, 0, len(mat[0].Values))
	for _, v := range mat[0].Values {
		t, _ := strconv.ParseFloat(fmt.Sprint(v[0]), 64)
		f, err := strconv.ParseFloat(fmt.Sprint(v[1]), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		out = append(out, [2]float64{t, round3(f)})
	}
	return out
}

func (m *Module) registerHistory(a huma.API) {
	op := api.Op("project-usage-history", http.MethodGet, "/v1/projects/{project}/usage/history", "projects usage history", api.RiskRead,
		"Show a project's usage over time",
		"Memory and CPU against the project's limits, requests per minute, p50 and p95 response time and the share of 5xx answers over the last hour, "+
			"24 hours, 7 or 30 days, for the whole project or one app; for the whole project also its data on disk (database, files, KV) and open "+
			"database connections. From the box's metrics store (sampled every 15 seconds, kept for its metrics retention), about 150 points per series. "+
			"Now, with every limit explained: projects usage.", "observe")
	huma.Register(a, op, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `query:"app" maxLength:"40" doc:"One app (default: the whole project)"`
		Range   string `query:"range" enum:"1h,24h,7d,30d" default:"24h" doc:"How far back"`
	}) (*struct{ Body *UsageHistory }, error) {
		if err := m.ready(); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		rng := in.Range
		if rng == "" {
			rng = "24h"
		}
		h, err := m.usageHistory(ctx, in.Project, in.App, rng, time.Now())
		if err != nil {
			return nil, storeDown("metrics", err)
		}
		return &struct{ Body *UsageHistory }{h}, nil
	}))
}
