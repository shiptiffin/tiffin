// Package remote reaches a box on a real server over SSH. It drives the
// system's OpenSSH client (macOS, Linux and Windows 10+ ship one), so the
// person's own keys, agent and ssh_config work as they do in a terminal.
//
// It backs two providers: Hetzner (which creates the server and a key for
// it) and plain SSH (any Ubuntu 24.04 or 26.04 server the person already has).
package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/provider"
	"golang.org/x/crypto/ssh"
)

// Target is an SSH destination.
type Target struct {
	User       string // login user; root on a new Hetzner server
	Host       string // IP address or host name
	Port       int    // default 22
	Identity   string // private key file; empty uses the person's ssh defaults
	KnownHosts string // known_hosts file for this box (host keys are pinned on first use)
	// TrustOwn also checks the person's own known_hosts (a server they
	// already reach over SSH): a key they trust there is honoured and a
	// different one refused. Off for servers Tiffin creates, whose
	// addresses a deleted server may have had.
	TrustOwn bool
}

var (
	userRE = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	hostRE = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)
)

// ParseTarget parses "user@host", "user@host:2222" or "user@[2001:db8::1]:22".
func ParseTarget(s string) (Target, error) {
	t := Target{Port: 22}
	user, host, ok := strings.Cut(s, "@")
	if !ok {
		return t, fmt.Errorf("--host %q: give user@host (the user needs sudo)", s)
	}
	t.User = user
	if h, p, err := net.SplitHostPort(host); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return t, fmt.Errorf("--host %q: bad port %q", s, p)
		}
		host, t.Port = h, n
	}
	t.Host = strings.Trim(host, "[]")
	if !userRE.MatchString(t.User) {
		return t, fmt.Errorf("--host %q: %q is not a valid user name", s, t.User)
	}
	if !hostRE.MatchString(t.Host) || strings.HasPrefix(t.Host, "-") {
		return t, fmt.Errorf("--host %q: %q is not a valid host", s, t.Host)
	}
	return t, nil
}

func (t Target) String() string {
	h := t.Host
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	if t.Port != 0 && t.Port != 22 {
		return fmt.Sprintf("%s@%s:%d", t.User, h, t.Port)
	}
	return t.User + "@" + h
}

// Machine is a server reached over SSH. It implements provider.Machine.
type Machine struct {
	T    Target
	arch string
}

// New returns a Machine for t.
func New(t Target) *Machine {
	if t.Port == 0 {
		t.Port = 22
	}
	return &Machine{T: t}
}

var _ provider.Machine = (*Machine)(nil)

// Available reports whether an OpenSSH client is installed.
func Available() error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("%w: ssh not found; install OpenSSH", provider.ErrUnavailable)
	}
	return nil
}

func (m *Machine) args(remote ...string) []string {
	a := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"-o", "LogLevel=ERROR",
		"-p", strconv.Itoa(m.T.Port),
	}
	a = append(a, HostKeyOptions(m.T.KnownHosts, m.T.TrustOwn)...)
	if m.T.Identity != "" {
		a = append(a, "-i", m.T.Identity, "-o", "IdentitiesOnly=yes")
	}
	a = append(a, "--", m.T.User+"@"+m.T.Host)
	return append(a, remote...)
}

// HostKeyOptions are the ssh options that check a box's host key against
// knownHosts: pinned the first time, a changed one refused after. With
// trustOwn the person's own known_hosts files (and the system's) are checked
// too, read-only: a key they already trust is used and a different one
// refused before Tiffin sends anything.
func HostKeyOptions(knownHosts string, trustOwn bool) []string {
	if knownHosts == "" {
		return nil
	}
	files := knownHosts
	if strings.ContainsAny(files, " \t") {
		files = `"` + files + `"`
	}
	o := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if trustOwn {
		// New keys go to the first file only.
		return append(o, "-o", "UserKnownHostsFile="+files+" ~/.ssh/known_hosts ~/.ssh/known_hosts2")
	}
	return append(o, "-o", "UserKnownHostsFile="+files, "-o", "GlobalKnownHostsFile=/dev/null")
}

// run runs ssh with stdin and returns stdout and stderr.
func (m *Machine) run(ctx context.Context, timeout time.Duration, stdin io.Reader, remote ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", m.args(remote...)...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("ssh %s timed out after %s: %w", m.T, timeout, err)
	}
	return out.String(), errb.String(), err
}

// Exec runs a bash script on the server as the login user. The script goes
// over stdin, so it needs no quoting.
func (m *Machine) Exec(ctx context.Context, script string) (string, string, error) {
	return m.run(ctx, 20*time.Minute, strings.NewReader(script), "bash", "-s")
}

// Copy copies a local file to remote (written to a temporary name, then renamed).
func (m *Machine) Copy(ctx context.Context, local, remote string) error {
	f, err := os.Open(local) // streamed: a binary is ~100 MB
	if err != nil {
		return err
	}
	defer f.Close()
	q := shellQuote(remote)
	if _, stderr, err := m.run(ctx, 15*time.Minute, f, "cat > "+shellQuote(remote+".part")+" && mv -f "+shellQuote(remote+".part")+" "+q); err != nil {
		return fmt.Errorf("copy %s to %s: %w\n%s", filepath.Base(local), m.T, err, stderr)
	}
	return nil
}

