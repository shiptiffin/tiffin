package managed

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/licence"
	"github.com/btahir/tiffin/internal/platform"
)

func testConfig(t *testing.T, url string) *platform.ManagedConfig {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	tok, err := licence.Sign(key, licence.Licence{BoxID: "box_1", Name: "shop", Domain: "shop.shiptiffin.app"})
	if err != nil {
		t.Fatal(err)
	}
	return &platform.ManagedConfig{ControlPlane: url, BoxID: "box_1", Licence: tok, PublicKey: licence.PublicKeyText(key.Public().(ed25519.PublicKey))}
}

func TestCheckInSendsOnlyVersionAndHealth(t *testing.T) {
	var got map[string]any
	var auth string
	answer := `{"managed":true,"active":true,"updates":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/box/heartbeat" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(raw, &got)
		io.WriteString(w, answer)
	}))
	defer srv.Close()
	cfg := testConfig(t, srv.URL)
	if err := checkLicence(cfg); err != nil {
		t.Fatal(err)
	}
	r := report("box_1", "1.4.0", 90*time.Minute, []platform.Check{{Name: "disk", OK: true}, {Name: "postgres", OK: false, Detail: "secret project details"}})
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	st := Send(context.Background(), srv.Client(), cfg, r, platform.ManagedState{}, now)
	if !st.Answered || !st.Active || !st.Updates || st.Error != "" || !st.LastAnswerAt.Equal(now) {
		t.Fatalf("state: %+v", st)
	}
	if auth != "Bearer "+cfg.Licence {
		t.Fatalf("authorization: %q", auth)
	}
	keys := []string{}
	for k := range got {
		keys = append(keys, k)
	}
	for _, k := range keys {
		switch k {
		case "boxID", "version", "uptimeSeconds", "checks", "failedChecks", "failing":
		default:
			t.Errorf("the check-in sends %q; only version and health may go out", k)
		}
	}
	if strings.Contains(strings.ToLower(toJSON(got)), "secret") || got["failedChecks"].(float64) != 1 {
		t.Fatalf("report: %v", got)
	}

	// Unpaid: updates off, said in words.
	answer = `{"managed":true,"active":false,"updates":false,"message":"Updates are paused"}`
	st = Send(context.Background(), srv.Client(), cfg, r, st, now.Add(time.Hour))
	if st.Active || st.Updates || st.Message != "Updates are paused" {
		t.Fatalf("unpaid: %+v", st)
	}
	// The control plane is down: the last answer stands.
	srv.Close()
	down := Send(context.Background(), &http.Client{Timeout: time.Second}, cfg, r, st, now.Add(2*time.Hour))
	if down.Answered || down.Error == "" || down.Updates != st.Updates || !down.LastAnswerAt.Equal(st.LastAnswerAt) {
		t.Fatalf("down: %+v", down)
	}
}

func TestReleasedBoxUpdatesAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"managed":false}`)
	}))
	defer srv.Close()
	st := Send(context.Background(), srv.Client(), testConfig(t, srv.URL), Report{}, platform.ManagedState{Updates: false}, time.Now())
	if !st.Updates || st.Active {
		t.Fatalf("a released box is its owner's: %+v", st)
	}
}

func TestLicenceMustMatchBox(t *testing.T) {
	cfg := testConfig(t, "https://shiptiffin.com")
	cfg.BoxID = "box_2"
	if err := checkLicence(cfg); err == nil {
		t.Fatal("a licence for another box must be refused")
	}
	cfg = testConfig(t, "https://shiptiffin.com")
	cfg.Licence = cfg.Licence[:len(cfg.Licence)-2] + "xx"
	if err := checkLicence(cfg); err == nil {
		t.Fatal("a changed licence must be refused")
	}
}

func TestUpdatesPausedOnlyOnAnAnswer(t *testing.T) {
	dir := t.TempDir()
	platform.ManagedConfigPath = filepath.Join(dir, "managed.json")
	platform.ManagedStatePath = filepath.Join(dir, "state.json")
	if p, _ := platform.ManagedUpdatesPaused(); p {
		t.Fatal("not managed: never paused")
	}
	raw, _ := json.Marshal(testConfig(t, "https://shiptiffin.com"))
	if err := os.WriteFile(platform.ManagedConfigPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, _ := platform.ManagedUpdatesPaused(); p {
		t.Fatal("no answer yet: not paused")
	}
	_ = platform.SaveManagedState(platform.ManagedState{LastAnswerAt: time.Now(), Updates: false})
	if p, why := platform.ManagedUpdatesPaused(); !p || !strings.Contains(why, "apps keep running") {
		t.Fatalf("paused: %v %q", p, why)
	}
	_ = platform.SaveManagedState(platform.ManagedState{LastAnswerAt: time.Now(), Updates: true})
	if p, _ := platform.ManagedUpdatesPaused(); p {
		t.Fatal("paid again: not paused")
	}
}

func TestResized(t *testing.T) {
	for _, c := range []struct {
		before, now int
		want        bool
	}{{0, 3800, false}, {3800, 3790, false}, {3800, 7700, true}, {7700, 3800, true}, {3800, 0, false}} {
		if got := Resized(c.before, c.now); got != c.want {
			t.Errorf("Resized(%d, %d) = %v", c.before, c.now, got)
		}
	}
}

func toJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
