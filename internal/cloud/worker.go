package cloud

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/install"
	"github.com/btahir/tiffin/internal/licence"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/provider"
	"github.com/btahir/tiffin/internal/provider/hetzner"
	"github.com/btahir/tiffin/internal/provider/remote"
	"github.com/btahir/tiffin/internal/tokens"
)

// Machine is a server the worker installs on.
type Machine interface {
	provider.Machine
	// Check says which Ubuntu runs (and that root, sudo and systemd work).
	Check(ctx context.Context) (string, error)
}

// Worker runs the control plane's jobs.
type Worker struct {
	Store Store
	// SealKey opens customers' Hetzner tokens (the website seals them to its
	// public half) and keys the MAC over the addresses DNS points at.
	SealKey *ecdh.PrivateKey
	Licence ed25519.PrivateKey
	DNS     DNS
	// ControlURL is where boxes check in, e.g. https://shiptiffin.com.
	ControlURL string
	// Offsite mints managed boxes' off-site backup credentials and empties
	// deleted boxes' folders (nil: off).
	Offsite *Offsite
	Log     *slog.Logger

	// HetznerEndpoint overrides the API (tests).
	HetznerEndpoint string
	// PollInterval for Hetzner actions (default 2 s).
	PollInterval time.Duration
	// EgressIPs are the addresses this worker reaches servers from: the
	// only ones the setup firewall rule lets reach SSH.
	EgressIPs func(ctx context.Context) ([]netip.Prefix, error)
	// Binary returns the signed Tiffin release build for linux/<arch>.
	Binary func(ctx context.Context, arch string) (string, error)
	// Connect waits for SSH on a new server.
	Connect func(ctx context.Context, t remote.Target) (Machine, error)
	// Install puts Tiffin on the machine (install.Install).
	Install func(ctx context.Context, m provider.Machine, bin string, o install.Options, progress func(string)) (*install.Result, error)
	// PrepareData formats and mounts the data volume (install.PrepareData).
	PrepareData func(ctx context.Context, m provider.Machine, d install.DataSpec, progress func(string)) ([]string, error)
	// WaitHTTPS waits, up to within, until url answers 200 over HTTPS with a
	// certificate that checks out.
	WaitHTTPS func(ctx context.Context, url string, within time.Duration) error
	// TempDir holds a job's setup key while it runs.
	TempDir string
	// Concurrency is how many jobs run at once (default 3).
	Concurrency int
	// Lease is how long a claim lasts without renewal (default 5 minutes).
	Lease time.Duration
	Now   func() time.Time
}

// Defaults fills in the real implementations of what tests replace.
func (w *Worker) defaults() {
	if w.PollInterval == 0 {
		w.PollInterval = 2 * time.Second
	}
	if w.Connect == nil {
		w.Connect = func(ctx context.Context, t remote.Target) (Machine, error) {
			m := remote.New(t)
			if err := m.Wait(ctx, 6*time.Minute); err != nil {
				return nil, err
			}
			return m, nil
		}
	}
	if w.Install == nil {
		w.Install = install.Install
	}
	if w.PrepareData == nil {
		w.PrepareData = install.PrepareData
	}
	if w.WaitHTTPS == nil {
		w.WaitHTTPS = waitHTTPS
	}
	if w.Concurrency == 0 {
		w.Concurrency = 3
	}
	if w.Lease == 0 {
		w.Lease = 5 * time.Minute
	}
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	if w.TempDir == "" {
		w.TempDir = os.TempDir()
	}
}

const (
	// MaxTokenAge: a job token never outlives this, used or not.
	MaxTokenAge = 2 * time.Hour
	// SigninFor is how long the box's one-time sign-in link works (the box enforces it).
	SigninFor = 24 * time.Hour
	// FreshHeartbeat: DNS comes back only for a box that checked in, from its
	// own address with its current licence, at most this long ago.
	FreshHeartbeat = 7 * time.Hour
	// ParkAfter: a box that has not checked in for this long loses its
	// address (its server may be gone and its IP someone else's).
	ParkAfter = 72 * time.Hour
	// GraceDays the address stays after the managed extras pause.
	GraceDays = 30
)

// Run claims and runs jobs until ctx ends. Every minute it also sweeps up
// after stopped workers and checks boxes still waiting for a certificate;
// every five minutes it renews off-site backup credentials and empties
// deleted boxes' folders (with Offsite set).
func (w *Worker) Run(ctx context.Context) {
	w.defaults()
	// Setup keys of jobs that were running when this worker last stopped:
	// those jobs are over, so nothing may use their keys again.
	if old, _ := filepath.Glob(filepath.Join(w.TempDir, "setup-*")); len(old) > 0 {
		for _, d := range old {
			_ = os.RemoveAll(d)
		}
		w.Log.Info("deleted setup keys left by a stopped worker", "n", len(old))
	}
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	lastSweep, lastOffsite := time.Time{}, time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastSweep) > time.Minute {
			if did, err := w.Store.Sweep(ctx, w.Now()); err != nil {
				w.Log.Warn("sweep", "err", err)
			} else if did != "" {
				w.Log.Info("sweep", "did", did)
			}
			w.CheckCertificates(ctx)
			lastSweep = time.Now()
		}
		if w.Offsite != nil && time.Since(lastOffsite) > offsiteEvery {
			w.RefreshOffsite(ctx)
			w.PurgeOffsite(ctx)
			lastOffsite = time.Now()
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		job, err := w.Store.Claim(ctx, w.Lease)
		if err != nil || job == nil {
			<-sem
			if err != nil && ctx.Err() == nil {
				w.Log.Warn("claim", "err", err)
			}
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			w.RunJob(ctx, job)
		}()
	}
}

