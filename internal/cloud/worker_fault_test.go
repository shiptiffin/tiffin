package cloud

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/install"
	"github.com/btahir/tiffin/internal/provider"
	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/btahir/tiffin/internal/provider/remote"
)

// faultStore is the Postgres store with faults injected into some calls.
type faultStore struct {
	*PG
	markReady func(ctx context.Context, boxID string) (bool, error)
	extend    func(ctx context.Context, l Lease, d time.Duration) error
}

func (f *faultStore) MarkReady(ctx context.Context, boxID string) (bool, error) {
	if f.markReady != nil {
		return f.markReady(ctx, boxID)
	}
	return f.PG.MarkReady(ctx, boxID)
}

func (f *faultStore) Extend(ctx context.Context, l Lease, d time.Duration) error {
	if f.extend != nil {
		return f.extend(ctx, l, d)
	}
	return f.PG.Extend(ctx, l, d)
}

func (h *harness) kept(boxID string) (servers, volumes int) {
	h.t.Helper()
	servers, volumes, _, _ = h.hz.Count()
	return servers, volumes
}

// "Ready" committed, but its answer was lost: the box is ready, and
// nothing of it is deleted.
func TestAmbiguousReadyCommitKeepsTheBox(t *testing.T) {
	h := newHarness(t)
	fs := &faultStore{PG: h.store}
	fs.markReady = func(ctx context.Context, id string) (bool, error) {
		_, _ = h.store.MarkReady(ctx, id)
		return false, errors.New("unexpected EOF (the commit went through)")
	}
	h.w.Store = fs
	b := h.provisioned("box_a1", "lost", ProvisionArgs{})
	if b.Status != "active" || b.ReadyAt == nil || b.DNSState != "live" || h.cf.Count() == 0 {
		t.Fatalf("box %+v, %d records", b, h.cf.Count())
	}
	if srv, vols := h.kept("box_a1"); srv != 1 || vols != 1 {
		t.Fatalf("a ready box lost its server or volume: %d %d", srv, vols)
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_a1' and kind = 'setup_failed'`) != 0 {
		t.Fatal("a ready box was reported failed")
	}

	// "Ready" didn't commit at all: still nothing deleted; the certificate check finishes it.
	fs.markReady = func(context.Context, string) (bool, error) { return false, errors.New("connection reset") }
	b = h.provisioned("box_a2", "lost2", ProvisionArgs{})
	if b.Status != "cert_pending" || h.cf.Records("lost2.shiptiffin.app") == nil {
		t.Fatalf("box %+v", b)
	}
	if srv, vols := h.kept("box_a2"); srv != 2 || vols != 2 {
		t.Fatalf("servers %d volumes %d", srv, vols)
	}
	fs.markReady = nil
	h.exec(`update cloud_boxes set https_checked_at = now() - interval '2 minutes' where id = 'box_a2'`)
	h.w.CheckCertificates(context.Background())
	if b := h.box("box_a2"); b.Status != "active" {
		t.Fatalf("box %+v", b)
	}
}

