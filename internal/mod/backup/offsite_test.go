package backup

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

func TestNormalizeOffsite(t *testing.T) {
	in := OffsiteInput{Endpoint: "acct.r2.cloudflarestorage.com", Bucket: "backups", AccessKeyID: "AK"}
	c, err := normalize(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "https://acct.r2.cloudflarestorage.com" || c.Region != "us-east-1" || c.Prefix != "tiffin" || c.URIStyle != "path" || c.RetentionDays != 30 {
		t.Fatalf("defaults: %+v", c)
	}
	for _, bad := range []OffsiteInput{
		{Endpoint: "http://minio.local:9000", Bucket: "b", AccessKeyID: "k"},
		{Endpoint: "https://s3.example.com/bucket", Bucket: "b", AccessKeyID: "k"},
		{Endpoint: "https://s3.example.com", AccessKeyID: "k"},
		{Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "k", Prefix: "../up"},
		{Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "k", Prefix: "a b"},
		{Endpoint: "", Bucket: "b", AccessKeyID: "k"},
	} {
		if _, err := normalize(bad, nil); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	c, _ = normalize(OffsiteInput{Endpoint: "https://fsn1.your-objectstorage.com:8443/", Bucket: "b", AccessKeyID: "k", Prefix: "/boxes/shop/", Region: "fsn1"}, &OffsiteConfig{RetentionDays: 90})
	if c.Endpoint != "https://fsn1.your-objectstorage.com:8443" || c.Prefix != "boxes/shop" || c.RetentionDays != 90 || c.Region != "fsn1" {
		t.Fatalf("normalized: %+v", c)
	}
}

func TestRepo2Settings(t *testing.T) {
	c := &OffsiteConfig{Endpoint: "https://host.lima.internal:9443", Region: "us-east-1", Bucket: "offsite", Prefix: "boxes/a",
		AccessKeyID: "AKID", URIStyle: "path", CACert: "-----BEGIN CERTIFICATE-----", RetentionDays: 30}
	s := &offsiteSecrets{SecretAccessKey: "s3cret", Passphrase: "pass-phrase"}
	env := strings.Join(repo2Env(c, s), "\n")
	for _, want := range []string{"PGBACKREST_REPO2_TYPE=s3", "PGBACKREST_REPO2_PATH=/boxes/a/pgbackrest", "PGBACKREST_REPO2_S3_ENDPOINT=host.lima.internal",
		"PGBACKREST_REPO2_STORAGE_PORT=9443", "PGBACKREST_REPO2_S3_KEY_SECRET=s3cret", "PGBACKREST_REPO2_CIPHER_TYPE=aes-256-cbc",
		"PGBACKREST_REPO2_CIPHER_PASS=pass-phrase", "PGBACKREST_REPO2_RETENTION_FULL_TYPE=time", "PGBACKREST_REPO2_RETENTION_FULL=30",
		"PGBACKREST_REPO2_STORAGE_CA_FILE=" + offsiteCAPath, "PGBACKREST_REPO2_S3_URI_STYLE=path"} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	conf := repo2Conf(c, s, 1<<30)
	for _, want := range []string{"[global]\n", "repo2-type=s3\n", "repo2-s3-key-secret=s3cret\n", "repo2-storage-port=9443\n", "repo2-cipher-pass=pass-phrase\n",
		"archive-async=y\n", "spool-path=/var/spool/pgbackrest\n", "archive-push-queue-max=1073741824\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf lacks %q:\n%s", want, conf)
		}
	}
	c.Endpoint, c.CACert = "https://acct.r2.cloudflarestorage.com", ""
	env = strings.Join(repo2Env(c, s), "\n")
	if strings.Contains(env, "STORAGE_PORT") || strings.Contains(env, "CA_FILE") {
		t.Fatalf("default port and CA must not be set:\n%s", env)
	}
	if repo2Env(nil, nil) != nil {
		t.Fatal("no destination, no env")
	}
	if !stanzaMismatch(errString("ERROR: [028]: backup and archive info files exist but do not match the database")) || stanzaMismatch(errString("ERROR: [039]: HTTP request failed")) {
		t.Fatal("stanzaMismatch")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestNextDrillSource(t *testing.T) {
	passed := func(src string) *BackupDrill { return &BackupDrill{Status: DrillPassed, Source: src} }
	failed := func(src string) *BackupDrill { return &BackupDrill{Status: DrillFailed, Source: src} }
	cases := []struct {
		last    *BackupDrill
		offsite bool
		want    string
	}{
		{nil, true, SourceLocal},
		{passed(SourceLocal), false, SourceLocal},
		{passed(SourceLocal), true, SourceOffsite},
		{passed(""), true, SourceOffsite}, // drills from before off-box copies
		{passed(SourceOffsite), true, SourceLocal},
		{failed(SourceOffsite), true, SourceOffsite},
		{failed(SourceLocal), true, SourceLocal},
		{failed(SourceOffsite), false, SourceLocal},
	}
	for i, c := range cases {
		if got := nextDrillSource(c.last, c.offsite); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

func sealedPlatform(t *testing.T) *platform.Platform {
	t.Helper()
	home := t.TempDir()
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec, err := platform.OpenSecrets(db, home)
	if err != nil {
		t.Fatal(err)
	}
	return &platform.Platform{DB: db, Secrets: sec, Home: home, Log: slog.New(slog.DiscardHandler)}
}

func TestOffsiteStatusAndCheck(t *testing.T) {
	ctx := context.Background()
	p := sealedPlatform(t)
	t.Cleanup(func() { remember(nil, nil) })
	remember(nil, nil)
	now := time.Now()
	if c := offsiteCheck(ctx, p, now); !c.OK || !strings.Contains(c.Detail, "only on this server") {
		t.Fatalf("off: %+v", c)
	}
	if v := offsiteView(ctx, p); v.Enabled || v.State != OffsiteOff {
		t.Fatalf("view off: %+v", v)
	}
	if _, on, _ := OffsiteAge(ctx, p); on {
		t.Fatal("age while off")
	}

	c := &OffsiteConfig{Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "bk", Prefix: "tiffin", AccessKeyID: "AK", URIStyle: "path",
		RetentionDays: 30, State: OffsiteActive, SetAt: now.Add(-time.Hour)}
	s := &offsiteSecrets{SecretAccessKey: "never-shown", Passphrase: "never-shown-either"}
	if err := saveOffsite(ctx, p, c, s); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := p.DB.KVGet(ctx, nsOffsite, "config")
	if strings.Contains(string(raw), "never-shown") {
		t.Fatal("secrets stored in the clear")
	}
	if c2, s2, err := loadOffsite(ctx, p); err != nil || c2.Bucket != "bk" || s2.Passphrase != "never-shown-either" {
		t.Fatalf("load: %v", err)
	}
	if ch := offsiteCheck(ctx, p, now); !ch.OK || !strings.Contains(ch.Detail, "first copy runs") {
		t.Fatalf("set, no copy yet: %+v", ch)
	}
	if ch := offsiteCheck(ctx, p, now.Add(30*time.Hour)); ch.OK {
		t.Fatalf("no copy 30 h after setting it: %+v", ch)
	}
	ok := &BackupOffsiteCopy{Backup: "bk_1", Status: "ok", StartedAt: now.Add(-3 * time.Hour), FinishedAt: now.Add(-2 * time.Hour), SentBytes: 5 << 20}
	putStatus(ctx, p, offsiteStatus{Last: ok, LastOK: ok})
	if ch := offsiteCheck(ctx, p, now); !ch.OK || !strings.Contains(ch.Detail, "2 hours ago (5.0 MB sent)") {
		t.Fatalf("copied: %+v", ch)
	}
	bad := &BackupOffsiteCopy{Backup: "bk_2", Status: "failed", Error: "postgres: HTTP 403", StartedAt: now.Add(-time.Hour), FinishedAt: now}
	putStatus(ctx, p, offsiteStatus{Last: bad, LastOK: ok})
	if ch := offsiteCheck(ctx, p, now); !ch.OK || !strings.Contains(ch.Detail, "latest copy failed: postgres: HTTP 403") {
		t.Fatalf("failing but recent: %+v", ch)
	}
	if ch := offsiteCheck(ctx, p, now.Add(25*time.Hour)); ch.OK || !strings.Contains(ch.Detail, "more than 26 hours") {
		t.Fatalf("stale: %+v", ch)
	}
	if h, on, sum := OffsiteAge(ctx, p); !on || h < 1.9 || h > 2.1 || sum == "" {
		t.Fatalf("age: %v %v %q", h, on, sum)
	}
	v := offsiteView(ctx, p)
	body, _ := json.Marshal(v)
	if strings.Contains(string(body), "never-shown") || !v.Enabled || v.LastOKAt == nil || v.LastCopy.Status != "failed" {
		t.Fatalf("view: %s", body)
	}
	c.State = OffsiteForeign
	remember(c, s)
	if ch := offsiteCheck(ctx, p, now); ch.OK || !strings.Contains(ch.Detail, "another Postgres cluster") {
		t.Fatalf("foreign: %+v", ch)
	}
}

func TestUncopyable(t *testing.T) {
	ctx := context.Background()
	p := sealedPlatform(t)
	now := time.Now()
	set := func(trigger string, age time.Duration) *Backup {
		return &Backup{ID: "bk_x", Trigger: trigger, Status: "ok", StartedAt: now.Add(-age)}
	}
	if why := uncopyable(ctx, p, set("schedule", time.Hour), now); why != "" {
		t.Fatalf("a fresh set: %s", why)
	}
	if why := uncopyable(ctx, p, set("schedule", 7*time.Hour), now); !strings.Contains(why, "only sets of the last 6 hours") {
		t.Fatalf("an old set: %q", why)
	}
	if why := uncopyable(ctx, p, set("pre-restore", time.Minute), now); !strings.Contains(why, "safety backup") {
		t.Fatalf("a safety set: %q", why)
	}
	_ = p.DB.KVPut(ctx, nsOffsite, "notBefore", []byte(now.Add(-30*time.Minute).UTC().Format(time.RFC3339Nano)))
	if why := uncopyable(ctx, p, set("schedule", time.Hour), now); !strings.Contains(why, "before the last restore") {
		t.Fatalf("a set from before a restore: %q", why)
	}
	if why := uncopyable(ctx, p, set("manual", 10*time.Minute), now); why != "" {
		t.Fatalf("a set after the restore: %s", why)
	}
}

func TestAdoptState(t *testing.T) {
	ctx := context.Background()
	p := sealedPlatform(t) // the live box
	t.Cleanup(func() { remember(nil, nil) })
	live := p.DB
	exec := func(db *state.DB, q string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	exec(live, `INSERT INTO tokens(id, name, kind, hash, scopes, projects, created_at) VALUES ('tok_live', 'owner', 'owner', X'01', '[]', '[]', ?)`, at)
	_ = live.KVPut(ctx, nsSets, "bk_live", []byte(`{"id":"bk_live"}`))
	_ = live.KVPut(ctx, "box", "domain", []byte(`{"domain":"new.example"}`))
	c := &OffsiteConfig{Endpoint: "https://e", Bucket: "b", Prefix: "tiffin", AccessKeyID: "AK", State: OffsiteForeign, SetAt: time.Now()}
	if err := saveOffsite(ctx, p, c, &offsiteSecrets{SecretAccessKey: "sk", Passphrase: "pp"}); err != nil {
		t.Fatal(err)
	}

	// The backup's state, from another box with its own key.
	dir := t.TempDir()
	in, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	exec(in, `INSERT INTO tokens(id, name, kind, hash, scopes, projects, created_at) VALUES ('tok_old', 'agent', 'agent', X'02', '[]', '[]', ?)`, at)
	_ = in.KVPut(ctx, nsSets, "bk_old", []byte(`{"id":"bk_old"}`))
	_ = in.KVPut(ctx, "box", "domain", []byte(`{"domain":"old.example"}`))
	_ = in.KVPut(ctx, "postgres.password", "shop", []byte("sealed-old"))
	_ = in.KVPut(ctx, "runtime/state", "shop/web", []byte(`{"instances":[{"id":"c1"}],"hash":"abc","live":"dep_1"}`))
	_ = in.SetResourceStatus(ctx, "shop", "service/postgres", "ready", "ok")
	_ = in.SetResourceStatus(ctx, "shop", "secret/API_KEY", "ready", "")
	in.Close()
	id, _ := age.GenerateX25519Identity()
	keyPath := filepath.Join(dir, "secrets.key")
	_ = os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600)

	if err := adoptState(ctx, p, filepath.Join(dir, "state.db"), keyPath); err != nil {
		t.Fatal(err)
	}
	got, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	var n int
	_ = got.SQL().QueryRowContext(ctx, `SELECT count(*) FROM tokens WHERE id IN ('tok_live', 'tok_old')`).Scan(&n)
	if n != 2 {
		t.Fatalf("tokens: %d of 2", n)
	}
	if sets, _ := got.KVList(ctx, nsSets); len(sets) != 1 || sets["bk_live"] == nil {
		t.Fatalf("this box's backups must stay: %v", sets)
	}
	if d, _, _ := got.KVGet(ctx, "box", "domain"); !strings.Contains(string(d), "new.example") {
		t.Fatalf("domain: %s", d)
	}
	if pw, _, _ := got.KVGet(ctx, "postgres.password", "shop"); string(pw) != "sealed-old" {
		t.Fatalf("the backup's own data must come along: %s", pw)
	}
	rt, _, _ := got.KVGet(ctx, "runtime/state", "shop/web")
	if strings.Contains(string(rt), "c1") || strings.Contains(string(rt), "abc") || !strings.Contains(string(rt), "dep_1") {
		t.Fatalf("runtime state: %s", rt)
	}
	st, _ := got.ResourceStatuses(ctx, "shop")
	if st["service/postgres"].State != platform.StatePending || st["secret/API_KEY"].State != "" {
		t.Fatalf("statuses: %+v", st)
	}
	// The destination's secrets now open with the incoming key.
	raw, _, _ := got.KVGet(ctx, nsOffsite, "config")
	var gc OffsiteConfig
	_ = json.Unmarshal(raw, &gc)
	r, err := age.Decrypt(strings.NewReader(string(gc.Sealed)), id)
	if err != nil {
		t.Fatalf("resealed config: %v", err)
	}
	var sec offsiteSecrets
	_ = json.NewDecoder(r).Decode(&sec)
	if sec.SecretAccessKey != "sk" || sec.Passphrase != "pp" || gc.Bucket != "b" {
		t.Fatalf("resealed: %+v %+v", gc, sec)
	}
}