// CheckCertificates looks again at boxes whose dashboard had no valid
// certificate yet, and makes those that have one active ("ready" email).
func (w *Worker) CheckCertificates(ctx context.Context) {
	w.defaults()
	boxes, err := w.Store.CertPending(ctx, w.Now().Add(-50*time.Second), 10)
	if err != nil {
		w.Log.Warn("certificate checks", "err", err)
		return
	}
	for _, b := range boxes {
		if b.Name == "" {
			continue
		}
		if err := w.WaitHTTPS(ctx, "https://dashboard."+w.DNS.Domain(b.Name)+"/v1/health", 15*time.Second); err != nil {
			_ = w.Store.CheckedHTTPS(ctx, b.ID)
			continue
		}
		if ok, err := w.Store.MarkReady(ctx, b.ID); err != nil {
			w.Log.Warn("mark ready", "box", b.ID, "err", err)
		} else if ok {
			w.Log.Info("box ready", "box", b.ID)
		}
	}
}

// RunJob runs one claimed job to its end. It renews the lease while it
// runs and stops (cancelling the job's context) as soon as it can't: from
// then on another worker may run the job, and every write this one would
// make is fenced off anyway.
func (w *Worker) RunJob(ctx context.Context, job *Job) {
	w.defaults()
	jctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go w.keepLease(jctx, cancel, job)
	w.Log.Info("job", "id", job.ID, "kind", job.Kind, "box", job.BoxID, "attempt", job.Attempts)
	err := w.handle(jctx, job)
	if errors.Is(context.Cause(jctx), ErrLeaseLost) {
		w.Log.Warn("job stopped: the lease was lost", "id", job.ID, "kind", job.Kind, "box", job.BoxID)
		return
	}
	if ctx.Err() != nil {
		// Shutting down: the lease runs out and the sweep retries or cleans up.
		return
	}
	if err != nil {
		w.Log.Warn("job failed", "id", job.ID, "kind", job.Kind, "box", job.BoxID, "err", err)
		// Deleting and cleaning up must finish, and an address must follow
		// its decision: try again later (with the key while it lasts).
		if retried[job.Kind] && job.Attempts < MaxAttempts {
			delay := time.Duration(1<<job.Attempts) * time.Minute
			if rerr := w.Store.Retry(context.WithoutCancel(ctx), job.Lease, err, delay); rerr == nil {
				return
			}
		}
	}
	// Finish clears the job's token whatever happened.
	if ferr := w.Store.Finish(context.WithoutCancel(ctx), job.Lease, err); ferr != nil {
		w.Log.Error("finish job", "id", job.ID, "err", ferr)
	}
}

// retried: job kinds tried again after a failure (setups and resizes are
// the customer's to try again).
var retried = map[string]bool{"delete_server": true, "cleanup": true, "dns_set": true, "dns_remove": true}

func (w *Worker) keepLease(ctx context.Context, cancel context.CancelCauseFunc, job *Job) {
	t := time.NewTicker(w.Lease / 6)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Every renewal has a deadline: the point where this worker must
			// stop anyway (half the lease after the last renewal that worked).
			// A database call that hangs can't keep the job running past it.
			deadline := last.Add(w.Lease / 2)
			rctx, rcancel := context.WithDeadline(ctx, deadline)
			res := make(chan error, 1)
			go func() { res <- w.Store.Extend(rctx, job.Lease, w.Lease) }()
			var err error
			select {
			case err = <-res:
			case <-rctx.Done(): // even a call that ignores its context
				err = rctx.Err()
			}
			rcancel()
			switch {
			case err == nil:
				last = time.Now()
			case errors.Is(err, ErrLeaseLost):
				cancel(ErrLeaseLost)
				return
			case !time.Now().Before(deadline):
				// The database is unreachable (or hangs): stop well before the
				// lease ends, so the job is never run twice at once.
				cancel(ErrLeaseLost)
				return
			}
		}
	}
}

// leaseHeld: the job's context was not cancelled for a lost lease.
func leaseHeld(ctx context.Context) bool { return !errors.Is(context.Cause(ctx), ErrLeaseLost) }

func (w *Worker) handle(ctx context.Context, job *Job) error {
	box, err := w.Store.Box(ctx, job.BoxID)
	if err != nil {
		return fmt.Errorf("read the box: %w", err)
	}
	progress := func(s string) {
		if err := w.Store.Step(ctx, job.Lease, s); err != nil {
			w.Log.Warn("step", "err", err)
		}
	}
	switch job.Kind {
	case "provision":
		var a ProvisionArgs
		if err := json.Unmarshal(job.Args, &a); err != nil {
			return err
		}
		return w.provision(ctx, job, box, a, progress)
	case "resize":
		var a ResizeArgs
		if err := json.Unmarshal(job.Args, &a); err != nil {
			return err
		}
		return w.resize(ctx, job, box, a, progress)
	case "delete_server":
		var a DeleteArgs
		_ = json.Unmarshal(job.Args, &a)
		return w.deleteServer(ctx, job, box, a, progress)
	case "cleanup":
		var a CleanupArgs
		_ = json.Unmarshal(job.Args, &a)
		return w.cleanup(ctx, job, box, a, progress)
	case "dns_set":
		var a DNSArgs
		_ = json.Unmarshal(job.Args, &a)
		return w.dnsSet(ctx, job, box, a, progress)
	case "dns_remove":
		var a DNSArgs
		_ = json.Unmarshal(job.Args, &a)
		return w.dnsRemove(ctx, job, box, a, progress)
	}
	return fmt.Errorf("unknown job kind %q", job.Kind)
}

