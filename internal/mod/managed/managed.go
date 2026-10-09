// Package managed is the box side of a ShipTiffin managed box: one that
// shiptiffin.com installed in the customer's own cloud account. It is off on
// every other box (no /etc/tiffin/managed.json).
//
// Every six hours (and once soon after it starts) the box checks in with
// the control plane: its Tiffin version, how long it has run, the names of
// any failing status checks, and whether its owner has signed in yet.
// Nothing else: no project names, no data, no addresses.
//
// Until the owner first signs in, the control plane may ask in its answer
// for a fresh one-time owner sign-in link (when the dashboard first answers
// over HTTPS, or when the customer asks for a new one): the box makes it
// itself (tokens.HandoffLink: the box enforces its 24 hours and its single
// use) and sends it at once in a second check-in. After the first sign-in
// the box refuses to make any more. While the hand-off is pending the
// control plane may ask the box to check in sooner (a few minutes). The answer says whether the subscription is active, which
// decides whether the box installs Tiffin updates by itself. Nothing here
// ever stops or slows the customer's apps, whatever the answer.
//
// Off-site backups: the box makes an X25519 key pair of its own and sends
// the public half with every check-in. While the subscription is active the
// answer carries temporary credentials for the box's folder in ShipTiffin's
// backup bucket, sealed to that key (the website passes them on and cannot
// read them); the box opens them and hands them to the backup module, which
// copies every backup there, encrypted with a passphrase only the owner holds.
//
// After a resize (the control plane changes the server type through the
// Hetzner API, without logging in) the box notices its memory changed and
// provisions again, so Postgres, Valkey and the apps' share are retuned.
package managed

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/licence"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/sealbox"
	"github.com/btahir/tiffin/internal/tokens"
)

func init() { platform.Register(&Module{}) }

const (
	// Every is how often a box checks in, give or take Jitter. The control
	// plane parks a box's shiptiffin.app address after 72 hours without a
	// check-in, and puts it back at the next one: often enough that a
	// renewal or a restart brings the address back within hours.
	Every  = 6 * time.Hour
	Jitter = 30 * time.Minute
	// Retry is the wait after a check-in that got no answer.
	Retry = time.Hour
	// FirstWithin: the first check-in comes within this after start.
	FirstWithin = 5 * time.Minute
	timeout     = 20 * time.Second
)

// Module checks in with the control plane.
type Module struct {
	client  *http.Client
	started time.Time
	links   Handoff
	// key opens the off-site grants sealed to the box (nil: none can be).
	key *ecdh.PrivateKey
	// given: the expiry of the grant last handed to the backup module.
	given time.Time
}

// Handoff is the box's side of handing it to its owner (tokens.Manager).
type Handoff interface {
	HandoffDone(ctx context.Context) (bool, error)
	HandoffLink(ctx context.Context, ttl time.Duration) (string, time.Time, error)
}

// MinCheckIn is the soonest a control plane may ask for the next check-in.
const MinCheckIn = time.Minute

func (*Module) Name() string { return "managed" }

// Order: with the other outward-facing loops, after every module whose
// checks it reports.
func (*Module) Order() int { return 91 }

// Start does nothing on a box that is not managed.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	cfg, err := platform.LoadManagedConfig()
	if err != nil {
		p.Log.Warn("managed: reading the config", "err", err)
		return nil
	}
	if cfg == nil || p.DB == nil {
		return nil
	}
	if err := checkLicence(cfg); err != nil {
		p.Log.Warn("managed: the licence does not check out; not checking in", "err", err)
		return nil
	}
	m.started = time.Now()
	if p.Tokens != nil {
		m.links = p.Tokens
	}
	if p.Secrets != nil {
		if m.key, err = offsiteKey(ctx, p); err != nil {
			p.Log.Warn("managed: the off-site backup key", "err", err)
		}
	}
	go m.retune(ctx, p)
	go func() {
		for !p.Started() {
			if !sleep(ctx, time.Second) {
				return
			}
		}
		// A grant from an earlier answer still holds until a check-in brings a newer one.
		m.giveGrant(ctx, p, cfg, platform.LoadManagedState())
		wait := mrand.N(FirstWithin)
		for sleep(ctx, wait) {
			st, soon := m.checkIn(ctx, p, cfg)
			switch {
			case st.Error != "":
				p.Log.Info("managed: check-in got no answer; trying again in an hour", "err", st.Error)
				wait = Retry
			case soon > 0:
				wait = soon
			default:
				wait = Every - Jitter + mrand.N(2*Jitter)
			}
		}
	}()
	return nil
}

func checkLicence(cfg *platform.ManagedConfig) error {
	pub, err := licence.ParsePublicKey(cfg.PublicKey)
	if err != nil {
		return err
	}
	l, err := licence.Verify(pub, cfg.Licence)
	if err != nil {
		return err
	}
	if l.BoxID != cfg.BoxID {
		return fmt.Errorf("the licence is for %s, not %s", l.BoxID, cfg.BoxID)
	}
	return nil
}

