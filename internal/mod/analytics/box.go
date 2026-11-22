package analytics

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// BoxTotals are the box's headline numbers: every project's, added up.
// Visitors are hashed per project, so a person on two projects counts in
// each, and the sum is exact.
type BoxTotals struct {
	Visitors  int64 `json:"visitors"`
	Pageviews int64 `json:"pageviews"`
}

// ProjectTraffic is one project's row in the box overview.
type ProjectTraffic struct {
	Project   string    `json:"project"`
	Visitors  int64     `json:"visitors"`
	Pageviews int64     `json:"pageviews"`
	Previous  BoxTotals `json:"previous" doc:"The same-length range just before"`
	Series    []int64   `json:"series" doc:"Visitors per step, in step with timeseries.points"`
	TopSource string    `json:"topSource,omitempty" doc:"The referrer source that sent the most visitors; empty when every visit was direct"`
}

// BoxOverview is every project's traffic side by side.
type BoxOverview struct {
	Period     string           `json:"period"`
	From       time.Time        `json:"from"`
	To         time.Time        `json:"to"`
	Totals     BoxTotals        `json:"totals"`
	Previous   BoxTotals        `json:"previous" doc:"The same-length range just before, for comparison"`
	Timeseries Timeseries       `json:"timeseries" doc:"The box's visitors and page views per step (hours for 24h, days beyond)"`
	Projects   []ProjectTraffic `json:"projects" doc:"Every project, most visitors first; a project without visits is listed at zero"`
}

// BoxOverview computes every project's traffic for a period. Each project's
// numbers come from the same totals, series and breakdown as its own
// overview, so the two always agree.
func (m *Module) BoxOverview(ctx context.Context, period string, now time.Time) (*BoxOverview, error) {
	from, to, label, aligned, err := resolve(period, "", "", now)
	if err != nil {
		return nil, err
	}
	if err := m.pipe.RollupDirty(ctx); err != nil {
		return nil, err
	}
	names, err := m.p.DB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	span := to.Sub(from)
	gran := "day"
	if span <= 48*time.Hour {
		gran = "hour"
	}
	o := &BoxOverview{Period: label, From: from, To: to, Timeseries: Timeseries{Granularity: gran, Points: []Point{}}, Projects: []ProjectTraffic{}}
	for _, name := range names {
		q := Query{Project: name, From: from, To: to}
		cur, err := m.totals(ctx, q, aligned)
		if err != nil {
			return nil, err
		}
		prev, err := m.totals(ctx, Query{Project: name, From: from.Add(-span), To: from}, aligned)
		if err != nil {
			return nil, err
		}
		ts, err := m.series(ctx, q, gran, now)
		if err != nil {
			return nil, err
		}
		row := ProjectTraffic{Project: name, Visitors: cur.Visitors, Pageviews: cur.Pageviews,
			Previous: BoxTotals{prev.Visitors, prev.Pageviews}, Series: make([]int64, len(ts.Points))}
		if len(o.Timeseries.Points) == 0 {
			for _, p := range ts.Points {
				o.Timeseries.Points = append(o.Timeseries.Points, Point{T: p.T})
			}
		}
		for i, p := range ts.Points {
			row.Series[i] = p.Visitors
			if i < len(o.Timeseries.Points) {
				b := &o.Timeseries.Points[i]
				b.Visitors += p.Visitors
				b.Pageviews += p.Pageviews
				b.Sessions += p.Sessions
				b.Bounces += p.Bounces
				b.DurationMS += p.DurationMS
			}
		}
		if cur.Visitors > 0 {
			top, err := m.store.Top(ctx, q, "referrer", 1)
			if err != nil {
				return nil, err
			}
			if len(top) > 0 {
				row.TopSource = top[0].Value
			}
		}
		o.Totals.Visitors += cur.Visitors
		o.Totals.Pageviews += cur.Pageviews
		o.Previous.Visitors += prev.Visitors
		o.Previous.Pageviews += prev.Pageviews
		o.Projects = append(o.Projects, row)
	}
	sort.SliceStable(o.Projects, func(i, j int) bool {
		a, b := o.Projects[i], o.Projects[j]
		if a.Visitors != b.Visitors {
			return a.Visitors > b.Visitors
		}
		if a.Pageviews != b.Pageviews {
			return a.Pageviews > b.Pageviews
		}
		return a.Project < b.Project
	})
	return o, nil
}

func (m *Module) registerBoxAPI(a huma.API) {
	huma.Register(a, api.Untrusted(api.Op("analytics-box", http.MethodGet, "/v1/analytics/box", "analytics box", api.RiskRead,
		"Show every project's web analytics",
		"The box at a glance: for a period, each project's visitors and page views, the same two for the period before, its visitors per hour or day and its top source, "+
			"most visitors first, with the box's totals and timeseries. A project without visits is listed at zero. "+
			"Each project's numbers are the ones its own analytics overview gives (bots and Global Privacy Control excluded, visitors unique per day). "+
			"Needs a key for all projects."+untrusted, "analytics")),
		api.Wrap(func(ctx context.Context, in *struct {
			Period string `query:"period" enum:"24h,7d,30d" doc:"24h, 7d (the default) or 30d. Days are UTC."`
		}) (*struct{ Body *BoxOverview }, error) {
			if m.store == nil {
				p := api.NewProblem(409, "precondition", "analytics runs on a Tiffin box; this server was started without --box")
				p.Hint = "use a box: tiffin up"
				return nil, p
			}
			if err := api.PrincipalFrom(ctx).RequireBox(tokens.ScopeRead); err != nil {
				return nil, err
			}
			o, err := m.BoxOverview(ctx, in.Period, time.Now())
			if err != nil {
				return nil, err
			}
			return &struct{ Body *BoxOverview }{o}, nil
		}))
}