// Once Tiffin is installed, a failure keeps the server, its volume and its
// address, and flags the box for a person; no clean-up deletes it later.
func TestFailureAfterInstallKeepsEverything(t *testing.T) {
	h := newHarness(t)
	h.machine.hook = func(script string) (string, bool) {
		if strings.Contains(script, "authorized_keys") {
			return "still-there\n", true
		}
		return "", false
	}
	h.addBox("box_b1", "keep")
	id := h.enqueue("box_b1", "provision", hetznertest.Token, ProvisionArgs{Name: "keep", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(id); j.Status != "failed" || !strings.Contains(j.text(), "data are kept") {
		t.Fatalf("job: %s", j.text())
	}
	b := h.box("box_b1")
	if b.Status != "cert_pending" || b.InstalledAt == nil || b.Attention == "" || b.DNSState != "live" || h.cf.Count() == 0 {
		t.Fatalf("box %+v", b)
	}
	if srv, vols := h.kept("box_b1"); srv != 1 || vols != 1 {
		t.Fatalf("servers %d volumes %d", srv, vols)
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_b1' and kind = 'attention'`) != 1 ||
		h.num(`select count(*) from cloud_outbox where box_id = 'box_b1' and kind = 'setup_failed'`) != 0 {
		t.Fatal("emails")
	}
	// A clean-up queued for it anyway (by an older bug, or by hand) deletes nothing.
	h.exec(`update cloud_boxes set status = 'failed' where id = 'box_b1'`)
	h.enqueue("box_b1", "cleanup", hetznertest.Token, CleanupArgs{Reason: "setup_failed", Gen: b.Generation})
	h.runNext()
	if srv, vols := h.kept("box_b1"); srv != 1 || vols != 1 || h.cf.Count() == 0 {
		t.Fatalf("a clean-up deleted an installed box: servers %d volumes %d records %d", srv, vols, h.cf.Count())
	}
	// Nor does setting it up again.
	h.enqueue("box_b1", "provision", hetznertest.Token, ProvisionArgs{Name: "keep", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if srv, vols := h.kept("box_b1"); srv != 1 || vols != 1 {
		t.Fatal("a second setup deleted an installed box")
	}

	// A worker that stopped after the install: the sweep flags it, queues no clean-up.
	h.addBox("box_b2", "keep2")
	h.exec(`update cloud_boxes set status = 'provisioning', generation = 2, installed_at = now() where id = 'box_b2'`)
	stale := h.enqueue("box_b2", "provision", hetznertest.Token, ProvisionArgs{Name: "keep2"})
	h.exec(`update cloud_jobs set status = 'running', lease_until = now() - interval '1 second' where id = $1`, stale)
	if _, err := h.store.Sweep(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if b := h.box("box_b2"); b.Status != "cert_pending" || b.Attention == "" {
		t.Fatalf("box %+v", b)
	}
	if h.num(`select count(*) from cloud_jobs where box_id = 'box_b2' and kind = 'cleanup'`) != 0 {
		t.Fatal("a clean-up was queued for an installed box")
	}
}

// Records published half way, then a failure, and removing them fails too:
// the server (and its IP) stays until the records are gone.
func TestHalfPublishedDNSGoesBeforeTheServer(t *testing.T) {
	h := newHarness(t)
	var posts atomic.Int32
	var broken atomic.Bool
	broken.Store(true)
	h.cf.SetFault(func(method, _ string) bool {
		if !broken.Load() {
			return false
		}
		switch method {
		case "POST":
			return posts.Add(1) > 1 // the first record goes in, the rest fail
		case "DELETE":
			return true
		}
		return false
	})
	h.addBox("box_c1", "half")
	id := h.enqueue("box_c1", "provision", hetznertest.Token, ProvisionArgs{Name: "half", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(id); j.Status != "failed" {
		t.Fatalf("job: %s", j.text())
	}
	b := h.box("box_c1")
	if b.Status != "failed" || b.DNSState != "pending" || h.cf.Count() == 0 {
		t.Fatalf("box %+v, %d records", b, h.cf.Count())
	}
	if srv, _ := h.kept("box_c1"); srv != 1 {
		t.Fatal("the server went while a record still points at its IP")
	}
	// The clean-up keeps trying the address first.
	cid := h.num(`select id from cloud_jobs where box_id = 'box_c1' and kind = 'cleanup' and status = 'queued'`)
	h.runNext()
	if j := h.job(cid); j.Status != "queued" || !strings.Contains(*j.Error, "server stays") {
		t.Fatalf("clean-up: %s", j.text())
	}
	if srv, _ := h.kept("box_c1"); srv != 1 {
		t.Fatal("the server went while a record still points at its IP")
	}
	// Cloudflare works again: the records go, then the server.
	broken.Store(false)
	h.exec(`update cloud_jobs set not_before = null where id = $1`, cid)
	h.runNext()
	if j := h.job(cid); j.Status != "done" {
		t.Fatalf("clean-up: %s", j.text())
	}
	if srv, vols := h.kept("box_c1"); srv != 0 || vols != 0 || h.cf.Count() != 0 || h.box("box_c1").DNSState != "removed" {
		t.Fatalf("after clean-up: servers %d volumes %d records %d", srv, vols, h.cf.Count())
	}

	// The sweep covers an address being published, not only a live one.
	h.addBox("box_c2", "half2")
	h.exec(`update cloud_boxes set status = 'failed', dns_state = 'pending' where id = 'box_c2'`)
	if _, err := h.store.Sweep(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if h.num(`select count(*) from cloud_jobs where box_id = 'box_c2' and kind = 'dns_remove'`) != 1 {
		t.Fatal("a half-published address of a failed box isn't removed")
	}
}

// A renewal that hangs (the database never answers) still stops the job
// before its lease runs out.
func TestHungRenewalStopsTheJob(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	defer close(release)
	fs := &faultStore{PG: h.store}
	fs.extend = func(context.Context, Lease, time.Duration) error {
		<-release // ignores its context, like a wedged connection
		return nil
	}
	h.w.Store = fs
	h.w.Lease = 600 * time.Millisecond
	started := make(chan struct{})
	stopped := make(chan error, 1)
	h.w.Connect = func(ctx context.Context, _ remote.Target) (Machine, error) {
		close(started)
		<-ctx.Done()
		stopped <- context.Cause(ctx)
		return nil, ctx.Err()
	}
	h.addBox("box_d1", "hang")
	id := h.enqueue("box_d1", "provision", hetznertest.Token, ProvisionArgs{Name: "hang", ServerType: "cax11", Location: "fsn1"})
	j, _ := h.store.Claim(context.Background(), h.w.Lease)
	begun := time.Now()
	done := make(chan struct{})
	go func() { h.w.RunJob(context.Background(), j); close(done) }()
	<-started
	select {
	case cause := <-stopped:
		if !errors.Is(cause, ErrLeaseLost) {
			t.Fatalf("stopped for %v", cause)
		}
		if d := time.Since(begun); d >= h.w.Lease {
			t.Fatalf("stopped after %s, not before the lease (%s) ran out", d, h.w.Lease)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a hung renewal kept the job running")
	}
	<-done
	if j := h.job(id); j.Status != "running" {
		t.Fatalf("the job is %s; the sweep decides what happens to it", j.Status)
	}
}

// A resize that stopped half way always gets the server running again:
// even after the subscription ended, with the job's key or the kept one;
// without any key the customer is told, by email and on the box.
func TestInterruptedResizeAlwaysPowersOn(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_e1", "pow", ProvisionArgs{})
	h.exec(`update cloud_boxes set plan_status = 'canceled', extras_paused_at = now() where id = 'box_e1'`)
	interrupted := func(token string, args ResizeArgs) int64 {
		h.hz.SetServerStatus("pow", "off")
		id := h.enqueue("box_e1", "resize", token, args)
		h.exec(`update cloud_jobs set checkpoint = '{"phase": "changing the type"}' where id = $1`, id)
		h.runNext()
		return id
	}
	id := interrupted(hetznertest.Token, ResizeArgs{ServerType: "cax21"})
	if j := h.job(id); j.Status != "done" || !strings.Contains(j.text(), "only make sure the server runs") {
		t.Fatalf("recovery: %s", j.text())
	}
	if s := h.hz.ServerByName("pow"); s.Status != "running" || len(h.hz.ChangeTypes) != 0 {
		t.Fatalf("server %s, type changes %v", s.Status, h.hz.ChangeTypes)
	}

	// No key at all: the server may be off, and the customer hears it.
	id = interrupted("", ResizeArgs{ServerType: "cax21"})
	if j := h.job(id); j.Status != "failed" {
		t.Fatalf("job: %s", j.text())
	}
	if b := h.box("box_e1"); b.Attention != ServerOffWhy {
		t.Fatalf("attention %q", b.Attention)
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_e1' and kind = 'server_off' and key = $1`, id) != 1 {
		t.Fatal("no email about a server that may be off")
	}

	// The key the customer kept works for this, even if the job didn't ask for it; the note goes.
	sealed, _ := Seal(h.seal.PublicKey(), []byte(hetznertest.Token), TokenAAD("box_e1"))
	h.exec(`update cloud_boxes set token_sealed = $1 where id = 'box_e1'`, sealed)
	id = interrupted("", ResizeArgs{ServerType: "cax21"})
	if j := h.job(id); j.Status != "done" || h.hz.ServerByName("pow").Status != "running" || h.box("box_e1").Attention != "" {
		t.Fatalf("recovery with the kept key: %s", j.text())
	}

	// A resize the sweep gives up on (its worker kept stopping) emails too.
	stale := h.enqueue("box_e1", "resize", "", ResizeArgs{ServerType: "cax21"})
	h.exec(`update cloud_jobs set status = 'running', attempts = $2, lease_until = now() - interval '1 second', checkpoint = '{"phase": "starting the server"}' where id = $1`, stale, MaxAttempts)
	if _, err := h.store.Sweep(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if h.num(`select count(*) from cloud_outbox where box_id = 'box_e1' and kind = 'server_off' and key = $1`, stale) != 1 || h.box("box_e1").Attention != ServerOffWhy {
		t.Fatal("a resize given up on half way must tell the customer")
	}
}

// A setup failed, its clean-up gave up on Cloudflare, and the customer sets
// the box up again: the old records go before the old server (and its IP)
// does, and while they can't, the old server stays.
func TestResetupRemovesDNSBeforeTheOldServer(t *testing.T) {
	h := newHarness(t)
	var broken atomic.Bool
	broken.Store(true)
	h.cf.SetFault(func(method, _ string) bool { return broken.Load() && method == "DELETE" })
	failInstall := func(context.Context, provider.Machine, string, install.Options, func(string)) (*install.Result, error) {
		return nil, errors.New("provision: apt failed")
	}
	okInstall := h.w.Install
	h.w.Install = failInstall
	h.addBox("box_r1", "again")
	args := ProvisionArgs{Name: "again", ServerType: "cax11", Location: "fsn1"}
	h.enqueue("box_r1", "provision", hetznertest.Token, args)
	h.runNext()
	old := h.hz.ServerByName("again")
	if old == nil || h.cf.Count() == 0 {
		t.Fatalf("after the failed setup: server %v, %d records", old, h.cf.Count())
	}
	// The clean-up exhausts its retries.
	h.exec(`update cloud_jobs set status = 'failed' where box_id = 'box_r1' and kind = 'cleanup'`)

	// Setup again while Cloudflare still refuses: nothing is deleted.
	h.w.Install = okInstall
	id := h.enqueue("box_r1", "provision", hetznertest.Token, args)
	h.runNext()
	if j := h.job(id); j.Status != "failed" || !strings.Contains(j.text(), "server stays") {
		t.Fatalf("re-setup: %s", j.text())
	}
	if s := h.hz.ServerByName("again"); s == nil || s.ID != old.ID || h.cf.Count() == 0 {
		t.Fatal("the old server went while its records still point at its IP")
	}
	h.exec(`update cloud_jobs set status = 'failed' where box_id = 'box_r1' and kind = 'cleanup' and status = 'queued'`)

	// Cloudflare works again: records, then the old server, then a new setup.
	broken.Store(false)
	id = h.enqueue("box_r1", "provision", hetznertest.Token, args)
	h.runNext()
	if j := h.job(id); j.Status != "done" {
		t.Fatalf("re-setup: %s", j.text())
	}
	if s := h.hz.ServerByName("again"); s == nil || s.ID == old.ID {
		t.Fatal("the old server is still there")
	}
	if b := h.box("box_r1"); b.DNSState != "live" || b.IPv4 == "" {
		t.Fatalf("box %+v", b)
	}
}
