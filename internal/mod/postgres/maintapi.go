package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

func init() { platform.Register(&Pooler{}) }

// Pooler is the module for the connection pooler and Postgres minor
// updates. It needs the cluster, so it provisions after the postgres module.
type Pooler struct{}

func (*Pooler) Name() string    { return "pgbouncer" }
func (*Pooler) Order() int      { return 11 }
func (*Pooler) Needs() []string { return []string{"postgres"} }

// Provision installs and runs the pooler.
func (*Pooler) Provision(ctx context.Context, s *platform.System) error {
	return provisionPooler(ctx, s)
}

// Start keeps the pools in step with the projects and runs updates in the
// maintenance window.
func (*Pooler) Start(ctx context.Context, p *platform.Platform) error {
	go syncLoop(ctx, p)
	go maintLoop(ctx, p)
	return nil
}

// window is the box's maintenance window ("HH:MM", server time), "" none.
func window() string {
	c, err := platform.LoadServerConfig()
	if err != nil || c == nil {
		return ""
	}
	return c.RebootWindow
}

// windowDelay starts updates a quarter hour into the window, after a
// reboot that unattended-upgrades may have scheduled for its start.
const windowDelay = 15 * time.Minute

// nextRun is when the window next opens for updates after now ("" none).
func nextRun(now time.Time, win string) time.Time {
	t, err := time.ParseInLocation("15:04", win, now.Location())
	if err != nil {
		return time.Time{}
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location()).Add(windowDelay)
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// due reports whether a scheduled update should run now: within the hour
// after the window opens, once a day.
func due(now time.Time, win string, last time.Time) bool {
	open := nextRun(now, win)
	if open.IsZero() {
		return false
	}
	open = open.AddDate(0, 0, -1) // the most recent opening at or before now
	return now.Sub(open) < time.Hour && last.Before(open)
}

// maintLoop runs the scheduled update in the window, checks for updates
// daily without one, and copies updates into the audit log.
func maintLoop(ctx context.Context, p *platform.Platform) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(time.Minute)
		auditUpdates(ctx, p)
		now := time.Now()
		r := loadMaint()
		win := window()
		switch {
		case win != "" && due(now, win, r.LastScheduled):
			u, _, err := runUpdate(ctx, sysBox{log: func(string) {}}, runOpts{Trigger: "schedule"})
			if err == nil && u == nil {
				err = editMaint(func(r *maintRecord) { r.LastScheduled = now.UTC() })
			}
			if err != nil && !errors.Is(err, errBusy) {
				p.Log.Warn("postgres: scheduled update", "err", err)
				_ = editMaint(func(r *maintRecord) { r.LastScheduled = now.UTC() }) // try again tomorrow
			}
			if u != nil {
				p.Log.Info("postgres: scheduled update", "status", u.Status, "summary", u.Summary)
			}
			auditUpdates(ctx, p)
		case now.Sub(r.CheckedAt) > 24*time.Hour:
			if _, _, err := runUpdate(ctx, sysBox{log: func(string) {}}, runOpts{Trigger: "schedule", CheckOnly: true}); err != nil && !errors.Is(err, errBusy) {
				p.Log.Debug("postgres: check for updates", "err", err)
				_ = editMaint(func(r *maintRecord) { r.CheckedAt = now.UTC() }) // offline: check again tomorrow
			}
		}
	}
}

// auditUpdates writes updates the audit log does not have yet (the ones
// the schedule or tiffin up ran).
func auditUpdates(ctx context.Context, p *platform.Platform) {
	r := loadMaint()
	for i := len(r.Updates) - 1; i >= 0; i-- {
		u := r.Updates[i]
		if slices.Contains(r.Audited, u.ID) {
			continue
		}
		auditUpdate(ctx, p, "system", "", &u)
	}
}

func auditUpdate(ctx context.Context, p *platform.Platform, actor, session string, u *PGUpdate) {
	if p == nil || p.DB == nil {
		return
	}
	detail := map[string]any{"summary": u.Summary, "status": u.Status, "trigger": u.Trigger, "from": u.From, "to": u.To,
		"packages": u.Packages, "restarted": u.Restarted, "pauseSeconds": u.PauseSeconds}
	if session != "" {
		detail["session"] = session
	}
	if u.Error != "" {
		detail["error"] = u.Error
	}
	if p.DB.Audit(ctx, actor, "postgres.update", u.ID, detail) == nil {
		_ = editMaint(func(r *maintRecord) { r.Audited = append(r.Audited, u.ID) })
		// Nobody watched an update the box ran itself: tell the owner when it
		// failed (one started with --now reports to its caller).
		if u.Status == "failed" && u.Trigger != "now" {
			p.Notify(ctx, "postgres-update", "Postgres update failed: "+u.Error+". The pooler resumed and Postgres "+u.To+
				" keeps running. `tiffin maintenance show` has the record; run `tiffin maintenance postgres-update --now` after fixing the cause.")
		}
	}
}

