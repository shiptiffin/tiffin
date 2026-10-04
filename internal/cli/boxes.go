package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/dnskit"
)

// boxConfig is what the CLI on your computer knows about a box.
type boxConfig struct {
	Provider   string    `json:"provider"`
	URL        string    `json:"url"`
	Token      string    `json:"token"`      // owner token: stays on this computer
	AgentToken string    `json:"agentToken"` // what `tiffin mcp` gives agents
	CAFile     string    `json:"caFile"`
	Build      string    `json:"build"`
	CreatedAt  time.Time `json:"createdAt"`
}

type boxesFile struct {
	Current string                `json:"current"`
	Boxes   map[string]*boxConfig `json:"boxes"`
}

func (a *app) configDir() string {
	if d := a.io.Env("TIFFIN_CONFIG_DIR"); d != "" {
		return d
	}
	return defaultHome(a.io.Env)
}

func (a *app) boxesPath() string { return filepath.Join(a.configDir(), "boxes.json") }

func (a *app) loadBoxes() (*boxesFile, error) {
	f := &boxesFile{Boxes: map[string]*boxConfig{}}
	raw, err := os.ReadFile(a.boxesPath())
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	} else if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, f); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", a.boxesPath(), err)
	}
	if f.Boxes == nil {
		f.Boxes = map[string]*boxConfig{}
	}
	return f, nil
}

func (a *app) saveBoxes(f *boxesFile) error {
	if err := os.MkdirAll(a.configDir(), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(f, "", "  ")
	tmp := a.boxesPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.boxesPath())
}

// currentBox returns the box the CLI targets by default, if any.
func (a *app) currentBox() (string, *boxConfig) {
	f, err := a.loadBoxes()
	if err != nil || f.Current == "" {
		return "", nil
	}
	return f.Current, f.Boxes[f.Current]
}

// boxTransport trusts exactly the box's CA (plus the system roots) and sends
// *.localhost names to the loopback interface, as browsers do.
func boxTransport(caFile string) (*http.Transport, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read box CA: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("box CA %s is not a PEM certificate", caFile)
		}
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err == nil && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
				addr = net.JoinHostPort("127.0.0.1", port)
			} else if ip, ok := dnskit.SslipAddr(host); ok && err == nil {
				// <ip>.sslip.io names spell their address: no DNS needed.
				addr = net.JoinHostPort(ip.String(), port)
			}
			return d.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}, nil
}
