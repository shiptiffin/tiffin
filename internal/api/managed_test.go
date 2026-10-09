package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// managedBox makes the box under test look ShipTiffin-managed, with
// controlPlane as its control plane ("" leaves it self-hosted).
func managedBox(t *testing.T, controlPlane string) {
	t.Helper()
	dir := t.TempDir()
	oldCfg, oldState := platform.ManagedConfigPath, platform.ManagedStatePath
	platform.ManagedConfigPath, platform.ManagedStatePath = filepath.Join(dir, "managed.json"), filepath.Join(dir, "state.json")
	t.Cleanup(func() { platform.ManagedConfigPath, platform.ManagedStatePath = oldCfg, oldState })
	if controlPlane == "" {
		return
	}
	raw, _ := json.Marshal(platform.ManagedConfig{ControlPlane: controlPlane, BoxID: "box_1", Licence: "tl1.x"})
	if err := os.WriteFile(platform.ManagedConfigPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStatusLinksAManagedBoxToItsAccount(t *testing.T) {
	e := newEnv(t)

	managedBox(t, "")
	if code, out, _ := e.call(e.owner, "GET", "/v1/status", nil); code != 200 || out["managed"] != nil {
		t.Fatalf("a self-hosted box has no account: %d %v", code, out["managed"])
	}

	managedBox(t, "https://shiptiffin.com")
	code, out, _ := e.call(e.owner, "GET", "/v1/status", nil)
	m, _ := out["managed"].(map[string]any)
	if code != 200 || m["account"] != "https://shiptiffin.com/account" || m["paused"] != nil {
		t.Fatalf("managed: %d %v", code, out["managed"])
	}
	if _, out, _ := e.call(e.key("all", "full"), "GET", "/v1/status", nil); out["managed"] == nil {
		t.Fatal("a box admin key sees the account link")
	}
	if code, out, _ := e.call(e.key("all", "read"), "GET", "/v1/status", nil); code != 200 || out["managed"] != nil {
		t.Fatalf("billing is for box admins only: %d %v", code, out["managed"])
	}

	// The control plane said the subscription lapsed: the reason comes along.
	_ = platform.SaveManagedState(platform.ManagedState{LastAnswerAt: time.Now(), Answered: true, Updates: false})
	_, out, _ = e.call(e.owner, "GET", "/v1/status", nil)
	if m, _ := out["managed"].(map[string]any); !strings.Contains(m["paused"].(string), "shiptiffin.com/account") {
		t.Fatalf("paused: %v", m)
	}

	for cp, want := range map[string]any{
		"https://cp.example.com/":       "https://cp.example.com/account",
		"https://cp.example.com/tiffin": "https://cp.example.com/tiffin/account",
		"http://127.0.0.1:8080":         "http://127.0.0.1:8080/account",
		"http://shiptiffin.com":         nil, // only https leaves the box
		"javascript:alert(1)":           nil,
	} {
		managedBox(t, cp)
		_, out, _ := e.call(e.owner, "GET", "/v1/status", nil)
		var got any
		if m, ok := out["managed"].(map[string]any); ok {
			got = m["account"]
		}
		if got != want {
			t.Errorf("control plane %q: account %v, want %v", cp, got, want)
		}
	}
}
