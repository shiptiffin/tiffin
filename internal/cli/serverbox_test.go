package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// fakeServer installs a box in a temp dir as `tiffin up` would on a server
// (its state and tiffin.service) and makes this test root.
func fakeServer(t *testing.T, o install.Options) (home string) {
	t.Helper()
	dir := t.TempDir()
	home, unit := filepath.Join(dir, "platform"), filepath.Join(dir, "tiffin.service")
	b, _, err := openBox(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if err := os.WriteFile(unit, []byte(install.Unit(o)), 0o644); err != nil {
		t.Fatal(err)
	}
	oldHome, oldUnit, oldEUID, oldRoot := serverHome, serverUnit, euid, rootUID
	serverHome, serverUnit, euid, rootUID = home, unit, func() int { return 0 }, uint32(os.Getuid())
	t.Cleanup(func() { serverHome, serverUnit, euid, rootUID = oldHome, oldUnit, oldEUID, oldRoot })
	return home
}

var managedServer = install.Options{Domain: "new-box.shiptiffin.app", HTTPSPort: 443, HTTPPort: 80, PublicIP: "203.0.113.7"}

// Root on the server runs `tiffin login` with nothing else set up: the
// installed box, an absolute link to its dashboard, and which box it was.
func TestLoginOnTheServer(t *testing.T) {
	fakeServer(t, managedServer)
	env := map[string]string{"HOME": t.TempDir()}
	code, out, errb := run(t, env, "login")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if u := decode(t, out)["url"].(string); !strings.HasPrefix(u, "https://dashboard.new-box.shiptiffin.app/login#tfl_") {
		t.Fatalf("link: %s", u)
	}
	if !strings.Contains(errb, "Using the box on this server: new-box.shiptiffin.app") || strings.Count(errb, "\n") != 1 {
		t.Fatalf("stderr: %q", errb)
	}
}

// The link follows the box's domain as set since install (another domain,
// another dashboard name) and the port people reach it on.
func TestServerLoginLinkFollowsTheDomain(t *testing.T) {
	home := fakeServer(t, install.Options{Domain: "box.example.net", HTTPSPort: 8443, HTTPPort: 8080, PublicIP: "203.0.113.7"})
	env := map[string]string{"HOME": t.TempDir()}
	_, out, errb := run(t, env, "login")
	if u, _ := decode(t, out)["url"].(string); !strings.HasPrefix(u, "https://dashboard.box.example.net:8443/login#") {
		t.Fatalf("link: %s (%s)", u, errb)
	}

	b, _, err := openBox(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	err = platform.SaveBoxDomain(context.Background(), b.db, platform.BoxDomain{Domain: "example.com", Dashboard: "admin"})
	b.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, out, errb = run(t, env, "login")
	if u, _ := decode(t, out)["url"].(string); !strings.HasPrefix(u, "https://admin.example.com:8443/login#") || !strings.Contains(errb, "server: example.com (") {
		t.Fatalf("link after domain set: %s (%s)", u, errb)
	}

	// --home naming the installed box gives an absolute link too, and
	// works for anyone who can read it (no "which box": it was named).
	euid = func() int { return 1000 }
	code, out, errb := run(t, map[string]string{"HOME": t.TempDir(), "TIFFIN_HOME": home}, "login")
	if u, _ := decode(t, out)["url"].(string); code != 0 || !strings.HasPrefix(u, "https://admin.example.com:8443/login#") || strings.Contains(errb, "Using the box") {
		t.Fatalf("explicit home: %d %s (%s)", code, u, errb)
	}
}

// Anyone but root, or a box whose state isn't root's alone, gets a clear
// refusal; a server with no state yet, or an explicit target, is left alone.
func TestServerBoxRefusals(t *testing.T) {
	home := fakeServer(t, managedServer)
	env := map[string]string{"HOME": t.TempDir()}
	login := func(env map[string]string) (int, string) {
		code, out, errb := run(t, env, "login")
		return code, errb + string(out) // off a terminal, errors are JSON on stdout
	}

	euid = func() int { return 1000 }
	if code, msg := login(env); code != ExitAuth || !strings.Contains(msg, "sudo") {
		t.Fatalf("not root: %d %s", code, msg)
	}
	euid = func() int { return 0 }

	db := filepath.Join(home, "state.db")
	_ = os.Chmod(db, 0o664)
	if code, msg := login(env); code != ExitInvalid || !strings.Contains(msg, "writable by others") {
		t.Fatalf("group-writable state: %d %s", code, msg)
	}
	_ = os.Chmod(db, 0o644)

	rootUID = uint32(os.Getuid()) + 1
	if code, msg := login(env); code != ExitInvalid || !strings.Contains(msg, "not owned by root") {
		t.Fatalf("someone else's state: %d %s", code, msg)
	}
	rootUID = uint32(os.Getuid())

	// TIFFIN_URL names another box: that one, not this server's.
	if _, msg := login(map[string]string{"HOME": t.TempDir(), "TIFFIN_URL": "https://dashboard.other.example"}); strings.Contains(msg, "Using the box") || !strings.Contains(msg, "TIFFIN_TOKEN") {
		t.Fatalf("TIFFIN_URL: %s", msg)
	}

	// No state yet: never made here.
	_ = os.Rename(db, db+".gone")
	if code, msg := login(env); code != ExitInvalid || !strings.Contains(msg, "knows no box yet") {
		t.Fatalf("no state: %d %s", code, msg)
	}
	if _, err := os.Stat(db); err == nil {
		t.Fatal("made a state.db")
	}
}
