package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) SecretKey {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSignVerify(t *testing.T) {
	k := newKey(t)
	msg := []byte(`{"version":"1.2.3"}`)
	sig, err := Sign(k, msg, "tiffin 1.2.3 stable")
	if err != nil {
		t.Fatal(err)
	}
	trusted, by, err := Verify([]PublicKey{k.Public()}, msg, sig)
	if err != nil || trusted != "tiffin 1.2.3 stable" || by.ID != k.ID {
		t.Fatalf("verify: %q %v", trusted, err)
	}
	// The message changed after signing.
	if _, _, err := Verify([]PublicKey{k.Public()}, []byte(`{"version":"9.9.9"}`), sig); err == nil {
		t.Fatal("a changed message verified")
	}
	// The trusted comment changed.
	lines := strings.Split(string(sig), "\n")
	lines[2] = "trusted comment: tiffin 9.9.9 stable"
	if _, _, err := Verify([]PublicKey{k.Public()}, msg, []byte(strings.Join(lines, "\n"))); err == nil || !strings.Contains(err.Error(), "trusted comment") {
		t.Fatalf("a changed trusted comment: %v", err)
	}
	// A key the build does not trust, even with the same ID.
	other := newKey(t)
	if _, _, err := Verify([]PublicKey{other.Public()}, msg, sig); err == nil || !strings.Contains(err.Error(), "does not trust") {
		t.Fatalf("untrusted key: %v", err)
	}
	impostor := PublicKey{ID: k.ID, Key: other.Public().Key}
	if _, _, err := Verify([]PublicKey{impostor}, msg, sig); err == nil {
		t.Fatal("a different key with the same ID verified")
	}
	if _, _, err := Verify([]PublicKey{k.Public()}, msg, []byte("garbage")); err == nil {
		t.Fatal("garbage verified")
	}
}

// Rotation: a build trusting the old and the new key takes releases signed
// by either; a build that dropped the old key refuses its signatures.
func TestKeyRotation(t *testing.T) {
	old, next := newKey(t), newKey(t)
	msg := []byte("manifest")
	byOld, _ := Sign(old, msg, "c")
	byNew, _ := Sign(next, msg, "c")
	both := []PublicKey{old.Public(), next.Public()}
	for name, sig := range map[string][]byte{"old": byOld, "new": byNew} {
		if _, by, err := Verify(both, msg, sig); err != nil {
			t.Errorf("signed by the %s key: %v", name, err)
		} else if name == "new" && by.ID != next.ID {
			t.Errorf("verified by the wrong key")
		}
	}
	if _, _, err := Verify([]PublicKey{next.Public()}, msg, byOld); err == nil {
		t.Fatal("the dropped key still verifies")
	}
}

func TestKeyFiles(t *testing.T) {
	k := newKey(t)
	back, err := ParseSecretKey(k.File())
	if err != nil || back.ID != k.ID || !bytes.Equal(back.Key, k.Key) {
		t.Fatalf("secret key round trip: %v", err)
	}
	pub, err := ParsePublicKey(k.Public().File())
	if err != nil || pub.ID != k.ID || !bytes.Equal(pub.Key, k.Public().Key) {
		t.Fatalf("public key round trip: %v", err)
	}
	if p2, err := ParsePublicKey(k.Public().String()); err != nil || p2.ID != k.ID {
		t.Fatalf("one-line public key: %v", err)
	}
	if !strings.Contains(k.Public().File(), k.Public().KeyID()) {
		t.Fatal("the .pub file names its key ID")
	}
	// A password-protected key (kdf "Sc") is refused with a hint.
	raw, _ := base64.StdEncoding.DecodeString(lastLine(k.File()))
	copy(raw[2:4], "Sc")
	if _, err := ParseSecretKey(base64.StdEncoding.EncodeToString(raw)); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("encrypted key: %v", err)
	}
	raw, _ = base64.StdEncoding.DecodeString(lastLine(k.File()))
	raw[60] ^= 1
	if _, err := ParseSecretKey(base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("a corrupted key parsed")
	}
}

