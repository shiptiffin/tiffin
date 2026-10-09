package backup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/objstore/objstoretest"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// managedBucket is ShipTiffin's backup bucket (a fake that checks
// signatures, session tokens, expiry and prefixes), with pgBackRest's side
// stubbed out: the config files it would write are recorded.
func managedBucket(t *testing.T) (*objstoretest.Fake, *[]string) {
	t.Helper()
	f := objstoretest.New("customer-backups")
	t.Cleanup(f.Close)
	var confs []string
	oldW, oldA, oldCA := writeConf, activateRepo, grantCA
	writeConf = func(c *OffsiteConfig, s *offsiteSecrets) error {
		if c == nil {
			confs = append(confs, "")
			return nil
		}
		confs = append(confs, offConf(c, s))
		return nil
	}
	activateRepo = func(context.Context) (string, error) { return OffsiteActive, nil }
	grantCA = f.CAPEM()
	t.Cleanup(func() {
		writeConf, activateRepo, grantCA = oldW, oldA, oldCA
		remember(nil, nil)
		grantState.tried, grantState.triedAt, grantState.note = time.Time{}, time.Time{}, ""
	})
	remember(nil, nil)
	return f, &confs
}

// mint is what the control plane would hand box_a: credentials the bucket
// accepts for box_a/ only, until exp.
func mint(f *objstoretest.Fake, n int, exp time.Time) *platform.OffsiteGrant {
	id := "tmp-" + string(rune('a'+n))
	f.Allow(objstoretest.Cred{AccessKeyID: id, SecretAccessKey: "secret-" + id, SessionToken: "token-" + id, Expires: exp, Prefixes: []string{"box_a/"}})
	return &platform.OffsiteGrant{Endpoint: f.URL, Bucket: "customer-backups", Prefix: "box_a", AccessKeyID: id, SecretAccessKey: "secret-" + id,
		SessionToken: "token-" + id, ExpiresAt: exp, RetentionDays: 30}
}

// A managed box sets its storage up by itself from the first grant: the
// copies are encrypted with a passphrase it made, shown to the owner until
// they say it is saved; later grants renew the credentials in place; when
// they run out, copies pause and say why.
func TestManagedStorage(t *testing.T) {
	ctx := context.Background()
	p := sealedPlatform(t)
	f, confs := managedBucket(t)
	now := time.Now()

	if v := offsiteView(ctx, p); v.ManagedAvailable {
		t.Fatal("managed storage offered before any grant")
	}
	g1 := mint(f, 1, now.Add(48*time.Hour))
	if err := keepGrant(ctx, p, g1); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := p.DB.KVGet(ctx, nsOffsite, "grant")
	if strings.Contains(string(raw), "secret-tmp-b") || strings.Contains(string(raw), "token-tmp-b") {
		t.Fatal("the grant is stored in the clear")
	}
	takeGrant(ctx, p, now)
	c, s := current()
	if c == nil || !c.Managed || c.State != OffsiteActive || c.Prefix != "box_a" || !c.PassphraseUnsaved || s.SessionToken != "token-tmp-b" {
		t.Fatalf("not set up: %+v %v", c, grantNote())
	}
	if keys := f.Keys("box_a/"); len(keys) != 1 || keys[0] != "box_a/tiffin/key" {
		t.Fatalf("the bucket holds %v, want the key bundle only (the probe object deleted)", keys)
	}
	last := (*confs)[len(*confs)-1]
	for _, want := range []string{"repo2-s3-key=tmp-b\n", "repo2-s3-key-secret=secret-tmp-b\n", "repo2-s3-token=token-tmp-b\n", "repo2-path=/box_a/pgbackrest\n", "repo2-s3-region=auto\n"} {
		if !strings.Contains(last, want) {
			t.Errorf("pgBackRest config lacks %q", want)
		}
	}
	v := offsiteView(ctx, p)
	body, _ := json.Marshal(v)
	if !v.Managed || !v.PassphraseUnsaved || v.Passphrase != "" || v.CredentialsExpire == nil || strings.Contains(string(body), s.Passphrase) {
		t.Fatalf("view: %s", body)
	}
	if !strings.Contains(v.Message, "ShipTiffin's backup storage") {
		t.Fatalf("message: %s", v.Message)
	}

	// The box's copies use the session token; another box's folder is out of reach.
	vt, err := openVault(current())
	if err != nil {
		t.Fatal(err)
	}
	if err := vt.st.Put(ctx, "box_a/tiffin/x", []byte("x")); err != nil {
		t.Fatalf("writing in its own folder: %v", err)
	}
	if err := vt.st.Put(ctx, "box_b/tiffin/x", []byte("x")); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("writing in another box's folder: %v", err)
	}

	// A newer grant renews the credentials in place: same passphrase, same keys.
	pass := s.Passphrase
	g2 := mint(f, 2, now.Add(60*time.Hour))
	_ = keepGrant(ctx, p, g2)
	takeGrant(ctx, p, now)
	c2, s2 := current()
	if c2.AccessKeyID != "tmp-c" || s2.SessionToken != "token-tmp-c" || s2.Passphrase != pass || !c2.ExpiresAt.Equal(g2.ExpiresAt.UTC()) || !c2.PassphraseUnsaved {
		t.Fatalf("renewal: %+v", c2)
	}
	if !strings.Contains((*confs)[len(*confs)-1], "repo2-s3-token=token-tmp-c\n") {
		t.Fatal("pgBackRest kept the old token")
	}
	if c3, s3, _ := loadOffsite(ctx, p); c3.AccessKeyID != "tmp-c" || s3.SessionToken != "token-tmp-c" {
		t.Fatal("the renewal was not stored")
	}

	// Expired: paused, with the reason; the check fails; copies refuse.
	later := now.Add(61 * time.Hour)
	if why := expired(c2, later); why == "" {
		t.Fatal("expired credentials not noticed")
	}
	if ch := offsiteCheck(ctx, p, later); ch.OK || !strings.Contains(ch.Detail, "Paused: ShipTiffin's backup storage comes with this box's subscription") {
		t.Fatalf("expired check: %+v", ch)
	}
	f.SetNow(func() time.Time { return later })
	if err := vt.st.Put(ctx, "box_a/tiffin/y", []byte("y")); err == nil {
		t.Fatal("expired credentials still work")
	}
	f.SetNow(time.Now)
	// An expired grant is not used.
	stale := mint(f, 3, now.Add(-time.Minute))
	_ = keepGrant(ctx, p, stale)
	takeGrant(ctx, p, now)
	if c4, _ := current(); c4.AccessKeyID != "tmp-c" {
		t.Fatal("an expired grant replaced live credentials")
	}
}

