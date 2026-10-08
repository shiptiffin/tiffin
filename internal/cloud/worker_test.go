package cloud

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
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

	"github.com/btahir/tiffin/internal/cloud/cftest"
	"github.com/btahir/tiffin/internal/install"
	"github.com/btahir/tiffin/internal/licence"
	"github.com/btahir/tiffin/internal/provider"
	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/btahir/tiffin/internal/provider/remote"
	"github.com/libdns/cloudflare"
)

// fakeMachine is a new server reached over SSH: it records what runs.
type fakeMachine struct {
	mu      sync.Mutex
	scripts []string
	copies  []string
	target  remote.Target
}

func (m *fakeMachine) Exec(_ context.Context, script string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scripts = append(m.scripts, script)
	if strings.Contains(script, "authorized_keys") {
		return "removed\n", "", nil
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
	kek     []byte
	key     ed25519.PrivateKey
	machine *fakeMachine
	opts    []install.Options
	connect []remote.Target
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, store: newStore(t), hz: hetznertest.New(), cf: cftest.New("shiptiffin.app"), machine: &fakeMachine{}}
	t.Cleanup(h.hz.Close)
	h.kek = make([]byte, 32)
	_, _ = rand.Read(h.kek)
	_, h.key, _ = ed25519.GenerateKey(rand.Reader)
	bin := filepath.Join(t.TempDir(), "tiffin")
	_ = os.WriteFile(bin, []byte("tiffin build"), 0o755)
	h.w = &Worker{
		Store: h.store, KEK: h.kek, Licence: h.key,
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
			return &install.Result{OwnerToken: "tft_owner_secret", Build: "abc"}, nil
		},
		PrepareData: func(context.Context, provider.Machine, install.DataSpec, func(string)) ([]string, error) {
			return nil, nil
		},
		WaitHTTPS: func(context.Context, string) error { return nil },
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

func (h *harness) addBox(id, name string) {
	h.exec(`insert into cloud_boxes (id, user_id, email, name, status, plan_status) values ($1, 'u1', 'sam@example.com', $2, 'paid', 'active')`, id, name)
}

// enqueue adds a job; token "" means none.
func (h *harness) enqueue(boxID, kind, token string, args any) int64 {
	h.t.Helper()
	raw, _ := json.Marshal(args)
	var sealed *string
	if token != "" {
		s, err := Seal(h.kek, []byte(token), TokenAAD(boxID))
		if err != nil {
			h.t.Fatal(err)
		}
		sealed = &s
	}
	var id int64
	if err := h.store.Pool.QueryRow(context.Background(), `insert into cloud_jobs (box_id, kind, args, token_sealed) values ($1, $2, $3, $4) returning id`,
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

func TestProvisionEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_1", "shop")
	id := h.enqueue("box_1", "provision", hetznertest.Token, ProvisionArgs{Name: "shop", ServerType: "cax11", Location: "fsn1"})
	h.runNext()

	j := h.job(id)
	if j.Status != "done" || j.Token != nil {
		t.Fatalf("job: %s err=%v token kept=%v", j.Status, j.Error, j.Token != nil)
	}
	var steps []string
	for _, s := range j.Steps {
		steps = append(steps, s["text"])
	}
	all := strings.Join(steps, "\n")
	for _, want := range []string{"Creating a cax11 server", "Pointing shop.shiptiffin.app", "Removing the setup key", "Closing SSH", "Forgot your Hetzner key", "Your box is ready"} {
		if !strings.Contains(all, want) {
			t.Errorf("steps lack %q:\n%s", want, all)
		}
	}

	// The box row.
	if st := h.str(`select status from cloud_boxes where id = 'box_1'`); st != "active" {
		t.Fatalf("status %s", st)
	}
	ip4 := h.str(`select ipv4 from cloud_boxes where id = 'box_1'`)
	if !strings.HasPrefix(ip4, "203.0.113.") || h.str(`select dns_state from cloud_boxes where id = 'box_1'`) != "live" {
		t.Fatalf("ipv4 %q", ip4)
	}
	if h.str(`select token_sealed from cloud_boxes where id = 'box_1'`) != "" {
		t.Fatal("the Hetzner key must be forgotten by default")
	}
	if fp := h.str(`select token_fingerprint from cloud_boxes where id = 'box_1'`); fp != Fingerprint(hetznertest.Token) {
		t.Fatalf("fingerprint %q", fp)
	}
	owner := h.str(`select owner_token_sealed from cloud_boxes where id = 'box_1'`)
	if raw, err := Open(h.kek, owner, OwnerAAD("box_1")); err != nil || string(raw) != "tft_owner_secret" {
		t.Fatalf("owner token: %q %v", raw, err)
	}

	// DNS: the box's name and its wildcard, nothing else.
	if got := h.cf.Records("shop.shiptiffin.app"); len(got) != 2 || got[0] != "A "+ip4 || !strings.HasPrefix(got[1], "AAAA 2001:db8:") {
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
	entries, _ := os.ReadDir(h.w.TempDir)
	if len(entries) != 0 {
		t.Fatalf("the setup key is still on disk: %v", entries)
	}
	if len(h.connect) != 1 || !strings.HasPrefix(h.connect[0].Identity, h.w.TempDir) {
		t.Fatalf("connect: %+v", h.connect)
	}

	// The install: the box's domain, and a licence for this box.
	o := h.opts[0]
	if o.Domain != "shop.shiptiffin.app" || o.Managed == nil || o.Managed.ControlPlane != "https://shiptiffin.com" || o.Managed.BoxID != "box_1" {
		t.Fatalf("install options: %+v %+v", o, o.Managed)
	}
	if len(o.Server.OwnerIPs) != 0 {
		t.Fatal("the control plane's address must not be allowlisted on the box")
	}
	pub, _ := licence.ParsePublicKey(o.Managed.PublicKey)
	l, err := licence.Verify(pub, o.Managed.Licence)
	if err != nil || l.BoxID != "box_1" || l.Domain != "shop.shiptiffin.app" {
		t.Fatalf("licence %+v %v", l, err)
	}

	// Every Hetzner call is in the log; the token in none of them.
	var n int
	_ = h.store.Pool.QueryRow(context.Background(), `select count(*) from cloud_hetzner_calls where box_id = 'box_1' and job_id = $1 and purpose = 'setup'`, id).Scan(&n)
	if n < 10 {
		t.Fatalf("only %d calls recorded", n)
	}
	var leaked int
	_ = h.store.Pool.QueryRow(context.Background(), `select count(*) from cloud_hetzner_calls where path like '%' || $1 || '%' or coalesce(error, '') like '%' || $1 || '%'`, hetznertest.Token).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("the token appears in the call log")
	}
	var posts int
	_ = h.store.Pool.QueryRow(context.Background(), `select count(*) from cloud_hetzner_calls where method = 'POST' and path like '%/servers' and status between 200 and 299`).Scan(&posts)
	if posts != 1 {
		t.Fatalf("server creations logged: %d", posts)
	}

	// Resize needs a key: none was kept, so it fails and asks for one.
	rid := h.enqueue("box_1", "resize", "", ResizeArgs{ServerType: "cax21", UseStored: true})
	h.runNext()
	if j := h.job(rid); j.Status != "failed" || !strings.Contains(*j.Error, "paste it again") {
		t.Fatalf("resize without a key: %+v", j)
	}
	// With a pasted key that it keeps this time.
	rid = h.enqueue("box_1", "resize", hetznertest.Token, ResizeArgs{ServerType: "cax21", KeepKey: true})
	h.runNext()
	if j := h.job(rid); j.Status != "done" || j.Token != nil {
		t.Fatalf("resize: %+v %v", j, j.Error)
	}
	if !slices.Contains(h.hz.ChangeTypes, "cax21 upgrade_disk=false") || h.str(`select server_type from cloud_boxes where id = 'box_1'`) != "cax21" {
		t.Fatalf("change types %v", h.hz.ChangeTypes)
	}
	// Now one click: the stored key.
	if h.str(`select token_sealed from cloud_boxes where id = 'box_1'`) == "" {
		t.Fatal("the key should be kept now")
	}
	rid = h.enqueue("box_1", "resize", "", ResizeArgs{ServerType: "cax31", UseStored: true})
	h.runNext()
	if j := h.job(rid); j.Status != "done" {
		t.Fatalf("one-click resize: %v", j.Error)
	}

	// The kill switch takes the address away and keeps it away.
	kid := h.enqueue("box_1", "dns_remove", "", DNSRemoveArgs{Kill: true})
	h.runNext()
	if j := h.job(kid); j.Status != "done" || h.cf.Count() != 0 || h.str(`select dns_state from cloud_boxes where id = 'box_1'`) != "killed" {
		t.Fatalf("kill: %+v records=%d", j, h.cf.Count())
	}
	sid := h.enqueue("box_1", "dns_set", "", struct{}{})
	h.runNext()
	if j := h.job(sid); j.Status != "failed" || h.cf.Count() != 0 {
		t.Fatalf("a killed address must not come back by itself: %+v", j)
	}

	// Deleting the server: only with a pasted key; the data volume stays unless asked.
	did := h.enqueue("box_1", "delete_server", hetznertest.Token, DeleteArgs{})
	h.runNext()
	if j := h.job(did); j.Status != "done" {
		t.Fatalf("delete: %v", j.Error)
	}
	if srv, vols, _, _ := h.hz.Count(); srv != 0 || vols != 1 {
		t.Fatalf("after delete: servers %d volumes %d", srv, vols)
	}
	if h.str(`select status from cloud_boxes where id = 'box_1'`) != "released" || h.str(`select token_sealed from cloud_boxes where id = 'box_1'`) != "" {
		t.Fatal("a deleted box is released and its key forgotten")
	}
}

func TestProvisionKeepsKeyWhenAsked(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_2", "tea")
	h.enqueue("box_2", "provision", hetznertest.Token, ProvisionArgs{Name: "tea", ServerType: "cx23", Location: "nbg1", KeepKey: true})
	h.runNext()
	sealed := h.str(`select token_sealed from cloud_boxes where id = 'box_2'`)
	raw, err := Open(h.kek, sealed, TokenAAD("box_2"))
	if err != nil || string(raw) != hetznertest.Token {
		t.Fatalf("kept key: %v", err)
	}
	if _, err := Open(h.kek, sealed, TokenAAD("box_1")); !errors.Is(err, ErrSealed) {
		t.Fatal("a sealed key must not open for another box")
	}
}

func TestProvisionFailureForgetsTheKey(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_3", "cafe")
	id := h.enqueue("box_3", "provision", "not-the-token", ProvisionArgs{Name: "cafe", ServerType: "cax11", Location: "fsn1", KeepKey: true})
	h.runNext()
	j := h.job(id)
	if j.Status != "failed" || j.Token != nil || !strings.Contains(*j.Error, "rejected the API token") {
		t.Fatalf("job: %+v %v", j, j.Error)
	}
	if h.str(`select status from cloud_boxes where id = 'box_3'`) != "failed" || h.str(`select token_sealed from cloud_boxes where id = 'box_3'`) != "" {
		t.Fatal("a failed setup keeps no key")
	}
	var rejected int
	_ = h.store.Pool.QueryRow(context.Background(), `select count(*) from cloud_hetzner_calls where box_id = 'box_3' and status = 401`).Scan(&rejected)
	if rejected == 0 {
		t.Fatal("rejected calls are logged too")
	}
}

// A setup that stopped half way leaves labelled resources; a retry without
// "fresh" refuses, with "fresh" deletes them and starts again.
func TestProvisionRetryCleansUp(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_4", "deli")
	h.w.Install = func(context.Context, provider.Machine, string, install.Options, func(string)) (*install.Result, error) {
		return nil, errors.New("provision: apt failed")
	}
	id := h.enqueue("box_4", "provision", hetznertest.Token, ProvisionArgs{Name: "deli", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(id); j.Status != "failed" {
		t.Fatalf("install failure: %+v", j)
	}
	h.w.Install = func(_ context.Context, _ provider.Machine, _ string, o install.Options, _ func(string)) (*install.Result, error) {
		return &install.Result{OwnerToken: "tft_x"}, nil
	}
	id = h.enqueue("box_4", "provision", hetznertest.Token, ProvisionArgs{Name: "deli", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if j := h.job(id); j.Status != "failed" || !strings.Contains(*j.Error, "Clean up and try again") {
		t.Fatalf("retry without clean-up: %+v %v", j, j.Error)
	}
	id = h.enqueue("box_4", "provision", hetznertest.Token, ProvisionArgs{Name: "deli", ServerType: "cax11", Location: "fsn1", Fresh: true})
	h.runNext()
	if j := h.job(id); j.Status != "done" {
		t.Fatalf("clean retry: %v", j.Error)
	}
	if srv, _, _, keys := h.hz.Count(); srv != 1 || keys != 0 {
		t.Fatalf("servers %d keys %d", srv, keys)
	}
}

func TestSweep(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_5", "shed")
	ctx := context.Background()
	h.exec(`update cloud_boxes set status = 'provisioning', owner_token_sealed = 'x', owner_token_expires_at = now() - interval '1 minute' where id = 'box_5'`)
	stale := h.enqueue("box_5", "provision", "tok", ProvisionArgs{Name: "shed"})
	h.exec(`update cloud_jobs set status = 'running', lease_until = now() - interval '1 second' where id = $1`, stale)
	old := h.enqueue("box_5", "dns_set", "tok", struct{}{})
	h.exec(`update cloud_jobs set created_at = now() - interval '3 hours' where id = $1`, old)
	did, err := h.store.Sweep(ctx, time.Now(), MaxTokenAge)
	if err != nil {
		t.Fatal(err)
	}
	if j := h.job(stale); j.Status != "failed" || j.Token != nil {
		t.Fatalf("stale job: %+v", j)
	}
	if j := h.job(old); j.Token != nil {
		t.Fatal("an old job token must be wiped")
	}
	if h.str(`select status from cloud_boxes where id = 'box_5'`) != "failed" || h.str(`select owner_token_sealed from cloud_boxes where id = 'box_5'`) != "" {
		t.Fatalf("box after sweep (%s)", did)
	}
}

func TestDNSRecords(t *testing.T) {
	cf := cftest.New("shiptiffin.app", "example.com")
	d := DNS{P: &cloudflare.Provider{APIToken: cftest.Token, HTTPClient: cf.Client()}, Zone: "shiptiffin.app"}
	ctx := context.Background()
	// Someone else's records that end the same way must survive.
	cf.Add("shiptiffin.app", "A", "myshop.shiptiffin.app", "192.0.2.9")
	cf.Add("shiptiffin.app", "TXT", "shop.shiptiffin.app", "keep me")
	if err := d.Set(ctx, "shop", "203.0.113.5", ""); err != nil {
		t.Fatal(err)
	}
	if got := cf.Records("shop.shiptiffin.app"); !slices.Equal(got, []string{"A 203.0.113.5", "TXT keep me"}) {
		t.Fatalf("records: %v", got)
	}
	// Moving to IPv4 + IPv6 replaces, never duplicates.
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
	// A wrong token gets nothing done.
	bad := DNS{P: &cloudflare.Provider{APIToken: "nope", HTTPClient: cf.Client()}, Zone: "shiptiffin.app"}
	if err := bad.Set(ctx, "cafe", "203.0.113.5", ""); err == nil {
		t.Fatal("a bad token must fail")
	}
}
