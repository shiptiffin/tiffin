package cloud

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud/schema"
	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/shiptiffin/tiffin/internal/provider"
	"github.com/shiptiffin/tiffin/internal/provider/hetzner/hetznertest"
	"github.com/shiptiffin/tiffin/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

// newPub makes a public key line, as a person pastes it.
func newPub(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))) + " " + comment
}

func TestSetupKeys(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	a, b, c := newPub(t, "ann@laptop"), newPub(t, "ann@desk"), newPub(t, "work")
	keys, err := setupKeys([]SSHKeyArg{{a, "github:ann"}, {b, "github:ann"}, {c, "pasted"}, {a, "pasted"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0].Source != "github:ann" || keys[2].Source != "pasted" || !keys[2].Added.Equal(now) {
		t.Fatalf("keys %+v", keys)
	}
	if !strings.HasSuffix(keys[0].Line(), " tiffin github:ann 2026-10-11") || strings.Contains(keys[0].Line(), "laptop") {
		t.Fatalf("line %q", keys[0].Line())
	}
	if keys, err := setupKeys(nil, now); err != nil || keys != nil {
		t.Fatalf("none: %v %v", keys, err)
	}
	for _, c := range []struct {
		args []SSHKeyArg
		want string
	}{
		{[]SSHKeyArg{{a, "pasted"}, {"ssh-ed25519 nope", "pasted"}}, "SSH key 2 (pasted): not an SSH public key"},
		{[]SSHKeyArg{{`command="sh" ` + a, "pasted"}}, "options"},
		{[]SSHKeyArg{{a + "\n" + b, "pasted"}}, "one key per entry"},
		{[]SSHKeyArg{{a, "dashboard"}}, "unknown source"},
		{[]SSHKeyArg{{a, "github:-bad-"}}, "unknown source"},
		{[]SSHKeyArg{{a, "Hetzner\nssh-ed25519 x"}}, "unknown source"},
		{[]SSHKeyArg{{"-----BEGIN OPENSSH PRIVATE KEY-----", "pasted"}}, "private key"},
		{slices.Repeat([]SSHKeyArg{{a, "pasted"}}, 11), "at most 10"},
	} {
		if _, err := setupKeys(c.args, now); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}

// runAccess runs the access script on a folder standing in for a server's /.
func runAccess(t *testing.T, root, bin, setupPub string, keys []sshkeys.Key) string {
	t.Helper()
	script, err := AccessScript(root, setupPub, keys)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("access script: %v\n%s", err, out)
	}
	return string(out)
}

func TestAccessScript(t *testing.T) {
	m := newFakeMachine(t)
	setup := newPub(t, "tiffin-shop")
	setupKey := strings.Fields(setup)[1]
	theirs := newPub(t, "theirs")
	// Root's file as a person may have left it: options, no final newline,
	// a comment that mentions the setup key's body, the setup key twice.
	own := strings.Join([]string{
		theirs,
		`from="192.0.2.1, 192.0.2.2" ` + setup,
		"# old: " + setupKey,
		setup,
		`restrict ` + theirs,
	}, "\n")
	ownPath := filepath.Join(m.root, "root/.ssh/authorized_keys")
	if err := os.WriteFile(ownPath, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := setupKeys([]SSHKeyArg{{newPub(t, "a"), "pasted"}, {newPub(t, "b"), "github:ann"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out := runAccess(t, m.root, m.bin, setup, keys)
	check, err := checkAccess(out, keys)
	if err != nil || check.SSHD != "" || len(check.Keys) != 2 {
		t.Fatalf("check %+v %v\n%s", check, err, out)
	}
	want := theirs + "\n# old: " + setupKey + "\nrestrict " + theirs + "\n"
	if got := m.file("root/.ssh/authorized_keys"); got != want {
		t.Fatalf("root's file:\n%s\nwant:\n%s", got, want)
	}
	if got := m.file("etc/ssh/authorized_keys.d/root"); got != string(sshkeys.Render(keys)) {
		t.Fatalf("key file:\n%s", got)
	}
	fi, _ := os.Stat(filepath.Join(m.root, "etc/ssh/authorized_keys.d/root"))
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(ownPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("root's file mode %v", fi.Mode())
	}
	// Again: the same result, nothing else changes.
	if out2 := runAccess(t, m.root, m.bin, setup, keys); out2 != out {
		t.Fatalf("a second run differs:\n%s\n%s", out, out2)
	}
	if m.file("root/.ssh/authorized_keys") != want {
		t.Fatal("a second run changed root's file")
	}
	entries, _ := os.ReadDir(filepath.Join(m.root, "etc/ssh/authorized_keys.d"))
	for _, e := range entries {
		if e.Name() != "root" && e.Name() != ".tiffin.lock" {
			t.Fatalf("left behind: %s", e.Name())
		}
	}
	// What checkAccess refuses.
	if _, err := checkAccess(strings.Replace(out, "setup-key gone", "", 1), keys); err == nil {
		t.Fatal("no word on the setup key")
	}
	if _, err := checkAccess(strings.Replace(out, "done", "", 1), keys); err == nil {
		t.Fatal("a script that stopped half way")
	}
	other, _ := setupKeys([]SSHKeyArg{{newPub(t, "c"), "pasted"}}, time.Now())
	if _, err := checkAccess(out, other); err == nil {
		t.Fatal("other keys than were written")
	}
	// sshd too loose, or not answering: the keys stay, SSH doesn't open.
	_ = os.WriteFile(m.sshdT, []byte(strings.Replace(goodSSHD, "passwordauthentication no", "passwordauthentication yes", 1)), 0o644)
	if c, err := checkAccess(runAccess(t, m.root, m.bin, setup, keys), keys); err != nil || c.SSHD != "passwordauthentication yes" {
		t.Fatalf("loose sshd: %+v %v", c, err)
	}
	_ = os.Remove(m.sshdT)
	if c, err := checkAccess(runAccess(t, m.root, m.bin, setup, keys), keys); err != nil || c.SSHD != "sshd -T failed" {
		t.Fatalf("no sshd: %+v %v", c, err)
	}
	// No owner keys: only the setup key goes; no key file is made.
	m2 := newFakeMachine(t)
	_ = os.WriteFile(filepath.Join(m2.root, "root/.ssh/authorized_keys"), []byte(setup+"\n"), 0o600)
	if c, err := checkAccess(runAccess(t, m2.root, m2.bin, setup, nil), nil); err != nil || c.SSHD != "" || len(c.Keys) != 0 {
		t.Fatalf("no keys: %+v %v", c, err)
	}
	if m2.file("root/.ssh/authorized_keys") != "" {
		t.Fatal("the setup key stayed")
	}
	if _, err := os.Stat(filepath.Join(m2.root, "etc/ssh/authorized_keys.d")); err == nil {
		t.Fatal("a key file without keys")
	}
}

func TestSSHDRefuses(t *testing.T) {
	parse := func(s string) map[string]string {
		m := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			k, v, _ := strings.Cut(l, " ")
			m[k] = v
		}
		return m
	}
	if why := sshdRefuses(parse(goodSSHD)); why != "" {
		t.Fatal(why)
	}
	for _, c := range []struct{ from, to, want string }{
		{"permitrootlogin without-password", "permitrootlogin yes", "permitrootlogin yes"},
		{"kbdinteractiveauthentication no", "kbdinteractiveauthentication yes", "kbdinteractiveauthentication yes"},
		{"pubkeyauthentication yes", "pubkeyauthentication no", "pubkeyauthentication no"},
		{"authenticationmethods publickey", "authenticationmethods publickey,password", "authenticationmethods publickey,password"},
		{"/etc/ssh/authorized_keys.d/%u", "/etc/ssh/other", "authorizedkeysfile doesn't read"},
		{"passwordauthentication no\n", "", "passwordauthentication not reported"},
	} {
		if why := sshdRefuses(parse(strings.Replace(goodSSHD, c.from, c.to, 1))); !strings.Contains(why, c.want) {
			t.Errorf("%q → %q: %q", c.from, c.to, why)
		}
	}
	ok := strings.Replace(goodSSHD, "without-password", "prohibit-password", 1)
	ok = strings.Replace(ok, "authenticationmethods publickey", "authenticationmethods any", 1)
	if why := sshdRefuses(parse(ok)); why != "" {
		t.Fatal(why)
	}
}

func TestKnownHostLines(t *testing.T) {
	a, b := newPub(t, ""), newPub(t, "")
	raw := strings.Join([]string{
		"|1|c2FsdA==|aGFzaA== " + a,
		"203.0.113.5 " + a,
		"@cert-authority * " + b,
		"203.0.113.5 ssh-ed25519 bm90IGEga2V5",
		"# a comment",
		"[203.0.113.5]:22 " + b,
	}, "\n")
	got := knownHostLines([]byte(raw), "shop.shiptiffin.app", "203.0.113.5")
	want := "shop.shiptiffin.app,203.0.113.5 " + strings.TrimSpace(a) + "\nshop.shiptiffin.app,203.0.113.5 " + strings.TrimSpace(b) + "\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if knownHostLines(nil, "shop.shiptiffin.app") != "" || knownHostLines([]byte(raw)) != "" {
		t.Fatal("nothing in, nothing out")
	}
}

// keyArgs are two pasted keys and one from GitHub.
func keyArgs(t *testing.T) []SSHKeyArg {
	return []SSHKeyArg{{newPub(t, "ann@laptop"), "pasted"}, {newPub(t, "ann@desk"), "github:ann"}}
}

func TestProvisionWithSSHKeys(t *testing.T) {
	h := newHarness(t)
	args := keyArgs(t)
	h.addBox("box_k1", "keys")
	id := h.enqueue("box_k1", "provision", hetznertest.Token, ProvisionArgs{Name: "keys", ServerType: "cax11", Location: "fsn1", SSHKeys: args})
	h.runNext()
	j := h.job(id)
	if j.Status != "done" {
		t.Fatalf("job: %s", j.text())
	}
	if want := "Making your one-time sign-in link (it works once, for 24 hours)\nAdding your SSH key and removing the setup key from your server\nClosing setup access in your Hetzner project (SSH stays open to your key only)\n"; !strings.Contains(j.text(), want) {
		t.Fatalf("steps:\n%s", j.text())
	}
	// On the server: the owner's keys in the managed file, the setup key gone.
	file := h.machine.file("etc/ssh/authorized_keys.d/root")
	installed := sshkeys.ParseFile([]byte(file))
	if len(installed) != 2 || installed[0].Source != "pasted" || installed[1].Source != "github:ann" {
		t.Fatalf("key file:\n%s", file)
	}
	if strings.Contains(h.machine.file("root/.ssh/authorized_keys"), "tiffin-keys") {
		t.Fatal("the setup key is still on the server")
	}
	// Hetzner: no key object, one SSH rule, from anywhere.
	if _, _, _, keys := h.hz.Count(); keys != 0 {
		t.Fatalf("SSH key objects: %d", keys)
	}
	var ssh []string
	for _, r := range h.hz.Firewall().Rules {
		if r.Port != nil && *r.Port == "22" {
			ssh = append(ssh, strings.Join(r.SourceIPs, ","))
		}
	}
	if !slices.Equal(ssh, []string{"0.0.0.0/0,::/0"}) {
		t.Fatalf("SSH rules %v", ssh)
	}
	// Recorded: the keys as read back, SSH open.
	var rec []sshkeys.Key
	if err := json.Unmarshal([]byte(h.str(`select ssh_keys::text from cloud_boxes where id = 'box_k1'`)), &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec) != 2 || rec[0].Fingerprint != installed[0].Fingerprint || rec[1].Source != "github:ann" || rec[0].Type != "ssh-ed25519" {
		t.Fatalf("recorded %+v", rec)
	}
	if h.str(`select ssh_open::text from cloud_boxes where id = 'box_k1'`) != "true" {
		t.Fatal("ssh_open")
	}
	// A resize from the account leaves the firewall alone.
	before := fmt.Sprint(h.hz.Firewall().Rules)
	rid := h.enqueue("box_k1", "resize", hetznertest.Token, ResizeArgs{ServerType: "cax21"})
	h.runNext()
	if j := h.job(rid); j.Status != "done" || fmt.Sprint(h.hz.Firewall().Rules) != before {
		t.Fatalf("resize: %s\nrules %v", j.text(), h.hz.Firewall().Rules)
	}
}

// A bad key stops the setup before anything is made.
func TestProvisionRefusesABadSSHKey(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_k2", "badkey")
	args := append(keyArgs(t), SSHKeyArg{Key: "ssh-rsa AAAAB3NzaC1yc2E= short", Source: "pasted"})
	id := h.enqueue("box_k2", "provision", hetznertest.Token, ProvisionArgs{Name: "badkey", ServerType: "cax11", Location: "fsn1", SSHKeys: args})
	h.runNext()
	j := h.job(id)
	if j.Status != "failed" || j.Token != nil || !strings.Contains(*j.Error, "SSH key 3 (pasted)") {
		t.Fatalf("job: %s", j.text())
	}
	if n := h.num(`select count(*) from cloud_hetzner_calls where box_id = 'box_k2'`); n != 0 || len(h.hz.Mutations) != 0 {
		t.Fatalf("Hetzner was called %d times", n)
	}
	if b := h.box("box_k2"); b.Generation != 0 || b.Status != "paid" {
		t.Fatalf("box %+v", b)
	}
}

// sshd that still takes passwords (or doesn't answer): the keys are
// installed, but port 22 stays closed.
func TestLooseSSHDKeepsSSHClosed(t *testing.T) {
	h := newHarness(t)
	_ = os.WriteFile(h.machine.sshdT, []byte(strings.Replace(goodSSHD, "passwordauthentication no", "passwordauthentication yes", 1)), 0o644)
	b := h.provisioned("box_k3", "loose", ProvisionArgs{SSHKeys: keyArgs(t)})
	j := h.job(h.num(`select id from cloud_jobs where box_id = 'box_k3'`))
	if !strings.Contains(j.text(), "SSH stays closed: its SSH settings didn't check out (passwordauthentication yes)\nClosing setup access in your Hetzner project\n") {
		t.Fatalf("steps:\n%s", j.text())
	}
	for _, r := range h.hz.Firewall().Rules {
		if r.Port != nil && *r.Port == "22" {
			t.Fatalf("SSH opened: %+v", r)
		}
	}
	if b.Status != "active" || h.str(`select ssh_open::text from cloud_boxes where id = 'box_k3'`) != "false" ||
		h.num(`select jsonb_array_length(ssh_keys) from cloud_boxes where id = 'box_k3'`) != 2 {
		t.Fatalf("box %+v", b)
	}
}

// The sign-in link can't be made or saved: the setup's access still goes
// (and the owner's keys still go in), then the box needs a person.
func TestLinkFailureStillFinishesAccess(t *testing.T) {
	for _, c := range []string{"make", "save"} {
		t.Run(c, func(t *testing.T) {
			h := newHarness(t)
			if c == "make" {
				h.machine.hook = func(script string) (string, bool) {
					return "", strings.Contains(script, "bootstrap-link")
				}
			} else {
				fs := &faultStore{PG: h.store}
				fs.setSignin = func(context.Context, Lease, string, string, time.Time) error { return errDBDown }
				h.w.Store = fs
			}
			h.addBox("box_l1", "link")
			id := h.enqueue("box_l1", "provision", hetznertest.Token, ProvisionArgs{Name: "link", ServerType: "cax11", Location: "fsn1", SSHKeys: keyArgs(t)})
			h.runNext()
			j := h.job(id)
			if j.Status != "failed" || !strings.Contains(j.text(), "Closing setup access in your Hetzner project (SSH stays open to your key only)") || !strings.Contains(j.text(), "data are kept") {
				t.Fatalf("job: %s", j.text())
			}
			if strings.Contains(h.machine.file("root/.ssh/authorized_keys"), "tiffin-link") || len(sshkeys.ParseFile([]byte(h.machine.file("etc/ssh/authorized_keys.d/root")))) != 2 {
				t.Fatal("the server's keys weren't finished")
			}
			if _, _, _, keys := h.hz.Count(); keys != 0 {
				t.Fatal("the setup key object is still there")
			}
			if b := h.box("box_l1"); b.Attention == "" || b.InstalledAt == nil {
				t.Fatalf("box %+v", b)
			}
			if h.str(`select ssh_open::text from cloud_boxes where id = 'box_l1'`) != "true" {
				t.Fatal("ssh_open")
			}
		})
	}
}

// The server's half failed (we can't tell the setup key is gone): port 22
// stays closed, the Hetzner half still goes, the box needs a person.
func TestServerHalfFailureKeepsSSHClosed(t *testing.T) {
	h := newHarness(t)
	h.machine.hook = func(script string) (string, bool) {
		return "setup-key still-there\n", strings.Contains(script, "authorized_keys")
	}
	h.addBox("box_l2", "half")
	id := h.enqueue("box_l2", "provision", hetznertest.Token, ProvisionArgs{Name: "half", ServerType: "cax11", Location: "fsn1", SSHKeys: keyArgs(t)})
	h.runNext()
	if j := h.job(id); j.Status != "failed" || !strings.Contains(j.text(), "Closing setup access in your Hetzner project\n") {
		t.Fatalf("job: %s", j.text())
	}
	for _, r := range h.hz.Firewall().Rules {
		if r.Port != nil && *r.Port == "22" {
			t.Fatalf("SSH open: %+v", r)
		}
	}
	if _, _, _, keys := h.hz.Count(); keys != 0 || h.box("box_l2").Attention == "" {
		t.Fatal("the setup key object must go and the box be flagged")
	}
}

// open-ssh: port 22 opens for the owner's keys on a box set up without
// them, with a key pasted for the job; other rules stay; the key goes.
func TestOpenSSHJob(t *testing.T) {
	h := newHarness(t)
	h.provisioned("box_o1", "later", ProvisionArgs{})
	h.hz.AddFirewallRule(schema.FirewallRule{Direction: "in", Protocol: "tcp", Port: ptr("8080"), SourceIPs: []string{"192.0.2.0/24"}})
	id := h.enqueue("box_o1", "open-ssh", "", nil)
	h.runNext()
	if j := h.job(id); j.Status != "failed" || !strings.Contains(*j.Error, "paste it again") {
		t.Fatalf("without a key: %s", j.text())
	}
	id = h.enqueue("box_o1", "open-ssh", hetznertest.Token, nil)
	h.runNext()
	j := h.job(id)
	if j.Status != "done" || j.Token != nil || !strings.Contains(j.text(), "Opening SSH in your Hetzner project (to your SSH keys only)\nSSH is open: ssh root@later.shiptiffin.app") {
		t.Fatalf("job: %s", j.text())
	}
	var ssh, other int
	for _, r := range h.hz.Firewall().Rules {
		if r.Port != nil && *r.Port == "22" && strings.Join(r.SourceIPs, ",") == "0.0.0.0/0,::/0" {
			ssh++
		}
		if r.Port != nil && *r.Port == "8080" {
			other++
		}
	}
	if ssh != 1 || other != 1 || h.str(`select ssh_open::text from cloud_boxes where id = 'box_o1'`) != "true" {
		t.Fatalf("rules %+v", h.hz.Firewall().Rules)
	}
	// Again: nothing changes. A box being deleted is refused.
	before := len(h.hz.Mutations)
	h.enqueue("box_o1", "open-ssh", hetznertest.Token, nil)
	h.runNext()
	if len(h.hz.Mutations) != before {
		t.Fatalf("a second open changed things: %v", h.hz.Mutations[before:])
	}
	h.exec(`update cloud_boxes set status = 'deleting' where id = 'box_o1'`)
	id = h.enqueue("box_o1", "open-ssh", hetznertest.Token, nil)
	h.runNext()
	if j := h.job(id); j.Status != "failed" || !strings.Contains(*j.Error, "only on a running box") {
		t.Fatalf("deleting box: %s", j.text())
	}
}

// A setup again starts with nothing recorded about the owner's access.
func TestNewGenerationForgetsAccess(t *testing.T) {
	h := newHarness(t)
	h.addBox("box_g1", "gen")
	h.exec(`update cloud_boxes set status = 'failed', ssh_keys = '[{"type": "ssh-ed25519"}]', host_keys = 'x', ssh_open = true where id = 'box_g1'`)
	h.w.Install = func(context.Context, provider.Machine, string, install.Options, func(string)) (*install.Result, error) {
		return nil, errors.New("apt failed")
	}
	h.enqueue("box_g1", "provision", hetznertest.Token, ProvisionArgs{Name: "gen", ServerType: "cax11", Location: "fsn1"})
	h.runNext()
	if h.str(`select ssh_keys::text from cloud_boxes where id = 'box_g1'`) != "[]" || h.str(`select host_keys from cloud_boxes where id = 'box_g1'`) != "" ||
		h.str(`select ssh_open::text from cloud_boxes where id = 'box_g1'`) != "false" {
		t.Fatal("the earlier setup's access is still recorded")
	}
}

func ptr[T any](v T) *T { return &v }
