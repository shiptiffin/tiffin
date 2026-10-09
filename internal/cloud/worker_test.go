package cloud

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/cloudflare"
	"github.com/shiptiffin/tiffin/internal/cloud/cftest"
	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/shiptiffin/tiffin/internal/licence"
	"github.com/shiptiffin/tiffin/internal/provider"
	"github.com/shiptiffin/tiffin/internal/provider/hetzner"
	"github.com/shiptiffin/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/shiptiffin/tiffin/internal/provider/remote"
)

// fakeMachine is a new server reached over SSH: it records what runs.
type fakeMachine struct {
	mu      sync.Mutex
	scripts []string
	copies  []string
	// hook, when set, answers a script first (ok false: the default answer).
	hook func(script string) (out string, ok bool)
}

func (m *fakeMachine) Exec(_ context.Context, script string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scripts = append(m.scripts, script)
	if m.hook != nil {
		if out, ok := m.hook(script); ok {
			return out, "", nil
		}
	}
	if strings.Contains(script, "authorized_keys") {
		return "removed\n", "", nil
	}
	if strings.Contains(script, "bootstrap-link") {
		return `{"code": "tfl_onetimecode", "expiresAt": "` + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339) + `"}`, "", nil
	}
	return "", "", nil
}
func (m *fakeMachine) Copy(_ context.Context, local, remote string) error {
	m.copies = append(m.copies, remote)
	return nil
}
func (m *fakeMachine) Arch() string                          { return "arm64" }
func (m *fakeMachine) Check(context.Context) (string, error) { return "26.04", nil }

type harness struct {
	t       *testing.T
	store   *PG
	hz      *hetznertest.Fake
	cf      *cftest.Fake
	w       *Worker
	seal    *ecdh.PrivateKey
	key     ed25519.PrivateKey
	machine *fakeMachine
	opts    []install.Options
	connect []remote.Target
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, store: newStore(t), hz: hetznertest.New(), cf: cftest.New("shiptiffin.app"), machine: &fakeMachine{}}
	t.Cleanup(h.hz.Close)
	h.seal, _ = ecdh.X25519().GenerateKey(rand.Reader)
	_, h.key, _ = ed25519.GenerateKey(rand.Reader)
	bin := filepath.Join(t.TempDir(), "tiffin")
	_ = os.WriteFile(bin, []byte("tiffin build"), 0o755)
	h.w = &Worker{
		Store: h.store, SealKey: h.seal, Licence: h.key,
		DNS:             DNS{P: &cloudflare.Provider{APIToken: cftest.Token, HTTPClient: h.cf.Client()}, Zone: "shiptiffin.app"},
		ControlURL:      "https://shiptiffin.com",
		HetznerEndpoint: h.hz.URL,
		PollInterval:    5 * time.Millisecond,
		EgressIPs: func(context.Context) ([]netip.Prefix, error) {
			return []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")}, nil
		},
		Binary: func(context.Context, string) (string, error) { return bin, nil },
		Connect: func(_ context.Context, tg remote.Target) (Machine, error) {
			h.connect = append(h.connect, tg)
			return h.machine, nil
		},
		Install: func(_ context.Context, _ provider.Machine, _ string, o install.Options, progress func(string)) (*install.Result, error) {
			progress("installing")
			h.opts = append(h.opts, o)
			return &install.Result{Build: "abc"}, nil
		},
		PrepareData: func(context.Context, provider.Machine, install.DataSpec, func(string)) ([]string, error) {
			return nil, nil
		},
		WaitHTTPS: func(context.Context, string, time.Duration) error { return nil },
		TempDir:   t.TempDir(),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return h
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.store.Pool.Exec(context.Background(), q, args...); err != nil {
		h.t.Fatal(err)
	}
}

// addBox adds a paid box that is ready to be set up.
func (h *harness) addBox(id, name string) {
	h.exec(`insert into cloud_boxes (id, user_id, email, name, status, plan_status, first_paid_at) values ($1, 'u1', 'sam@example.com', $2, 'paid', 'active', now())`, id, name)
}

// enqueue adds a job; token "" means none.
func (h *harness) enqueue(boxID, kind, token string, args any) int64 {
	h.t.Helper()
	raw, _ := json.Marshal(args)
	var sealed *string
	if token != "" {
		s, err := Seal(h.seal.PublicKey(), []byte(token), TokenAAD(boxID))
		if err != nil {
			h.t.Fatal(err)
		}
		sealed = &s
	}
	var id int64
	if err := h.store.Pool.QueryRow(context.Background(), `insert into cloud_jobs (box_id, kind, args, token_sealed, token_expires_at) values ($1, $2, $3, $4, now() + interval '2 hours') returning id`,
		boxID, kind, string(raw), sealed).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

// runNext claims and runs the next job.
func (h *harness) runNext() *Job {
	h.t.Helper()
	j, err := h.store.Claim(context.Background(), time.Minute)
	if err != nil || j == nil {
		h.t.Fatalf("claim: %v %v", j, err)
	}
	h.w.RunJob(context.Background(), j)
	return j
}

type jobRow struct {
	Status string
	Error  *string
	Token  *string
	Steps  []map[string]string
}

func (r jobRow) text() string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s["text"])
	}
	if r.Error != nil {
		out = append(out, "error: "+*r.Error)
	}
	return strings.Join(out, "\n")
}

func (h *harness) job(id int64) jobRow {
	h.t.Helper()
	var r jobRow
	var steps []byte
	if err := h.store.Pool.QueryRow(context.Background(), `select status, error, token_sealed, steps from cloud_jobs where id = $1`, id).
		Scan(&r.Status, &r.Error, &r.Token, &steps); err != nil {
		h.t.Fatal(err)
	}
	_ = json.Unmarshal(steps, &r.Steps)
	return r
}