// ProvisionArgs is what the customer chose.
type ProvisionArgs struct {
	Name       string `json:"name"`
	ServerType string `json:"serverType"`
	Location   string `json:"location"`
	VolumeGB   int    `json:"volumeGB"`
	// OwnerName is the name on the customer's ShipTiffin account, if it has
	// a real one: the box's owner starts with it instead of "Owner".
	OwnerName string `json:"ownerName,omitempty"`
}

// ResizeArgs changes the server type.
type ResizeArgs struct {
	ServerType string `json:"serverType"`
}

// DeleteArgs deletes the customer's server (they asked and pasted a key).
type DeleteArgs struct {
	DeleteData bool `json:"deleteData"`
}

// CleanupArgs removes what a failed setup made.
type CleanupArgs struct {
	Reason string `json:"reason"`
	Gen    int64  `json:"gen"`
}

// DNSArgs say why an address goes or comes back. The worker checks the
// reason still holds when the job runs (a queue can be minutes behind):
// a box that paid again meanwhile keeps its address, a box that checked in
// meanwhile is not parked, and only the kill switch's own job sets "killed".
type DNSArgs struct {
	// dns_remove: kill, grace, parked, setup_failed, released (also for a deleted box).
	// dns_set: heartbeat, renewed, admin_restore.
	Reason string `json:"reason"`
	Gen    int64  `json:"gen"`
	// PausedAt: the extras_paused_at a grace removal was decided on.
	PausedAt *time.Time `json:"pausedAt,omitempty"`
	// Kill is the old spelling of reason "kill".
	Kill bool `json:"kill,omitempty"`
}

// token opens the job's Hetzner token.
func (w *Worker) token(job *Job, box *Box) (string, error) {
	sealed := job.TokenSealed
	if sealed == "" {
		return "", errors.New("no Hetzner key for this job (it is forgotten after two hours): paste it again")
	}
	raw, err := Open(w.SealKey, sealed, TokenAAD(box.ID))
	if err != nil {
		return "", errors.New("the Hetzner key could not be opened; paste it again")
	}
	return string(raw), nil
}

// hetzner makes a provider that sees only what we made for this box (the
// shiptiffin-box=<box id> label) and records every request for the customer.
func (w *Worker) hetzner(job *Job, box *Box, purpose, token string, cfg hetzner.Config) (*hetzner.Provider, error) {
	cfg.Token, cfg.Endpoint, cfg.PollInterval, cfg.Version, cfg.Owner = token, w.HetznerEndpoint, w.PollInterval, "control-plane", box.ID
	cfg.HTTPClient = &http.Client{Timeout: 2 * time.Minute, Transport: &Recorder{Record: func(c Call) {
		if err := w.Store.RecordCall(context.Background(), box.ID, job.ID, purpose, c); err != nil {
			w.Log.Warn("record a Hetzner call", "err", err)
		}
	}}}
	if cfg.Name == "" {
		cfg.Name = box.Name
	}
	return hetzner.New(cfg)
}

// extrasOn: the subscription is in good standing (past_due only after a
// first payment went through).
func extrasOn(b *Box) bool {
	if b.ExtrasPausedAt != nil || !b.FirstPaid {
		return false
	}
	return b.PlanStatus == "active" || b.PlanStatus == "trialing" || b.PlanStatus == "past_due"
}

// DefaultVolumeGB is a managed box's data volume.
const DefaultVolumeGB = 40

// MaintenanceWindow is when a managed box installs Tiffin and system updates
// (server time, UTC on Hetzner); the owner can move it in the dashboard.
// Without one a box never updates by itself.
const MaintenanceWindow = "03:00"