// The owner's choices win: a bucket of their own stays, and copies turned
// off stay off; a folder that holds copies waits for their passphrase.
func TestManagedStorageOwnerChoices(t *testing.T) {
	ctx := context.Background()
	p := sealedPlatform(t)
	f, _ := managedBucket(t)
	now := time.Now()

	own := &OffsiteConfig{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "mine", Prefix: "tiffin", AccessKeyID: "AK", URIStyle: "path", State: OffsiteActive}
	remember(own, &offsiteSecrets{SecretAccessKey: "s"})
	_ = keepGrant(ctx, p, mint(f, 1, now.Add(48*time.Hour)))
	takeGrant(ctx, p, now)
	if c, _ := current(); c.Managed || c.Bucket != "mine" {
		t.Fatal("the grant replaced the owner's bucket")
	}

	remember(nil, nil)
	_ = p.DB.KVPut(ctx, nsOffsite, "managedOff", []byte("x"))
	takeGrant(ctx, p, now)
	if c, _ := current(); c != nil {
		t.Fatal("set up after the owner turned copies off")
	}
	if v := offsiteView(ctx, p); !v.ManagedAvailable || !strings.Contains(v.Message, "tiffin backups offsite managed") {
		t.Fatalf("off view: %+v", v)
	}
	_ = p.DB.KVDelete(ctx, nsOffsite, "managedOff")

	// The folder holds an earlier server's copies: nothing is set up, and the view says how to use them.
	f.Put("box_a/tiffin/key", []byte("an earlier server's key bundle"))
	takeGrant(ctx, p, now)
	if c, _ := current(); c != nil {
		t.Fatal("set up over existing copies")
	}
	if v := offsiteView(ctx, p); !strings.Contains(v.Message, "already holds encrypted copies") {
		t.Fatalf("view: %s", v.Message)
	}
	// Tried again with the next grant, not every five minutes.
	n := len(f.Keys(""))
	reqs := f.Seen()
	takeGrant(ctx, p, now.Add(5*time.Minute))
	if f.Seen() != reqs || len(f.Keys("")) != n {
		t.Fatal("the same grant was tried again at once")
	}
}
