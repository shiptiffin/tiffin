package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/provider/hetzner"
	"github.com/shiptiffin/tiffin/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

// The owner's way into a managed box after setup: the SSH keys they chose
// at shiptiffin.com/start (none is fine), in the key file Tiffin manages on
// the box, and port 22 open to those keys only. The setup's own key goes,
// on the server and in the Hetzner project, so ShipTiffin keeps no way in.

// SSHKeyArg is one public key the customer chose for the box, as they gave
// it (the worker parses it again before anything is made).
type SSHKeyArg struct {
	Key    string `json:"key"`
	Source string `json:"source"` // pasted, hetzner or github:<user>
}

// setupKeys checks the keys a setup was given: every one an accepted public
// key (sshkeys.Parse, one call per source, all added now), at most
// sshkeys.MaxKeys of them, duplicates dropped. Any bad key refuses them all.
func setupKeys(args []SSHKeyArg, now time.Time) ([]sshkeys.Key, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if len(args) > sshkeys.MaxKeys {
		return nil, fmt.Errorf("at most %d SSH keys (got %d)", sshkeys.MaxKeys, len(args))
	}
	var sources []string
	group := map[string][]string{}
	for i, a := range args {
		src := sshkeys.Source(a.Source)
		if !src.Valid() || src == sshkeys.SourceDashboard {
			return nil, fmt.Errorf("SSH key %d: unknown source %q", i+1, a.Source)
		}
		k := strings.TrimSpace(a.Key)
		if strings.ContainsAny(k, "\r\n") {
			return nil, fmt.Errorf("SSH key %d: one key per entry", i+1)
		}
		// Each on its own first, so the message names the key that's wrong.
		if _, err := sshkeys.Parse(k, src, now); err != nil {
			return nil, fmt.Errorf("SSH key %d (%s): %s", i+1, a.Source, strings.TrimPrefix(err.Error(), "line 1: "))
		}
		if _, ok := group[a.Source]; !ok {
			sources = append(sources, a.Source)
		}
		group[a.Source] = append(group[a.Source], k)
	}
	var keys []sshkeys.Key
	for _, src := range sources {
		ks, err := sshkeys.Parse(strings.Join(group[src], "\n"), sshkeys.Source(src), now)
		if err != nil {
			return nil, fmt.Errorf("SSH keys (%s): %w", src, err)
		}
		if keys, err = sshkeys.Merge(keys, ks); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// accessScript writes the owner's keys to the key file Tiffin manages,
// removes the setup key's entry from root's own authorized_keys, and reads
// both back, all in one SSH command: once the setup key is gone no other
// command can log in. Nothing a person typed reaches the shell: the key file
// goes as base64 of sshkeys.Render, the setup key as its re-encoded type and
// base64. Every write is a temporary file renamed over the old one; every
// other line of root's file stays as it was. Running it again converges.
const accessScript = `set -eu
PATH="$PATH:/usr/sbin:/sbin"
dir=@ROOT@/etc/ssh/authorized_keys.d
own=@ROOT@/root/.ssh/authorized_keys
keys='@KEYS@'
t='@TYPE@'
k='@KEY@'
# The setup key's entry: its type then its key, whatever options or comment surround them.
setup_entry() { awk -v t="$t" -v k="$k" '{ for (i = 1; i < NF; i++) if ($i == t && $(i+1) == k) found = 1 } END { exit !found }' "$1"; }
if [ -n "$keys" ]; then
  if [ -L "$dir" ]; then echo "$dir is a symbolic link" >&2; exit 3; fi
  mkdir -p "$dir"
  chmod 755 "$dir"
  if command -v flock >/dev/null 2>&1; then exec 9>>"$dir/.tiffin.lock"; flock 9; fi
  tmp=$(mktemp "$dir/.root-XXXXXX")
  printf '%s' "$keys" | base64 -d > "$tmp"
  chmod 644 "$tmp"
  mv -f "$tmp" "$dir/root"
fi
if [ -f "$own" ]; then
  tmp=$(mktemp "$own.tiffin-XXXXXX")
  awk -v t="$t" -v k="$k" '{ hit = 0; for (i = 1; i < NF; i++) if ($i == t && $(i+1) == k) hit = 1; if (!hit) print }' "$own" > "$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$own"
fi
if [ -f "$own" ] && setup_entry "$own"; then echo "setup-key still-there"; exit 4; fi
echo "setup-key gone"
if [ -n "$keys" ]; then
  echo "managed $(base64 < "$dir/root" | tr -d '\n')"
  echo "mode-dir $(ls -ld "$dir" | cut -c1-10)"
  echo "mode-file $(ls -l "$dir/root" | cut -c1-10)"
  if cfg=$(sshd -T -C user=root,host=localhost,addr=192.0.2.1 2>/dev/null); then
    printf '%s\n' "$cfg" | awk '$1 ~ /^(passwordauthentication|kbdinteractiveauthentication|permitrootlogin|pubkeyauthentication|authorizedkeysfile|authenticationmethods)$/ { print "sshd", $0 }'
  else
    echo "sshd-error"
  fi
fi
echo "done"
`

// AccessScript is the one command that finishes the owner's access on the
// server (see accessScript). root prefixes the paths it edits (tests).
func AccessScript(root, setupPub string, keys []sshkeys.Key) (string, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(setupPub))
	if err != nil {
		return "", fmt.Errorf("the setup key: %w", err)
	}
	typ, key, _ := strings.Cut(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), " ")
	enc := ""
	if len(keys) > 0 {
		enc = base64.StdEncoding.EncodeToString(sshkeys.Render(keys))
	}
	return strings.NewReplacer("@ROOT@", shellQuote(root), "@KEYS@", enc, "@TYPE@", typ, "@KEY@", key).Replace(accessScript), nil
}

