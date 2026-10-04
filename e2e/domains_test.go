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
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDomains is the M12 domains acceptance test on a box that believes it
// is a public server: Pebble (Let's Encrypt's test CA) and its DNS server
// run inside the VM, and the service gets --tls acme --public-ip 127.0.0.1
// through a systemd drop-in. Through the CLI it checks:
//
//   - zero setup: the box takes 127-0-0-1.sslip.io and the dashboard gets a
//     certificate from the ACME CA; apps get theirs on the first visit; the
//     ask gate refuses names the box does not serve;
//   - a custom domain: waiting_for_dns ("points to 10.9.9.9, not this box")
//     → issuing → live, with the www redirect, captured in the manifest;
//   - the box domain switch: tiffin domain set example.test restarts the
//     service, the new names get certificates and the old ones keep working;
//   - apps on a domain of their own: tiffin domain set example.test
//     --apps-domain apps.test keeps the dashboard on example.test, moves
//     apps to <app>.apps.test, and the old app names keep working.
//
// DNS-01 wildcards and renewals are covered in process (internal/edge).
// TIFFIN_E2E_INSTANCE (+ TIFFIN_E2E_PORT, TIFFIN_E2E_CONFIG) reuses a box.
func TestDomains(t *testing.T) {
	RequireLima(t)
	dir := t.TempDir()
	cli := buildTiffin(t, dir, "", "")
	instance, port, config := os.Getenv("TIFFIN_E2E_INSTANCE"), 0, os.Getenv("TIFFIN_E2E_CONFIG")
	fresh := instance == ""
	disk := newDiskName()
	if fresh {
		instance, port, config = newName(), freePort(t), filepath.Join(dir, "config")
		t.Cleanup(func() {
			if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
				_ = exec.Command("limactl", "delete", "-f", instance).Run()
				_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
			}
		})
	} else {
		port, _ = strconv.Atoi(os.Getenv("TIFFIN_E2E_PORT"))
		disk = os.Getenv("TIFFIN_E2E_DISK")
	}
	env := append(os.Environ(), "TIFFIN_CONFIG_DIR="+config, "TIFFIN_LIMA_INSTANCE="+instance, "TIFFIN_LIMA_DISK="+disk,
		fmt.Sprintf("TIFFIN_LIMA_PORT=%d", port), "TIFFIN_LIMA_MEMORY=3GiB", "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	run := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(cli, args...)
		cmd.Env, cmd.Dir = env, dir
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			out = append(out, ee.Stderr...)
		} else if err != nil {
			t.Fatalf("tiffin %v: %v", args, err)
		}
		return code, string(out)
	}
	ok := func(v any, args ...string) {
		t.Helper()
		code, out := run(args...)
		if code != 0 {
			t.Fatalf("tiffin %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
		if v != nil {
			if err := decodeFirst(out, v); err != nil {
				t.Fatalf("tiffin %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
	}
	box := func(script string) string {
		t.Helper()
		out, err := exec.Command("limactl", "shell", "--workdir", "/", instance, "--", "sudo", "bash", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("box: %v\n%s\n%s", err, script, out)
		}
		return strings.TrimSpace(string(out))
	}
	phase := phaseLogger(t)

	// ---- the box (a normal local box first) ----
	p := time.Now()
	linux := buildTiffin(t, dir, "linux", "0.0.12-domains")
	ok(nil, "up", "--binary", linux)
	phase("up", p)

	// ---- Pebble and its DNS server inside the VM ----
	p = time.Now()
	pebbleSrc := goModDir(t, "github.com/letsencrypt/pebble/v2")
	for _, c := range []string{"pebble", "pebble-challtestsrv"} {
		cmd := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(dir, c), "github.com/letsencrypt/pebble/v2/cmd/"+c)
		cmd.Dir, cmd.Env = RepoRoot(), append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+HostArch())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", c, err, out)
		}
	}
	for local, remote := range map[string]string{
		filepath.Join(dir, "pebble"): "/tmp/pebble", filepath.Join(dir, "pebble-challtestsrv"): "/tmp/pebble-challtestsrv",
		filepath.Join(pebbleSrc, "test/certs/localhost/cert.pem"): "/tmp/pebble-cert.pem",
		filepath.Join(pebbleSrc, "test/certs/localhost/key.pem"):  "/tmp/pebble-key.pem",
		filepath.Join(pebbleSrc, "test/certs/pebble.minica.pem"):  "/tmp/pebble-minica.pem",
	} {
		if out, err := exec.Command("limactl", "copy", local, instance+":"+remote).CombinedOutput(); err != nil {
			t.Fatalf("copy %s: %v\n%s", local, err, out)
		}
	}
	const opt = "/opt/tiffin-e2e"
	box(`set -e; pkill -x pebble || true; pkill -x pebble-challtes || true; install -d ` + opt + `
install -m 0755 /tmp/pebble /tmp/pebble-challtestsrv ` + opt + `/
install -m 0644 /tmp/pebble-cert.pem /tmp/pebble-key.pem /tmp/pebble-minica.pem ` + opt + `/
cat > ` + opt + `/pebble.json <<'JSON'
{"pebble": {"listenAddress": "127.0.0.1:14000", "managementListenAddress": "127.0.0.1:15000",
 "certificate": "` + opt + `/pebble-cert.pem", "privateKey": "` + opt + `/pebble-key.pem",
 "httpPort": 8080, "tlsPort": 8443, "ocspResponderURL": "", "externalAccountBindingRequired": false,
 "retryAfter": {"authz": 1, "order": 1}, "keyAlgorithm": "ecdsa"}}
JSON
cd ` + opt + `
(setsid ./pebble-challtestsrv -defaultIPv4 127.0.0.1 -defaultIPv6 "" -dnsserver 127.0.0.1:8053 -management 127.0.0.1:8055 \
   -http01 "" -https01 "" -doh "" -tlsalpn01 "" > challtestsrv.log 2>&1 &)
(PEBBLE_VA_NOSLEEP=1 PEBBLE_WFE_NONCEREJECT=0 setsid ./pebble -config pebble.json -dnsserver 127.0.0.1:8053 > pebble.log 2>&1 &)
for i in $(seq 1 50); do curl -sfk https://127.0.0.1:14000/dir >/dev/null && break; sleep 0.2; done
curl -sfk https://127.0.0.1:14000/dir >/dev/null`)
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
			_ = exec.Command("limactl", "shell", instance, "--", "sudo", "bash", "-c",
				"rm -f /etc/systemd/system/tiffin.service.d/e2e-acme.conf; systemctl daemon-reload; pkill -x pebble; pkill -x pebble-challtes; true").Run()
		}
	})
	dnsSet := func(path, body string) {
		t.Helper()
		box(`curl -sf -X POST -d '` + body + `' http://127.0.0.1:8055/` + path)
	}
	phase("pebble", p)

	// ---- the box becomes a "public server" ----
	p = time.Now()
	box(`install -d /etc/systemd/system/tiffin.service.d
cat > /etc/systemd/system/tiffin.service.d/e2e-acme.conf <<'EOF'
[Service]
Environment=TIFFIN_TLS=acme TIFFIN_PUBLIC_IP=127.0.0.1 TIFFIN_ACME_CA=https://127.0.0.1:14000/dir TIFFIN_ACME_CA_ROOTS=` + opt + `/pebble-minica.pem TIFFIN_DNS_RESOLVER=127.0.0.1:8053
EOF
systemctl daemon-reload && systemctl restart tiffin
for i in $(seq 1 100); do curl -sf http://127.0.0.1:7070/v1/health >/dev/null && break; sleep 0.3; done`)
	root := box(`curl -sfk https://127.0.0.1:15000/roots/0`)
	internalCA, err := os.ReadFile(filepath.Join(config, "boxes", "local", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(dir, "box-and-pebble.pem")
	if err := os.WriteFile(caFile, append(append(internalCA, '\n'), []byte(root+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// The CLI on this computer: the new address, and Pebble's root standing
	// in for the public roots a real box's certificates chain to.
	boxesPath := filepath.Join(config, "boxes.json")
	var boxes map[string]any
	raw, _ := os.ReadFile(boxesPath)
	_ = json.Unmarshal(raw, &boxes)
	cur := boxes["boxes"].(map[string]any)[boxes["current"].(string)].(map[string]any)
	cur["url"], cur["caFile"] = fmt.Sprintf("https://dashboard.127-0-0-1.sslip.io:%d", port), caFile
	raw, _ = json.MarshalIndent(boxes, "", "  ")
	if err := os.WriteFile(boxesPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(root)) // only Pebble: proves the certificates are ACME-issued
	mac := &http.Client{Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
			}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(host, path string) (int, http.Header, string) {
		t.Helper()
		res, err := mac.Get(fmt.Sprintf("https://%s:%d%s", host, port, path))
		if err != nil {
			t.Fatalf("GET %s%s: %v", host, path, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(b)
	}
	type boxDomain struct {
		Domain, AppsDomain, Source, Dashboard, State, Certificates, Summary string
		DashboardCertificate                                                struct{ State, Issuer, Error string }
		Previous                                                            *struct{ Domain, AppsDomain string }
	}
	waitBox := func(domain, apps string) boxDomain {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for {
			var st boxDomain
			code, out := run("domain")
			if code == 0 {
				_ = decodeFirst(out, &st)
			}
			if st.Domain == domain && st.AppsDomain == apps && st.State == "live" {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("box domain %s (apps %s) not live: exit %d %s\npebble: %s", domain, apps, code, out, box("tail -5 "+opt+"/pebble.log"))
			}
			time.Sleep(time.Second)
		}
	}

	// ---- 1. zero setup: sslip.io with an ACME certificate ----
	st := waitBox("127-0-0-1.sslip.io", "127-0-0-1.sslip.io")
	if st.Source != "sslip" || st.Certificates != "acme" || !strings.Contains(st.DashboardCertificate.Issuer, "Pebble") {
		t.Fatalf("zero-setup domain: %+v", st)
	}
	if code, h, _ := get("dashboard.127-0-0-1.sslip.io", "/v1/health"); code != 200 || h.Get("Strict-Transport-Security") != "" {
		t.Fatalf("dashboard: %d (no HSTS with a test CA: %q)", code, h.Get("Strict-Transport-Security"))
	}
	phase("sslip live", p)

	// ---- an app: its certificate on the first visit ----
	p = time.Now()
	manifest := filepath.Join(dir, "shop.json")
	_ = os.WriteFile(manifest, []byte(`{"project":"shop","apps":{"web":{"framework":"static","routes":["shop"]}}}`), 0o644)
	var plan map[string]any
	code, out := run("plan", manifest)
	_ = json.Unmarshal([]byte(out), &plan)
	if code != 0 {
		t.Fatalf("plan: %d %s", code, out)
	}
	ok(nil, "apply", manifest, "--confirm", plan["hash"].(string)[:12], "-m", "e2e domains")
	files, _ := json.Marshal(map[string]any{"files": map[string]string{"index.html": "<h1>shop on a real name</h1>"}})
	var dep map[string]any
	ok(&dep, "deploys", "create", "shop", "web", "--body", string(files))
	for i := 0; ; i++ {
		var d map[string]any
		ok(&d, "deploys", "get", "shop", "web", dep["id"].(string))
		if d["status"] == "live" {
			break
		}
		if d["status"] == "failed" || i > 300 {
			t.Fatalf("deploy: %v", d)
		}
		time.Sleep(time.Second)
	}
	if code, _, body := get("shop.127-0-0-1.sslip.io", "/"); code != 200 || !strings.Contains(body, "shop on a real name") {
		t.Fatalf("app over ACME: %d %s", code, body)
	}
	// The ask gate: no certificate for names the box does not serve.
	if _, err := mac.Get(fmt.Sprintf("https://nothere.127-0-0-1.sslip.io:%d/", port)); err == nil {
		t.Error("a name nobody serves got a certificate")
	}
	phase("app on demand", p)

	// ---- 2. a custom domain: waiting → issuing → live ----
	p = time.Now()
	dnsSet("add-a", `{"host":"shop.test","addresses":["10.9.9.9"]}`)
	code, out = run("domains", "add", "shop", "--domain", "shop.test", "--app", "web", "--www")
	var prob struct{ Plan struct{ Hash string } }
	_ = decodeFirst(out, &prob)
	if code != 4 || prob.Plan.Hash == "" {
		t.Fatalf("domains add without confirm: exit %d %s", code, out)
	}
	var added struct {
		Applied bool
		Domain  struct{ State, Reason string }
	}
	ok(&added, "domains", "add", "shop", "--domain", "shop.test", "--app", "web", "--www", "--confirm", prob.Plan.Hash[:12])
	if !added.Applied || added.Domain.State != "waiting_for_dns" || !strings.Contains(added.Domain.Reason, "points to 10.9.9.9, not this box") {
		t.Fatalf("added: %+v", added)
	}
	dnsSet("clear-a", `{"host":"shop.test"}`) // back to the default: 127.0.0.1, this box
	ok(nil, "domains", "check", "shop", "shop.test")
	ok(nil, "domains", "check", "shop", "www.shop.test")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var list []struct{ Domain, State, Reason string }
		ok(&list, "domains", "list", "shop")
		live := 0
		for _, d := range list {
			if d.State == "live" {
				live++
			}
			if d.State == "error" {
				t.Fatalf("%s: %s", d.Domain, d.Reason)
			}
		}
		if live == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("custom domains not live: %+v", list)
		}
		time.Sleep(time.Second)
	}
	if code, _, body := get("shop.test", "/"); code != 200 || !strings.Contains(body, "shop on a real name") {
		t.Fatalf("custom domain: %d %s", code, body)
	}
	if code, h, _ := get("www.shop.test", "/a?b=1"); code != 308 || !strings.HasPrefix(h.Get("Location"), "https://shop.test") || !strings.HasSuffix(h.Get("Location"), "/a?b=1") {
		t.Errorf("www redirect: %d %q", code, h.Get("Location"))
	}
	var man struct{ Config string }
	ok(&man, "projects", "manifest", "shop")
	if !strings.Contains(man.Config, `"shop.test"`) || !strings.Contains(man.Config, `www: "redirect"`) {
		t.Errorf("tiffin pull would not capture the domain:\n%s", man.Config)
	}
	phase("custom domain", p)

	// ---- 3. the box's own domain ----
	p = time.Now()
	var chk struct {
		OK      bool
		Records []struct{ Name, Type, Value string }
	}
	ok(&chk, "domain", "check", "--domain", "example.test")
	if !chk.OK || len(chk.Records) != 2 {
		t.Fatalf("domain check: %+v", chk)
	}
	code, out = run("domain", "set", "example.test")
	if code != 0 {
		t.Fatalf("domain set: exit %d\n%s\nbox: %s", code, out, box("journalctl -u tiffin -n 40 --no-pager"))
	}
	st = waitBox("example.test", "example.test")
	if st.Source != "set" || st.Previous == nil || st.Previous.Domain != "127-0-0-1.sslip.io" {
		t.Errorf("after set: %+v", st)
	}
	if code, _, _ := get("dashboard.example.test", "/v1/health"); code != 200 {
		t.Errorf("new dashboard: %d", code)
	}
	if code, _, body := get("shop.example.test", "/"); code != 200 || !strings.Contains(body, "shop on a real name") {
		t.Errorf("app under the new domain: %d %s", code, body)
	}
	if code, _, _ := get("shop.127-0-0-1.sslip.io", "/"); code != 200 {
		t.Errorf("the old name stopped working during the grace period: %d", code)
	}
	if code, _, _ := get("shop.test", "/"); code != 200 {
		t.Errorf("the custom domain after the switch: %d", code)
	}
	phase("domain set", p)

	// ---- 4. apps on a domain of their own (dashboard.example.test, <app>.apps.test) ----
	p = time.Now()
	ok(&chk, "domain", "check", "--domain", "example.test", "--apps-domain", "apps.test")
	if !chk.OK || len(chk.Records) != 2 || chk.Records[0].Name != "dashboard.example.test" || chk.Records[1].Name != "*.apps.test" {
		t.Fatalf("domain check --apps-domain: %+v", chk)
	}
	code, out = run("domain", "set", "example.test", "--apps-domain", "apps.test")
	if code != 0 {
		t.Fatalf("domain set --apps-domain: exit %d\n%s\nbox: %s", code, out, box("journalctl -u tiffin -n 40 --no-pager"))
	}
	st = waitBox("example.test", "apps.test")
	// The sslip names from before step 3 are still in their grace period, so they stay alongside example.test.
	if st.Dashboard != "dashboard.example.test" || st.Previous == nil || st.Previous.Domain != "127-0-0-1.sslip.io" || st.Previous.AppsDomain != "example.test" {
		t.Errorf("after set --apps-domain: %+v", st)
	}
	if code, _, _ := get("dashboard.example.test", "/v1/health"); code != 200 {
		t.Errorf("dashboard on the box domain: %d", code)
	}
	if code, _, body := get("shop.apps.test", "/"); code != 200 || !strings.Contains(body, "shop on a real name") {
		t.Errorf("app under the apps domain: %d %s", code, body)
	}
	if code, _, _ := get("shop.example.test", "/"); code != 200 {
		t.Errorf("the old app name stopped working during the grace period: %d", code)
	}
	if _, err := mac.Get(fmt.Sprintf("https://dashboard.apps.test:%d/", port)); err == nil {
		t.Error("the dashboard answers under the apps domain")
	}
	if code, _, _ := get("shop.test", "/"); code != 200 {
		t.Errorf("the custom domain after the apps switch: %d", code)
	}
	phase("apps domain", p)
}

// decodeFirst decodes the first JSON value in a command's output.
func decodeFirst(out string, v any) error {
	i := strings.IndexAny(out, "{[")
	if i < 0 {
		return fmt.Errorf("no JSON in %q", out)
	}
	return json.NewDecoder(strings.NewReader(out[i:])).Decode(v)
}

// goModDir is the directory of a module in the build list.
func goModDir(t *testing.T, mod string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", mod)
	cmd.Dir = RepoRoot()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v", mod, err)
	}
	return strings.TrimSpace(string(out))
}