func (w *Worker) provision(ctx context.Context, job *Job, box *Box, a ProvisionArgs, progress func(string)) (err error) {
	var (
		started   bool // past the checks: a failure from here on is cleaned up
		installed bool // Tiffin is on the server: from here on nothing is deleted
		gen       int64
		hp        *hetzner.Provider
	)
	defer func() {
		// A lost lease or a shutdown: the sweep fails the job and queues
		// the clean-up (or, once installed, flags the box), so a worker
		// that may no longer own the box does nothing.
		if err == nil || ctx.Err() != nil {
			return
		}
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		switch {
		case installed:
			w.needsAttention(bg, job, box, progress, "Tiffin is installed, but the last setup steps didn't finish ("+firstLine(err.Error())+"). Your server and its data are kept; we look at it and email you.")
		case started:
			w.failSetup(bg, job, box, gen, hp, progress, err)
		case box.Status == "provisioning" && box.ReadyAt == nil && box.InstalledAt == nil:
			_ = w.Store.SetStatus(bg, job.Lease, box.ID, "failed")
			progress("Setup stopped: " + firstLine(err.Error()))
		}
	}()
	if err := ValidName(a.Name); err != nil {
		return fmt.Errorf("box name %q: %w", a.Name, err)
	}
	if box.Name != a.Name {
		return fmt.Errorf("the job is for %s but the box is named %s", a.Name, box.Name)
	}
	if box.Killed {
		return errors.New("this box was turned off by the abuse kill switch")
	}
	if !extrasOn(box) {
		return errors.New("the box's first payment hasn't gone through, or its subscription isn't active")
	}
	if box.Status != "provisioning" && box.Status != "paid" && box.Status != "failed" {
		return fmt.Errorf("the box is %s; setup runs only for a new or failed box", box.Status)
	}
	if box.ReadyAt != nil || box.InstalledAt != nil {
		return errors.New("this box was set up before; it is not set up again")
	}
	token, err := w.token(job, box)
	if err != nil {
		return err
	}
	if gen, err = w.Store.NextGeneration(ctx, job.Lease, box.ID); err != nil {
		return err
	}
	box.Generation, started = gen, true
	if err := w.Store.SetFingerprint(ctx, job.Lease, box.ID, Fingerprint(token)); err != nil {
		return err
	}
	if err := w.Store.SetStatus(ctx, job.Lease, box.ID, "provisioning"); err != nil {
		return err
	}
	if a.VolumeGB == 0 {
		a.VolumeGB = DefaultVolumeGB
	}

	// The setup key: made for this job, in a folder only it uses, deleted
	// when it ends (with the private half, which never leaves this worker),
	// and at the worker's next start if it stops meanwhile.
	dir, err := os.MkdirTemp(w.TempDir, "setup-"+box.ID+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	progress("Finding the address this setup connects from")
	from, err := w.EgressIPs(ctx)
	if err != nil {
		return fmt.Errorf("find the worker's address: %w", err)
	}
	if hp, err = w.hetzner(job, box, "setup", token, hetzner.Config{Name: a.Name, Location: a.Location, ServerType: a.ServerType, VolumeGB: a.VolumeGB,
		SSHFrom: from, KeyPath: filepath.Join(dir, "id_ed25519"), KnownHosts: filepath.Join(dir, "known_hosts")}); err != nil {
		hp = nil
		return err
	}
	progress("Checking your Hetzner project")
	in, err := hp.Inventory(ctx)
	if err != nil {
		return err
	}
	if !in.Empty() || box.DNSState == "pending" || box.DNSState == "live" {
		// An earlier attempt's address may still point at the server below
		// (its clean-up may have given up on Cloudflare): the records go
		// first, and nothing is deleted (no IP released) until they have.
		progress("Removing " + w.DNS.Domain(box.Name) + " first")
		if err := w.DNS.Remove(ctx, box.Name); err != nil {
			return fmt.Errorf("remove the DNS records of the earlier attempt (its server stays until they are gone): %w", err)
		}
		if err := w.Store.SetDNS(ctx, job.Lease, box.ID, "removed"); err != nil {
			return err
		}
	}
	if !in.Empty() {
		// Only what an earlier attempt for this very box made (it carries the
		// box's shiptiffin-box label); the box never ran, so there's no data.
		progress("Deleting what an earlier attempt for this box left in your project")
		if _, err := hp.DestroyAll(ctx, true, progress); err != nil {
			return err
		}
	}
	plan, err := hp.Plan(ctx)
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("Creating a %s server in %s (Hetzner bills you about %.2f %s a month before VAT)", plan.ServerType, plan.City, plan.MonthlyNet, plan.Currency))
	target, err := hp.Ensure(ctx, progress)
	if err != nil {
		return err
	}
	if err := w.saveResources(ctx, job, box, hp); err != nil {
		return err
	}
	ip4, ip6 := hetzner.PublicIPs(hp.Server)
	si := ServerInfo{ID: hp.Server.ID, IPv4: ip4, IPv6: ip6, Location: a.Location, Type: a.ServerType, VolumeGB: a.VolumeGB,
		MAC: AddrMAC(w.SealKey, box.ID, a.Name, ip4, ip6, gen)}
	if hp.Server.ServerType != nil {
		si.Type = hp.Server.ServerType.Name
	}
	if hp.Server.Location != nil {
		si.Location = hp.Server.Location.Name
	}
	if err := w.Store.SetServer(ctx, job.Lease, box.ID, si); err != nil {
		return err
	}

	domain := w.DNS.Domain(a.Name)
	progress("Pointing " + domain + " at your server")
	// The intent first: if publishing stops half way (or this worker does),
	// the records are known to exist and are removed with the rest.
	if err := w.Store.SetDNS(ctx, job.Lease, box.ID, "pending"); err != nil {
		return err
	}
	if err := w.DNS.Set(ctx, a.Name, ip4, ip6); err != nil {
		return fmt.Errorf("set the DNS records: %w", err)
	}
	if err := w.Store.SetDNS(ctx, job.Lease, box.ID, "live"); err != nil {
		return err
	}

	progress("Waiting for the server to start")
	m, err := w.Connect(ctx, target)
	if err != nil {
		return err
	}
	if _, err := m.Check(ctx); err != nil {
		return err
	}
	arch := m.Arch()
	if arch != "arm64" && arch != "amd64" {
		return fmt.Errorf("the server's architecture %q is not supported", arch)
	}
	progress("Fetching the signed Tiffin release for " + arch)
	bin, err := w.Binary(ctx, arch)
	if err != nil {
		return fmt.Errorf("fetch Tiffin: %w", err)
	}
	if _, err := w.PrepareData(ctx, m, install.DataSpec{Device: hp.VolumeDevice()}, progress); err != nil {
		return err
	}
	var machine *platform.ServerMachine
	if mi, err := hp.Machine(ctx, 4); err == nil {
		raw, _ := json.Marshal(mi)
		var sm platform.ServerMachine
		if json.Unmarshal(raw, &sm) == nil {
			machine = &sm
		}
	}
	tok, err := licence.Sign(w.Licence, licence.Licence{BoxID: box.ID, Name: a.Name, Domain: domain, Issued: w.Now().Unix(), Gen: gen})
	if err != nil {
		return err
	}
	opts := install.Options{Domain: domain, HTTPSPort: 443, HTTPPort: 80, PublicIP: ip4, PublicIPv6: ip6, NoOwnerToken: true,
		Server: &platform.ServerConfig{Provider: "hetzner", Name: a.Name, PublicIP: ip4, PublicIPv6: ip6, Machine: machine, RebootWindow: MaintenanceWindow},
		Managed: &platform.ManagedConfig{ControlPlane: w.ControlURL, BoxID: box.ID, Licence: tok,
			PublicKey: licence.PublicKeyText(w.Licence.Public().(ed25519.PublicKey)), OwnerName: tokens.CleanName(a.OwnerName)}}
	if _, err := w.Install(ctx, m, bin, opts, progress); err != nil {
		return err
	}
	// From here on the server holds the customer's box: whatever fails
	// next, nothing deletes it (failures need a person, not a clean-up).
	installed = true
	if err := w.markInstalled(ctx, job, box.ID); err != nil {
		return err
	}
	// The one sign-in we keep: a link the box made, which works once and
	// which the box itself refuses after a day. The owner token stays on the box.
	progress("Making your one-time sign-in link (it works once, for 24 hours)")
	code, exp, err := bootstrapLink(ctx, m)
	if err != nil {
		return err
	}
	if err := w.Store.SetSignin(ctx, job.Lease, box.ID, code, exp); err != nil {
		return err
	}

	// Take away our way in: the key's line on the server first (over the
	// connection it opens), then the key object and the firewall's SSH rule.
	progress("Removing the setup key from your server")
	pub, err := os.ReadFile(filepath.Join(dir, "id_ed25519.pub"))
	if err != nil {
		return err
	}
	if out, stderr, err := m.Exec(ctx, RemoveKeyScript(string(pub))); err != nil || !strings.Contains(out, "removed") {
		return fmt.Errorf("remove the setup key from the server: %v %s %s", err, out, stderr)
	}
	progress("Closing SSH and deleting the setup key from your Hetzner project")
	if err := hp.CloseSetupAccess(ctx, func(string) {}); err != nil {
		return err
	}
	if open, keys, err := hp.SSHOpen(ctx); err != nil || open || keys > 0 {
		return fmt.Errorf("the setup access is still there (SSH open: %v, keys: %d): %v", open, keys, err)
	}
	if err := w.Store.SetResources(ctx, job.Lease, box.ID, Resources{}); err != nil { // the SSH key is gone
		return err
	}

	progress("Forgot your Hetzner key (only its fingerprint " + Fingerprint(token) + " stays)")

	// Ready means the dashboard answers over HTTPS with a valid certificate.
	if err := w.Store.SetStatus(ctx, job.Lease, box.ID, "cert_pending"); err != nil {
		return err
	}
	url := "https://dashboard." + domain
	progress("Waiting for " + url + " to answer over HTTPS with a valid certificate")
	if err := w.WaitHTTPS(ctx, url+"/v1/health", 5*time.Minute); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = w.Store.CheckedHTTPS(ctx, box.ID)
		progress("Installed. The certificate is still pending (" + firstLine(err.Error()) + "): we check every minute and email you when the dashboard is ready")
		return nil
	}
	if _, err := w.Store.MarkReady(ctx, box.ID); err != nil {
		// The commit may have happened with its answer lost: read it back.
		// Either way the box is installed, so nothing is deleted: a box
		// still cert_pending is made active by the certificate check.
		if b, rerr := w.reread(ctx, box.ID); rerr == nil && b.ReadyAt != nil {
			progress("Your box is ready")
			return nil
		}
		progress("The dashboard answers; we confirm it within a minute and email you")
		return nil
	}
	progress("Your box is ready")
	return nil
}