func shellQuote(s string) string {
	if s == "" {
		return ""
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// accessCheck is what the access script reported.
type accessCheck struct {
	// Keys are the owner's keys read back from the server.
	Keys []sshkeys.Key
	// SSHD is empty when sshd's effective settings allow opening port 22
	// (keys only, root by key only, the managed key file read); otherwise why not.
	SSHD string
}

// checkAccess reads the access script's answer: the setup key is gone, the
// key file holds exactly what was sent (and its modes are right), and
// whether sshd's settings let port 22 open.
func checkAccess(out string, keys []sshkeys.Key) (accessCheck, error) {
	var c accessCheck
	got := map[string]string{}
	sshd := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		head, rest, _ := strings.Cut(line, " ")
		if head == "sshd" {
			k, v, _ := strings.Cut(rest, " ")
			sshd[strings.ToLower(k)] = strings.TrimSpace(v)
			continue
		}
		if line != "" {
			got[head] = rest
		}
	}
	if _, ok := got["done"]; !ok {
		return c, fmt.Errorf("the access script stopped half way: %q", firstLine(out))
	}
	if got["setup-key"] != "gone" {
		return c, errors.New("the setup key is still in root's authorized_keys")
	}
	if len(keys) == 0 {
		return c, nil
	}
	raw, err := base64.StdEncoding.DecodeString(got["managed"])
	if err != nil || !bytes.Equal(raw, sshkeys.Render(keys)) {
		return c, fmt.Errorf("%s doesn't hold the keys that were written", sshkeys.File)
	}
	c.Keys = sshkeys.ParseFile(raw)
	if !sameFingerprints(c.Keys, keys) {
		return c, fmt.Errorf("%s doesn't hold the keys that were written", sshkeys.File)
	}
	if got["mode-dir"] != "drwxr-xr-x" || got["mode-file"] != "-rw-r--r--" {
		return c, fmt.Errorf("%s has modes %s and %s; sshd wants 0755 and 0644", sshkeys.File, got["mode-dir"], got["mode-file"])
	}
	if _, failed := got["sshd-error"]; failed {
		c.SSHD = "sshd -T failed"
	} else {
		c.SSHD = sshdRefuses(sshd)
	}
	return c, nil
}

func sameFingerprints(a, b []sshkeys.Key) bool {
	fp := func(ks []sshkeys.Key) []string {
		var out []string
		for _, k := range ks {
			out = append(out, k.Fingerprint)
		}
		sort.Strings(out)
		return out
	}
	return slices.Equal(fp(a), fp(b))
}

// sshdRefuses says why sshd's effective settings (sshd -T, lower-case keys)
// don't allow opening port 22 to everyone; "" when they do: no passwords,
// no keyboard-interactive, keys on, root by key only, keys only, and the
// managed key file among those read.
func sshdRefuses(s map[string]string) string {
	var bad []string
	want := func(k string, ok ...string) {
		v, set := s[k]
		if !set {
			bad = append(bad, k+" not reported")
		} else if !slices.Contains(ok, v) {
			bad = append(bad, k+" "+v)
		}
	}
	want("passwordauthentication", "no")
	want("kbdinteractiveauthentication", "no")
	want("pubkeyauthentication", "yes")
	want("permitrootlogin", "prohibit-password", "without-password")
	files := strings.Fields(s["authorizedkeysfile"])
	if !slices.Contains(files, sshkeys.Dir+"/%u") && !slices.Contains(files, sshkeys.File) {
		bad = append(bad, "authorizedkeysfile doesn't read "+sshkeys.Dir+"/%u")
	}
	if m, set := s["authenticationmethods"]; set && m != "any" {
		for _, list := range strings.Fields(m) {
			for _, method := range strings.Split(list, ",") {
				if method != "publickey" {
					bad = append(bad, "authenticationmethods "+m)
				}
			}
		}
	}
	return strings.Join(slices.Compact(bad), "; ")
}

// knownHostLines turns the host keys this setup pinned (the job's own
// known_hosts: one server, maybe hashed, under the address it connected
// to) into known_hosts lines for the names the owner uses.
func knownHostLines(raw []byte, hosts ...string) string {
	var names []string
	for _, h := range hosts {
		if h != "" {
			names = append(names, h)
		}
	}
	if len(names) == 0 {
		return ""
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "@") {
			continue // markers (@cert-authority, @revoked) are not host keys
		}
		blob, err := base64.StdEncoding.DecodeString(f[2])
		if err != nil {
			continue
		}
		pk, err := ssh.ParsePublicKey(blob)
		if err != nil || pk.Type() != f[1] {
			continue
		}
		l := strings.Join(names, ",") + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// finishAccess takes the setup's way in away and leaves the owner's: one
// command on the server (their keys in, the setup key out, both read
// back), then the Hetzner half (the setup key object deleted; port 22 open
// to everyone only when their keys are on the server and sshd takes keys
// only, closed otherwise), then what was installed is recorded. Every part
// runs even when an earlier one failed, so a failure never leaves the
// setup's access open; the first failure is returned.
func (w *Worker) finishAccess(ctx context.Context, job *Job, box *Box, m Machine, hp *hetzner.Provider, dir string, keys []sshkeys.Key, hosts []string, progress func(string)) error {
	if len(keys) > 0 {
		progress("Adding your SSH key and removing the setup key from your server")
	} else {
		progress("Removing the setup key from your server")
	}
	var check accessCheck
	serverErr := func() error {
		pub, err := os.ReadFile(filepath.Join(dir, "id_ed25519.pub"))
		if err != nil {
			return err
		}
		script, err := AccessScript(w.AccessRoot, string(pub), keys)
		if err != nil {
			return err
		}
		out, stderr, err := m.Exec(ctx, script)
		if err != nil {
			return fmt.Errorf("finish access on the server: %v %s", err, firstLine(stderr))
		}
		check, err = checkAccess(out, keys)
		return err
	}()
	open := len(keys) > 0 && serverErr == nil && check.SSHD == ""
	if len(keys) > 0 && serverErr == nil && check.SSHD != "" {
		w.Log.Warn("sshd settings keep SSH closed", "box", box.ID, "why", check.SSHD)
		progress("Your SSH key is on the server, but SSH stays closed: its SSH settings didn't check out (" + check.SSHD + ")")
	}

	closing := "Closing setup access in your Hetzner project"
	if open {
		closing += " (SSH stays open to your key only)"
	}
	progress(closing)
	pol := hetzner.AccessPolicy{SSHOpen: open}
	if hp.Key != nil {
		pol.SetupKey = hp.Key.ID
	}
	hetznerErr := hp.FinishSetupAccess(ctx, pol, func(string) {})
	if hetznerErr == nil {
		hetznerErr = hp.AccessMatches(ctx, pol)
	}
	if hetznerErr == nil {
		if err := w.Store.SetResources(ctx, job.Lease, box.ID, Resources{}); err != nil { // the SSH key is gone
			hetznerErr = err
		}
	}

	known, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	recErr := w.Store.SetAccess(ctx, job.Lease, box.ID, Access{SSHKeys: check.Keys, HostKeys: knownHostLines(known, hosts...), SSHOpen: open && hetznerErr == nil})
	for _, err := range []error{serverErr, hetznerErr, recErr} {
		if err != nil {
			return err
		}
	}
	return nil
}

// openSSH opens port 22 on a box's firewall (its labelled one only) for the
// owner's keys, with a Hetzner key the customer pasted for this job alone.
// For a box set up without SSH keys, once the owner has added some in the
// dashboard (Settings › Server access).
func (w *Worker) openSSH(ctx context.Context, job *Job, box *Box, progress func(string)) error {
	if box.Status != "active" && box.Status != "cert_pending" {
		return fmt.Errorf("the box is %s; SSH is opened only on a running box", box.Status)
	}
	token, err := w.token(job, box)
	if err != nil {
		return err
	}
	hp, err := w.hetzner(job, box, "open-ssh", token, hetzner.Config{})
	if err != nil {
		return err
	}
	progress("Opening SSH in your Hetzner project (to your SSH keys only)")
	if err := hp.OpenSSH(ctx, func(string) {}); err != nil {
		return err
	}
	if err := w.Store.SetSSHOpen(ctx, job.Lease, box.ID, true); err != nil {
		return err
	}
	progress("SSH is open: ssh root@" + w.DNS.Domain(box.Name))
	return nil
}
