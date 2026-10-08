package cloud

import (
	"context"
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
)

// Machine is a server the worker installs on.
type Machine interface {
	provider.Machine
	// Check says which Ubuntu runs (and that root, sudo and systemd work).
	Check(ctx context.Context) (string, error)
}

// Worker runs the control plane's jobs.
type Worker struct {
	Store   Store
	KEK     []byte
	Licence ed25519.PrivateKey
	DNS     DNS
	// ControlURL is where boxes check in, e.g. https://shiptiffin.com.
	ControlURL string
	Log        *slog.Logger

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
	// WaitHTTPS waits until the box answers on its address (best effort).
	WaitHTTPS func(ctx context.Context, url string) error
	// TempDir holds a job's setup key while it runs.
	TempDir string
	// Concurrency is how many jobs run at once (default 3).
	Concurrency int
	Now         func() time.Time
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
	lease = 5 * time.Minute
	// MaxTokenAge: a job token never outlives this, used or not.
	MaxTokenAge = 2 * time.Hour
	// OwnerTokenFor is the longest the new box's owner token is kept.
	OwnerTokenFor = 7 * 24 * time.Hour
)

// Run claims and runs jobs until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	w.defaults()
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	lastSweep := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastSweep) > time.Minute {
			if did, err := w.Store.Sweep(ctx, w.Now(), MaxTokenAge); err != nil {
				w.Log.Warn("sweep", "err", err)
			} else if did != "" {
				w.Log.Info("sweep", "did", did)
			}
			lastSweep = time.Now()
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		job, err := w.Store.Claim(ctx, lease)
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

// RunJob runs one claimed job to its end.
func (w *Worker) RunJob(ctx context.Context, job *Job) {
	w.defaults()
	jctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // keep the lease while it runs
		t := time.NewTicker(lease / 3)
		defer t.Stop()
		for {
			select {
			case <-jctx.Done():
				return
			case <-t.C:
				_ = w.Store.Extend(ctx, job.ID, lease)
			}
		}
	}()
	w.Log.Info("job", "id", job.ID, "kind", job.Kind, "box", job.BoxID)
	err := w.handle(jctx, job)
	if err != nil {
		w.Log.Warn("job failed", "id", job.ID, "kind", job.Kind, "box", job.BoxID, "err", err)
	}
	// Finish clears the job's token whatever happened.
	if ferr := w.Store.Finish(context.WithoutCancel(ctx), job.ID, err); ferr != nil {
		w.Log.Error("finish job", "id", job.ID, "err", ferr)
	}
}

func (w *Worker) handle(ctx context.Context, job *Job) error {
	box, err := w.Store.Box(ctx, job.BoxID)
	if err != nil {
		return fmt.Errorf("read the box: %w", err)
	}
	progress := func(s string) {
		if err := w.Store.Step(ctx, job.ID, s); err != nil {
			w.Log.Warn("step", "err", err)
		}
	}
	switch job.Kind {
	case "provision":
		var a ProvisionArgs
		if err := json.Unmarshal(job.Args, &a); err != nil {
			return err
		}
		err := w.provision(ctx, job, box, a, progress)
		if err != nil {
			_ = w.Store.SetStatus(context.WithoutCancel(ctx), box.ID, "failed")
			progress("Setup stopped: " + firstLine(err.Error()))
		}
		return err
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
	case "dns_set":
		if box.DNSState == "killed" {
			return errors.New("this box's address was turned off by the abuse kill switch; only the owner can restore it")
		}
		if err := w.DNS.Set(ctx, box.Name, box.IPv4, box.IPv6); err != nil {
			return fmt.Errorf("set the DNS records: %w", err)
		}
		progress(w.DNS.Domain(box.Name) + " points at the box again")
		return w.Store.SetDNS(ctx, box.ID, "live")
	case "dns_remove":
		var a DNSRemoveArgs
		_ = json.Unmarshal(job.Args, &a)
		if box.Name != "" {
			if err := w.DNS.Remove(ctx, box.Name); err != nil {
				return fmt.Errorf("remove the DNS records: %w", err)
			}
		}
		state := "removed"
		if a.Kill {
			state = "killed"
		}
		progress(w.DNS.Domain(box.Name) + " no longer points at the box")
		return w.Store.SetDNS(ctx, box.ID, state)
	}
	return fmt.Errorf("unknown job kind %q", job.Kind)
}

// ProvisionArgs is what the customer chose.
type ProvisionArgs struct {
	Name       string `json:"name"`
	ServerType string `json:"serverType"`
	Location   string `json:"location"`
	VolumeGB   int    `json:"volumeGB"`
	KeepKey    bool   `json:"keepKey"`
	// Fresh: an earlier setup failed; delete what it left in the project
	// (labelled for this box) before starting again.
	Fresh bool `json:"fresh"`
}

