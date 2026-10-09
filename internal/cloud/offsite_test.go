package cloud

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/cloud/cftest"
	"github.com/btahir/tiffin/internal/objstore"
	"github.com/btahir/tiffin/internal/objstore/objstoretest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/btahir/tiffin/internal/sealbox"
)

// withOffsite gives the harness's worker the customer backup bucket: a fake
// R2 bucket that accepts what the fake Cloudflare API mints, for the
// prefixes and lifetime asked for.
func withOffsite(h *harness) *objstoretest.Fake {
	bucket := objstoretest.New("shiptiffin-customer-backups")
	h.t.Cleanup(bucket.Close)
	h.cf.OnMint = func(m cftest.Mint, access, secret, token string) {
		bucket.Allow(objstoretest.Cred{AccessKeyID: access, SecretAccessKey: secret, SessionToken: token,
			Expires: time.Now().Add(time.Duration(m.TTLSeconds) * time.Second), Prefixes: m.Prefixes, ReadOnly: m.Permission == "object-read-only"})
	}
	h.w.Offsite = &Offsite{R2: &R2{AccountID: "acct123", Token: cftest.Token, ParentAccessKeyID: "parent-key-id", Bucket: "shiptiffin-customer-backups",
		Endpoint: bucket.URL, Client: h.cf.Client(), CACert: bucket.CAPEM()}}
	return bucket
}

// activeBox adds a running, paid box with its own off-site key.
func (h *harness) activeBox(id string) *ecdh.PrivateKey {
	h.t.Helper()
	k, _ := ecdh.X25519().GenerateKey(rand.Reader)
	h.exec(`insert into cloud_boxes (id, user_id, email, name, status, plan_status, first_paid_at, backup_key) values ($1, 'u1', 'sam@example.com', $1, 'active', 'active', now(), $2)`,
		id, sealbox.KeyText(k.PublicKey().Bytes()))
	return k
}

// openGrant opens the credentials stored for a box with the box's key.
func (h *harness) openGrant(id string, k *ecdh.PrivateKey) *platform.OffsiteGrant {
	h.t.Helper()
	sealed := h.str(`select offsite_sealed from cloud_boxes where id = $1`, id)
	if sealed == "" {
		h.t.Fatalf("%s has no off-site credentials", id)
	}
	plain, err := sealbox.Open(k, sealed, platform.OffsiteGrantAAD(id))
	if err != nil {
		h.t.Fatal(err)
	}
	var g platform.OffsiteGrant
	if err := json.Unmarshal(plain, &g); err != nil {
		h.t.Fatal(err)
	}
	return &g
}