func (h *harness) str(q string, args ...any) string {
	h.t.Helper()
	var s *string
	if err := h.store.Pool.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	if s == nil {
		return ""
	}
	return *s
}

func (h *harness) num(q string, args ...any) int64 {
	h.t.Helper()
	var n int64
	if err := h.store.Pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) box(id string) *Box {
	h.t.Helper()
	b, err := h.store.Box(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return b
}

// provisioned runs a successful setup of a box and returns it.
func (h *harness) provisioned(id, name string, args ProvisionArgs) *Box {
	h.t.Helper()
	h.addBox(id, name)
	args.Name = name
	if args.ServerType == "" {
		args.ServerType, args.Location = "cax11", "fsn1"
	}
	jid := h.enqueue(id, "provision", hetznertest.Token, args)
	h.runNext()
	if j := h.job(jid); j.Status != "done" {
		h.t.Fatalf("setup: %s", j.text())
	}
	return h.box(id)
}

func TestProvisionEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_1", "shop")
	id := h.enqueue("box_1", "provision", hetznertest.Token, ProvisionArgs{Name: "shop", ServerType: "cax11", Location: "fsn1", OwnerName: " Ann\n Lee "})
	h.runNext()

	j := h.job(id)
	if j.Status != "done" || j.Token != nil {
		t.Fatalf("job: %s token kept=%v\n%s", j.Status, j.Token != nil, j.text())
	}
	for _, want := range []string{"Creating a cax11 server", "Pointing shop.shiptiffin.app", "one-time sign-in link", "Removing the setup key", "Closing SSH", "Forgot your Hetzner key", "valid certificate", "Your box is ready"} {
		if !strings.Contains(j.text(), want) {
			t.Errorf("steps lack %q:\n%s", want, j.text())
		}
	}

	b := h.box("box_1")
	if b.Status != "active" || b.ReadyAt == nil || b.Generation != 1 || b.DNSState != "live" {
		t.Fatalf("box: %+v", b)
	}
	if !strings.HasPrefix(b.IPv4, "203.0.113.") || !AddrOK(h.seal, b.AddrMAC, "box_1", "shop", b.IPv4, b.IPv6, 1) {
		t.Fatalf("addresses %q %q not MAC'd", b.IPv4, b.IPv6)
	}
	if n := h.num(`select count(*) from information_schema.columns where table_name = 'cloud_boxes' and column_name like 'token%' and column_name <> 'token_fingerprint'`); n != 0 {
		t.Fatal("a box row must have no place for a Hetzner key")
	}
	if fp := h.str(`select token_fingerprint from cloud_boxes where id = 'box_1'`); fp != Fingerprint(hetznertest.Token) {
		t.Fatalf("fingerprint %q", fp)
	}
	// No owner token: only the box's own one-time link, which it expires itself.
	if h.str(`select signin_code from cloud_boxes where id = 'box_1'`) != "tfl_onetimecode" {
		t.Fatal("the one-time sign-in link must be kept until it is used")
	}
	if len(h.opts) != 1 || !h.opts[0].NoOwnerToken {
		t.Fatal("the install must leave the owner token on the box")
	}
	if n := h.num(`select count(*) from cloud_outbox where box_id = 'box_1' and kind = 'ready'`); n != 1 {
		t.Fatalf("ready emails queued: %d", n)
	}
	// What we made carries the box's id; its IDs are recorded.
	if srv := h.hz.ServerByName("shop"); srv == nil || srv.Labels[hetzner.LabelOwner] != "box_1" {
		t.Fatalf("server labels: %+v", srv)
	}
	if h.num(`select coalesce(hetzner_server_id, 0) from cloud_boxes where id = 'box_1'`) == 0 || h.num(`select coalesce(hetzner_volume_id, 0) from cloud_boxes where id = 'box_1'`) == 0 ||
		h.num(`select coalesce(hetzner_firewall_id, 0) from cloud_boxes where id = 'box_1'`) == 0 || h.num(`select coalesce(hetzner_ssh_key_id, 0) from cloud_boxes where id = 'box_1'`) != 0 {
		t.Fatal("resource IDs not recorded (or the deleted SSH key's kept)")
	}

	// DNS: the box's name and its wildcard, nothing else.
	if got := h.cf.Records("shop.shiptiffin.app"); len(got) != 2 || got[0] != "A "+b.IPv4 || !strings.HasPrefix(got[1], "AAAA 2001:db8:") {
		t.Fatalf("records: %v", got)
	}
	if got := h.cf.Records("*.shop.shiptiffin.app"); len(got) != 2 {
		t.Fatalf("wildcard: %v", got)
	}

	// Hetzner: the server stays; our way in is gone.
	srv, _, _, keys := h.hz.Count()
	if srv != 1 || keys != 0 {
		t.Fatalf("servers %d, SSH keys %d", srv, keys)
	}
	for _, r := range h.hz.Firewall().Rules {
		if r.Port != nil && *r.Port == "22" {
			t.Fatalf("SSH is still open: %+v", r)
		}
	}
	var removal string
	for _, s := range h.machine.scripts {
		if strings.Contains(s, "authorized_keys") {
			removal = s
		}
	}
	if removal == "" || h.machine.scripts[len(h.machine.scripts)-1] != removal {
		t.Fatalf("the key removal must be the last thing run on the server: %d scripts", len(h.machine.scripts))
	}
	if entries, _ := os.ReadDir(h.w.TempDir); len(entries) != 0 {
		t.Fatalf("the setup key is still on disk: %v", entries)
	}

	// The install: the box's domain, and a licence for this box and generation.
	o := h.opts[0]
	if o.Domain != "shop.shiptiffin.app" || o.Managed == nil || o.Managed.ControlPlane != "https://shiptiffin.com" || o.Managed.BoxID != "box_1" {
		t.Fatalf("install options: %+v %+v", o, o.Managed)
	}
	if o.Managed.OwnerName != "Ann Lee" {
		t.Fatalf("the owner's name reaches the box tidied: %q", o.Managed.OwnerName)
	}
	if o.Server.RebootWindow != MaintenanceWindow || len(o.Server.OwnerIPs) != 0 {
		t.Fatalf("server config: %+v", o.Server)
	}
	pub, _ := licence.ParsePublicKey(o.Managed.PublicKey)
	l, err := licence.Verify(pub, o.Managed.Licence)
	if err != nil || l.BoxID != "box_1" || l.Domain != "shop.shiptiffin.app" || l.Gen != 1 {
		t.Fatalf("licence %+v %v", l, err)
	}

	// Every Hetzner call is in the log; the token in none of them.
	if n := h.num(`select count(*) from cloud_hetzner_calls where box_id = 'box_1' and job_id = $1 and purpose = 'setup'`, id); n < 10 {
		t.Fatalf("only %d calls recorded", n)
	}
	if n := h.num(`select count(*) from cloud_hetzner_calls where path like '%' || $1 || '%' or coalesce(error, '') like '%' || $1 || '%'`, hetznertest.Token); n != 0 {
		t.Fatal("the token appears in the call log")
	}

	// Every resize needs a key of its own, used for that job and forgotten.
	rid := h.enqueue("box_1", "resize", "", ResizeArgs{ServerType: "cax21"})
	h.runNext()
	if j := h.job(rid); j.Status != "failed" || !strings.Contains(*j.Error, "paste it again") {
		t.Fatalf("resize without a key: %+v", j)
	}
	rid = h.enqueue("box_1", "resize", hetznertest.Token, ResizeArgs{ServerType: "cax21"})
	h.runNext()
	if j := h.job(rid); j.Status != "done" || j.Token != nil {
		t.Fatalf("resize: %s", j.text())
	}
	if !slices.Contains(h.hz.ChangeTypes, "cax21 upgrade_disk=false") || h.str(`select server_type from cloud_boxes where id = 'box_1'`) != "cax21" {
		t.Fatalf("change types %v", h.hz.ChangeTypes)
	}
	rid = h.enqueue("box_1", "resize", "", ResizeArgs{ServerType: "cax31"})
	h.runNext()
	if j := h.job(rid); j.Status != "failed" || !strings.Contains(*j.Error, "paste it again") {
		t.Fatalf("the next resize needs a key again: %+v", j)
	}
	rid = h.enqueue("box_1", "resize", hetznertest.Token, ResizeArgs{ServerType: "cax31"})
	h.runNext()
	if j := h.job(rid); j.Status != "done" || j.Token != nil {
		t.Fatalf("second resize: %s", j.text())
	}
	if b := h.box("box_1"); !AddrOK(h.seal, b.AddrMAC, b.ID, b.Name, b.IPv4, b.IPv6, b.Generation) {
		t.Fatal("a resize must keep the addresses' MAC")
	}
}

