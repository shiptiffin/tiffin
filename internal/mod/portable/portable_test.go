package portable

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
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/mod/runtime"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

func openState(t *testing.T, path string) *state.DB {
	t.Helper()
	db, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func putJSON(t *testing.T, db *state.DB, ns, key string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := db.KVPut(context.Background(), ns, key, b); err != nil {
		t.Fatal(err)
	}
}

// TestFixState: the imported state keeps this box's owner token and
// backups, restarts apps fresh and marks every resource pending.
func TestFixState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// The box being imported onto ("B").
	live := openState(t, filepath.Join(dir, "live.db"))
	defer live.Close()
	liveOwner, _, err := tokens.NewManager(live).Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	putJSON(t, live, "backup.sets", "bk_B", map[string]string{"id": "bk_B"})
	_ = live.KVPut(ctx, "backup", "since", []byte("2026-10-03T00:00:00Z"))

	// The exported box ("A").
	srcPath := filepath.Join(dir, "imported.db")
	src := openState(t, srcPath)
	tm := tokens.NewManager(src)
	srcOwner, _, err := tm.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := tm.Authenticate(ctx, srcOwner)
	agent, _, err := tm.Create(ctx, owner, tokens.CreateRequest{Name: "claude-code", Kind: tokens.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	c := &change.Change{ID: "chg_1", Project: "shop", Version: 1, At: time.Now(),
		Plan: change.Plan{Project: "shop", Ops: []change.Op{{Action: change.Create, Address: "app/web", After: json.RawMessage(`{"framework":"static"}`)},
			{Action: change.Create, Address: "service/postgres", After: json.RawMessage(`{}`)}}}}
	if err := src.Commit(ctx, c); err != nil {
		t.Fatal(err)
	}
	_ = src.SetResourceStatus(ctx, "shop", "service/postgres", "ready", "")
	putJSON(t, src, "backup.sets", "bk_A", map[string]string{"id": "bk_A"})
	putJSON(t, src, nsRuntimeState, "shop/web", runtime.AppState{Project: "shop", App: "web", Live: "dep_2",
		Instances: []runtime.Instance{{Name: "tf.shop.web.prod.3", Port: 20001, Deploy: "dep_2"}}, Hash: "abc", Serial: 3})
	putJSON(t, src, nsRuntimeDeploys("shop", "web"), "dep_2", runtime.Deploy{ID: "dep_2", Status: "live", Image: "docker.io/tiffin/shop-web:dep_2"})
	putJSON(t, src, nsRuntimeDeploys("shop", "web"), "dep_1", runtime.Deploy{ID: "dep_1", Status: "superseded", Image: "docker.io/tiffin/shop-web:dep_1"})
	src.Close()

	man := &boxfile.Manifest{Images: []string{"docker.io/tiffin/shop-web:dep_2"}}
	if err := fixState(ctx, srcPath, live, man); err != nil {
		t.Fatal(err)
	}
	got := openState(t, srcPath)
	defer got.Close()
	gtm := tokens.NewManager(got)
	for name, secret := range map[string]string{"this box's owner": liveOwner, "imported owner": srcOwner, "imported agent": agent} {
		if _, err := gtm.Authenticate(ctx, secret); err != nil {
			t.Errorf("%s token: %v", name, err)
		}
	}
	sets, _ := got.KVList(ctx, "backup.sets")
	if _, ok := sets["bk_B"]; !ok || len(sets) != 1 {
		t.Fatalf("backup sets must be this box's: %v", sets)
	}
	var st map[string]any
	raw, _, _ := got.KVGet(ctx, nsRuntimeState, "shop/web")
	_ = json.Unmarshal(raw, &st)
	if st["instances"] != nil || st["hash"] != "" || st["live"] != "dep_2" || st["serial"].(float64) != 3 {
		t.Fatalf("app state: %v", st)
	}
	var d1, d2 runtime.Deploy
	raw, _, _ = got.KVGet(ctx, nsRuntimeDeploys("shop", "web"), "dep_1")
	_ = json.Unmarshal(raw, &d1)
	raw, _, _ = got.KVGet(ctx, nsRuntimeDeploys("shop", "web"), "dep_2")
	_ = json.Unmarshal(raw, &d2)
	if d1.Image != "" || d2.Image == "" {
		t.Fatalf("only exported images stay rollback targets: dep_1=%q dep_2=%q", d1.Image, d2.Image)
	}
	sts, _ := got.ResourceStatuses(ctx, "shop")
	if len(sts) != 2 || sts["service/postgres"].State != "pending" || sts["app/web"].State != "pending" {
		t.Fatalf("statuses: %+v", sts)
	}
}

func TestCheckKey(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	other, _ := age.GenerateX25519Identity()
	s := Summary{Recipient: id.Recipient().String()}
	if err := checkKey(s, ""); err == nil || !strings.Contains(err.Error(), s.Recipient) {
		t.Fatalf("missing key: %v", err)
	}
	if err := checkKey(s, "nonsense"); err == nil {
		t.Fatal("garbage key accepted")
	}
	if err := checkKey(s, other.String()); err == nil || !strings.Contains(err.Error(), "not the one") {
		t.Fatalf("wrong key: %v", err)
	}
	if err := checkKey(s, "  "+id.String()+"\n"); err != nil {
		t.Fatalf("right key: %v", err)
	}
	s.IncludesKey = true
	if err := checkKey(s, ""); err != nil {
		t.Fatalf("key inside: %v", err)
	}
}

func TestPreviewKey(t *testing.T) {
	ctx := context.Background()
	db := openState(t, filepath.Join(t.TempDir(), "s.db"))
	defer db.Close()
	p := &platform.Platform{DB: db, Log: slog.Default()}
	rec := &Import{ID: "im_1", SHA256: "abc", Source: Summary{Projects: []string{"shop"}, Databases: []string{"p_shop"}}}
	a, err := preview(ctx, p, rec, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ExistingProjects) != 0 || !strings.Contains(a.SafetyBackup, "none") {
		t.Fatalf("fresh box preview: %+v", a)
	}
	b, _ := preview(ctx, p, rec, true)
	ka, _ := json.Marshal(a.Key())
	kb, _ := json.Marshal(b.Key())
	if string(ka) == string(kb) {
		t.Fatal("replace must change the confirm value")
	}
	// A project appearing between preview and apply changes it too.
	_ = db.Commit(ctx, &change.Change{ID: "chg_1", Project: "blog", Version: 1, At: time.Now(),
		Plan: change.Plan{Project: "blog", Ops: []change.Op{{Action: change.Create, Address: "env/X", After: json.RawMessage(`"1"`)}}}})
	c, _ := preview(ctx, p, rec, true)
	kc, _ := json.Marshal(c.Key())
	if string(kb) == string(kc) || !strings.Contains(strings.Join(c.Overwrites, " "), "blog") {
		t.Fatalf("existing projects: %s / %+v", kc, c.Overwrites)
	}
}

func TestSets(t *testing.T) {
	observe, _ := setByName("observe")
	skip := observe.skip(false, nil)
	for rel, want := range map[string]bool{"observe.db": false, "retention.env": false, "metrics": true, "metrics/data/x": true, "logs": true, "bin": true} {
		if got := skip(rel, nil); got != want {
			t.Errorf("observe skip %q = %v", rel, got)
		}
	}
	if observe.skip(true, nil)("metrics", nil) {
		t.Error("with history the metrics store travels")
	}
	if k := observe.keep(false); strings.Join(k, ",") != "bin,metrics,logs" {
		t.Errorf("observe keep: %v", k)
	}
	if u := observe.units(true); len(u) != 2 {
		t.Errorf("observe units with history: %v", u)
	}
	storage, _ := setByName("storage")
	// Object keys that look like SQLite side files are objects, not side files.
	if storage.skip(false, nil)("data/shop-media/backup.db-wal", nil) {
		t.Error("an object named *-wal must travel")
	}
	analytics, _ := setByName("analytics")
	s := analytics.skip(false, map[string]string{"analytics.db": "/snap"})
	if !s("analytics.db-wal", nil) || !s("dbip-country.mmdb", nil) || s("analytics.db", nil) {
		t.Error("analytics skip")
	}
	p := &platform.Platform{Home: "/srv/box/platform", DataRoot: "/srv/box"}
	edge, _ := setByName("edge")
	if edge.Path(p) != "/srv/box/platform/edge" || storage.Path(p) != "/srv/box/storage" {
		t.Errorf("paths: %s %s", edge.Path(p), storage.Path(p))
	}
	names := map[string]bool{}
	for _, s := range sets() {
		if names[s.Name] {
			t.Errorf("duplicate set %s", s.Name)
		}
		names[s.Name] = true
		if s.History && !strings.HasPrefix(s.prefix(), "history/") || !s.History && !strings.HasPrefix(s.prefix(), "files/") {
			t.Errorf("prefix of %s: %s", s.Name, s.prefix())
		}
	}
}

func TestJobRecords(t *testing.T) {
	root := t.TempDir()
	p := &platform.Platform{DataRoot: root, Home: filepath.Join(root, "platform"), Log: slog.Default()}
	m := &Module{}
	for _, id := range []string{"ex_01A", "ex_01C", "ex_01B"} {
		if err := m.save(p, "exports", id, &Export{ID: id, Status: ExportDone}); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(archivePath(p, "exports", "ex_01C"), []byte("x"), 0o600) // archives are not records
	l, err := list[Export](p, "exports")
	if err != nil || len(l) != 3 || l[0].ID != "ex_01C" || l[2].ID != "ex_01A" {
		t.Fatalf("newest first: %v %v", l, err)
	}
	if _, err := load[Export](p, "exports", "../../etc/passwd"); err == nil {
		t.Fatal("path escape")
	}
	if l, _ := list[Import](p, "imports"); len(l) != 0 {
		t.Fatal("no imports yet")
	}
}

func TestReceiveRefusesBadArchives(t *testing.T) {
	root := t.TempDir()
	db := openState(t, filepath.Join(root, "s.db"))
	defer db.Close()
	p := &platform.Platform{DB: db, DataRoot: root, Home: filepath.Join(root, "platform"), Log: slog.Default()}
	m := &Module{}
	if _, err := m.receive(context.Background(), p, strings.NewReader("not an archive"), 14, "tok_1", "dev"); err == nil || !strings.Contains(err.Error(), "invalid archive") {
		t.Fatalf("garbage: %v", err)
	}
	// A newer format is refused, and nothing is kept.
	var buf strings.Builder
	w, _ := boxfile.NewWriter(&buf, &boxfile.Manifest{Kind: boxfile.ManifestKind, Format: boxfile.FormatVersion + 1})
	_, _, _, _ = w.Close()
	if _, err := m.receive(context.Background(), p, strings.NewReader(buf.String()), int64(buf.Len()), "tok_1", "dev"); err == nil || !strings.Contains(err.Error(), "newer Tiffin") {
		t.Fatalf("newer: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Join(root, "portable", "imports"))
	if len(ents) != 0 {
		t.Fatalf("refused uploads must leave nothing: %v", ents)
	}
	// A good one is kept and summarized.
	buf.Reset()
	w, _ = boxfile.NewWriter(&buf, &boxfile.Manifest{Kind: boxfile.ManifestKind, Format: boxfile.FormatVersion, Projects: []string{"shop"}, Recipient: "age1x"})
	w.Part("platform", boxfile.Stats{Files: 1, Bytes: 10})
	_, sum, _, _ := w.Close()
	rec, err := m.receive(context.Background(), p, strings.NewReader(buf.String()), int64(buf.Len()), "tok_1", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.SHA256 != sum || rec.SizeBytes != int64(buf.Len()) || rec.Source.Projects[0] != "shop" || rec.Parts["platform"].Bytes != 10 {
		t.Fatalf("record: %+v", rec)
	}
	if _, err := os.Stat(archivePath(p, "imports", rec.ID)); err != nil {
		t.Fatal("archive not kept")
	}
}
