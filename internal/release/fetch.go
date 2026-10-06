package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// DefaultSource is where boxes look for releases: a rolling GitHub release
// per channel holds that channel's newest manifest and its signature; the
// manifest points at the versioned release's binaries.
const DefaultSource = "https://github.com/shiptiffin/tiffin/releases/download/channel-{channel}/manifest.json"

// ManifestURL is the manifest's URL for a channel ({channel} in source).
func ManifestURL(source, channel string) string {
	return strings.ReplaceAll(source, "{channel}", channel)
}

const maxManifest = 64 << 10

// ErrRefused marks a release the box refused: unsigned, signed by a key it
// does not trust, changed after signing, for another channel, or a build
// that does not match its manifest.
var ErrRefused = errors.New("release refused")

type refusal string

func (r refusal) Error() string        { return string(r) }
func (refusal) Is(target error) bool   { return target == ErrRefused }
func refused(f string, a ...any) error { return refusal(fmt.Sprintf(f, a...)) }

// Fetch downloads a channel's manifest and signature from source, checks
// the signature against keys and returns the manifest. A manifest signed
// for another channel, or by a key the box does not trust, is refused.
func Fetch(ctx context.Context, c *http.Client, source, channel string, keys []PublicKey) (*Manifest, error) {
	if len(keys) == 0 {
		return nil, errors.New("this build trusts no release signing key, so it cannot check releases (it updates with tiffin up)")
	}
	u := ManifestURL(source, channel)
	raw, err := get(ctx, c, u, maxManifest)
	if err != nil {
		return nil, err
	}
	sig, err := get(ctx, c, u+".minisig", 4096)
	if err != nil {
		return nil, refused("refused the release manifest at %s: no signature (%v)", u, err)
	}
	trusted, _, err := Verify(keys, raw, sig)
	if err != nil {
		return nil, refused("refused the release manifest at %s: %v", u, err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, refused("refused the release manifest at %s: %v", u, err)
	}
	if m.Channel != channel || trusted != m.TrustedComment() {
		return nil, refused("refused the release manifest at %s: it is signed as %q, not for the %s channel", u, trusted, channel)
	}
	return m, nil
}

// Download fetches the artifact for goos/goarch to dst (mode 0755) and
// checks its size and sha256 against the manifest, which came from
// manifestURL (relative artifact URLs resolve against it).
func Download(ctx context.Context, c *http.Client, m *Manifest, manifestURL, platform, dst string) (*Artifact, error) {
	a, ok := m.Artifacts[platform]
	if !ok {
		return nil, fmt.Errorf("release %s has no build for %s", m.Version, platform)
	}
	base, err := url.Parse(manifestURL)
	if err != nil {
		return nil, err
	}
	ref := a.URL
	if ref == "" {
		ref = a.Name
	}
	u, err := base.Parse(ref)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: HTTP %d", u, res.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, a.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", u, err)
	}
	if n != a.Size {
		return nil, refused("refused %s: %d bytes, the signed manifest says %d", a.Name, n, a.Size)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != a.SHA256 {
		return nil, refused("refused %s: its sha256 %s does not match the signed manifest's %s", a.Name, sum[:12], a.SHA256[:12])
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	return &a, nil
}

func get(ctx context.Context, c *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("GET %s: more than %d bytes", u, limit)
	}
	return raw, nil
}