func grantClient(t *testing.T, g *platform.OffsiteGrant, ca string) *objstore.Client {
	t.Helper()
	cl, err := objstore.New(objstore.Config{Endpoint: g.Endpoint, Region: "auto", Bucket: g.Bucket, AccessKeyID: g.AccessKeyID,
		SecretAccessKey: g.SecretAccessKey, SessionToken: g.SessionToken, CACert: ca})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

// Credentials go to active, paid boxes with a key only: minted for the
// box's folder alone, sealed to the box, renewed before they expire, and
// dropped when the box may no longer have them.
func TestOffsiteCredentials(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	bucket := withOffsite(h)
	keyA := h.activeBox("box_a")
	h.activeBox("box_unpaid")
	h.exec(`update cloud_boxes set plan_status = 'unpaid', extras_paused_at = now() where id = 'box_unpaid'`)
	h.activeBox("box_gone")
	h.exec(`update cloud_boxes set status = 'released' where id = 'box_gone'`)
	h.exec(`insert into cloud_boxes (id, user_id, email, status, plan_status, first_paid_at) values ('box_nokey', 'u1', 'sam@example.com', 'active', 'active', now())`)

	if n := h.w.RefreshOffsite(ctx); n != 1 {
		t.Fatalf("minted %d, want 1 (box_a only)", n)
	}
	mints := h.cf.Mints()
	if len(mints) != 1 {
		t.Fatalf("mints: %+v", mints)
	}
	m := mints[0]
	if m.Account != "acct123" || m.Bucket != "shiptiffin-customer-backups" || m.ParentAccessKeyID != "parent-key-id" || m.Permission != "object-read-write" ||
		m.TTLSeconds != 48*3600 || len(m.Prefixes) != 1 || m.Prefixes[0] != "box_a/" || len(m.Objects) != 0 {
		t.Fatalf("mint request: %+v", m)
	}
	for _, id := range []string{"box_unpaid", "box_gone", "box_nokey"} {
		if h.str(`select offsite_sealed from cloud_boxes where id = $1`, id) != "" {
			t.Errorf("%s got credentials", id)
		}
	}
	raw := h.str(`select offsite_sealed from cloud_boxes where id = 'box_a'`)
	if strings.Contains(raw, "tmpsecret") || strings.Contains(raw, "tmptoken") {
		t.Fatal("credentials stored in the clear")
	}
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	if _, err := sealbox.Open(other, raw, platform.OffsiteGrantAAD("box_a")); err == nil {
		t.Fatal("another key opens the credentials")
	}
	if _, err := sealbox.Open(keyA, raw, platform.OffsiteGrantAAD("box_b")); err == nil {
		t.Fatal("the credentials open as another box's")
	}
	g := h.openGrant("box_a", keyA)
	if g.Endpoint != bucket.URL || g.Bucket != "shiptiffin-customer-backups" || g.Prefix != "box_a" || g.RetentionDays != 30 ||
		g.ExpiresAt.Before(time.Now().Add(47*time.Hour)) || g.ExpiresAt.After(time.Now().Add(48*time.Hour)) {
		t.Fatalf("grant: %+v", g)
	}
	if err := g.Validate("box_a", time.Now()); err != nil {
		t.Fatal(err)
	}

	// The box reaches its own folder with them, and nothing else.
	cl := grantClient(t, g, bucket.CAPEM())
	if err := cl.Put(ctx, "box_a/tiffin/key", []byte("k")); err != nil {
		t.Fatalf("own folder: %v", err)
	}
	for _, key := range []string{"box_b/tiffin/key", "box_a", "box_ab/x"} {
		if err := cl.Put(ctx, key, []byte("x")); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
			t.Errorf("%s: %v", key, err)
		}
	}

	// Not due yet: nothing minted. Due (less than 36 hours left): renewed.
	if n := h.w.RefreshOffsite(ctx); n != 0 {
		t.Fatalf("minted %d with 48 hours left", n)
	}
	h.exec(`update cloud_boxes set offsite_expires_at = now() + interval '30 hours' where id = 'box_a'`)
	if n := h.w.RefreshOffsite(ctx); n != 1 {
		t.Fatalf("renewal: minted %d", n)
	}
	if g2 := h.openGrant("box_a", keyA); g2.AccessKeyID == g.AccessKeyID {
		t.Fatal("renewal kept the old credentials")
	}

	// Credentials sealed to a key the box no longer has are not stored.
	if ok, err := h.store.SetOffsite(ctx, "box_a", "an-old-key", "v2.x.y", time.Now().Add(time.Hour)); err != nil || ok {
		t.Fatalf("stale key: %v %v", ok, err)
	}

	// Unpaid: dropped at the next round, and none minted.
	h.exec(`update cloud_boxes set plan_status = 'unpaid' where id = 'box_a'`)
	before := len(h.cf.Mints())
	h.w.RefreshOffsite(ctx)
	if h.str(`select offsite_sealed from cloud_boxes where id = 'box_a'`) != "" || len(h.cf.Mints()) != before {
		t.Fatal("an unpaid box kept or got credentials")
	}
	// Paid again: they come back.
	h.exec(`update cloud_boxes set plan_status = 'active' where id = 'box_a'`)
	if n := h.w.RefreshOffsite(ctx); n != 1 {
		t.Fatalf("paid again: minted %d", n)
	}

	// Cloudflare refuses: the error says so, without the token.
	h.cf.SetFault(func(method, path string) bool { return strings.Contains(path, "temp-access-credentials") })
	h.exec(`update cloud_boxes set offsite_expires_at = now() where id = 'box_a'`)
	if n := h.w.RefreshOffsite(ctx); n != 0 {
		t.Fatal("minted while Cloudflare fails")
	}
	_, err := h.w.Offsite.R2.Mint(ctx, "box_a/", "object-read-write", time.Hour)
	if err == nil || strings.Contains(err.Error(), cftest.Token) {
		t.Fatalf("error: %v", err)
	}
	h.cf.SetFault(nil)
	for name, try := range map[string]func() error{
		"no folder":  func() error { _, err := h.w.Offsite.R2.Mint(ctx, "", "object-read-write", time.Hour); return err },
		"the bucket": func() error { _, err := h.w.Offsite.R2.Mint(ctx, "/", "object-read-write", time.Hour); return err },
		"not a box": func() error {
			_, err := h.w.Offsite.R2.Mint(ctx, "shiptiffin-box/", "object-read-write", time.Hour)
			return err
		},
		"over 7 days": func() error {
			_, err := h.w.Offsite.R2.Mint(ctx, "box_a/", "object-read-write", 8*24*time.Hour)
			return err
		},
		"bad perm":       func() error { _, err := h.w.Offsite.R2.Mint(ctx, "box_a/", "everything", time.Hour); return err },
		"empty a bucket": func() error { _, err := h.w.Offsite.R2.EmptyFolder(ctx, ""); return err },
	} {
		if try() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A deleted box's folder is emptied seven days after, and only its folder.
func TestOffsitePurgeAfterDelete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	bucket := withOffsite(h)
	h.provisioned("box_p", "gone", ProvisionArgs{})
	k, _ := ecdh.X25519().GenerateKey(rand.Reader)
	h.exec(`update cloud_boxes set backup_key = $1 where id = 'box_p'`, sealbox.KeyText(k.PublicKey().Bytes()))
	h.w.RefreshOffsite(ctx)
	_ = h.openGrant("box_p", k)
	for _, key := range []string{"box_p/tiffin/key", "box_p/pgbackrest/archive/x", "box_pq/tiffin/key", "box_q/tiffin/key"} {
		bucket.Put(key, []byte("x"))
	}

	h.exec(`update cloud_boxes set status = 'deleting' where id = 'box_p'`)
	id := h.enqueue("box_p", "delete_server", hetznertest.Token, DeleteArgs{})
	h.runNext()
	if j := h.job(id); j.Status != "done" {
		t.Fatalf("delete: %s", j.text())
	}
	if h.str(`select offsite_sealed from cloud_boxes where id = 'box_p'`) != "" {
		t.Fatal("a deleted box kept its credentials")
	}
	if n := h.num(`select count(*) from cloud_boxes where id = 'box_p' and offsite_purge_after between now() + interval '6 days 23 hours' and now() + interval '7 days 1 hour'`); n != 1 {
		t.Fatalf("purge not set for 7 days on: %s", h.str(`select offsite_purge_after::text from cloud_boxes where id = 'box_p'`))
	}
	h.w.PurgeOffsite(ctx)
	if len(bucket.Keys("box_p/")) != 2 {
		t.Fatal("emptied before the 7 days")
	}
	h.w.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	h.w.PurgeOffsite(ctx)
	if left := bucket.Keys(""); strings.Join(left, ",") != "box_pq/tiffin/key,box_q/tiffin/key" {
		t.Fatalf("left: %v", left)
	}
	if h.str(`select offsite_purged_at::text from cloud_boxes where id = 'box_p'`) == "" {
		t.Fatal("not recorded")
	}
	last := h.cf.Mints()[len(h.cf.Mints())-1]
	if last.Prefixes[0] != "box_p/" || last.TTLSeconds != 3600 {
		t.Fatalf("the purge's credentials: %+v", last)
	}
	n := len(h.cf.Mints())
	h.w.PurgeOffsite(ctx)
	if len(h.cf.Mints()) != n {
		t.Fatal("emptied again")
	}
}

// Without CLOUD_R2_* settings nothing happens.
func TestOffsiteOff(t *testing.T) {
	h := newHarness(t)
	h.activeBox("box_a")
	if n := h.w.RefreshOffsite(context.Background()); n != 0 || len(h.cf.Mints()) != 0 {
		t.Fatal("minted without settings")
	}
	h.w.PurgeOffsite(context.Background())
}
