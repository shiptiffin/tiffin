package managed

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/sealbox"
	"github.com/shiptiffin/tiffin/internal/state"
)

// The box's off-site key is made once and kept sealed; a grant sealed to
// it in a check-in's answer is opened, checked and handed to the backup
// module once; one for another box, or an inactive subscription, is not.
func TestOffsiteGrant(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sec, err := platform.OpenSecrets(db, home)
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Secrets: sec, Log: slog.New(slog.DiscardHandler)}
	key, err := offsiteKey(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := offsiteKey(ctx, p)
	if !key.Equal(again) {
		t.Fatal("the key changed between starts")
	}
	raw, _, _ := db.KVGet(ctx, nsManaged, offsiteKeyKey)
	if strings.Contains(string(raw), string(key.Bytes())) {
		t.Fatal("the key is stored in the clear")
	}

	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	grant := platform.OffsiteGrant{Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "customer-backups", Prefix: "box_1",
		AccessKeyID: "tmp", SecretAccessKey: "tmp-secret", SessionToken: "tok", ExpiresAt: exp, RetentionDays: 30}
	seal := func(g platform.OffsiteGrant, box string) string {
		plain, _ := json.Marshal(g)
		s, err := sealbox.Seal(key.PublicKey(), plain, platform.OffsiteGrantAAD(box))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	var answer atomic.Value
	var sent atomic.Value
	answer.Store(`{"managed":true,"active":true,"updates":true,"offsite":{"sealed":"` + seal(grant, "box_1") + `","expiresAt":"` + exp.Format(time.RFC3339) + `"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent.Store(string(b))
		io.WriteString(w, answer.Load().(string))
	}))
	defer srv.Close()
	cfg := testConfig(t, srv.URL)

	var got []*platform.OffsiteGrant
	platform.HandleOffsiteGrants(func(_ context.Context, g *platform.OffsiteGrant) error { got = append(got, g); return nil })
	t.Cleanup(func() { platform.HandleOffsiteGrants(nil) })
	m := &Module{key: key}
	r := Report{BoxID: "box_1", BackupKey: sealbox.KeyText(key.PublicKey().Bytes())}
	st := Send(ctx, srv.Client(), cfg, r, platform.ManagedState{}, time.Now())
	if !strings.Contains(sent.Load().(string), `"backupKey":"`+r.BackupKey+`"`) {
		t.Fatalf("the report lacks the key: %s", sent.Load())
	}
	if st.OffsiteSealed == "" || !st.OffsiteExpiresAt.Equal(exp) {
		t.Fatalf("state: %+v", st)
	}
	m.giveGrant(ctx, p, cfg, st)
	m.giveGrant(ctx, p, cfg, st)
	if len(got) != 1 || got[0].SessionToken != "tok" || got[0].Prefix != "box_1" {
		t.Fatalf("handed over: %+v", got)
	}

	// Sealed for another box, or for this box's folder but naming another: refused.
	for name, sealed := range map[string]string{
		"another box's seal":   seal(grant, "box_2"),
		"another box's folder": seal(func() platform.OffsiteGrant { g := grant; g.Prefix = "box_2"; return g }(), "box_1"),
		"expired":              seal(func() platform.OffsiteGrant { g := grant; g.ExpiresAt = time.Now().Add(-time.Minute); return g }(), "box_1"),
	} {
		if _, err := OpenGrant(key, "box_1", sealed, time.Now()); err == nil {
			t.Errorf("%s: opened", name)
		}
	}

	// The subscription stopped: the answer carries none, and the state forgets it.
	answer.Store(`{"managed":true,"active":false,"updates":false}`)
	st = Send(ctx, srv.Client(), cfg, r, st, time.Now())
	if st.OffsiteSealed != "" {
		t.Fatalf("an inactive box kept its grant: %+v", st)
	}
	// Unreachable: the last answer's grant stands.
	answer.Store(`{"managed":true,"active":true,"updates":true,"offsite":{"sealed":"` + seal(grant, "box_1") + `","expiresAt":"` + exp.Format(time.RFC3339) + `"}}`)
	st = Send(ctx, srv.Client(), cfg, r, st, time.Now())
	srv.Close()
	if down := Send(ctx, &http.Client{Timeout: time.Second}, cfg, r, st, time.Now()); down.OffsiteSealed != st.OffsiteSealed {
		t.Fatal("an unanswered check-in dropped the grant")
	}
}