// Checks reports the pooler and the last update.
func (*Pooler) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var out []platform.Check
	if st, err := poolerStats(ctx); err != nil {
		out = append(out, platform.Check{Name: "pgbouncer", OK: false, Detail: "the connection pooler is not answering on " + SocketDir + ": " + err.Error() +
			"; apps' DATABASE_URL goes through it (journalctl -u " + PoolerUnit + ")"})
	} else {
		out = append(out, platform.Check{Name: "pgbouncer", OK: true, Detail: fmt.Sprintf("PgBouncer %s on 127.0.0.1:%d (transaction pooling): %d pool(s), %d client(s), %d server connection(s)",
			st.Version, PoolerPort, st.Pools, st.Clients, st.Servers)})
	}
	return append(out, updateCheck(loadMaint(), window(), time.Now()))
}

// updateCheck says how Postgres updates stand.
func updateCheck(r maintRecord, win string, now time.Time) platform.Check {
	c := platform.Check{Name: "postgres-updates", OK: true}
	var parts []string
	if len(r.Updates) > 0 {
		u := r.Updates[0]
		when := u.At.Local().Format("2006-01-02 15:04")
		if u.Status == "failed" {
			c.OK = false
			parts = append(parts, fmt.Sprintf("the update at %s failed: %s. Fix the cause and run `tiffin maintenance postgres-update --now`", when, u.Error))
		} else {
			parts = append(parts, fmt.Sprintf("last update %s (%s)", u.Summary, when))
		}
	}
	what := availableWords(r.Available, r.Restart)
	switch {
	case what != "" && win != "":
		parts = append(parts, fmt.Sprintf("%s: installs in the maintenance window at %s", what, nextRun(now, win).Format("15:04")))
	case what != "":
		parts = append(parts, what+": run `tiffin maintenance postgres-update --now` (or set a maintenance window: tiffin up --reboot-window 04:00)")
	case !r.CheckedAt.IsZero():
		parts = append(parts, "up to date (checked "+r.CheckedAt.Local().Format("2006-01-02 15:04")+")")
	default:
		parts = append(parts, "not checked yet")
	}
	if slices.Contains(r.Restart, PoolerUnit) {
		parts = append(parts, poolerWaits)
	}
	c.Detail = strings.Join(parts, "; ")
	return c
}

const poolerWaits = "PgBouncer runs on replaced files until the box reboots (to restart it now, which closes apps' client connections: " +
	"tiffin maintenance postgres-update --now --restart-pooler)"

// availableWords lists what waits for the window ("" nothing); the
// pooler's restart is not among it.
func availableWords(pkgs []PGPackage, restart []string) string {
	var w []string
	for _, p := range pkgs {
		w = append(w, fmt.Sprintf("%s %s", p.Name, upstream(p.To)))
	}
	var units []string
	for _, u := range restart {
		if u != PoolerUnit {
			units = append(units, u)
		}
	}
	if len(units) > 0 {
		w = append(w, "restart of "+strings.Join(trimUnits(units), ", ")+" for updated libraries")
	}
	if len(w) == 0 {
		return ""
	}
	return strings.Join(w, ", ") + " waiting"
}

// PGMaintenance is how the box keeps Postgres up to date.
type PGMaintenance struct {
	Postgres  string      `json:"postgres" doc:"Running Postgres version"`
	Pooler    string      `json:"pooler,omitempty" doc:"Running PgBouncer version"`
	CheckedAt *time.Time  `json:"checkedAt,omitempty" doc:"When the box last looked for newer packages"`
	Available []PGPackage `json:"available" doc:"Newer packages waiting to install"`
	Restart   []string    `json:"restart" doc:"Box services running on replaced libraries, waiting for a restart"`
	Window    string      `json:"window,omitempty" doc:"The maintenance window (HH:MM, server time): updates run 15 minutes after it opens. Empty: none (tiffin up --reboot-window 04:00 sets one)"`
	NextRun   *time.Time  `json:"nextRun,omitempty"`
	Updates   []PGUpdate  `json:"updates" doc:"Recent updates, newest first"`
}

func maintenance(ctx context.Context) *PGMaintenance {
	r := loadMaint()
	out := &PGMaintenance{Postgres: sysBox{}.ServerVersion(ctx), Available: r.Available, Restart: r.Restart, Window: window(), Updates: r.Updates}
	if st, err := poolerStats(ctx); err == nil {
		out.Pooler = st.Version
	}
	if !r.CheckedAt.IsZero() {
		out.CheckedAt = &r.CheckedAt
	}
	if out.Window != "" {
		n := nextRun(time.Now(), out.Window)
		out.NextRun = &n
	}
	for _, l := range []*[]PGPackage{&out.Available} {
		if *l == nil {
			*l = []PGPackage{}
		}
	}
	if out.Restart == nil {
		out.Restart = []string{}
	}
	if out.Updates == nil {
		out.Updates = []PGUpdate{}
	}
	return out
}