// Report is what a check-in sends: version and health, whether the owner
// has signed in yet, and (only when the control plane asked) a fresh
// one-time sign-in link. Nothing else.
type Report struct {
	BoxID         string   `json:"boxID"`
	Version       string   `json:"version"`
	UptimeSeconds int64    `json:"uptimeSeconds"`
	Checks        int      `json:"checks"`
	FailedChecks  int      `json:"failedChecks"`
	Failing       []string `json:"failing,omitempty"`
	// Handoff: "pending" (the owner hasn't signed in yet) or "done".
	Handoff string  `json:"handoff,omitempty"`
	Signin  *Signin `json:"signin,omitempty"`
	// BackupKey is the public half of the box's X25519 key: off-site
	// credentials are sealed to it.
	BackupKey string `json:"backupKey,omitempty"`
}

// Signin is a one-time owner sign-in link the box made for its hand-off.
type Signin struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Answer is the control plane's reply.
type Answer struct {
	Managed bool   `json:"managed"` // false: the box was released from ShipTiffin
	Active  bool   `json:"active"`
	Updates bool   `json:"updates"`
	Message string `json:"message,omitempty"`
	// Signin asks for a fresh hand-off link (refused once the owner signed in).
	Signin bool `json:"signin,omitempty"`
	// CheckInSeconds asks for the next check-in sooner (at least MinCheckIn).
	CheckInSeconds int `json:"checkInSeconds,omitempty"`
	// Offsite is the off-site backup grant, sealed to BackupKey (only
	// while the subscription is active).
	Offsite *SealedGrant `json:"offsite,omitempty"`
}