// ResizeArgs changes the server type.
type ResizeArgs struct {
	ServerType string `json:"serverType"`
	KeepKey    bool   `json:"keepKey"`
	// UseStored: use the key the customer asked us to keep.
	UseStored bool `json:"useStored"`
}

// DeleteArgs deletes the customer's server (they asked and pasted a key).
type DeleteArgs struct {
	DeleteData bool `json:"deleteData"`
}

// DNSRemoveArgs: Kill is the abuse kill switch (never restored by itself).
type DNSRemoveArgs struct {
	Kill bool `json:"kill"`
}

// token opens the job's Hetzner token (or the box's stored one).
func (w *Worker) token(job *Job, box *Box, useStored bool) (string, error) {
	sealed := job.TokenSealed
	if sealed == "" && useStored {
		sealed = box.TokenSealed
	}
	if sealed == "" {
		return "", errors.New("no Hetzner key for this job (it is forgotten after two hours): paste it again")
	}
	raw, err := Open(w.KEK, sealed, TokenAAD(box.ID))
	if err != nil {
		return "", errors.New("the Hetzner key could not be opened; paste it again")
	}
	return string(raw), nil
}

// hetzner makes a provider whose every request is recorded for the customer.
func (w *Worker) hetzner(job *Job, box *Box, purpose, token string, cfg hetzner.Config) (*hetzner.Provider, error) {
	cfg.Token, cfg.Endpoint, cfg.PollInterval, cfg.Version = token, w.HetznerEndpoint, w.PollInterval, "control-plane"
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

// DefaultVolumeGB is a managed box's data volume.
const DefaultVolumeGB = 40

// MaintenanceWindow is when a managed box installs Tiffin and system updates
// (server time, UTC on Hetzner); the owner can move it in the dashboard.
// Without one a box never updates by itself.
const MaintenanceWindow = "03:00"

func (w *Worker) provision(ctx context.Context, job *Job, box *Box, a ProvisionArgs, progress func(string)) error {
	if err := ValidName(a.Name); err != nil {
		return fmt.Errorf("box name %q: %w", a.Name, err)
	}
	if box.Name != a.Name {
		return fmt.Errorf("the job is for %s but the box is named %s", a.Name, box.Name)
	}
	if box.DNSState == "killed" {
		return errors.New("this box was turned off by the abuse kill switch")
	}
	token, err := w.token(job, box, false)
	if err != nil {
		return err
	}
	_ = w.Store.SetFingerprint(ctx, box.ID, Fingerprint(token))
	if err := w.Store.SetStatus(ctx, box.ID, "provisioning"); err != nil {
		return err
	}
	if a.VolumeGB == 0 {
		a.VolumeGB = DefaultVolumeGB
	}

	// The setup key: made for this job, in a folder only it uses, deleted
	// when it ends (with the private half, which never leaves this worker).
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
	hp, err := w.hetzner(job, box, "setup", token, hetzner.Config{Name: a.Name, Location: a.Location, ServerType: a.ServerType, VolumeGB: a.VolumeGB,
		SSHFrom: from, KeyPath: filepath.Join(dir, "id_ed25519"), KnownHosts: filepath.Join(dir, "known_hosts")})
	if err != nil {
		return err
	}
	progress("Checking your Hetzner project")
	in, err := hp.Inventory(ctx)
	if err != nil {
		return err
	}
	if !in.Empty() {
		if !a.Fresh {
			return fmt.Errorf("your Hetzner project already has resources labelled for a box called %s; delete them in the Hetzner console, or choose “Clean up and try again”", a.Name)
		}
		progress("Deleting what the earlier attempt left in your project")
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
	ip4, ip6 := hetzner.PublicIPs(hp.Server)
	si := ServerInfo{ID: hp.Server.ID, IPv4: ip4, IPv6: ip6, Location: a.Location, Type: a.ServerType, VolumeGB: a.VolumeGB}
	if hp.Server.ServerType != nil {
		si.Type = hp.Server.ServerType.Name
	}
	if hp.Server.Location != nil {
		si.Location = hp.Server.Location.Name
	}
	if err := w.Store.SetServer(ctx, box.ID, si); err != nil {
		return err
	}

	domain := w.DNS.Domain(a.Name)
	progress("Pointing " + domain + " at your server")
	if err := w.DNS.Set(ctx, a.Name, ip4, ip6); err != nil {
		return fmt.Errorf("set the DNS records: %w", err)
	}
	if err := w.Store.SetDNS(ctx, box.ID, "live"); err != nil {
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
	tok, err := licence.Sign(w.Licence, licence.Licence{BoxID: box.ID, Name: a.Name, Domain: domain, Issued: w.Now().Unix()})
	if err != nil {
		return err
	}
	opts := install.Options{Domain: domain, HTTPSPort: 443, HTTPPort: 80, PublicIP: ip4, PublicIPv6: ip6,
		Server: &platform.ServerConfig{Provider: "hetzner", Name: a.Name, PublicIP: ip4, PublicIPv6: ip6, Machine: machine, RebootWindow: MaintenanceWindow},
		Managed: &platform.ManagedConfig{ControlPlane: w.ControlURL, BoxID: box.ID, Licence: tok,
			PublicKey: licence.PublicKeyText(w.Licence.Public().(ed25519.PublicKey))}}
	res, err := w.Install(ctx, m, bin, opts, progress)
	if err != nil {
		return err
	}
	sealedOwner, err := Seal(w.KEK, []byte(res.OwnerToken), OwnerAAD(box.ID))
	if err != nil {
		return err
	}
	if err := w.Store.SetOwnerToken(ctx, box.ID, sealedOwner, w.Now().Add(OwnerTokenFor)); err != nil {
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

	if a.KeepKey {
		sealed, err := Seal(w.KEK, []byte(token), TokenAAD(box.ID))
		if err != nil {
			return err
		}
		if err := w.Store.KeepToken(ctx, box.ID, sealed); err != nil {
			return err
		}
		progress("Kept your Hetzner key, sealed, for one-click resizes (remove it any time)")
	} else {
		progress("Forgot your Hetzner key (only its fingerprint " + Fingerprint(token) + " stays)")
	}

	url := "https://dashboard." + domain
	progress("Waiting for " + url + " to answer over HTTPS")
	if err := w.WaitHTTPS(ctx, url+"/v1/health"); err != nil {
		progress("The dashboard is not answering over HTTPS yet (its certificate can take a few minutes): " + firstLine(err.Error()))
	}
	if err := w.Store.SetStatus(ctx, box.ID, "active"); err != nil {
		return err
	}
	progress("Your box is ready")
	return nil
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
	token, err := w.token(job, box, a.UseStored)
	if err != nil {
		return err
	}
	hp, err := w.hetzner(job, box, "resize", token, hetzner.Config{Name: box.Name})
	if err != nil {
		return err
	}
	r, err := hp.PlanResize(ctx, a.ServerType, 0)
	if err != nil {
		return err
	}
	if r.To == nil {
		progress("The box is already " + a.ServerType)
		return nil
	}
	progress(fmt.Sprintf("Changing %s to %s: about 2 minutes offline (Hetzner then bills about %.2f %s a month before VAT)", r.From.Name, r.To.Name, r.MonthlyNetAfter, r.Currency))
	if err := hp.ChangeType(ctx, r.To.Name, progress); err != nil {
		return err
	}
	srv, _, err := hp.FindServer(ctx, box.Name)
	if err == nil && srv != nil {
		ip4, ip6 := hetzner.PublicIPs(srv)
		_ = w.Store.SetServer(ctx, box.ID, ServerInfo{ID: srv.ID, IPv4: ip4, IPv6: ip6, Type: r.To.Name, Location: box.Location})
	}
	if a.KeepKey && job.TokenSealed != "" {
		sealed, err := Seal(w.KEK, []byte(token), TokenAAD(box.ID))
		if err == nil {
			_ = w.Store.KeepToken(ctx, box.ID, sealed)
		}
	}
	progress("Resized. The box retunes Postgres and the apps' memory as it starts")
	return nil
}

func (w *Worker) deleteServer(ctx context.Context, job *Job, box *Box, a DeleteArgs, progress func(string)) error {
	token, err := w.token(job, box, false)
	if err != nil {
		return err
	}
	hp, err := w.hetzner(job, box, "delete", token, hetzner.Config{Name: box.Name})
	if err != nil {
		return err
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
	if box.Name != "" {
		if err := w.DNS.Remove(ctx, box.Name); err != nil {
			return err
		}
		state := "removed"
		if box.DNSState == "killed" {
			state = "killed"
		}
		_ = w.Store.SetDNS(ctx, box.ID, state)
	}
	_ = w.Store.KeepToken(ctx, box.ID, "")
	_ = w.Store.SetOwnerToken(ctx, box.ID, "", time.Time{})
	return w.Store.SetStatus(ctx, box.ID, "released")
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

// waitHTTPS polls url until it answers 200, for up to 5 minutes.
func waitHTTPS(ctx context.Context, url string) error {
	c := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(5 * time.Minute)
	var last error
	for time.Now().Before(deadline) {
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
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return last
}