func TestVersions(t *testing.T) {
	for _, bad := range []string{"dev", "6e5765c", "1.2", "v1.4.0-3-gabc1234", "v1.4.0-3-gabc1234-dirty", "1.4.0-dirty", "1.2.3.4"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("%q parsed as a release", bad)
		}
	}
	order := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "v1.0.1", "1.10.0"}
	for i := 1; i < len(order); i++ {
		a, _ := ParseVersion(order[i-1])
		b, err := ParseVersion(order[i])
		if err != nil {
			t.Fatal(err)
		}
		if a.Compare(b) != -1 || b.Compare(a) != 1 || b.Compare(b) != 0 {
			t.Errorf("%s < %s does not hold", order[i-1], order[i])
		}
	}
}

func testManifest() *Manifest {
	return &Manifest{Version: "1.5.0", Channel: "stable", Rollout: 100, Artifacts: map[string]Artifact{
		"linux/amd64": {Name: "tiffin-linux-amd64", SHA256: strings.Repeat("a", 64), Size: 10}}}
}

func TestAllowed(t *testing.T) {
	m := testManifest()
	if err := m.Allowed("1.4.9"); err != nil {
		t.Fatalf("upgrade refused: %v", err)
	}
	for _, cur := range []string{"1.5.0", "1.6.0", "v2.0.0"} {
		if err := m.Allowed(cur); !errors.Is(err, ErrNotNewer) {
			t.Errorf("from %s: a downgrade or the same version must be refused, got %v", cur, err)
		}
	}
	if err := m.Allowed("dev"); !errors.Is(err, ErrNotRelease) {
		t.Errorf("a development build: %v", err)
	}
	m.MinVersion = "1.4.0"
	if err := m.Allowed("1.3.9"); err == nil || !strings.Contains(err.Error(), "1.4.0") {
		t.Errorf("below minVersion: %v", err)
	}
	if err := m.Allowed("1.4.0"); err != nil {
		t.Errorf("at minVersion: %v", err)
	}
}

func TestManifestValidate(t *testing.T) {
	for name, f := range map[string]func(*Manifest){
		"version": func(m *Manifest) { m.Version = "dev" },
		"channel": func(m *Manifest) { m.Channel = "nightly" },
		"rollout": func(m *Manifest) { m.Rollout = 101 },
		"sha":     func(m *Manifest) { m.Artifacts["linux/amd64"] = Artifact{Name: "x", SHA256: "abc", Size: 1} },
		"name": func(m *Manifest) {
			m.Artifacts["linux/amd64"] = Artifact{Name: "../x", SHA256: strings.Repeat("a", 64), Size: 1}
		},
		"none":     func(m *Manifest) { m.Artifacts = nil },
		"minimum":  func(m *Manifest) { m.MinVersion = "soon" },
		"negative": func(m *Manifest) { m.Rollout = -1 },
	} {
		m := testManifest()
		f(m)
		if m.Validate() == nil {
			t.Errorf("%s: invalid manifest passed", name)
		}
	}
	if err := testManifest().Validate(); err != nil {
		t.Fatal(err)
	}
}

// Buckets are stable, spread evenly, and differ between versions so the
// same boxes are not always first.
func TestRolloutBuckets(t *testing.T) {
	m := testManifest()
	m.Rollout = 10
	in, first := 0, 0
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("box_%d", i)
		if Bucket(id, "1.5.0") != Bucket(id, "1.5.0") {
			t.Fatal("bucket not stable")
		}
		if m.InRollout(id) {
			in++
			if Bucket(id, "1.6.0") < 10 {
				first++
			}
		}
	}
	if in < 850 || in > 1150 {
		t.Fatalf("10%% rollout took %d of 10000 boxes", in)
	}
	if first > in/4 {
		t.Fatalf("%d of the %d first boxes are first again for the next version", first, in)
	}
	m.Rollout = 0
	if m.InRollout("box_1") {
		t.Fatal("rollout 0 applies")
	}
	m.Rollout = 100
	for i := 0; i < 1000; i++ {
		if !m.InRollout(fmt.Sprintf("b%d", i)) {
			t.Fatal("rollout 100 skips a box")
		}
	}
}

// release serves a signed manifest for channel and one artifact.
type release struct {
	key      SecretKey
	manifest []byte
	sig      []byte
	artifact []byte
	srv      *httptest.Server
}