// markInstalled records the install, trying a few times (a lost answer is
// harmless: it only ever sets installed_at once).
func (w *Worker) markInstalled(ctx context.Context, job *Job, boxID string) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = w.Store.MarkInstalled(ctx, job.Lease, boxID); err == nil || errors.Is(err, ErrLeaseLost) {
			return err
		}
		sleepCtx(ctx, time.Duration(i+1)*time.Second)
	}
	return err
}

// reread reads the box again, retrying briefly.
func (w *Worker) reread(ctx context.Context, boxID string) (*Box, error) {
	var b *Box
	var err error
	for i := 0; i < 3; i++ {
		if b, err = w.Store.Box(ctx, boxID); err == nil {
			return b, nil
		}
		sleepCtx(ctx, time.Duration(i+1)*time.Second)
	}
	return nil, err
}

// needsAttention: something after the install failed. The server, its
// volume and the address stay; the box is flagged for a person and the
// customer is emailed.
func (w *Worker) needsAttention(ctx context.Context, job *Job, box *Box, progress func(string), why string) {
	progress(why)
	if err := w.Store.Attention(ctx, job.Lease, box.ID, why); err != nil {
		w.Log.Error("flag a box for attention", "box", box.ID, "err", err)
	}
	_ = w.Store.QueueEmail(ctx, job.Lease, box.ID, "attention", fmt.Sprint(job.ID), map[string]any{"why": why})
}

// saveResources records the IDs of what Ensure made or reused.
func (w *Worker) saveResources(ctx context.Context, job *Job, box *Box, hp *hetzner.Provider) error {
	r := Resources{}
	if hp.Server != nil {
		r.Server = hp.Server.ID
	}
	if hp.Volume != nil {
		r.Volume = hp.Volume.ID
	}
	if in, err := hp.Inventory(ctx); err == nil {
		if len(in.Firewalls) > 0 {
			r.Firewall = in.Firewalls[0].ID
		}
		if len(in.SSHKeys) > 0 {
			r.SSHKey = in.SSHKeys[0].ID
		}
	}
	return w.Store.SetResources(ctx, job.Lease, box.ID, r)
}