// SealedGrant is a platform.OffsiteGrant sealed to the box's key.
type SealedGrant struct {
	Sealed    string    `json:"sealed"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (m *Module) checkIn(ctx context.Context, p *platform.Platform, cfg *platform.ManagedConfig) (platform.ManagedState, time.Duration) {
	var checks []platform.Check
	if p.BoxChecks != nil {
		checks = p.BoxChecks(ctx)
	} else {
		checks = p.Checks(ctx)
	}
	r := report(cfg.BoxID, p.Version, time.Since(m.started), checks)
	if m.key != nil {
		r.BackupKey = sealbox.KeyText(m.key.PublicKey().Bytes())
	}
	st, soon := CheckIn(ctx, m.httpClient(), cfg, r, m.links, platform.LoadManagedState(), time.Now())
	if err := platform.SaveManagedState(st); err != nil {
		p.Log.Warn("managed: saving the state", "err", err)
	}
	m.giveGrant(ctx, p, cfg, st)
	return st, soon
}

// ---- off-site backups ----

const (
	nsManaged     = "managed"
	offsiteKeyKey = "offsite-key"
)

// offsiteKey is the box's X25519 key for off-site grants, made once and
// kept sealed with the box key.
func offsiteKey(ctx context.Context, p *platform.Platform) (*ecdh.PrivateKey, error) {
	if raw, ok, err := p.DB.KVGet(ctx, nsManaged, offsiteKeyKey); err != nil {
		return nil, err
	} else if ok {
		plain, err := p.Secrets.Unseal(raw)
		if err != nil {
			return nil, err
		}
		return ecdh.X25519().NewPrivateKey(plain)
	}
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sealed, err := p.Secrets.Seal(k.Bytes())
	if err != nil {
		return nil, err
	}
	if err := p.DB.KVPut(ctx, nsManaged, offsiteKeyKey, sealed); err != nil {
		return nil, err
	}
	return k, nil
}

// OpenGrant opens a sealed grant and checks it is for this box and current.
func OpenGrant(key *ecdh.PrivateKey, boxID, sealed string, now time.Time) (*platform.OffsiteGrant, error) {
	plain, err := sealbox.Open(key, sealed, platform.OffsiteGrantAAD(boxID))
	if err != nil {
		return nil, fmt.Errorf("off-site grant: %w", err)
	}
	var g platform.OffsiteGrant
	if err := json.Unmarshal(plain, &g); err != nil {
		return nil, fmt.Errorf("off-site grant: %w", err)
	}
	if err := g.Validate(boxID, now); err != nil {
		return nil, err
	}
	return &g, nil
}

// giveGrant hands the newest grant to the backup module, once.
func (m *Module) giveGrant(ctx context.Context, p *platform.Platform, cfg *platform.ManagedConfig, st platform.ManagedState) {
	if m.key == nil || st.OffsiteSealed == "" || st.OffsiteExpiresAt.Equal(m.given) {
		return
	}
	g, err := OpenGrant(m.key, cfg.BoxID, st.OffsiteSealed, time.Now())
	if err == nil {
		err = platform.GiveOffsiteGrant(ctx, g)
	}
	if err != nil {
		p.Log.Warn("managed: off-site backup credentials", "err", err)
		return
	}
	m.given = st.OffsiteExpiresAt
}

// CheckIn sends one report with the hand-off's state and, when the answer
// asks for a sign-in link (and the owner hasn't signed in), makes one and
// sends it in a second report. soon is when the control plane wants the next
// check-in (0: the usual time).
func CheckIn(ctx context.Context, c *http.Client, cfg *platform.ManagedConfig, r Report, links Handoff, prev platform.ManagedState, now time.Time) (st platform.ManagedState, soon time.Duration) {
	if links != nil {
		if done, err := links.HandoffDone(ctx); err == nil {
			r.Handoff = "pending"
			if done {
				r.Handoff = "done"
			}
		}
	}
	st, ans := exchange(ctx, c, cfg, r, prev, now)
	if ans != nil && ans.Signin && r.Handoff == "pending" {
		code, exp, err := links.HandoffLink(ctx, tokens.BootstrapTTL)
		switch {
		case err == nil:
			r.Signin = &Signin{Code: code, ExpiresAt: exp.UTC()}
			var again *Answer
			if st, again = exchange(ctx, c, cfg, r, st, now); again != nil {
				ans = again
			}
		case errors.Is(err, tokens.ErrHandoffDone):
			r.Handoff = "done"
			st, _ = exchange(ctx, c, cfg, r, st, now)
		}
	}
	if ans != nil && ans.CheckInSeconds > 0 {
		soon = min(max(time.Duration(ans.CheckInSeconds)*time.Second, MinCheckIn), Every)
	}
	return st, soon
}

func report(boxID, version string, up time.Duration, checks []platform.Check) Report {
	r := Report{BoxID: boxID, Version: version, UptimeSeconds: int64(up.Seconds()), Checks: len(checks)}
	if r.Version == "" {
		r.Version = "dev"
	}
	for _, c := range checks {
		if !c.OK {
			r.FailedChecks++
			r.Failing = append(r.Failing, c.Name)
		}
	}
	return r
}

// Send posts one report and folds the answer into the previous state. With
// no answer the previous answer stands (an unreachable control plane never
// changes what the box does).
func Send(ctx context.Context, c *http.Client, cfg *platform.ManagedConfig, r Report, prev platform.ManagedState, now time.Time) platform.ManagedState {
	st, _ := exchange(ctx, c, cfg, r, prev, now)
	return st
}

func exchange(ctx context.Context, c *http.Client, cfg *platform.ManagedConfig, r Report, prev platform.ManagedState, now time.Time) (platform.ManagedState, *Answer) {
	st := prev
	st.CheckedAt, st.Answered, st.Error = now.UTC(), false, ""
	ans, err := post(ctx, c, cfg, r)
	if err != nil {
		st.Error = err.Error()
		return st, nil
	}
	st.Answered, st.LastAnswerAt = true, now.UTC()
	st.Active, st.Updates, st.Message = ans.Active, ans.Updates, ans.Message
	if !ans.Managed {
		// Released from ShipTiffin: the box is the customer's own, like any
		// box made with tiffin up; it updates as it did before.
		st.Active, st.Updates = false, true
	}
	switch {
	case ans.Offsite != nil && ans.Offsite.Sealed != "":
		st.OffsiteSealed, st.OffsiteExpiresAt = ans.Offsite.Sealed, ans.Offsite.ExpiresAt.UTC()
	case !st.Active:
		// The storage comes with the subscription; the credentials the
		// backup module holds run out by themselves.
		st.OffsiteSealed, st.OffsiteExpiresAt = "", time.Time{}
	}
	return st, ans
}

func post(ctx context.Context, c *http.Client, cfg *platform.ManagedConfig, r Report) (*Answer, error) {
	body, _ := json.Marshal(r)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := strings.TrimRight(cfg.ControlPlane, "/") + "/api/box/heartbeat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Licence)
	req.Header.Set("User-Agent", "tiffin/"+r.Version)
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check in: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("check in: the control plane answered %s", res.Status)
	}
	var a Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("check in: %w", err)
	}
	return &a, nil
}

func (m *Module) httpClient() *http.Client {
	if m.client != nil {
		return m.client
	}
	return &http.Client{Timeout: timeout}
}

// ---- retune after a resize ----

var (
	memoryMB        = platform.MemoryMB
	launchProvision = func(ctx context.Context) error {
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		out, err := exec.CommandContext(ctx, "systemd-run", "--unit", "tiffin-provision-resize", "--collect", "--quiet", "--wait", bin, "provision").CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

// Resized reports whether the memory changed enough since the box was last
// provisioned to retune it (more than 10%: the kernel's figure moves a
// little between boots).
func Resized(before, now int) bool {
	if before <= 0 || now <= 0 {
		return false
	}
	d := now - before
	if d < 0 {
		d = -d
	}
	return d*10 > before
}

func (m *Module) retune(ctx context.Context, p *platform.Platform) {
	now := memoryMB()
	st := platform.LoadManagedState()
	if Resized(st.MemoryMB, now) {
		p.Log.Info("managed: the server's memory changed; provisioning again to retune", "fromMB", st.MemoryMB, "toMB", now)
		if err := launchProvision(ctx); err != nil {
			p.Log.Warn("managed: provisioning after a resize", "err", err)
			return
		}
	}
	if now > 0 && st.MemoryMB != now {
		st = platform.LoadManagedState()
		st.MemoryMB = now
		_ = platform.SaveManagedState(st)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