// Arch is the server's Go architecture (arm64 or amd64), asked once.
func (m *Machine) Arch() string {
	if m.arch != "" {
		return m.arch
	}
	out, _, err := m.run(context.Background(), time.Minute, nil, "uname", "-m")
	if err != nil {
		return ""
	}
	m.arch = GoArch(strings.TrimSpace(out))
	return m.arch
}

// GoArch maps `uname -m` to Go's GOARCH.
func GoArch(uname string) string {
	switch uname {
	case "aarch64", "arm64":
		return "arm64"
	case "x86_64", "amd64":
		return "amd64"
	}
	return uname
}

// Wait retries until SSH answers (a new server takes a minute to boot) and
// cloud-init has finished, so apt is free.
func (m *Machine) Wait(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last string
	for {
		_, stderr, err := m.run(ctx, 30*time.Second, nil, "true")
		if err == nil {
			break
		}
		last = strings.TrimSpace(stderr)
		if last == "" {
			last = err.Error()
		}
		if strings.Contains(last, "REMOTE HOST IDENTIFICATION HAS CHANGED") || strings.Contains(last, "Host key verification failed") {
			where := m.T.KnownHosts
			if m.T.TrustOwn {
				name := m.T.Host
				if m.T.Port != 22 {
					name = fmt.Sprintf("'[%s]:%d'", m.T.Host, m.T.Port)
				}
				where += " (or your ~/.ssh/known_hosts: ssh-keygen -R " + name + ")"
			}
			return fmt.Errorf("the server's SSH host key is not the one trusted for %s; if you rebuilt the server, remove its line from %s", m.T, where)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not reach %s over SSH after %s: %s", m.T, d.Round(time.Second), last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	// cloud-init holds the apt lock while it finishes first boot.
	_, _, _ = m.run(ctx, 10*time.Minute, nil, "command -v cloud-init >/dev/null && sudo cloud-init status --wait >/dev/null 2>&1; true")
	return nil
}

// UbuntuReleases are the Ubuntu LTS releases a box runs on.
var UbuntuReleases = []string{"24.04", "26.04"}

// Check makes sure the server can host a box: Ubuntu 24.04 or 26.04,
// passwordless sudo, systemd. It returns the Ubuntu version, or a
// plain-words error.
func (m *Machine) Check(ctx context.Context) (string, error) {
	out, stderr, err := m.Exec(ctx, `. /etc/os-release; echo "$ID $VERSION_ID"
sudo -n true 2>/dev/null && echo sudo-ok || echo sudo-no
[ -d /run/systemd/system ] && echo systemd-ok || echo systemd-no`)
	if err != nil {
		return "", fmt.Errorf("could not run commands on %s: %v %s", m.T, err, strings.TrimSpace(stderr))
	}
	lines := strings.Fields(out)
	if len(lines) < 4 {
		return "", fmt.Errorf("unexpected answer from %s: %q", m.T, out)
	}
	if lines[0] != "ubuntu" || !slices.Contains(UbuntuReleases, lines[1]) {
		return "", fmt.Errorf("%s runs %s %s; Tiffin needs Ubuntu 24.04 or 26.04 LTS", m.T, lines[0], lines[1])
	}
	if lines[2] != "sudo-ok" {
		return "", fmt.Errorf("%s on %s cannot use sudo without a password; Tiffin needs root to install system services (log in as root, or allow NOPASSWD sudo for this user)", m.T.User, m.T.Host)
	}
	if lines[3] != "systemd-ok" {
		return "", fmt.Errorf("%s does not run systemd (a container?); Tiffin needs a full VM or a dedicated server", m.T)
	}
	return lines[1], nil
}

// ClientIP is the address the server sees this computer connect from (the
// first field of $SSH_CLIENT): the owner's IP to keep out of bans.
func (m *Machine) ClientIP(ctx context.Context) (string, error) {
	out, _, err := m.run(ctx, time.Minute, nil, `echo "$SSH_CLIENT"`)
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return "", errors.New("the server did not report this computer's address")
	}
	a, err := netip.ParseAddr(f[0])
	if err != nil {
		return "", err
	}
	return a.Unmap().String(), nil
}

// OwnerRange is what to allowlist for an address: the IP itself, or its /64
// for IPv6 (one home or office network).
func OwnerRange(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

// GenerateKey writes a new ed25519 key pair at path (0600) and path.pub
// unless path already holds one. It returns the public key line.
func GenerateKey(path, comment string) (string, error) {
	if raw, err := os.ReadFile(path + ".pub"); err == nil {
		if _, err := os.Stat(path); err == nil {
			return strings.TrimSpace(string(raw)), nil
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
	if err := os.WriteFile(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	return line, nil
}

// Fingerprint is the MD5 fingerprint of an authorized_keys line, the form
// Hetzner uses ("b7:2f:...").
func Fingerprint(line string) (string, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", err
	}
	return ssh.FingerprintLegacyMD5(pk), nil
}

// ForgetHost removes host's lines from a known_hosts file (a new server may
// reuse an old one's IP with a new host key).
func ForgetHost(knownHosts string, hosts ...string) error {
	raw, err := os.ReadFile(knownHosts)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var keep []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		drop := false
		if len(f) > 0 {
			for _, name := range strings.Split(f[0], ",") {
				for _, h := range hosts {
					if h != "" && (name == h || name == "["+h+"]:22" || strings.HasPrefix(name, "["+h+"]:")) {
						drop = true
					}
				}
			}
		}
		if !drop && line != "" {
			keep = append(keep, line)
		}
	}
	out := strings.Join(keep, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(knownHosts, []byte(out), 0o600)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