// failSetup cleans up after a setup that stopped before Tiffin was
// installed: the box is failed, its address goes (no name may point at a
// server that may never be finished, or whose IP Hetzner hands to someone
// else), and then what this setup made in the customer's project is deleted
// (the box never ran, so there's no data to keep). The decision is the
// database's: only a box never installed nor ready is failed and deleted.
// The server (and so its IP) goes only once the address is gone. What can't
// be done now is retried by a clean-up job.
func (w *Worker) failSetup(ctx context.Context, job *Job, box *Box, gen int64, hp *hetzner.Provider, progress func(string), cause error) {
	progress("Setup stopped: " + firstLine(cause.Error()))
	retry := func(why string) {
		progress(why + "; we try again")
		_ = w.Store.EnqueueCleanup(ctx, job.Lease, box.ID, CleanupArgs{Reason: "setup_failed", Gen: gen}, job.TokenSealed, job.TokenExpiry)
	}
	ok, err := w.Store.FailSetup(ctx, job.Lease, box.ID, gen)
	if err != nil {
		// Unknown whether it committed: the clean-up job decides again.
		retry("Couldn't record the failure (" + firstLine(err.Error()) + ")")
		return
	}
	if !ok {
		progress("The box was installed meanwhile: nothing is deleted")
		return
	}
	_ = w.Store.QueueEmail(ctx, job.Lease, box.ID, "setup_failed", fmt.Sprint(gen), map[string]any{"error": firstLine(cause.Error())})
	if box.Name != "" && ValidName(box.Name) == nil {
		if err := w.DNS.Remove(ctx, box.Name); err != nil {
			retry("Couldn't remove " + w.DNS.Domain(box.Name) + " yet (" + firstLine(err.Error()) + "), so the server stays until it is gone")
			return
		}
		_ = w.Store.SetDNS(ctx, job.Lease, box.ID, "removed")
	}
	if hp != nil {
		progress("Deleting what this setup made in your Hetzner project")
		if _, err := hp.DestroyAll(ctx, true, func(string) {}); err != nil {
			retry("Couldn't delete it all yet (" + firstLine(err.Error()) + ")")
		}
	}
}

// cleanup finishes what failSetup could not (or what a stopped worker
// left): the same database decision first, then the address (whatever state
// was recorded: a publish may have stopped half way), and only then the
// server and volume.
func (w *Worker) cleanup(ctx context.Context, job *Job, box *Box, a CleanupArgs, progress func(string)) error {
	ok, err := w.Store.FailSetup(ctx, job.Lease, box.ID, a.Gen)
	if err != nil {
		return err
	}
	if !ok {
		progress("Nothing to clean up: the box has moved on")
		return nil
	}
	_ = w.Store.QueueEmail(ctx, job.Lease, box.ID, "setup_failed", fmt.Sprint(a.Gen), nil)
	if box.Name != "" {
		if err := w.DNS.Remove(ctx, box.Name); err != nil {
			return fmt.Errorf("remove the DNS records (the server stays until they are gone): %w", err)
		}
		if err := w.Store.SetDNS(ctx, job.Lease, box.ID, "removed"); err != nil {
			return err
		}
	}
	if job.TokenSealed == "" {
		progress("Your Hetzner key is forgotten, so what the setup made stays: delete what carries the label shiptiffin-box=" + box.ID + " in the Hetzner console, or try the setup again (it cleans up first)")
		return nil
	}
	token, err := w.token(job, box)
	if err != nil {
		return err
	}
	hp, err := w.hetzner(job, box, "cleanup", token, hetzner.Config{})
	if err != nil {
		return err
	}
	rep, err := hp.DestroyAll(ctx, true, progress)
	if err != nil {
		return err
	}
	for _, d := range rep.Deleted {
		progress("Deleted " + d)
	}
	return nil
}

// bootstrapLink asks the new box for its one-time owner sign-in link.
func bootstrapLink(ctx context.Context, m Machine) (string, time.Time, error) {
	out, stderr, err := m.Exec(ctx, "sudo "+install.BinLink+" --home "+install.Home+" bootstrap-link --valid "+SigninFor.String())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("make the sign-in link: %v %s", err, firstLine(stderr))
	}
	var r struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if json.Unmarshal([]byte(out), &r) != nil || !strings.HasPrefix(r.Code, "tfl_") || r.ExpiresAt.IsZero() {
		return "", time.Time{}, fmt.Errorf("make the sign-in link: unexpected answer %q", firstLine(out))
	}
	return r.Code, r.ExpiresAt, nil
}

// RemoveKeyScript deletes one public key's line from root's authorized_keys
// and says "removed" once it is gone (or was never there).
func RemoveKeyScript(pub string) string {
	f := strings.Fields(pub)
	key := pub
	if len(f) >= 2 {
		key = f[1] // the base64 body: unique, and free of quotes
	}
	return `set -eu
f=/root/.ssh/authorized_keys
k='` + strings.ReplaceAll(key, "'", "") + `'
if [ -f "$f" ]; then
  grep -vF "$k" "$f" > "$f.tiffin-new" || true
  cat "$f.tiffin-new" > "$f"
  rm -f "$f.tiffin-new"
fi
if [ -f "$f" ] && grep -qF "$k" "$f"; then echo still-there; exit 3; fi
echo removed`
}

