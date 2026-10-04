//go:build e2e

package e2e

import (
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmail is the email acceptance test, through the CLI on a fresh box:
// send via the API and via SMTP_URL from inside the box → both in the dev
// inbox (links extracted, HTML sanitised) → configure a relay pointing at a
// test SMTP sink on the box → mail is delivered there and not captured →
// a hard bounce is suppressed and later mail to it is not sent → manual
// suppression is honoured over SMTP too → preview senders still go to the
// inbox → removing the relay returns to the inbox.
func TestEmail(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	p := time.Now()
	b := newCLIBox(t, "email", "shop")
	phase("up", p)

	p = time.Now()
	b.apply("email", `{"project":"shop","services":{"email":{}}}`)
	b.waitReady("service/email")
	if st := b.ok("email", "status"); st["mode"] != "inbox" {
		t.Fatalf("status: %v", st)
	}
	phase("apply", p)

	// ---- the dev inbox: API and SMTP_URL ----
	p = time.Now()
	res := b.ok("email", "send", "shop", "--to", "ada@example.com", "--subject", "Verify your email",
		"--text", "Open https://shop.tiffin.localhost/verify?t=abc", "--html", `<p>Hi <a href="https://shop.tiffin.localhost/verify?t=abc">verify</a></p><script>steal()</script>`)
	if res["delivery"] != "inbox" || res["status"] != "captured" {
		t.Fatalf("send: %v", res)
	}
	msg := b.ok("email", "messages", "get", "shop", res["id"].(string))
	if links, _ := msg["links"].([]any); len(links) != 1 || links[0] != "https://shop.tiffin.localhost/verify?t=abc" ||
		strings.Contains(msg["html"].(string), "script") || msg["from"] != "shop@tiffin.localhost" {
		t.Fatalf("message: %v", msg)
	}
	smtp := b.ok("email", "smtp", "shop")
	smtpURL, _ := smtp["SMTP_URL"].(string)
	if !strings.HasPrefix(smtpURL, "smtp://shop:") || smtp["EMAIL_FROM"] != "shop@tiffin.localhost" {
		t.Fatalf("smtp env: %v", smtp)
	}
	sendSMTP := func(u, to, subject string) string {
		return b.inBox(`printf 'To: ` + to + `\r\nSubject: ` + subject + `\r\n\r\nhello\r\n' > /tmp/m.eml
curl -s --url '` + u + `' --mail-from app@shop.test --mail-rcpt ` + to + ` --upload-file /tmp/m.eml -w '%{response_code}' || true`)
	}
	if code := sendSMTP(smtpURL, "bob@example.com", "via SMTP_URL"); code != "250" {
		t.Fatalf("SMTP submission: %q", code)
	}
	inbox := b.list("email", "messages", "list", "shop")
	if len(inbox) != 2 || inbox[0]["subject"] != "via SMTP_URL" || inbox[0]["source"] != "smtp" {
		t.Fatalf("inbox: %v", inbox)
	}
	if l := b.list("email", "messages", "list", "shop", "--q", "verify"); len(l) != 1 {
		t.Fatalf("search: %v", l)
	}
	phase("dev inbox", p)

	// ---- a relay: the test sink on the box ----
	p = time.Now()
	sink := filepath.Join(b.dir, "smtpsink")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", sink, "./e2e/smtpsink")
	build.Dir, build.Env = RepoRoot(), append(b.env, "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+HostArch())
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build smtpsink: %v\n%s", err, out)
	}
	if out, err := exec.Command("limactl", "copy", sink, b.instance+":/tmp/smtpsink").CombinedOutput(); err != nil {
		t.Fatalf("copy smtpsink: %v\n%s", err, out)
	}
	b.inBox(`sudo systemd-run --unit e2e-smtpsink /tmp/smtpsink -addr 127.0.0.1:2626 -dir /tmp/sink && sleep 1`)
	count := func() string { return b.inBox(`ls /tmp/sink 2>/dev/null | grep -c '\.eml$' || true`) }

	st := b.ok("email", "relay", "set", "--host", "127.0.0.1", "--port", "2626", "--tls", "none", "--username", "sink", "--password", "sinkpass")
	if st["mode"] != "relay" {
		t.Fatalf("relay set: %v", st)
	}
	if r := b.ok("email", "relay", "test", "--to", "ops@example.com"); r["ok"] != true {
		t.Fatalf("relay test: %v", r)
	}
	res = b.ok("email", "send", "shop", "--to", "carol@example.com", "--subject", "real mail", "--text", "hi carol")
	if res["delivery"] != "relay" || res["status"] != "queued" {
		t.Fatalf("send with relay: %v", res)
	}
	waitFor(t, "relay delivery", func() bool {
		m := b.ok("email", "messages", "get", "shop", res["id"].(string))
		return m["status"] == "sent"
	})
	if c := count(); c != "2" {
		t.Fatalf("sink has %s messages, want 2 (test + real)", c)
	}
	if got := b.inBox(`grep -l 'Subject: real mail' /tmp/sink/*.eml | wc -l`); got != "1" {
		t.Fatalf("real mail not at the sink")
	}
	if l := b.list("email", "messages", "list", "shop"); len(l) != 2 {
		t.Fatalf("relayed mail must not be in the inbox: %d messages", len(l))
	}
	phase("relay", p)

	// ---- suppression ----
	p = time.Now()
	res = b.ok("email", "send", "shop", "--to", "bounce@example.com", "--subject", "will bounce", "--text", "x")
	waitFor(t, "bounce", func() bool {
		m := b.ok("email", "messages", "get", "shop", res["id"].(string))
		return m["status"] == "failed"
	})
	sup := b.list("email", "suppressions", "list", "shop")
	if len(sup) != 1 || sup[0]["address"] != "bounce@example.com" || sup[0]["reason"] != "bounce" {
		t.Fatalf("suppressions after bounce: %v", sup)
	}
	if r := b.ok("email", "send", "shop", "--to", "bounce@example.com", "--subject", "again", "--text", "x"); r["status"] != "suppressed" {
		t.Fatalf("send to bounced address: %v", r)
	}
	b.ok("email", "suppressions", "add", "shop", "--address", "dave@example.com", "--reason", "unsubscribe")
	r := b.ok("email", "send", "shop", "--to", "dave@example.com", "--to", "erin@example.com", "--subject", "partly", "--text", "x")
	if s, _ := r["suppressed"].([]any); len(s) != 1 || s[0] != "dave@example.com" {
		t.Fatalf("partial suppression: %v", r)
	}
	if code := sendSMTP(smtpURL, "dave@example.com", "smtp to suppressed"); code == "250" {
		t.Fatalf("SMTP to a suppressed address was accepted")
	}
	phase("suppression", p)

	// ---- previews always land in the inbox; removing the relay too ----
	p = time.Now()
	u, _ := url.Parse(smtpURL)
	pw, _ := u.User.Password()
	u.User = url.UserPassword("shop+feature-x", pw)
	before := count()
	if code := sendSMTP(u.String(), "someone@example.com", "from a preview"); code != "250" {
		t.Fatalf("preview SMTP: %q", code)
	}
	if l := b.list("email", "messages", "list", "shop", "--q", "from a preview"); len(l) != 1 || !strings.Contains(l[0]["reason"].(string), "preview") {
		t.Fatalf("preview mail: %v", l)
	}
	b.ok("email", "relay", "delete")
	if r := b.ok("email", "send", "shop", "--to", "frank@example.com", "--subject", "after relay removed", "--text", "x"); r["delivery"] != "inbox" {
		t.Fatalf("after relay delete: %v", r)
	}
	time.Sleep(2 * time.Second)
	if after := count(); after != before {
		t.Fatalf("sink grew from %s to %s after relay removal/preview", before, after)
	}
	phase("preview+inbox", p)
	t.Logf("TOTAL email acceptance: %s", time.Since(start).Round(time.Second))
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Second)
	}
}
