package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/release"
)

// ReleaseBinaries fetches signed Tiffin release builds: the channel's
// manifest must be signed by a key this build trusts, and each build must
// match its size and sha256 in it (internal/release). Builds are cached by
// version in dir.
type ReleaseBinaries struct {
	Source  string // release.DefaultSource unless set
	Channel string // "stable" unless set
	Dir     string
	Client  *http.Client
	mu      sync.Mutex
}

// Get returns the path of the newest release build for linux/<arch>.
func (r *ReleaseBinaries) Get(ctx context.Context, arch string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	src, ch := r.Source, r.Channel
	if src == "" {
		src = release.DefaultSource
	}
	if ch == "" {
		ch = "stable"
	}
	c := r.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Minute}
	}
	m, err := release.Fetch(ctx, c, src, ch, release.TrustedKeys())
	if err != nil {
		return "", err
	}
	dst := filepath.Join(r.Dir, m.Version, "tiffin-linux-"+arch)
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		if a, ok := m.Artifacts["linux/"+arch]; ok && a.Size == fi.Size() {
			if sum, err := install.FileSHA(dst); err == nil && sum == a.SHA256 {
				return dst, nil
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if _, err := release.Download(ctx, c, m, release.ManifestURL(src, ch), "linux/"+arch, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// EgressIPs finds the worker's public addresses: CLOUD_SSH_FROM (comma
// separated) when set, else asked of ipify, as `tiffin up` does.
func EgressIPs(fixed string) func(ctx context.Context) ([]netip.Prefix, error) {
	return func(ctx context.Context) ([]netip.Prefix, error) {
		var out []netip.Prefix
		for _, s := range strings.Split(fixed, ",") {
			if s = strings.TrimSpace(s); s != "" {
				p, err := platform.ParseIPOrPrefix(s)
				if err != nil {
					return nil, fmt.Errorf("CLOUD_SSH_FROM: %w", err)
				}
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
		for _, u := range []string{"https://api.ipify.org", "https://api6.ipify.org"} {
			cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(res.Body, 100))
				res.Body.Close()
				if a, err := netip.ParseAddr(strings.TrimSpace(string(body))); err == nil && res.StatusCode == 200 {
					a = a.Unmap()
					out = append(out, netip.PrefixFrom(a, a.BitLen()))
				}
			}
			cancel()
		}
		if len(out) == 0 {
			return nil, errors.New("could not find this worker's public address; set CLOUD_SSH_FROM")
		}
		return out, nil
	}
}