func (w *Worker) resize(ctx context.Context, job *Job, box *Box, a ResizeArgs, progress func(string)) error {
	// A resize that stopped half way (its checkpoint says how far it got)
	// may have left the server off. Starting it again is always allowed
	// with the job's key, whatever the box's subscription or status is now.
	// Without a key, the customer is told.
	var phase string
	_ = json.Unmarshal(job.Checkpoint["phase"], &phase)
	recovering := phase != "" && phase != "done"
	if !recovering {
		if box.Status != "active" && box.Status != "cert_pending" {
			return fmt.Errorf("the box is %s; only a running box is resized", box.Status)
		}
		if !extrasOn(box) {
			return errors.New("resizing from your account is part of the subscription, which isn't active")
		}
	}
	token, err := w.token(job, box)
	if err != nil {
		if recovering {
			w.serverMayBeOff(ctx, job, box, progress)
		}
		return err
	}
	hp, err := w.hetzner(job, box, "resize", token, hetzner.Config{})
	if err != nil {
		return err
	}
	// Whatever happens below (a failure, a cancelled job, a retry after the
	// worker stopped half way), the server ends up running, or the
	// customer hears that it may not be.
	defer func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if err := hp.EnsureRunning(bg, 3*time.Minute); err != nil {
			progress("Couldn't start the server again (" + firstLine(err.Error()) + "): start it in the Hetzner console")
			w.serverMayBeOff(bg, job, box, progress)
		}
	}()
	if recovering {
		progress("Picking up a resize that stopped while " + phase)
		if (box.Status != "active" && box.Status != "cert_pending") || !extrasOn(box) {
			progress("The box is " + box.Status + " and its subscription may have ended since, so we only make sure the server runs")
			if err := hp.EnsureRunning(ctx, 3*time.Minute); err != nil {
				return err
			}
			_ = w.Store.Checkpoint(ctx, job.Lease, "phase", "done")
			_ = w.Store.ClearAttention(ctx, job.Lease, box.ID, ServerOffWhy)
			progress("The server runs")
			return nil
		}
	}
	r, err := hp.PlanResize(ctx, a.ServerType, 0)
	if err != nil {
		return err
	}
	if r.To != nil {
		progress(fmt.Sprintf("Changing %s to %s: about 2 minutes offline (Hetzner then bills about %.2f %s a month before VAT)", r.From.Name, r.To.Name, r.MonthlyNetAfter, r.Currency))
		if err := w.Store.Checkpoint(ctx, job.Lease, "phase", "changing the type"); err != nil {
			return err
		}
		if err := hp.ChangeType(ctx, r.To.Name, progress); err != nil {
			return err
		}
	} else {
		progress("The server is already " + a.ServerType)
	}
	if err := w.Store.Checkpoint(ctx, job.Lease, "phase", "starting the server"); err != nil {
		return err
	}
	if err := hp.EnsureRunning(ctx, 3*time.Minute); err != nil {
		return err
	}
	if in, err := hp.Inventory(ctx); err == nil && len(in.Servers) == 1 {
		srv := in.Servers[0]
		ip4, ip6 := hetzner.PublicIPs(srv)
		si := ServerInfo{ID: srv.ID, IPv4: ip4, IPv6: ip6, Type: a.ServerType, Location: box.Location}
		if srv.ServerType != nil {
			si.Type = srv.ServerType.Name
		}
		if ip4 == box.IPv4 && ip6 == box.IPv6 {
			si.MAC = AddrMAC(w.SealKey, box.ID, box.Name, ip4, ip6, box.Generation)
		}
		if err := w.Store.SetServer(ctx, job.Lease, box.ID, si); err != nil {
			return err
		}
	}
	_ = w.Store.Checkpoint(ctx, job.Lease, "phase", "done")
	_ = w.Store.ClearAttention(ctx, job.Lease, box.ID, ServerOffWhy)
	progress("Resized. The box retunes Postgres and the apps' memory as it starts")
	return nil
}

// serverMayBeOff tells the customer (an email, and a note on the box) that
// a resize may have left their server off and we couldn't start it.
func (w *Worker) serverMayBeOff(ctx context.Context, job *Job, box *Box, progress func(string)) {
	progress(ServerOffWhy)
	if err := w.Store.Attention(ctx, job.Lease, box.ID, ServerOffWhy); err != nil {
		w.Log.Error("flag a box for attention", "box", box.ID, "err", err)
	}
	_ = w.Store.QueueEmail(ctx, job.Lease, box.ID, "server_off", fmt.Sprint(job.ID), nil)
}

