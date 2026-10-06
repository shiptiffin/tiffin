// Package update keeps the box on the newest Tiffin release of its channel
// by itself. Once a day (at a time of its own) it fetches the channel's
// signed manifest and checks it: signed by a key this build trusts, for
// this channel, newer than the running version, and this version at least
// the release's minVersion. In the maintenance window, half an hour in
// (after Postgres's own updates), it installs it when the box falls in the
// release's rollout: download the build and check its sha256 against the
// signed manifest, take a backup and wait for it, then let `tiffin
// self-update --staged` switch to it (provision, unit files, health check,
// rollback) in a unit of its own, since it restarts this service. The
// outcome comes back as a file; the box records it in the audit log and
// alerts the owner when an update failed or rolled back.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/install"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/release"
)

// Update is one Tiffin update the box ran.
type Update struct {
	ID            string    `json:"id"`
	At            time.Time `json:"at"`
	Trigger       string    `json:"trigger" enum:"schedule,now" doc:"The maintenance window, or someone (update apply)"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	Status        string    `json:"status" enum:"running,ok,rolled-back,failed"`
	Backup        string    `json:"backup,omitempty" doc:"The backup taken just before it"`
	EdgeRestart   bool      `json:"edgeRestart,omitempty" doc:"The release changed the edge, so the edge restarted on it too"`
	EdgeRestartMs int64     `json:"edgeRestartMs,omitempty" doc:"How long the edge's restart took (its socket held the ports meanwhile)"`
	Seconds       float64   `json:"seconds,omitempty"`
	Error         string    `json:"error,omitempty"`
	Summary       string    `json:"summary"`
}

// Available is the newest release of the box's channel, when it is newer
// than what runs.
type Available struct {
	Version     string    `json:"version"`
	Date        time.Time `json:"date"`
	Notes       string    `json:"notes,omitempty" doc:"Release notes URL"`
	Rollout     int       `json:"rollout" doc:"Percentage of boxes it goes to automatically now"`
	InRollout   bool      `json:"inRollout" doc:"This box is among them"`
	EdgeRestart bool      `json:"edgeRestart,omitempty"`
	Blocked     string    `json:"blocked,omitempty" doc:"Why this box will not install it (it needs a release in between first, say)"`
}

// record is the update state, a file: the staged update (another process)
// writes its outcome next to it.
type record struct {
	BoxID         string     `json:"boxID"`
	Manual        bool       `json:"manual,omitempty"` // automatic updates off
	Channel       string     `json:"channel,omitempty"`
	Source        string     `json:"source,omitempty"`
	Window        string     `json:"window,omitempty"`
	CheckedAt     time.Time  `json:"checkedAt"`
	NextCheck     time.Time  `json:"nextCheck"`
	Available     *Available `json:"available,omitempty"`
	CheckError    string     `json:"checkError,omitempty"`
	LastScheduled time.Time  `json:"lastScheduled"`
	Updates       []Update   `json:"updates"` // newest first
	Audited       []string   `json:"audited"`
}

func (r record) channel() string { return or(r.Channel, "stable") }
func (r record) source() string  { return or(r.Source, release.DefaultSource) }

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Paths, overridable in tests.
var (
	statePath = install.Home + "/update.json"
	dlDir     = install.UpdatesDir
)

var fileMu sync.Mutex

func load() record {
	var r record
	if raw, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(raw, &r)
	}
	return r
}

// edit changes the record under the lock; it gives the box an ID first.
func edit(f func(*record)) (record, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	r := load()
	if r.BoxID == "" {
		r.BoxID = ids.New("box")
	}
	f(&r)
	if len(r.Updates) > 30 {
		r.Updates = r.Updates[:30]
	}
	if len(r.Audited) > 60 {
		r.Audited = r.Audited[len(r.Audited)-60:]
	}
	raw, _ := json.MarshalIndent(r, "", "  ")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return r, err
	}
	if err := os.WriteFile(statePath+".tmp", raw, 0o600); err != nil {
		return r, err
	}
	return r, os.Rename(statePath+".tmp", statePath)
}

// box is what an update acts on: the real box, fakes in tests.
type box interface {
	// HoldPostgres keeps Postgres updates from starting until released.
	HoldPostgres() (release func(), err error)
	// Backup takes a backup and waits for it, and for one already running.
	Backup(ctx context.Context) (string, error)
	// Launch starts `tiffin self-update --staged id bin` in its own unit.
	Launch(ctx context.Context, id, bin string, restartEdge bool) error
}

// updater runs checks and updates.
type updater struct {
	box      box
	client   *http.Client
	keys     []release.PublicKey
	version  string // the running version
	platform string // "linux/arm64"
	notify   func(ctx context.Context, subject, summary string)
	log      func(msg string, args ...any)

	mu     sync.Mutex // one check or update at a time
	heldMu sync.Mutex
	held   func() // the Postgres hold of a launched update
}

func newUpdater(b box, version string, notify func(context.Context, string, string), log func(string, ...any)) *updater {
	return &updater{box: b, client: &http.Client{Timeout: 5 * time.Minute}, keys: release.TrustedKeys(), version: version,
		platform: runtime.GOOS + "/" + runtime.GOARCH, notify: notify, log: log}
}

// errBusy: a check or update is running.
var errBusy = errors.New("an update is already running; try again in a minute")

// check fetches the channel's manifest and records what it offers.
func (u *updater) check(ctx context.Context) (*release.Manifest, record, error) {
	r, _ := edit(func(*record) {})
	m, err := release.Fetch(ctx, u.client, r.source(), r.channel(), u.keys)
	now := time.Now().UTC()
	prevErr := r.CheckError
	r, serr := edit(func(r *record) {
		r.CheckedAt, r.CheckError, r.Available = now, "", nil
		r.NextCheck = now.Add(20*time.Hour + rand.N(8*time.Hour)) // daily, at a time of its own
		if err != nil {
			r.CheckError = err.Error()
			return
		}
		if aerr := m.Allowed(u.version); !errors.Is(aerr, release.ErrNotNewer) {
			r.Available = &Available{Version: m.Version, Date: m.Date, Notes: m.Notes, Rollout: m.Rollout,
				InRollout: m.InRollout(r.BoxID), EdgeRestart: m.EdgeRestart}
			if aerr != nil {
				r.Available.Blocked = aerr.Error()
			}
		}
	})
	if err == nil {
		err = serr
	}
	// A release the box refused (signature, channel) is worth a look, once.
	if errors.Is(err, release.ErrRefused) && err.Error() != prevErr {
		u.notify(ctx, "tiffin-update", "The box refused a Tiffin release: "+err.Error()+". Nothing was installed.")
	}
	return m, r, err
}

// outcome of apply when nothing was started.
type nothing struct{ why string }

func (n nothing) Error() string { return n.why }

// apply installs the channel's newest release now (trigger "now"), or in
// the window when the box is in its rollout ("schedule"). It returns the
// running update, or a nothing error that says why none started.
func (u *updater) apply(ctx context.Context, trigger string) (*Update, error) {
	if !u.mu.TryLock() {
		return nil, errBusy
	}
	defer u.mu.Unlock()
	if r := load(); running(r) != nil {
		return nil, errBusy
	}
	m, r, err := u.check(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.Allowed(u.version); err != nil {
		if errors.Is(err, release.ErrNotNewer) {
			return nil, nothing{"up to date: Tiffin " + u.version}
		}
		return nil, nothing{err.Error()}
	}
	if trigger == "schedule" && !m.InRollout(r.BoxID) {
		return nil, nothing{fmt.Sprintf("Tiffin %s goes to %d%% of boxes for now, and this one is not among them yet", m.Version, m.Rollout)}
	}
	up := &Update{ID: ids.New("upd"), At: time.Now().UTC(), Trigger: trigger, From: u.version, To: m.Version, Status: "running", EdgeRestart: m.EdgeRestart}
	fail := func(err error) (*Update, error) {
		up.Status, up.Error = "failed", err.Error()
		up.Seconds = time.Since(up.At).Round(100 * time.Millisecond).Seconds()
		up.Summary = summary(up)
		_ = u.save(up)
		return up, nil
	}
	dir := filepath.Join(dlDir, up.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(err)
	}
	bin := filepath.Join(dir, "tiffin")
	if _, err := release.Download(ctx, u.client, m, release.ManifestURL(r.source(), r.channel()), u.platform, bin); err != nil {
		_ = os.RemoveAll(dir)
		return fail(err)
	}
	hold, err := u.box.HoldPostgres()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, errBusy // a Postgres update runs: the next minute
	}
	if up.Backup, err = u.box.Backup(ctx); err != nil {
		hold()
		_ = os.RemoveAll(dir)
		return fail(fmt.Errorf("the backup before the update failed, so nothing was installed: %w", err))
	}
	up.Summary = summary(up)
	if err := u.save(up); err != nil {
		hold()
		return nil, err
	}
	if err := u.box.Launch(ctx, up.ID, bin, m.EdgeRestart); err != nil {
		hold()
		_ = os.RemoveAll(dir)
		return fail(fmt.Errorf("start the update: %w", err))
	}
	u.heldMu.Lock()
	u.held = hold // until the outcome is in, or this process stops
	u.heldMu.Unlock()
	return up, nil
}

func (u *updater) save(up *Update) error {
	_, err := edit(func(r *record) {
		for i := range r.Updates {
			if r.Updates[i].ID == up.ID {
				r.Updates[i] = *up
				return
			}
		}
		r.Updates = append([]Update{*up}, r.Updates...)
	})
	return err
}

// running is the update in progress, or nil.
func running(r record) *Update {
	for i := range r.Updates {
		if r.Updates[i].Status == "running" {
			return &r.Updates[i]
		}
	}
	return nil
}

// stale is how long an update may run before the box gives up on it (the
// machine restarted in the middle, say).
const stale = time.Hour

// collect takes in the outcomes staged updates wrote, and gives up on an
// update that never wrote one.
func (u *updater) collect(now time.Time) {
	r := load()
	up := running(r)
	if up == nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(dlDir, up.ID+".result.json"))
	var res install.StagedResult
	switch {
	case err == nil && json.Unmarshal(raw, &res) == nil:
		up.Status, up.Error, up.Seconds, up.EdgeRestartMs = res.Status, res.Error, res.Seconds, res.EdgeRestartMs
	case now.Sub(up.At) > stale:
		up.Status, up.Error = "failed", "it never finished (did the machine restart?); the build that answers runs"
	default:
		return
	}
	up.Summary = summary(up)
	if u.save(up) == nil {
		_ = os.Remove(filepath.Join(dlDir, up.ID+".result.json"))
		_ = os.RemoveAll(filepath.Join(dlDir, up.ID))
	}
	u.heldMu.Lock()
	if u.held != nil {
		u.held()
		u.held = nil
	}
	u.heldMu.Unlock()
}

func summary(up *Update) string {
	switch up.Status {
	case "running":
		return fmt.Sprintf("updating Tiffin %s → %s (backup %s taken)", up.From, up.To, up.Backup)
	case "ok":
		s := fmt.Sprintf("Tiffin %s → %s in %.0f s", up.From, up.To, up.Seconds)
		if up.EdgeRestart {
			s += fmt.Sprintf(", edge restarted in %d ms", up.EdgeRestartMs)
		}
		return s
	case "rolled-back":
		return fmt.Sprintf("Tiffin %s was unhealthy and rolled back: %s keeps running", up.To, up.From)
	}
	return fmt.Sprintf("the update to Tiffin %s failed: %s", up.To, firstLine(up.Error))
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// audit writes finished updates the audit log does not have yet, and tells
// the owner about the ones that failed.
func audit(ctx context.Context, p *platform.Platform) {
	if p == nil || p.DB == nil {
		return
	}
	r := load()
	for i := len(r.Updates) - 1; i >= 0; i-- {
		up := r.Updates[i]
		if up.Status == "running" || slices.Contains(r.Audited, up.ID) {
			continue
		}
		detail := map[string]any{"summary": up.Summary, "status": up.Status, "trigger": up.Trigger, "from": up.From, "to": up.To,
			"backup": up.Backup, "edgeRestart": up.EdgeRestart, "seconds": up.Seconds}
		if up.Error != "" {
			detail["error"] = up.Error
		}
		if p.DB.Audit(ctx, "system", "box.update", up.ID, detail) != nil {
			continue
		}
		_, _ = edit(func(r *record) { r.Audited = append(r.Audited, up.ID) })
		if up.Status != "ok" {
			p.Notify(ctx, "tiffin-update", "Tiffin update failed: "+up.Summary+". `tiffin update status` has the record.")
		}
	}
}

// window is when updates may install ("HH:MM", server time): the update
// setting, else the box's maintenance window (tiffin up --reboot-window).
func window(r record) string {
	if r.Window != "" {
		return r.Window
	}
	if c, err := platform.LoadServerConfig(); err == nil && c != nil {
		return c.RebootWindow
	}
	return ""
}

// windowDelay starts Tiffin updates half an hour into the window: after a
// reboot unattended-upgrades may do at its start, and after the Postgres
// update a quarter hour in.
const windowDelay = 30 * time.Minute

// nextRun is when the window next opens for updates after now (zero: none).
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

// due reports whether the scheduled update should run now: within the
// hour after its time, once a day.
func due(now time.Time, win string, last time.Time) bool {
	open := nextRun(now, win)
	if open.IsZero() {
		return false
	}
	open = open.AddDate(0, 0, -1)
	return now.Sub(open) < time.Hour && last.Before(open)
}

// tick is one minute of the loop.
func (u *updater) tick(ctx context.Context, p *platform.Platform, now time.Time) {
	u.collect(now)
	audit(ctx, p)
	r := load()
	if running(r) != nil {
		return
	}
	if win := window(r); !r.Manual && win != "" && due(now, win, r.LastScheduled) {
		up, err := u.apply(ctx, "schedule")
		if errors.Is(err, errBusy) {
			return // a Postgres update or a check runs: the next minute
		}
		_, _ = edit(func(r *record) { r.LastScheduled = now.UTC() })
		var n nothing
		switch {
		case up != nil:
			u.log("tiffin update", "status", up.Status, "summary", up.Summary)
		case err != nil && !errors.As(err, &n):
			u.log("tiffin update: check", "err", err)
		}
		audit(ctx, p)
		return
	}
	if !r.NextCheck.After(now) {
		_, _, _ = u.checkNow(ctx)
	}
}

// checkNow is check, unless an update or another check runs.
func (u *updater) checkNow(ctx context.Context) (*release.Manifest, record, error) {
	if !u.mu.TryLock() {
		return nil, load(), errBusy
	}
	defer u.mu.Unlock()
	return u.check(ctx)
}
