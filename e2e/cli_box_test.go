//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliBox is a fresh box driven through the CLI, the way a person or agent
// uses it (used by the storage and email acceptance tests).
type cliBox struct {
	t        *testing.T
	dir      string
	cli      string
	instance string
	port     int
	env      []string
	project  string
	hide     func(string) string // masks values that must not show in failure messages
}

// fatalf fails the test, with hide applied to the message.
func (b *cliBox) fatalf(format string, a ...any) {
	b.t.Helper()
	msg := fmt.Sprintf(format, a...)
	if b.hide != nil {
		msg = b.hide(msg)
	}
	b.t.Fatal(msg)
}

func newCLIBox(t *testing.T, label, project string) *cliBox {
	t.Helper()
	RequireLima(t)
	dir := t.TempDir()
	return newCLIBoxFrom(t, dir, project, buildTiffin(t, dir, "", ""), buildTiffin(t, dir, "linux", "0.0.1-"+label))
}

// newCLIBoxFrom is newCLIBox with the given host CLI and box build.
func newCLIBoxFrom(t *testing.T, dir, project, cli, bin string) *cliBox {
	t.Helper()
	RequireLima(t)
	b := &cliBox{t: t, dir: dir, project: project, cli: cli}
	disk := newDiskName()
	b.instance, b.port = newName(), freePort(t)
	b.env = append(os.Environ(),
		"TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"),
		"TIFFIN_LIMA_INSTANCE="+b.instance, "TIFFIN_LIMA_DISK="+disk, fmt.Sprintf("TIFFIN_LIMA_PORT=%d", b.port),
		"TIFFIN_LIMA_MEMORY=3GiB", "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
			_ = exec.Command("limactl", "delete", "-f", b.instance).Run()
			_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
		}
	})
	b.ok("up", "--binary", bin)
	return b
}

func (b *cliBox) run(args ...string) (int, string) {
	b.t.Helper()
	return b.runWith("", args...)
}

// runWith runs a CLI command with stdin (for a --body-file - that holds a
// secret, which must not show in the arguments or in failure messages).
func (b *cliBox) runWith(stdin string, args ...string) (int, string) {
	b.t.Helper()
	cmd := exec.Command(b.cli, args...)
	cmd.Env, cmd.Dir = b.env, b.dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
		out = append(out, ee.Stderr...)
	} else if err != nil {
		b.fatalf("tiffin %v: %v", args, err)
	}
	return code, string(out)
}

// ok runs a CLI command that must succeed and decodes its JSON output.
func (b *cliBox) ok(args ...string) map[string]any {
	b.t.Helper()
	code, out := b.run(args...)
	if code != 0 {
		b.fatalf("tiffin %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(out), &m)
	return m
}

// list runs a CLI command that returns a JSON array.
func (b *cliBox) list(args ...string) []map[string]any {
	b.t.Helper()
	code, out := b.run(args...)
	if code != 0 {
		b.fatalf("tiffin %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	var l []map[string]any
	if err := json.Unmarshal([]byte(out), &l); err == nil {
		return l
	}
	// A paged list: its first page.
	var pg struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &pg); err != nil || pg.Items == nil {
		b.fatalf("tiffin %s: not a JSON array or page: %s", strings.Join(args, " "), out)
	}
	return pg.Items
}

// inBox runs a shell command inside the VM.
func (b *cliBox) inBox(script string) string {
	b.t.Helper()
	out, err := exec.Command("limactl", "shell", "--workdir", "/", b.instance, "--", "bash", "-c", script).CombinedOutput()
	if err != nil {
		b.fatalf("in box %q: %v\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// apply writes a JSON manifest, plans and applies it; it returns the change ID.
func (b *cliBox) apply(name, manifest string) string {
	b.t.Helper()
	// A folder of its own: the CLI refuses a manifest beside its settings (dir/config).
	proj := filepath.Join(b.dir, "project")
	_ = os.MkdirAll(proj, 0o755)
	path := filepath.Join(proj, name+".json")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		b.t.Fatal(err)
	}
	plan := b.ok("plan", path)
	hash, _ := plan["hash"].(string)
	if len(hash) < 12 {
		b.fatalf("plan: %v", plan)
	}
	res := b.ok("apply", path, "--confirm", hash[:12], "-m", "e2e "+name)
	ch, _ := res["change"].(map[string]any)
	id, _ := ch["id"].(string)
	return id
}

// waitReady waits until every listed address is ready (or gone, for "").
func (b *cliBox) waitReady(addrs ...string) {
	b.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		_, out := b.run("projects", "get", b.project)
		var st struct {
			Status map[string]struct {
				State   string `json:"state"`
				Message string `json:"message"`
			} `json:"status"`
		}
		_ = json.Unmarshal([]byte(out), &st)
		done := true
		for _, a := range addrs {
			s := st.Status[a]
			if s.State == "failed" {
				b.fatalf("%s failed: %s", a, s.Message)
			}
			done = done && s.State == "ready"
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			b.fatalf("not ready in time: %s", out)
		}
		time.Sleep(time.Second)
	}
}

// https returns a client that trusts the box's CA and reaches *.localhost
// on the forwarded port.
func (b *cliBox) https() *http.Client {
	b.t.Helper()
	pem, err := os.ReadFile(filepath.Join(b.dir, "config", "boxes", "local", "ca.crt"))
	if err != nil {
		b.t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
	}}
}

func (b *cliBox) url(host string) string {
	return fmt.Sprintf("https://%s.tiffin.localhost:%d", host, b.port)
}

// get fetches a URL over the box's HTTPS edge.
func (b *cliBox) get(c *http.Client, method, u string, body io.Reader) (int, http.Header, string) {
	b.t.Helper()
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		b.t.Fatal(err)
	}
	res, err := c.Do(req)
	if err != nil {
		b.fatalf("%s %s: %v", method, u, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(raw)
}

func phaseLogger(t *testing.T) func(string, time.Time) {
	return func(name string, since time.Time) {
		t.Logf("PHASE %-16s %s", name, time.Since(since).Round(100*time.Millisecond))
	}
}