// deleteServer deletes the customer's server when they ask: the address
// first (so no name points at an IP Hetzner may hand to someone else), then
// only the resources carrying this box's label; then the box is deleted,
// for good, and the customer emailed. Each phase is checkpointed; a retry
// skips what is done. The account page reads the steps (site/lib/cloud/progress.ts).
func (w *Worker) deleteServer(ctx context.Context, job *Job, box *Box, a DeleteArgs, progress func(string)) error {
	if box.Status != "deleting" {
		return fmt.Errorf("the box is %s, not being deleted", box.Status)
	}
	if _, done := job.Checkpoint["dns"]; !done {
		if box.Name != "" {
			progress("Removing " + w.DNS.Domain(box.Name) + " first")
			if err := w.DNS.Remove(ctx, box.Name); err != nil {
				return fmt.Errorf("remove the DNS records: %w", err)
			}
		}
		if err := w.Store.SetDNS(ctx, job.Lease, box.ID, "removed"); err != nil {
			return err
		}
		if err := w.Store.Checkpoint(ctx, job.Lease, "dns", true); err != nil {
			return err
		}
	}
	if _, done := job.Checkpoint["hetzner"]; !done {
		token, err := w.token(job, box)
		if err != nil {
			return err
		}
		hp, err := w.hetzner(job, box, "delete", token, hetzner.Config{})
		if err != nil {
			return err
		}
		progress("Deleting the server in your Hetzner project")
		in, err := hp.Inventory(ctx)
		if err != nil {
			return err
		}
		for _, s := range in.Servers {
			if box.ServerID != 0 && s.ID != box.ServerID {
				return fmt.Errorf("a server labelled for this box (%s, id %d) isn't the one we recorded (id %d); nothing was deleted: write to hello@shiptiffin.com", s.Name, s.ID, box.ServerID)
			}
		}
		rep, err := hp.DestroyAll(ctx, a.DeleteData, progress)
		if err != nil {
			return err
		}
		for _, d := range rep.Deleted {
			progress("Deleted " + d)
		}
		for _, k := range rep.Kept {
			progress("Kept " + k + " (delete it in the Hetzner console when you no longer need it)")
		}
		if err := w.Store.Checkpoint(ctx, job.Lease, "hetzner", rep); err != nil {
			return err
		}
	}
	if err := w.Store.Deleted(ctx, job.Lease, box.ID, a.DeleteData); err != nil {
		return err
	}
	progress("Deleted")
	return nil
}

// dnsSet puts an address back, once it checks out that the box is the one
// at its IP: a fresh check-in with the current generation's licence from
// that very address (the website records only those), and the addresses
// are the ones this worker recorded (their MAC).
func (w *Worker) dnsSet(ctx context.Context, job *Job, box *Box, a DNSArgs, progress func(string)) error {
	why := ""
	switch {
	case box.Killed:
		why = "the abuse kill switch turned it off; an admin restores it"
	case box.Status != "active" && box.Status != "cert_pending":
		why = "the box is " + box.Status
	case !extrasOn(box):
		why = "the subscription isn't active"
	case a.Gen != 0 && a.Gen != box.Generation:
		why = "the box was set up again since"
	case box.LastHeartbeatAt == nil || w.Now().Sub(*box.LastHeartbeatAt) > FreshHeartbeat:
		why = "the box hasn't checked in lately; the address comes back at its next check-in"
	case !AddrOK(w.SealKey, box.AddrMAC, box.ID, box.Name, box.IPv4, box.IPv6, box.Generation):
		why = "its recorded addresses don't check out"
	}
	if why != "" {
		progress(w.DNS.Domain(box.Name) + " stays off: " + why)
		return nil
	}
	// The intent first, so records a failed publish leaves are removed with the rest.
	if err := w.Store.SetDNS(ctx, job.Lease, box.ID, "pending"); err != nil {
		return err
	}
	if err := w.DNS.Set(ctx, box.Name, box.IPv4, box.IPv6); err != nil {
		return fmt.Errorf("set the DNS records: %w", err)
	}
	progress(w.DNS.Domain(box.Name) + " points at the box again")
	return w.Store.SetDNS(ctx, job.Lease, box.ID, "live")
}

// dnsRemove takes an address away, if the reason it was queued for still holds.
func (w *Worker) dnsRemove(ctx context.Context, job *Job, box *Box, a DNSArgs, progress func(string)) error {
	if a.Kill {
		a.Reason = "kill"
	}
	state, why := "removed", ""
	now := w.Now()
	switch a.Reason {
	case "kill":
		state = "killed"
		if !box.Killed {
			why = "the kill was undone meanwhile"
		}
	case "grace":
		switch {
		case extrasOn(box) || box.ExtrasPausedAt == nil:
			why = "the subscription is active again"
		case a.PausedAt != nil && !a.PausedAt.Equal(*box.ExtrasPausedAt):
			why = "the subscription changed since"
		case now.Before(box.ExtrasPausedAt.AddDate(0, 0, GraceDays)):
			why = "the grace period isn't over"
		}
	case "parked":
		state = "parked"
		last := box.LastHeartbeatAt
		if last == nil {
			last = box.ReadyAt
		}
		if last != nil && now.Sub(*last) < ParkAfter {
			why = "the box checked in meanwhile"
		}
	case "setup_failed", "released":
		if box.Status != "failed" && box.Status != "released" && box.Status != "deleting" && box.Status != "deleted" {
			why = "the box is " + box.Status
		}
	default:
		why = fmt.Sprintf("unknown reason %q", a.Reason)
	}
	if a.Gen != 0 && a.Gen != box.Generation && a.Reason != "kill" {
		why = "the box was set up again since"
	}
	if why != "" {
		progress(w.DNS.Domain(box.Name) + " stays: " + why)
		return nil
	}
	if box.Name != "" {
		if err := w.DNS.Remove(ctx, box.Name); err != nil {
			return fmt.Errorf("remove the DNS records: %w", err)
		}
	}
	progress(w.DNS.Domain(box.Name) + " no longer points at the box")
	return w.Store.SetDNS(ctx, job.Lease, box.ID, state)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// waitHTTPS polls url until it answers 200 over HTTPS (Go's client checks
// the certificate: a public CA's, for this name, in date), for up to within.
func waitHTTPS(ctx context.Context, url string, within time.Duration) error {
	c := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(within)
	var last error
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		res, err := c.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return nil
			}
			err = fmt.Errorf("HTTP %d", res.StatusCode)
		}
		last = err
		if !time.Now().Add(5 * time.Second).Before(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