func serve(t *testing.T, k SecretKey, channel string, artifact []byte) *release {
	t.Helper()
	r := &release{key: k, artifact: artifact}
	sum := sha256.Sum256(artifact)
	m := Manifest{Version: "1.5.0", Channel: channel, Date: time.Now().UTC(), Rollout: 100,
		Artifacts: map[string]Artifact{"linux/arm64": {Name: "tiffin-linux-arm64", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(artifact))}}}
	r.manifest, _ = json.Marshal(m)
	r.sig, _ = Sign(k, r.manifest, m.TrustedComment())
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/" + channel + "/manifest.json":
			w.Write(r.manifest)
		case "/" + channel + "/manifest.json.minisig":
			if r.sig == nil {
				http.NotFound(w, req)
				return
			}
			w.Write(r.sig)
		case "/" + channel + "/tiffin-linux-arm64":
			w.Write(r.artifact)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func TestFetchAndDownload(t *testing.T) {
	ctx := context.Background()
	k := newKey(t)
	keys := []PublicKey{k.Public()}
	r := serve(t, k, "stable", []byte("the new build"))
	src := r.srv.URL + "/{channel}/manifest.json"

	m, err := Fetch(ctx, http.DefaultClient, src, "stable", keys)
	if err != nil || m.Version != "1.5.0" {
		t.Fatalf("fetch: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "tiffin")
	if _, err := Download(ctx, http.DefaultClient, m, ManifestURL(src, "stable"), "linux/arm64", dst); err != nil {
		t.Fatalf("download: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "the new build" {
		t.Fatalf("downloaded %q", b)
	}
	if _, err := Download(ctx, http.DefaultClient, m, ManifestURL(src, "stable"), "linux/riscv64", dst); err == nil {
		t.Fatal("a platform the release lacks")
	}

	// A tampered artifact (same size, other bytes; then another size).
	for _, bad := range []string{"the bad build", "a much longer bad build"} {
		r.artifact = []byte(bad)
		dst2 := filepath.Join(t.TempDir(), "tiffin")
		if _, err := Download(ctx, http.DefaultClient, m, ManifestURL(src, "stable"), "linux/arm64", dst2); !errors.Is(err, ErrRefused) {
			t.Fatalf("tampered artifact %q: %v", bad, err)
		}
		if _, err := os.Stat(dst2); err == nil {
			t.Fatal("a refused artifact was left in place")
		}
	}

	// The wrong key; no keys at all.
	if _, err := Fetch(ctx, http.DefaultClient, src, "stable", []PublicKey{newKey(t).Public()}); !errors.Is(err, ErrRefused) {
		t.Fatalf("untrusted key: %v", err)
	}
	if _, err := Fetch(ctx, http.DefaultClient, src, "stable", nil); err == nil {
		t.Fatal("no trusted keys")
	}
	// A tampered manifest (a bigger rollout, say).
	good := r.manifest
	r.manifest = bytes.Replace(good, []byte(`"rollout":100`), []byte(`"rollout":99`), 1)
	if _, err := Fetch(ctx, http.DefaultClient, src, "stable", keys); !errors.Is(err, ErrRefused) {
		t.Fatalf("tampered manifest: %v", err)
	}
	r.manifest = good
	// Unsigned.
	r.sig = nil
	if _, err := Fetch(ctx, http.DefaultClient, src, "stable", keys); !errors.Is(err, ErrRefused) {
		t.Fatalf("unsigned manifest: %v", err)
	}

	// An edge manifest served where the stable one should be.
	e := serve(t, k, "edge", []byte("x"))
	mux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.URL.Path = strings.Replace(req.URL.Path, "/stable/", "/edge/", 1)
		resp, err := http.Get(e.srv.URL + req.URL.Path)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		w.Write(b.Bytes())
	}))
	defer mux.Close()
	if _, err := Fetch(ctx, http.DefaultClient, mux.URL+"/{channel}/manifest.json", "stable", keys); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "channel") {
		t.Fatalf("edge manifest on stable: %v", err)
	}
}

func TestTrustedKeysFromLinkFlags(t *testing.T) {
	a, b := newKey(t), newKey(t)
	defer func(s string) { extraKeys = s }(extraKeys)
	extraKeys = a.Public().String() + "," + b.Public().File()
	got := TrustedKeys()
	if len(got) != len(trustedKeys)+2 || got[len(got)-1].ID != b.ID {
		t.Fatalf("trusted keys: %d", len(got))
	}
}