// PGUpdateResult is the outcome of postgres-update.
type PGUpdateResult struct {
	Update      *PGUpdate      `json:"update,omitempty" doc:"The update that ran (with now). Absent when nothing needed installing or restarting."`
	Summary     string         `json:"summary"`
	Maintenance *PGMaintenance `json:"maintenance"`
}

// RegisterAPI adds the maintenance operations.
func (*Pooler) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "postgres"
	admin := func(ctx context.Context) error {
		if !api.PrincipalFrom(ctx).BoxAdmin() {
			return fmt.Errorf("%w: box maintenance needs a key with full access to all projects", tokens.ErrForbidden)
		}
		return onBox(p)
	}

	show := api.Op("maintenance-show", http.MethodGet, "/v1/box/maintenance", "maintenance show", api.RiskRead,
		"Show Postgres updates",
		"How the box keeps Postgres, its extensions and the connection pooler (PgBouncer) up to date: running versions, newer packages waiting, "+
			"the maintenance window and recent updates with how long queries were held. Box admins only.", tag)
	huma.Register(a, show, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *PGMaintenance }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		return &struct{ Body *PGMaintenance }{maintenance(ctx)}, nil
	}))

	up := api.Op("maintenance-postgres-update", http.MethodPost, "/v1/box/maintenance/postgres-update", "maintenance postgres-update", api.RiskWrite,
		"Update Postgres to its newest minor version",
		"Looks for newer Postgres 18, pgvector, pg_cron and PgBouncer packages (minor versions only) and box services running on replaced libraries. "+
			"Without now it only checks and says when the maintenance window installs them. With now it installs them at once: "+
			"download and install while the old version runs, CHECKPOINT, pause the connection pooler (apps' queries wait instead of failing), "+
			"restart Postgres, wait until it answers, resume; then restart other box services on replaced libraries. The pause is usually well under a second. "+
			"Direct connections (DIRECT_DATABASE_URL, LISTEN) are closed by the restart and reconnect. The pooler itself restarts only with restartPooler, "+
			"because that closes every app's client connections. Nothing is deleted; if the new version does not start, the old packages go back, "+
			"and the pooler always resumes. Box admins only.", tag)
	up.Errors = append(up.Errors, 409)
	huma.Register(a, up, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Now           bool `json:"now,omitempty" doc:"Install now instead of in the maintenance window"`
			RestartPooler bool `json:"restartPooler,omitempty" doc:"With now: also restart PgBouncer when a new version of it is installed or its libraries were replaced. Every client connection closes; apps' pools reconnect (node-postgres needs pool.on('error') to survive it). Without it the pooler takes the new files at the next reboot."`
		}
	}) (*struct{ Body *PGUpdateResult }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		if in.Body.RestartPooler && !in.Body.Now {
			return nil, api.NewProblem(422, "validation", "restartPooler goes with now")
		}
		pr := api.PrincipalFrom(ctx)
		u, plan, err := runUpdate(ctx, sysBox{log: func(string) {}}, runOpts{Trigger: "now", CheckOnly: !in.Body.Now, RestartPooler: in.Body.RestartPooler})
		if errors.Is(err, errBusy) {
			return nil, api.NewProblem(409, "conflict", "an update is already running; try again in a minute")
		}
		if err != nil {
			pb := api.NewProblem(502, "internal", "could not check for updates: "+err.Error())
			pb.Hint = "the box reads apt.postgresql.org; check its network and try again"
			return nil, pb
		}
		out := &PGUpdateResult{Update: u}
		if u != nil {
			auditUpdate(ctx, p, pr.TokenID, pr.Session, u)
		}
		out.Maintenance = maintenance(ctx)
		what := availableWords(plan.Packages, plan.Flagged)
		switch {
		case u != nil && u.Status == "failed":
			pb := api.NewProblem(500, "internal", "the update failed: "+u.Summary)
			pb.Hint = "nothing was lost: the pooler resumed and Postgres runs. `tiffin maintenance show` has the record; journalctl -u " + UnitName + " has details"
			return nil, pb
		case u != nil:
			out.Summary = u.Summary
		case what == "":
			out.Summary = "up to date: Postgres " + out.Maintenance.Postgres
		case out.Maintenance.NextRun != nil:
			out.Summary = what + "; installs at " + out.Maintenance.NextRun.Format("2006-01-02 15:04") + " (maintenance window), or now with --now"
		default:
			out.Summary = what + "; run with --now to install (there is no maintenance window)"
		}
		if slices.Contains(out.Maintenance.Restart, PoolerUnit) {
			out.Summary += "; " + poolerWaits
		}
		return &struct{ Body *PGUpdateResult }{out}, nil
	}))
}