// A setup that fails cleans up after itself: no address, nothing it made
// left in the customer's project, no key kept, an email.
func TestProvisionFailureCleansUp(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_4", "deli")
	h.w.Install = func(context.Context, provider.Machine, string, install.Options, func(string)) (*install.Result, error) {
		return nil, errors.New("provision: apt failed")
	}
	id := h.enqueue("box_4", "provision", hetznertest.Token, ProvisionArgs{Name: "deli", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	j := h.job(id)
	if j.Status != "failed" || j.Token != nil {
		t.Fatalf("install failure: %s", j.text())
	}
	b := h.box("box_4")
	if b.Status != "failed" || b.DNSState != "removed" || h.cf.Count() != 0 {
		t.Fatalf("after failure: %+v, %d records", b, h.cf.Count())
	}
	if srv, vols, fws, keys := h.hz.Count(); srv+vols+fws+keys != 0 {
		t.Fatalf("left in the project: %d servers %d volumes %d firewalls %d keys", srv, vols, fws, keys)
	}
	if n := h.num(`select count(*) from cloud_outbox where box_id = 'box_4' and kind = 'setup_failed'`); n != 1 {
		t.Fatalf("setup_failed emails: %d", n)
	}
	// Trying again works, with the next generation.
	h.w.Install = func(_ context.Context, _ provider.Machine, _ string, o install.Options, _ func(string)) (*install.Result, error) {
		h.opts = append(h.opts, o)
		return &install.Result{}, nil
	}
	id = h.enqueue("box_4", "provision", hetznertest.Token, ProvisionArgs{Name: "deli", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(id); j.Status != "done" || h.box("box_4").Generation != 2 {
		t.Fatalf("retry: %s", j.text())
	}
}

func TestProvisionWrongKey(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_3", "cafe")
	id := h.enqueue("box_3", "provision", "not-the-token", ProvisionArgs{Name: "cafe", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	j := h.job(id)
	if j.Status != "failed" || j.Token != nil || !strings.Contains(*j.Error, "rejected the API token") {
		t.Fatalf("job: %s", j.text())
	}
	if b := h.box("box_3"); b.Status != "failed" {
		t.Fatalf("box %s", b.Status)
	}
	if h.num(`select count(*) from cloud_hetzner_calls where box_id = 'box_3' and status = 401`) == 0 {
		t.Fatal("rejected calls are logged too")
	}
}

// Never delete what we didn't make: a box the customer made with tiffin up
// under the same name stays, whatever our setups do around it.
func TestNeverTouchesTheCustomersOwnResources(t *testing.T) {
	h := newHarness(t)
	theirs := h.hz.AddLabelledServer("deli", map[string]string{hetzner.LabelKind: "box", hetzner.LabelBox: "deli"})
	h.addBox("box_6", "deli")
	id := h.enqueue("box_6", "provision", hetznertest.Token, ProvisionArgs{Name: "deli", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(id); j.Status != "failed" || !strings.Contains(j.text(), "already exists") {
		t.Fatalf("setup next to the customer's own box: %s", j.text())
	}
	if s := h.hz.ServerByName("deli"); s == nil || s.ID != theirs {
		t.Fatal("the customer's own server was deleted")
	}
	// And a retry: the same, never a "fresh" delete of their box.
	h.enqueue("box_6", "provision", hetznertest.Token, ProvisionArgs{Name: "deli", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if s := h.hz.ServerByName("deli"); s == nil || s.ID != theirs {
		t.Fatal("a retry deleted the customer's own server")
	}
}

// What an earlier attempt for this very box left (it carries our label) is
// cleaned up by the next attempt.
func TestRetryCleansOnlyOurLeftovers(t *testing.T) {
	h := newHarness(t)
	h.hz.AddLabelledServer("deli", map[string]string{hetzner.LabelKind: "box", hetzner.LabelBox: "deli", hetzner.LabelOwner: "box_7"})
	h.hz.AddLabelledServer("other", map[string]string{hetzner.LabelKind: "box", hetzner.LabelBox: "other", hetzner.LabelOwner: "box_99"})
	h.provisioned("box_7", "deli", ProvisionArgs{})
	if s := h.hz.ServerByName("other"); s == nil {
		t.Fatal("another box's server went")
	}
	if srv, _, _, _ := h.hz.Count(); srv != 2 {
		t.Fatalf("servers: %d", srv)
	}
}

func TestLeaseFencing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.provisioned("box_8", "fence", ProvisionArgs{})
	id := h.enqueue("box_8", "dns_remove", "", DNSArgs{Reason: "kill"})
	old, err := h.store.Claim(ctx, time.Minute)
	if err != nil || old == nil || old.ID != id {
		t.Fatalf("claim: %+v %v", old, err)
	}
	// One job per box: the next one waits.
	h.enqueue("box_8", "dns_set", "", DNSArgs{Reason: "heartbeat"})
	if j, _ := h.store.Claim(ctx, time.Minute); j != nil {
		t.Fatalf("a second job of the box ran at once: %+v", j)
	}
	// The lease runs out; the sweep hands the job to another worker.
	h.exec(`update cloud_jobs set lease_until = now() - interval '1 second' where id = $1`, id)
	if _, err := h.store.Sweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	cur, _ := h.store.Claim(ctx, time.Minute)
	if cur == nil || cur.ID != id || cur.Gen != old.Gen+1 {
		t.Fatalf("reclaim: %+v", cur)
	}
	// The old holder can write nothing more.
	if err := h.store.Extend(ctx, old.Lease, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("extend: %v", err)
	}
	if err := h.store.SetDNS(ctx, old.Lease, "box_8", "removed"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a stale lease wrote the box: %v", err)
	}
	if err := h.store.Finish(ctx, old.Lease, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a stale lease finished the job: %v", err)
	}
	_ = h.store.Step(ctx, old.Lease, "from the old worker")
	if strings.Contains(h.job(id).text(), "old worker") || h.box("box_8").DNSState != "live" {
		t.Fatal("a stale lease changed something")
	}
	if err := h.store.Finish(ctx, cur.Lease, nil); err != nil {
		t.Fatal(err)
	}
}

// A worker that loses its lease stops: its job's context is cancelled and
// it writes nothing more.
func TestLostLeaseStopsTheWorker(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_9", "slow")
	h.w.Lease = 300 * time.Millisecond
	started := make(chan struct{})
	stopped := make(chan error, 1)
	h.w.Connect = func(ctx context.Context, _ remote.Target) (Machine, error) {
		close(started)
		<-ctx.Done()
		stopped <- context.Cause(ctx)
		return nil, ctx.Err()
	}
	id := h.enqueue("box_9", "provision", hetznertest.Token, ProvisionArgs{Name: "slow", ServerType: "cax11", Location: "fsn1"})
	j, _ := h.store.Claim(context.Background(), h.w.Lease)
	done := make(chan struct{})
	go func() { h.w.RunJob(context.Background(), j); close(done) }()
	<-started
	h.exec(`update cloud_jobs set lease_gen = lease_gen + 1 where id = $1`, id) // someone else holds it now
	select {
	case cause := <-stopped:
		if !errors.Is(cause, ErrLeaseLost) {
			t.Fatalf("stopped for %v", cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker kept going without its lease")
	}
	<-done
	if j := h.job(id); j.Status != "running" {
		t.Fatalf("the old worker finished a job it no longer held: %s", j.Status)
	}
	// It did not clean up behind the new holder's back either.
	if srv, _, _, _ := h.hz.Count(); srv != 1 {
		t.Fatalf("servers: %d", srv)
	}
}

// An interrupted resize never leaves the server off.
func TestResizeReconcilesPower(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_10", "power", ProvisionArgs{})
	// The worker stopped after the type change, with the server off.
	h.hz.SetServerStatus("power", "off")
	id := h.enqueue("box_10", "resize", hetznertest.Token, ResizeArgs{ServerType: "cax11"})
	h.exec(`update cloud_jobs set checkpoint = '{"phase": "changing the type"}' where id = $1`, id)
	h.runNext()
	if j := h.job(id); j.Status != "done" || !strings.Contains(j.text(), "Picking up") {
		t.Fatalf("resize: %s", j.text())
	}
	if s := h.hz.ServerByName("power"); s.Status != "running" {
		t.Fatalf("server %s", s.Status)
	}
	// Even a resize that fails starts the server again.
	h.hz.SetServerStatus("power", "off")
	id = h.enqueue("box_10", "resize", hetznertest.Token, ResizeArgs{ServerType: "no-such-type"})
	h.runNext()
	if j := h.job(id); j.Status != "failed" || h.hz.ServerByName("power").Status != "running" {
		t.Fatalf("failed resize: %s / %s", j.text(), h.hz.ServerByName("power").Status)
	}
}

// Deleting removes the address first; a delete that fails half way leaves
// no address behind, and a retry finishes.
func TestDeleteRemovesDNSFirst(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_11", "gone", ProvisionArgs{})
	h.exec(`update cloud_boxes set status = 'deleting' where id = 'box_11'`)
	id := h.enqueue("box_11", "delete_server", "a-revoked-token", DeleteArgs{})
	h.runNext()
	// It failed after removing the address; it's queued to run again later, with its key and checkpoint.
	j := h.job(id)
	if j.Status != "queued" || j.Token == nil || j.Error == nil {
		t.Fatalf("delete with a dead key: %s", j.text())
	}
	if b := h.box("box_11"); b.DNSState != "removed" || h.cf.Count() != 0 || b.Status != "deleting" {
		t.Fatalf("after a failed delete: %+v %d records", b, h.cf.Count())
	}
	if srv, _, _, _ := h.hz.Count(); srv != 1 {
		t.Fatal("the server went without a working key?")
	}
	if c, _ := h.store.Claim(context.Background(), time.Minute); c != nil {
		t.Fatal("a retry ran before its time")
	}
	// The retry, with the address already gone (its checkpoint): the customer's key works now.
	sealed, _ := Seal(h.seal.PublicKey(), []byte(hetznertest.Token), TokenAAD("box_11"))
	h.exec(`update cloud_jobs set not_before = null, token_sealed = $2 where id = $1`, id, sealed)
	_ = h.w.DNS.Set(context.Background(), "gone", "203.0.113.77", "") // would be removed again if the checkpoint were ignored
	h.runNext()
	if j := h.job(id); j.Status != "done" {
		t.Fatalf("retry: %s", j.text())
	}
	if h.cf.Count() == 0 {
		t.Fatal("the retry redid a phase its checkpoint says is done")
	}
	if srv, vols, _, _ := h.hz.Count(); srv != 0 || vols != 1 {
		t.Fatalf("after delete: servers %d volumes %d (the data volume stays unless asked)", srv, vols)
	}
	// Deleted, for good, with when and whether the data went too; the customer is emailed once.
	b := h.box("box_11")
	if b.Status != "deleted" || h.str(`select signin_code from cloud_boxes where id = 'box_11'`) != "" {
		t.Fatalf("box %+v", b)
	}
	if h.num(`select count(*) from cloud_boxes where id = 'box_11' and deleted_at is not null and data_deleted = false and released_at is null`) != 1 {
		t.Fatal("deleted_at and data_deleted not recorded")
	}
	if h.str(`select params->>'dataDeleted' from cloud_outbox where box_id = 'box_11' and kind = 'deleted' and key = $1`, fmt.Sprint(id)) != "false" {
		t.Fatal("no deleted email")
	}
	if j := h.job(id); !strings.HasSuffix(j.text(), "Deleted") {
		t.Fatalf("the last step says it's deleted: %s", j.text())
	}
}

// A delete that keeps failing gives up after MaxAttempts: the box stays
// "deleting" (the account offers to try again) and is never marked deleted.
func TestDeleteGivesUp(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_31", "stuck", ProvisionArgs{})
	h.exec(`update cloud_boxes set status = 'deleting' where id = 'box_31'`)
	id := h.enqueue("box_31", "delete_server", "a-revoked-token", DeleteArgs{})
	for i := 0; i < MaxAttempts; i++ {
		h.exec(`update cloud_jobs set not_before = null where id = $1`, id)
		h.runNext()
	}
	if j := h.job(id); j.Status != "failed" || j.Token != nil {
		t.Fatalf("after %d tries: %s", MaxAttempts, j.text())
	}
	if b := h.box("box_31"); b.Status != "deleting" || b.DNSState != "removed" {
		t.Fatalf("box %+v", b)
	}
	if srv, _, _, _ := h.hz.Count(); srv != 1 {
		t.Fatal("the server went without a working key?")
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_31' and kind = 'deleted'`) != 0 {
		t.Fatal("a deleted email for a box that wasn't deleted")
	}
}

// Deleting with the data deletes the volume too, and says so.
func TestDeleteWithData(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_32", "wipe", ProvisionArgs{})
	h.exec(`update cloud_boxes set status = 'deleting' where id = 'box_32'`)
	id := h.enqueue("box_32", "delete_server", hetznertest.Token, DeleteArgs{DeleteData: true})
	// Emails about the running box still waiting (the subscription's end, a warning) go unsent; others stay.
	h.exec(`insert into cloud_outbox (box_id, kind, key) values ('box_32', 'extras_paused', 'sub_1'), ('box_32', 'dns_soon', 'x'), ('box_32', 'refunded', '')`)
	h.runNext()
	if j := h.job(id); j.Status != "done" {
		t.Fatalf("delete: %s", j.text())
	}
	if srv, vols, _, _ := h.hz.Count(); srv != 0 || vols != 0 {
		t.Fatalf("servers %d volumes %d", srv, vols)
	}
	if h.num(`select count(*) from cloud_boxes where id = 'box_32' and status = 'deleted' and data_deleted`) != 1 {
		t.Fatal("not recorded as deleted with its data")
	}
	if h.str(`select params->>'dataDeleted' from cloud_outbox where box_id = 'box_32' and kind = 'deleted'`) != "true" {
		t.Fatal("the email doesn't know the data went")
	}
	if h.str(`select string_agg(kind || '=' || status, ',' order by kind) from cloud_outbox where box_id = 'box_32'`) != "deleted=queued,dns_soon=dropped,extras_paused=dropped,ready=dropped,refunded=queued" {
		t.Fatalf("outbox: %s", h.str(`select string_agg(kind || '=' || status, ',' order by kind) from cloud_outbox where box_id = 'box_32'`))
	}
}

// Nothing brings a deleted box back: not a late job, not a write that
// forgets to check (the table refuses), not the sweep.
func TestDeletedIsForGood(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.provisioned("box_33", "late", ProvisionArgs{})
	h.exec(`update cloud_boxes set status = 'deleting' where id = 'box_33'`)
	h.enqueue("box_33", "delete_server", hetznertest.Token, DeleteArgs{})
	h.runNext()
	if b := h.box("box_33"); b.Status != "deleted" {
		t.Fatalf("box %+v", b)
	}
	// A late address restore (a check-in queued it) stays off.
	h.exec(`update cloud_boxes set last_heartbeat_at = now() where id = 'box_33'`)
	set := h.enqueue("box_33", "dns_set", "", DNSArgs{Reason: "heartbeat", Gen: 1})
	h.runNext()
	if j := h.job(set); h.cf.Count() != 0 || !strings.Contains(j.text(), "the box is deleted") {
		t.Fatalf("a deleted box's address came back: %s", j.text())
	}
	// A late setup or resize refuses.
	prov := h.enqueue("box_33", "provision", hetznertest.Token, ProvisionArgs{Name: "late", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(prov); j.Status != "failed" {
		t.Fatalf("a setup ran on a deleted box: %s", j.text())
	}
	rs := h.enqueue("box_33", "resize", hetznertest.Token, ResizeArgs{ServerType: "cax21"})
	h.runNext()
	if j := h.job(rs); j.Status != "failed" {
		t.Fatalf("a resize ran on a deleted box: %s", j.text())
	}
	// Any write that would change the status keeps it deleted, with its date and data flag.
	h.exec(`update cloud_boxes set status = 'active', deleted_at = null, data_deleted = true, name = name where id = 'box_33'`)
	if h.num(`select count(*) from cloud_boxes where id = 'box_33' and status = 'deleted' and deleted_at is not null and data_deleted = false`) != 1 {
		t.Fatal("a deleted box changed back")
	}
	if b := h.box("box_33"); b.Status != "deleted" {
		t.Fatalf("box %+v", b)
	}
	// The sweep queues nothing for it.
	if _, err := h.store.Sweep(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := h.num(`select count(*) from cloud_jobs where box_id = 'box_33' and status = 'queued'`); n != 0 {
		t.Fatalf("%d jobs queued for a deleted box", n)
	}
	if srv, _, _, _ := h.hz.Count(); srv != 0 {
		t.Fatal("servers came back")
	}
}

// Boxes deleted before the "deleted" status existed were left "released":
// applying the schema finds them by their finished delete_server job. A box
// released with "Stop managed service" (no delete job) stays released.
func TestSchemaFindsEarlierDeletes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addBox("box_34", "acme")
	h.addBox("box_35", "kept")
	h.addBox("box_36", "half")
	h.exec(`alter table cloud_boxes disable trigger cloud_boxes_keep_deleted`)
	h.exec(`update cloud_boxes set status = 'released' where id in ('box_34', 'box_35', 'box_36')`)
	h.exec(`insert into cloud_jobs (box_id, kind, status, args, finished_at) values ('box_34', 'delete_server', 'done', '{"deleteData": true}', '2026-10-09T10:00:00Z')`)
	h.exec(`insert into cloud_jobs (box_id, kind, status, args, finished_at) values ('box_36', 'delete_server', 'failed', '{}', now())`)
	h.exec(`alter table cloud_boxes enable trigger cloud_boxes_keep_deleted`)
	if err := h.store.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if h.num(`select count(*) from cloud_boxes where id = 'box_34' and status = 'deleted' and deleted_at = '2026-10-09T10:00:00Z' and data_deleted`) != 1 {
		t.Fatal("an earlier delete wasn't found")
	}
	if h.box("box_35").Status != "released" || h.box("box_36").Status != "released" {
		t.Fatal("a released box (or one whose delete didn't finish) was marked deleted")
	}
}

func TestKillSwitchSticks(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_12", "bad", ProvisionArgs{})
	h.exec(`update cloud_boxes set killed_at = now(), dns_state = 'killed', last_heartbeat_at = now() where id = 'box_12'`)
	h.enqueue("box_12", "dns_remove", "", DNSArgs{Reason: "kill"})
	h.runNext()
	if h.cf.Count() != 0 || h.box("box_12").DNSState != "killed" {
		t.Fatal("kill")
	}
	// An ordinary removal queued before the kill (parking, grace) can't turn killed into removed.
	h.exec(`update cloud_boxes set last_heartbeat_at = now() - interval '4 days' where id = 'box_12'`)
	h.enqueue("box_12", "dns_remove", "", DNSArgs{Reason: "parked", Gen: 1})
	h.runNext()
	if s := h.box("box_12").DNSState; s != "killed" {
		t.Fatalf("state %s", s)
	}
	// Nor can a restore, even with a fresh check-in.
	h.exec(`update cloud_boxes set last_heartbeat_at = now() where id = 'box_12'`)
	id := h.enqueue("box_12", "dns_set", "", DNSArgs{Reason: "heartbeat", Gen: 1})
	h.runNext()
	if j := h.job(id); h.cf.Count() != 0 || !strings.Contains(j.text(), "kill switch") {
		t.Fatalf("a killed address came back: %s", j.text())
	}
}

// Removals queued minutes ago check their reason still holds.
func TestStaleRemovalsRevalidate(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_13", "late", ProvisionArgs{})
	paused := time.Now().Add(-31 * 24 * time.Hour)
	h.exec(`update cloud_boxes set plan_status = 'canceled', extras_paused_at = $1, last_heartbeat_at = now() where id = 'box_13'`, paused)
	h.enqueue("box_13", "dns_remove", "", DNSArgs{Reason: "grace", Gen: 1, PausedAt: &paused})
	// The customer paid again before the job ran.
	h.exec(`update cloud_boxes set plan_status = 'active', extras_paused_at = null where id = 'box_13'`)
	h.runNext()
	if h.cf.Count() == 0 || h.box("box_13").DNSState != "live" {
		t.Fatal("a paying customer lost their address to a stale job")
	}
	// Parking a box that checked in meanwhile: no.
	h.enqueue("box_13", "dns_remove", "", DNSArgs{Reason: "parked", Gen: 1})
	h.runNext()
	if h.box("box_13").DNSState != "live" {
		t.Fatal("parked a box that checks in")
	}
	// A box set up again since: an old job does nothing.
	h.exec(`update cloud_boxes set last_heartbeat_at = now() - interval '4 days' where id = 'box_13'`)
	h.enqueue("box_13", "dns_remove", "", DNSArgs{Reason: "parked", Gen: 0 + 7})
	h.runNext()
	if h.box("box_13").DNSState != "live" {
		t.Fatal("a job for another generation acted")
	}
	// Still eligible: parked.
	h.enqueue("box_13", "dns_remove", "", DNSArgs{Reason: "parked", Gen: 1})
	h.runNext()
	if h.box("box_13").DNSState != "parked" || h.cf.Count() != 0 {
		t.Fatal("park")
	}
}

// The address comes back only for the current installation, checking in
// lately from its own address (the website records only such check-ins),
// and only to the addresses the worker recorded.
func TestRestoreNeedsAFreshCheckIn(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_14", "back", ProvisionArgs{})
	h.exec(`update cloud_boxes set dns_state = 'parked', last_heartbeat_at = now() - interval '4 days' where id = 'box_14'`)
	_ = h.w.DNS.Remove(context.Background(), "back")
	id := h.enqueue("box_14", "dns_set", "", DNSArgs{Reason: "renewed", Gen: 1})
	h.runNext()
	if h.cf.Count() != 0 || !strings.Contains(h.job(id).text(), "hasn't checked in") {
		t.Fatalf("restored without a check-in: %s", h.job(id).text())
	}
	// Someone with database access points it elsewhere: refused.
	h.exec(`update cloud_boxes set last_heartbeat_at = now(), ipv4 = '198.51.100.66' where id = 'box_14'`)
	id = h.enqueue("box_14", "dns_set", "", DNSArgs{Reason: "heartbeat", Gen: 1})
	h.runNext()
	if h.cf.Count() != 0 || !strings.Contains(h.job(id).text(), "don't check out") {
		t.Fatalf("pointed at an address the worker never recorded: %s", h.job(id).text())
	}
	// A licence of an earlier generation: refused.
	ip := h.hz.ServerByName("back").PublicNet.IPv4.IP
	h.exec(`update cloud_boxes set ipv4 = $1 where id = 'box_14'`, ip)
	h.enqueue("box_14", "dns_set", "", DNSArgs{Reason: "heartbeat", Gen: 0 + 9})
	h.runNext()
	if h.cf.Count() != 0 {
		t.Fatal("restored for another generation")
	}
	h.enqueue("box_14", "dns_set", "", DNSArgs{Reason: "heartbeat", Gen: 1})
	h.runNext()
	if h.cf.Count() == 0 || h.box("box_14").DNSState != "live" {
		t.Fatal("a fresh check-in must restore")
	}
}

func TestCertificatePending(t *testing.T) {
	h := newHarness(t)
	h.w.WaitHTTPS = func(context.Context, string, time.Duration) error {
		return errors.New("x509: certificate is not valid yet")
	}
	h.addBox("box_15", "slowcert")
	id := h.enqueue("box_15", "provision", hetznertest.Token, ProvisionArgs{Name: "slowcert", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(id); j.Status != "done" || !strings.Contains(j.text(), "certificate is still pending") {
		t.Fatalf("job: %s", j.text())
	}
	if b := h.box("box_15"); b.Status != "cert_pending" || b.ReadyAt != nil {
		t.Fatalf("box %+v", b)
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_15' and kind = 'ready'`) != 0 {
		t.Fatal("ready before the dashboard is")
	}
	h.exec(`update cloud_boxes set https_checked_at = now() - interval '2 minutes' where id = 'box_15'`)
	h.w.WaitHTTPS = func(context.Context, string, time.Duration) error { return nil }
	h.w.CheckCertificates(context.Background())
	if b := h.box("box_15"); b.Status != "active" || b.ReadyAt == nil {
		t.Fatalf("box %+v", b)
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_15' and kind = 'ready'`) != 1 {
		t.Fatal("no ready email")
	}
}

func TestSweep(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addBox("box_5", "shed")
	h.exec(`update cloud_boxes set status = 'provisioning', generation = 3, signin_code = 'tfl_x', signin_expires_at = now() - interval '1 minute' where id = 'box_5'`)
	stale := h.enqueue("box_5", "provision", "tok", ProvisionArgs{Name: "shed"})
	h.exec(`update cloud_jobs set status = 'running', lease_until = now() - interval '1 second' where id = $1`, stale)
	h.addBox("box_16", "ship")
	h.exec(`update cloud_boxes set status = 'active', dns_state = 'live' where id = 'box_16'`)
	dns := h.enqueue("box_16", "dns_remove", "", DNSArgs{Reason: "parked"})
	h.exec(`update cloud_jobs set status = 'running', attempts = 1, lease_until = now() - interval '1 second' where id = $1`, dns)
	old := h.enqueue("box_16", "resize", "tok", ResizeArgs{})
	h.exec(`update cloud_jobs set token_expires_at = now() - interval '1 minute' where id = $1`, old)
	h.addBox("box_17", "fail")
	h.exec(`update cloud_boxes set status = 'failed', dns_state = 'live' where id = 'box_17'`)

	did, err := h.store.Sweep(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if j := h.job(stale); j.Status != "failed" || j.Token != nil {
		t.Fatalf("stale setup: %+v", j)
	}
	if h.box("box_5").Status != "failed" || h.str(`select signin_code from cloud_boxes where id = 'box_5'`) != "" {
		t.Fatalf("box after sweep (%s)", did)
	}
	// Its clean-up has the key (until it expires) and the generation.
	var args string
	var tok *string
	_ = h.store.Pool.QueryRow(ctx, `select args::text, token_sealed from cloud_jobs where box_id = 'box_5' and kind = 'cleanup'`).Scan(&args, &tok)
	if tok == nil || !strings.Contains(args, `"gen": 3`) {
		t.Fatalf("clean-up job: %s token=%v", args, tok != nil)
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_5' and kind = 'setup_failed'`) != 1 {
		t.Fatal("no setup_failed email")
	}
	if j := h.job(dns); j.Status != "queued" {
		t.Fatalf("a stale DNS job is retried: %s", j.Status)
	}
	if j := h.job(old); j.Token != nil {
		t.Fatal("an expired job token must be wiped")
	}
	if h.num(`select count(*) from cloud_jobs where box_id = 'box_17' and kind = 'dns_remove'`) != 1 {
		t.Fatalf("a failed box's live address must be removed (%s)", did)
	}
	// Not again within five minutes.
	_, _ = h.store.Sweep(ctx, time.Now())
	if h.num(`select count(*) from cloud_jobs where box_id = 'box_17' and kind = 'dns_remove'`) != 1 {
		t.Fatal("removal queued twice")
	}
}

// The clean-up after a stopped setup deletes what it made, unless the box moved on.
func TestCleanupJob(t *testing.T) {
	h := newHarness(t)
	h.hz.AddLabelledServer("left", map[string]string{hetzner.LabelKind: "box", hetzner.LabelBox: "left", hetzner.LabelOwner: "box_18"})
	h.addBox("box_18", "left")
	h.exec(`update cloud_boxes set status = 'failed', generation = 2, dns_state = 'live' where id = 'box_18'`)
	_ = h.w.DNS.Set(context.Background(), "left", "203.0.113.9", "")
	h.enqueue("box_18", "cleanup", hetznertest.Token, CleanupArgs{Reason: "setup_failed", Gen: 1})
	h.runNext()
	if srv, _, _, _ := h.hz.Count(); srv != 1 || h.cf.Count() == 0 {
		t.Fatal("a clean-up for an older generation acted")
	}
	h.enqueue("box_18", "cleanup", hetznertest.Token, CleanupArgs{Reason: "setup_failed", Gen: 2})
	h.runNext()
	if srv, _, _, _ := h.hz.Count(); srv != 0 || h.cf.Count() != 0 || h.box("box_18").DNSState != "removed" {
		t.Fatal("clean-up")
	}
}

func TestRestartDeletesOldSetupKeys(t *testing.T) {
	h := newHarness(t)
	_ = os.MkdirAll(filepath.Join(h.w.TempDir, "setup-box_1-123"), 0o700)
	_ = os.WriteFile(filepath.Join(h.w.TempDir, "setup-box_1-123", "id_ed25519"), []byte("private"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.w.Run(ctx)
	if entries, _ := os.ReadDir(h.w.TempDir); len(entries) != 0 {
		t.Fatalf("left: %v", entries)
	}
}

func TestDNSRecords(t *testing.T) {
	cf := cftest.New("shiptiffin.app", "example.com")
	d := DNS{P: &cloudflare.Provider{APIToken: cftest.Token, HTTPClient: cf.Client()}, Zone: "shiptiffin.app"}
	ctx := context.Background()
	cf.Add("shiptiffin.app", "A", "myshop.shiptiffin.app", "192.0.2.9")
	cf.Add("shiptiffin.app", "TXT", "shop.shiptiffin.app", "keep me")
	if err := d.Set(ctx, "shop", "203.0.113.5", ""); err != nil {
		t.Fatal(err)
	}
	if got := cf.Records("shop.shiptiffin.app"); !slices.Equal(got, []string{"A 203.0.113.5", "TXT keep me"}) {
		t.Fatalf("records: %v", got)
	}
	if err := d.Set(ctx, "shop", "203.0.113.6", "2001:db8::1"); err != nil {
		t.Fatal(err)
	}
	if got := cf.Records("*.shop.shiptiffin.app"); !slices.Equal(got, []string{"A 203.0.113.6", "AAAA 2001:db8::1"}) {
		t.Fatalf("wildcard: %v", got)
	}
	if err := d.Remove(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if got := cf.Records("shop.shiptiffin.app"); !slices.Equal(got, []string{"TXT keep me"}) {
		t.Fatalf("after remove: %v", got)
	}
	if got := cf.Records("myshop.shiptiffin.app"); len(got) != 1 {
		t.Fatalf("another box's record went: %v", got)
	}
	if _, err := d.Records("www", "203.0.113.5", ""); err == nil {
		t.Fatal("a reserved name must not get records")
	}
	if _, err := d.Records("shop", "2001:db8::1", ""); err == nil {
		t.Fatal("an IPv6 address is not an A record")
	}
	bad := DNS{P: &cloudflare.Provider{APIToken: "nope", HTTPClient: cf.Client()}, Zone: "shiptiffin.app"}
	if err := bad.Set(ctx, "cafe", "203.0.113.5", ""); err == nil {
		t.Fatal("a bad token must fail")
	}
}
