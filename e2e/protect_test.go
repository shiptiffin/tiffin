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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/edge"
)

// TestProtect is the M11 acceptance test on a fresh box, through the CLI,
// curl inside the box and HTTPS from the Mac through the forwarded port:
//
//   - the firewall leaves SSH and the edge ports open and drops the rest;
//   - a flood gets 429s, then recovers; a flood that ignores 429s is banned;
//   - a sign-in brute force gets a CrowdSec ban, is blocked, and unban works;
//   - under attack, plain curl gets the challenge, a scripted solver gets
//     through, bearer-token clients and the dashboard are untouched, and
//     the switch turns itself off;
//   - the opt-in WAF blocks an XSS probe;
//   - legitimate traffic and the dashboard keep working throughout.
//
// "Attackers" live in a network namespace on the box with TEST-NET-3
// addresses (203.0.113.0/24): real sources CrowdSec does not whitelist,
// arriving on an interface the firewall filters.
//
// TIFFIN_E2E_INSTANCE (+ TIFFIN_E2E_PORT, TIFFIN_E2E_CONFIG) reuses an existing
// box instead of creating one.
func TestProtect(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	dir := t.TempDir()
	cli := buildTiffin(t, dir, "", "")
	instance, port, config := os.Getenv("TIFFIN_E2E_INSTANCE"), 0, os.Getenv("TIFFIN_E2E_CONFIG")
	fresh := instance == ""
	if fresh {
		instance, port, config = newName(), freePort(t), filepath.Join(dir, "config")
	} else {
		port, _ = strconv.Atoi(os.Getenv("TIFFIN_E2E_PORT"))
	}
	disk := newDiskName()
	env := append(os.Environ(), "TIFFIN_CONFIG_DIR="+config, "TIFFIN_LIMA_INSTANCE="+instance, "TIFFIN_LIMA_DISK="+disk,
		fmt.Sprintf("TIFFIN_LIMA_PORT=%d", port), "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	if fresh {
		t.Cleanup(func() {
			if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
				_ = exec.Command("limactl", "delete", "-f", instance).Run()
				_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
			}
		})
	}
	run := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(cli, args...)
		cmd.Env = env
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("tiffin %v: %v", args, err)
		}
		return code, string(out)
	}
	runJSON := func(v any, args ...string) {
		t.Helper()
		code, out := run(args...)
		if code != 0 {
			t.Fatalf("tiffin %v: exit %d\n%s", args, code, out)
		}
		if err := json.Unmarshal([]byte(out), v); err != nil {
			t.Fatalf("tiffin %v: %v\n%s", args, err, out)
		}
	}
	box := func(script string) string {
		t.Helper()
		out, err := exec.Command("limactl", "shell", instance, "--", "sudo", "bash", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("box: %v\n%s\n%s", err, script, out)
		}
		return strings.TrimSpace(string(out))
	}
	phase := func(name string, since time.Time) {
		t.Logf("PHASE %-22s %s", name, time.Since(since).Round(100*time.Millisecond))
	}

	if fresh {
		p := time.Now()
		linux := buildTiffin(t, dir, "linux", "0.0.11-protect")
		if code, out := run("up", "--binary", linux); code != 0 {
			t.Fatalf("up: exit %d\n%s", code, out)
		}
		phase("up", p)
	}

	// The Mac's client: the box CA, any host dialled at the forwarded port.
	caPEM, err := os.ReadFile(filepath.Join(config, "boxes", "local", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	mac := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	hostURL := func(h string) string { return fmt.Sprintf("https://%s.tiffin.localhost:%d", h, port) }
	fetch := func(method, u string, hdr map[string]string, body string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, u, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := mac.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, u, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	legit := func(when string) {
		t.Helper()
		if r, _ := fetch("GET", hostURL("dashboard")+"/", nil, ""); r.StatusCode != 200 {
			t.Errorf("%s: dashboard from the Mac: %d", when, r.StatusCode)
		}
		var st map[string]any
		runJSON(&st, "status")
		if st["ok"] != true {
			t.Errorf("%s: status not ok: %v", when, st)
		}
	}

	// ---- status ----
	p := time.Now()
	var st struct {
		Edge     struct{ Applied bool }
		CrowdSec struct{ Installed, Running, Enforced bool }
		Firewall struct {
			Active    bool
			OpenPorts []int
		}
	}
	runJSON(&st, "protect", "status")
	if !st.Edge.Applied || !st.CrowdSec.Running || !st.CrowdSec.Enforced || !st.Firewall.Active {
		t.Fatalf("protection not fully up: %+v", st)
	}
	legit("start")
	phase("status", p)

	// ---- attacker namespace ----
	box(`ip netns del probe 2>/dev/null; ip link del vprobe0 2>/dev/null; true
ip netns add probe && ip link add vprobe0 type veth peer name vprobe1 && ip link set vprobe1 netns probe
ip addr add 203.0.113.1/24 dev vprobe0 && ip link set vprobe0 up
ip netns exec probe sh -c 'ip addr add 203.0.113.7/24 dev vprobe1; ip addr add 203.0.113.8/24 dev vprobe1; ip link set vprobe1 up; ip link set lo up'
(setsid python3 -m http.server 9999 --bind 0.0.0.0 >/dev/null 2>&1 &) ; sleep 1`)
	t.Cleanup(func() {
		_ = exec.Command("limactl", "shell", instance, "--", "sudo", "bash", "-c",
			"ip netns del probe; ip link del vprobe0; rm -f /etc/tiffin/firewall-interfaces; pkill -f 'http.server 9999'; true").Run()
	})
	// from runs curl in the namespace as src, against the box at 203.0.113.1.
	from := func(src, args string) string {
		return box(fmt.Sprintf(`ip netns exec probe curl -sk --interface %s --max-time 3 -o /dev/null -w '%%{http_code}\n' --resolve victim.tiffin.localhost:8443:203.0.113.1 %s || true`, src, args))
	}

	// ---- firewall ----
	p = time.Now()
	if got := box(`ip netns exec probe curl -s --max-time 3 -o /dev/null -w '%{http_code}' http://203.0.113.1:9999/ || true`); got != "200" {
		t.Fatalf("control: an unfiltered interface reaches port 9999: %q", got)
	}
	box(`echo vprobe0 > /etc/tiffin/firewall-interfaces && tiffin provision >/tmp/provision.log 2>&1`)
	if got := box(`ip netns exec probe curl -s --max-time 3 -o /dev/null -w '%{http_code}' http://203.0.113.1:9999/ || true`); got != "000" {
		t.Errorf("firewall: port 9999 on a filtered interface must be dropped, got %q", got)
	}
	if got := box(`ip netns exec probe timeout 3 bash -c '</dev/tcp/203.0.113.1/22' && echo open || echo closed`); got != "open" {
		t.Errorf("firewall: SSH must stay open: %s", got)
	}
	if got := from("203.0.113.7", "https://victim.tiffin.localhost:8443/"); got != "404" {
		t.Errorf("firewall: the edge must stay open: %s", got)
	}
	if out, err := exec.Command("limactl", "shell", instance, "--", "true").CombinedOutput(); err != nil {
		t.Fatalf("firewall cut off Lima's SSH: %v %s", err, out)
	}
	legit("firewall")
	phase("firewall", p)

	// ---- flood: 429s, then recovery ----
	p = time.Now()
	codes := box(`ip netns exec probe curl -sk --interface 203.0.113.7 -o /dev/null -w '%{http_code}\n' --resolve victim.tiffin.localhost:8443:203.0.113.1 $(printf 'https://victim.tiffin.localhost:8443/flood %.0s' $(seq 1 340))`)
	n429 := strings.Count(codes, "429")
	if n429 < 20 || strings.Count(codes, "404") < 250 {
		t.Fatalf("flood: want ~300 served then 429s, got %d 429s of %d", n429, len(strings.Fields(codes)))
	}
	t.Logf("flood: %d of 340 got 429", n429)
	legit("during flood")
	time.Sleep(11 * time.Second)
	if got := from("203.0.113.7", "https://victim.tiffin.localhost:8443/"); got != "404" {
		t.Errorf("after the window the flooder is served again: %s", got)
	}
	phase("flood", p)

	waitDecision := func(ip, scenario string) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			var ds []struct{ Value, Scenario string }
			runJSON(&ds, "protect", "decisions")
			for _, d := range ds {
				if d.Value == ip && strings.Contains(d.Scenario, scenario) {
					return
				}
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("no %s decision for %s", scenario, ip)
	}
	waitFrom := func(src, want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		got := ""
		for time.Now().Before(deadline) {
			if got = from(src, "https://victim.tiffin.localhost:8443/"); got == want {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s: got %s, want %s", src, got, want)
	}

	// ---- brute force: CrowdSec ban, blocked, unban ----
	p = time.Now()
	codes = box(`for i in $(seq 1 30); do ip netns exec probe curl -sk --interface 203.0.113.7 -o /dev/null -w '%{http_code} ' --resolve victim.tiffin.localhost:8443:203.0.113.1 -X POST -H 'Content-Type: application/json' -d '{"email":"a@b.c","password":"guess'$i'"}' https://victim.tiffin.localhost:8443/api/auth/sign-in/email; done`)
	if !strings.Contains(codes, "429") {
		t.Errorf("brute force: the sign-in limit never kicked in: %s", codes)
	}
	waitDecision("203.0.113.7", "tiffin/http-auth-bruteforce")
	waitFrom("203.0.113.7", "403")
	if r, body := fetch("GET", hostURL("dashboard")+"/", nil, ""); r.StatusCode != 200 || strings.Contains(body, "blocked for now") {
		t.Errorf("the ban must not touch others: %d", r.StatusCode)
	}
	var ub struct{ Deleted int }
	runJSON(&ub, "protect", "unban", "--ip", "203.0.113.7")
	if ub.Deleted < 1 {
		t.Errorf("unban deleted %d", ub.Deleted)
	}
	waitFrom("203.0.113.7", "404")
	legit("brute force")
	phase("brute force", p)

	// ---- a flood that ignores 429s gets banned ----
	p = time.Now()
	box(`ip netns exec probe curl -sk --interface 203.0.113.8 -o /dev/null --resolve victim.tiffin.localhost:8443:203.0.113.1 $(printf 'https://victim.tiffin.localhost:8443/x %.0s' $(seq 1 520)) || true`)
	waitDecision("203.0.113.8", "tiffin/http-rate-limit-ignored")
	waitFrom("203.0.113.8", "403")
	runJSON(&ub, "protect", "unban", "--ip", "203.0.113.8")
	phase("flood ban", p)

	// ---- manual ban ----
	p = time.Now()
	var bans []struct{ Value string }
	runJSON(&bans, "protect", "ban", "--ip", "203.0.113.7", "--duration", "5m", "--reason", "e2e")
	if len(bans) != 1 || bans[0].Value != "203.0.113.7" {
		t.Errorf("ban: %+v", bans)
	}
	waitFrom("203.0.113.7", "403")
	if code, out := run("protect", "ban", "--ip", "127.0.0.1"); code == 0 {
		t.Errorf("banning loopback must be refused: %s", out)
	}
	runJSON(&ub, "protect", "unban", "--ip", "203.0.113.7")
	waitFrom("203.0.113.7", "404")
	phase("manual ban", p)

	// ---- under attack ----
	p = time.Now()
	var ua struct {
		UnderAttack struct {
			On          bool
			MinutesLeft int
		}
	}
	runJSON(&ua, "protect", "under-attack", "--on", "--minutes", "1")
	if !ua.UnderAttack.On {
		t.Fatalf("under attack: %+v", ua)
	}
	if got := box(`curl -sk -o /dev/null -w '%{http_code}' --resolve victim.tiffin.localhost:8443:127.0.0.1 https://victim.tiffin.localhost:8443/`); got != "403" {
		t.Errorf("plain curl in the box under attack: %s", got)
	}
	ua1 := map[string]string{"User-Agent": "e2e-solver/1.0"}
	r, page := fetch("GET", hostURL("victim")+"/deep/link?q=1", ua1, "")
	if r.StatusCode != 403 || r.Header.Get("X-Tiffin-Challenge") != "required" || !strings.Contains(page, "Just a quick check") {
		t.Fatalf("challenge from the Mac: %d %q", r.StatusCode, r.Header)
	}
	m := regexp.MustCompile(`name="token" value="([0-9a-f.]+)"`).FindStringSubmatch(page)
	bits := regexp.MustCompile(`data-bits="(\d+)"`).FindStringSubmatch(page)
	if m == nil || bits == nil {
		t.Fatal("challenge page lacks a token")
	}
	diff, _ := strconv.Atoi(bits[1])
	solveStart := time.Now()
	nonce := edge.SolveChallenge(m[1], diff)
	t.Logf("solved %d-bit challenge in Go in %s", diff, time.Since(solveStart).Round(time.Millisecond))
	form := url.Values{"token": {m[1]}, "nonce": {nonce}, "next": {"/deep/link?q=1"}}.Encode()
	r, _ = fetch("POST", hostURL("victim")+edge.ChallengePath, map[string]string{"User-Agent": "e2e-solver/1.0", "Content-Type": "application/x-www-form-urlencoded"}, form)
	if r.StatusCode != 303 || r.Header.Get("Location") != "/deep/link?q=1" {
		t.Fatalf("solution: %d %v", r.StatusCode, r.Header)
	}
	var cookie string
	for _, c := range r.Cookies() {
		if strings.Contains(c.Name, "clearance") {
			cookie = c.Name + "=" + c.Value
		}
	}
	if r, body := fetch("GET", hostURL("victim")+"/deep/link?q=1", map[string]string{"User-Agent": "e2e-solver/1.0", "Cookie": cookie}, ""); r.StatusCode != 404 || !strings.Contains(body, "Nothing here") {
		t.Errorf("cleared client must reach the host's own response: %d", r.StatusCode)
	}
	if r, _ := fetch("GET", hostURL("victim")+"/api/x", map[string]string{"Authorization": "Bearer xyz"}, ""); r.StatusCode != 404 {
		t.Errorf("bearer clients are not challenged: %d", r.StatusCode)
	}
	legit("under attack")
	// It turns itself off.
	deadline := time.Now().Add(90 * time.Second)
	for {
		runJSON(&ua, "protect", "status")
		if !ua.UnderAttack.On {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("under attack did not turn itself off")
		}
		time.Sleep(5 * time.Second)
	}
	for i := 0; ; i++ {
		got := box(`curl -sk -o /dev/null -w '%{http_code}' --resolve victim.tiffin.localhost:8443:127.0.0.1 https://victim.tiffin.localhost:8443/`)
		if got == "404" {
			break
		}
		if i == 10 {
			t.Errorf("after under attack the challenge must be gone: %s", got)
			break
		}
		time.Sleep(time.Second)
	}
	phase("under attack", p)

	// ---- WAF (opt-in) ----
	p = time.Now()
	var ws struct{ Effective struct{ WAF bool } }
	runJSON(&ws, "protect", "set", "--body", `{"waf":true}`)
	if !ws.Effective.WAF {
		t.Fatalf("waf on: %+v", ws)
	}
	if r, body := fetch("GET", hostURL("victim")+"/search?q="+url.QueryEscape("<script>alert(1)</script>"), nil, ""); r.StatusCode != 403 || !strings.Contains(body, "firewall") {
		t.Errorf("WAF must block an XSS probe: %d", r.StatusCode)
	}
	if r, _ := fetch("GET", hostURL("victim")+"/search?q=shoes", nil, ""); r.StatusCode != 404 {
		t.Errorf("WAF must pass ordinary requests: %d", r.StatusCode)
	}
	runJSON(&ws, "protect", "set", "--body", `{"waf":false}`)
	legit("waf")
	phase("waf", p)

	// Restore the firewall to the box's own interfaces.
	box(`rm -f /etc/tiffin/firewall-interfaces && tiffin provision >/tmp/provision.log 2>&1`)
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}
