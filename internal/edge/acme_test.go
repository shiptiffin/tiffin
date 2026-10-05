package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/dnskit/dnstest"
	pca "github.com/letsencrypt/pebble/v2/ca"
	pdb "github.com/letsencrypt/pebble/v2/db"
	pva "github.com/letsencrypt/pebble/v2/va"
	pwfe "github.com/letsencrypt/pebble/v2/wfe"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// pebble is Let's Encrypt's test CA, in process. Its validation authority
// resolves names through dnsAddr and connects to the edge's ports.
type pebble struct {
	dir   string         // ACME directory URL
	roots string         // PEM file: the ACME server's own TLS certificate
	pool  *x509.CertPool // the issuing root, for clients
}

func startPebble(t *testing.T, dnsAddr string, httpPort, tlsPort int, validity uint64) *pebble {
	t.Helper()
	t.Setenv("PEBBLE_VA_NOSLEEP", "1")
	t.Setenv("PEBBLE_WFE_NONCEREJECT", "0")
	logger := log.New(io.Discard, "pebble ", 0)
	if os.Getenv("PEBBLE_DEBUG") != "" {
		logger = log.New(os.Stderr, "pebble ", log.Ltime)
	}
	db := pdb.NewMemoryStore()
	ca := pca.New(logger, db, "", "ecdsa", 0, 1, map[string]pca.Profile{"default": {Description: "test", ValidityPeriod: validity}})
	va := pva.New(logger, httpPort, tlsPort, false, dnsAddr, db)
	wfe := pwfe.New(logger, db, va, ca, []string{"pebble.letsencrypt.org"}, false, false, 1, 1)
	srv := httptest.NewTLSServer(wfe.Handler())
	t.Cleanup(srv.Close)
	roots := filepath.Join(t.TempDir(), "acme-server.pem")
	if err := os.WriteFile(roots, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.GetRootCert(0).Cert)
	return &pebble{dir: srv.URL + "/dir", roots: roots, pool: pool}
}

// acmeClient dials the edge for any host and trusts only Pebble's root.
func acmeClient(t *testing.T, pool *x509.CertPool, httpsPort, httpPort int) *http.Client {
	t.Helper()
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			p := httpsPort
			if port == strconv.Itoa(httpPort) {
				p = httpPort
			}
			var d net.Dialer
			return d.DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(p))
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func waitCert(t *testing.T, host, state string, d time.Duration) CertInfo {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		ci := CertStatus(host)
		if ci.State == state {
			return ci
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: state %q, want %q (%+v)", host, ci.State, state, ci)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// handshake tries TLS for host and returns the leaf (nil on failure).
func handshake(pool *x509.CertPool, port int, host string) (*x509.Certificate, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", "127.0.0.1:"+strconv.Itoa(port), &tls.Config{ServerName: host, RootCAs: pool})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0], nil
}

func TestACMEIssuanceAndAskGate(t *testing.T) {
	isolate(t)
	dns := dnstest.Start(t)
	dns.AddZone("box.test")
	dns.AddZone("example.test")
	dns.Set("*.box.test", "A", "127.0.0.1")
	dns.Set("example.test", "A", "127.0.0.1")
	dns.Set("www.example.test", "A", "127.0.0.1")
	dns.Set("waiting.example.test", "A", "127.0.0.1") // points here, but the box was not told yet
	ports := freePorts(t, 2)
	pb := startPebble(t, dns.Addr(), ports[0], ports[1], 0)

	var events []CertEvent
	evc := make(chan CertEvent, 64)
	defer OnCertEvent(func(e CertEvent) { evc <- e })()

	platform := upstreamServer(t, "platform")
	app := upstreamServer(t, "app")
	cfg := Config{
		Domain: "box.test", Upstream: addr(platform), DataDir: filepath.Join(t.TempDir(), "caddy"),
		HTTPPort: ports[0], HTTPSPort: ports[1], HSTS: time.Hour,
		ACME: &ACME{CA: pb.dir, TrustedRoots: pb.roots, Ready: []string{"example.test", "www.example.test"}},
		Routes: []Route{
			{Host: "app.box.test", Upstream: addr(app)},
			{Host: "example.test", Upstream: addr(app)},
			{Host: "www.example.test", RedirectTo: "example.test"},
			{Host: "waiting.example.test", Upstream: addr(app)},
		},
	}
	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	// Proactive: the dashboard and the ready custom domains.
	dash := waitCert(t, "dashboard.box.test", "live", 30*time.Second)
	if !strings.Contains(dash.Issuer, "Pebble") {
		t.Errorf("dashboard issuer = %q", dash.Issuer)
	}
	waitCert(t, "example.test", "live", 30*time.Second)
	waitCert(t, "www.example.test", "live", 30*time.Second)
	if st := CertStatus("app.box.test").State; st != "none" {
		t.Errorf("app.box.test before its first visit: %s, want none (on demand)", st)
	}

	c := acmeClient(t, pb.pool, ports[1], ports[0])
	res, body := get(t, c, "https://dashboard.box.test/")
	if res.StatusCode != 200 || !strings.Contains(body, "hello from platform") {
		t.Fatalf("dashboard: %d %s", res.StatusCode, body)
	}
	if h := res.Header.Get("Strict-Transport-Security"); h != "max-age=3600" {
		t.Errorf("HSTS = %q", h)
	}
	// HTTP/3: advertised, and served over QUIC on the HTTPS port.
	if h := res.Header.Get("Alt-Svc"); !strings.Contains(h, `h3=":`+strconv.Itoa(ports[1])+`"`) {
		t.Errorf("Alt-Svc = %q", h)
	}
	h3 := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: pb.pool},
		Dial: func(ctx context.Context, _ string, tc *tls.Config, qc *quic.Config) (*quic.Conn, error) {
			return quic.DialAddrEarly(ctx, "127.0.0.1:"+strconv.Itoa(ports[1]), tc, qc)
		}}
	defer h3.Close()
	res, body = get(t, &http.Client{Transport: h3, Timeout: 10 * time.Second}, "https://dashboard.box.test/h3")
	if res.StatusCode != 200 || res.ProtoMajor != 3 || body != "hello from platform /h3" {
		t.Errorf("HTTP/3: %d %s %q", res.StatusCode, res.Proto, body)
	}
	// On demand: the first visit gets the certificate (the gate allows it).
	res, body = get(t, c, "https://app.box.test/hi")
	if res.StatusCode != 200 || body != "hello from app /hi" {
		t.Fatalf("app: %d %s", res.StatusCode, body)
	}
	waitCert(t, "app.box.test", "live", 5*time.Second)
	res, body = get(t, c, "https://example.test/")
	if res.StatusCode != 200 || !strings.Contains(body, "hello from app") {
		t.Fatalf("custom domain: %d %s", res.StatusCode, body)
	}
	res, _ = get(t, c, "https://www.example.test/a/b?c=1")
	if want := "https://example.test:" + strconv.Itoa(ports[1]) + "/a/b?c=1"; res.StatusCode != 308 || res.Header.Get("Location") != want {
		t.Errorf("www redirect: %d %q, want 308 %q", res.StatusCode, res.Header.Get("Location"), want)
	}
	// Plain HTTP goes to HTTPS (challenges aside).
	res, _ = get(t, c, "http://app.box.test:"+strconv.Itoa(ports[0])+"/x")
	if res.StatusCode != 308 || !strings.HasPrefix(res.Header.Get("Location"), "https://app.box.test") {
		t.Errorf("http: %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	// The gate: no certificate for names the box does not serve, nor for a
	// custom domain whose DNS the box has not confirmed.
	for _, h := range []string{"unknown.box.test", "waiting.example.test", "evil.test"} {
		if _, err := handshake(pb.pool, ports[1], h); err == nil {
			t.Errorf("%s: handshake succeeded; the ask gate must refuse it", h)
		}
	}
	time.Sleep(200 * time.Millisecond)
	for len(evc) > 0 {
		events = append(events, <-evc)
	}
	for _, ev := range events {
		if ev.Host == "unknown.box.test" || ev.Host == "waiting.example.test" || ev.Host == "evil.test" {
			t.Errorf("an issuance was attempted for %s: %+v", ev.Host, ev)
		}
	}
	if !hasEvent(events, "app.box.test", "obtained") {
		t.Errorf("no obtained event for app.box.test: %+v", events)
	}

	// Once the box confirms its DNS, the waiting domain gets its certificate.
	cfg.ACME.Ready = append(cfg.ACME.Ready, "waiting.example.test")
	if err := e.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	waitCert(t, "waiting.example.test", "live", 30*time.Second)
	if _, err := handshake(pb.pool, ports[1], "waiting.example.test"); err != nil {
		t.Errorf("waiting.example.test after ready: %v", err)
	}
}

func hasEvent(evs []CertEvent, host, kind string) bool {
	for _, e := range evs {
		if e.Host == host && e.Kind == kind {
			return true
		}
	}
	return false
}

// TestACMEWildcardDNS01 gets *.<domain> through a DNS provider: every box
// host is covered by one certificate, with no per-name issuance.
func TestACMEWildcardDNS01(t *testing.T) {
	isolate(t)
	dns := dnstest.Start(t)
	dns.AddZone("wild.test")
	dns.Set("*.wild.test", "A", "127.0.0.1")
	prov := dnstest.NewProvider(dns)
	ports := freePorts(t, 2)
	pb := startPebble(t, dns.Addr(), ports[0], ports[1], 0)
	up := upstreamServer(t, "platform")
	app := upstreamServer(t, "preview")
	// The cert source, as the domains module registers it.
	SetCertSource(func() CertState {
		return CertState{Wildcard: &DNSChallenge{Name: "fake", Provider: prov, Resolvers: []string{dns.Addr()}, PropagationTimeout: -1}}
	})
	defer SetCertSource(nil)
	e, err := Start(context.Background(), Config{
		Domain: "wild.test", Upstream: addr(up), DataDir: filepath.Join(t.TempDir(), "caddy"),
		HTTPPort: ports[0], HTTPSPort: ports[1],
		ACME:   &ACME{CA: pb.dir, TrustedRoots: pb.roots},
		Routes: []Route{{Host: "pr-7--web.wild.test", Upstream: addr(app)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	ci := waitCert(t, "dashboard.wild.test", "live", 30*time.Second)
	if ci.Subject != "*.wild.test" {
		t.Errorf("dashboard is covered by %q, want the wildcard", ci.Subject)
	}
	c := acmeClient(t, pb.pool, ports[1], ports[0])
	res, body := get(t, c, "https://pr-7--web.wild.test/")
	if res.StatusCode != 200 || !strings.Contains(body, "hello from preview") {
		t.Fatalf("preview: %d %s", res.StatusCode, body)
	}
	leaf, err := handshake(pb.pool, ports[1], "pr-7--web.wild.test")
	if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "*.wild.test" {
		t.Fatalf("preview certificate: %v %v", leaf, err)
	}
	if prov.Calls["AppendRecords"] == 0 || prov.Calls["DeleteRecords"] == 0 {
		t.Errorf("the DNS-01 TXT record was not created and cleaned up: %v", prov.Calls)
	}
	if v := dns.Get("_acme-challenge.wild.test", "TXT"); len(v) != 0 {
		t.Errorf("challenge record left behind: %v", v)
	}
	// A route reload does not re-issue: the same wildcard keeps serving.
	before := prov.Calls["AppendRecords"]
	if err := e.Reload(Config{
		Domain: "wild.test", Upstream: addr(up), DataDir: e.Config().DataDir,
		HTTPPort: ports[0], HTTPSPort: ports[1],
		ACME:   &ACME{CA: pb.dir, TrustedRoots: pb.roots},
		Routes: []Route{{Host: "pr-7--web.wild.test", Upstream: addr(app)}, {Host: "new.wild.test", Upstream: addr(app)}},
	}); err != nil {
		t.Fatal(err)
	}
	if res, _ := get(t, c, "https://new.wild.test/"); res.StatusCode != 200 {
		t.Errorf("new app: %d", res.StatusCode)
	}
	if prov.Calls["AppendRecords"] != before {
		t.Errorf("reload re-issued the wildcard (%d → %d DNS writes)", before, prov.Calls["AppendRecords"])
	}
}

// TestACMERenewal: short-lived certificates are renewed before they expire.
func TestACMERenewal(t *testing.T) {
	isolate(t)
	dns := dnstest.Start(t)
	dns.AddZone("renew.test")
	dns.Set("*.renew.test", "A", "127.0.0.1")
	ports := freePorts(t, 2)
	pb := startPebble(t, dns.Addr(), ports[0], ports[1], 30) // 30-second certificates
	up := upstreamServer(t, "platform")
	e, err := Start(context.Background(), Config{
		Domain: "renew.test", Upstream: addr(up), DataDir: filepath.Join(t.TempDir(), "caddy"),
		HTTPPort: ports[0], HTTPSPort: ports[1],
		ACME: &ACME{CA: pb.dir, TrustedRoots: pb.roots, RenewInterval: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	first := waitCert(t, "dashboard.renew.test", "live", 30*time.Second)
	deadline := time.Now().Add(45 * time.Second)
	for {
		ci := CertStatus("dashboard.renew.test")
		if ci.State == "live" && ci.NotAfter.After(first.NotAfter) {
			leaf, err := handshake(pb.pool, ports[1], "dashboard.renew.test")
			if err != nil || !leaf.NotAfter.After(first.NotAfter) {
				t.Fatalf("renewed certificate not served: %v %v", leaf, err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not renewed: %+v (first expired %s)", ci, first.NotAfter)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestAliasesAndRedirectInternal(t *testing.T) {
	isolate(t)
	up := upstreamServer(t, "platform")
	app := upstreamServer(t, "shop")
	cfg := testConfig(t, addr(up))
	cfg.Aliases = []string{"old.localhost"}
	cfg.Routes = []Route{{Host: "shop.tiffin.localhost", Upstream: addr(app)}, {Host: "www.tiffin.localhost", RedirectTo: "shop.tiffin.localhost"}}
	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	pemRoot, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	c := client(t, pemRoot, cfg.HTTPSPort)
	port := strconv.Itoa(cfg.HTTPSPort)
	for host, want := range map[string]string{
		"shop.tiffin.localhost": "hello from shop", "shop.old.localhost": "hello from shop",
		"dashboard.old.localhost": "hello from platform", "dashboard.tiffin.localhost": "hello from platform",
	} {
		res, body := get(t, c, "https://"+host+":"+port+"/")
		if res.StatusCode != 200 || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s", host, res.StatusCode, body)
		}
	}
	res, _ := get(t, c, "https://www.tiffin.localhost:"+port+"/p")
	if res.StatusCode != 308 || res.Header.Get("Location") != "https://shop.tiffin.localhost:"+port+"/p" {
		t.Errorf("redirect: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if h := res.Header.Get("Strict-Transport-Security"); h != "max-age=300" {
		t.Errorf("internal HSTS = %q", h)
	}
	// Dropping the alias stops serving the old names.
	cfg.Aliases = []string{}
	if err := e.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Get("https://shop.old.localhost:" + port + "/"); err == nil {
		res.Body.Close()
		t.Errorf("old name still served after the alias is dropped: %d", res.StatusCode)
	}
}

func TestACMEConfigShape(t *testing.T) {
	base := Config{Domain: "example.com", Upstream: "127.0.0.1:7070", DataDir: "/x", ACME: &ACME{Email: "ops@example.com", Ready: []string{"Shop.Example.org"}},
		Routes: []Route{{Host: "web.example.com", Upstream: "127.0.0.1:1"}, {Host: "shop.example.org", Upstream: "127.0.0.1:1"}, {Host: "late.example.org", Upstream: "127.0.0.1:1"}}}
	c, err := base.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.proactiveHosts(); strings.Join(got, ",") != "dashboard.example.com,shop.example.org" {
		t.Errorf("proactive = %v", got)
	}
	al := c.Allowed()
	for h, want := range map[string]bool{"dashboard.example.com": true, "web.example.com": true, "shop.example.org": true, "late.example.org": false, "x.example.com": false} {
		if al[h] != want {
			t.Errorf("allowed[%s] = %v", h, al[h])
		}
	}
	iss := c.issuers(nil)
	if len(iss) != 2 || iss[1]["ca"] != ZeroSSL {
		t.Errorf("want Let's Encrypt then ZeroSSL with an email: %v", iss)
	}
	if c.hstsMaxAge() != hstsPublic {
		t.Errorf("public CA HSTS = %s", c.hstsMaxAge())
	}
	c.ACME.CA = LetsEncryptStaging
	if c.hstsMaxAge() != 0 || len(c.issuers(nil)) != 1 {
		t.Error("staging: no HSTS, no ZeroSSL fallback")
	}
	if _, err := ConfigJSON(Config{Domain: "example.com", Upstream: "127.0.0.1:1", DataDir: "/x", ACME: &ACME{CA: "http://insecure"}}); err == nil {
		t.Error("an http:// CA must be refused")
	}
	if _, err := ConfigJSON(Config{Domain: "example.com", Upstream: "127.0.0.1:1", DataDir: "/x", ACME: &ACME{Wildcard: &DNSChallenge{Name: "x"}}}); err == nil {
		t.Error("a DNS challenge without a provider must be refused")
	}
}

// TestAppsDomainConfig: apps on their own domain (example.app beside
// example.com): one-label hosts, aliases, the wildcard and the dashboard.
func TestAppsDomainConfig(t *testing.T) {
	prov := dnstest.NewProvider(nil)
	base := Config{Domain: "example.com", Apps: "Example.APP.", Upstream: "127.0.0.1:7070", DataDir: "/x",
		Aliases: []string{"example.com", "old.example", "example.app"},
		ACME:    &ACME{Ready: []string{"shop.example.org"}, Wildcard: &DNSChallenge{Name: "fake", Provider: prov}},
		Routes: []Route{{Host: "web.example.app", Upstream: "127.0.0.1:1"}, {Host: "shop.example.org", Upstream: "127.0.0.1:1"},
			{Host: "blog.example.com", Upstream: "127.0.0.1:1"}}}
	c, err := base.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if c.Apps != "example.app" || c.DashboardHost() != "dashboard.example.com" || strings.Join(c.Aliases, ",") != "example.com,old.example" {
		t.Fatalf("normalized: apps %q dashboard %q aliases %v", c.Apps, c.DashboardHost(), c.Aliases)
	}
	if got := strings.Join(c.dashboardHosts(), ","); got != "dashboard.example.com,dashboard.old.example" {
		t.Errorf("dashboard hosts = %s", got)
	}
	if got := strings.Join(c.hostsFor("web.example.app"), ","); got != "web.example.app,web.example.com,web.old.example" {
		t.Errorf("hostsFor = %s", got)
	}
	// The wildcard covers the apps; the dashboard is not under it, so it
	// gets its own certificate right away.
	if got := c.proactiveHosts(); strings.Join(got, ",") != "dashboard.example.com,shop.example.org" {
		t.Errorf("proactive = %v", got)
	}
	pol := c.acmeTLS()["automation"].(obj)["policies"].([]obj)
	if s := pol[0]["subjects"].([]string); len(s) != 1 || s[0] != "*.example.app" {
		t.Errorf("wildcard policy = %v", s)
	}
	al := c.Allowed()
	for h, want := range map[string]bool{"dashboard.example.com": true, "web.example.app": true, "web.example.com": true, "web.old.example": true,
		"shop.example.org": true, "x.example.app": false, "dashboard.example.app": false,
		"blog.example.com": true, // under an alias: the old apps domain's names keep working
	} {
		if al[h] != want {
			t.Errorf("allowed[%s] = %v", h, al[h])
		}
	}
	// Without the alias, a name under the box domain is a custom domain:
	// served only once its DNS points here.
	c.Aliases = []string{"old.example"}
	if c.Allowed()["blog.example.com"] {
		t.Error("blog.example.com allowed without being ready")
	}
	// The internal CA manages the apps wildcard and the dashboard.
	c.ACME, c.Internal = nil, true
	if got := c.managedHosts(); got[0] != "*.example.app" || got[1] != "dashboard.example.com" {
		t.Errorf("managed = %v", got)
	}
	// Without a separate apps domain nothing changes.
	same := Config{Domain: "example.com", Apps: "example.com", Upstream: "127.0.0.1:1", DataDir: "/x", ACME: &ACME{Wildcard: &DNSChallenge{Name: "fake", Provider: prov}}}
	if c, err := same.normalized(); err != nil || c.Apps != "" || len(c.proactiveHosts()) != 0 {
		t.Errorf("apps = domain: %q %v %v", c.Apps, c.proactiveHosts(), err)
	}
	if _, err := ConfigJSON(Config{Domain: "example.com", Apps: "bad_name", Upstream: "127.0.0.1:1", DataDir: "/x", Internal: true}); err == nil {
		t.Error("an invalid apps domain must be refused")
	}
}

// TestACMEAppsDomain: with apps on their own domain, the DNS provider's
// wildcard covers *.<apps> and the dashboard gets its own certificate.
func TestACMEAppsDomain(t *testing.T) {
	isolate(t)
	dns := dnstest.Start(t)
	dns.AddZone("box.test")
	dns.AddZone("apps.test")
	dns.Set("dashboard.box.test", "A", "127.0.0.1")
	dns.Set("*.apps.test", "A", "127.0.0.1")
	prov := dnstest.NewProvider(dns)
	ports := freePorts(t, 2)
	pb := startPebble(t, dns.Addr(), ports[0], ports[1], 0)
	up := upstreamServer(t, "platform")
	app := upstreamServer(t, "preview")
	SetCertSource(func() CertState {
		return CertState{Wildcard: &DNSChallenge{Name: "fake", Provider: prov, Resolvers: []string{dns.Addr()}, PropagationTimeout: -1}}
	})
	defer SetCertSource(nil)
	e, err := Start(context.Background(), Config{
		Domain: "box.test", Apps: "apps.test", Upstream: addr(up), DataDir: filepath.Join(t.TempDir(), "caddy"),
		HTTPPort: ports[0], HTTPSPort: ports[1],
		ACME:   &ACME{CA: pb.dir, TrustedRoots: pb.roots},
		Routes: []Route{{Host: "pr-7--web.apps.test", Upstream: addr(app)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	if ci := waitCert(t, "dashboard.box.test", "live", 30*time.Second); ci.Subject != "dashboard.box.test" {
		t.Errorf("dashboard certificate covers %q", ci.Subject)
	}
	c := acmeClient(t, pb.pool, ports[1], ports[0])
	if res, body := get(t, c, "https://dashboard.box.test/"); res.StatusCode != 200 || !strings.Contains(body, "hello from platform") {
		t.Errorf("dashboard: %d %s", res.StatusCode, body)
	}
	waitCert(t, "pr-7--web.apps.test", "live", 30*time.Second)
	if res, body := get(t, c, "https://pr-7--web.apps.test/"); res.StatusCode != 200 || !strings.Contains(body, "hello from preview") {
		t.Fatalf("preview: %d %s", res.StatusCode, body)
	}
	leaf, err := handshake(pb.pool, ports[1], "pr-7--web.apps.test")
	if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "*.apps.test" {
		t.Fatalf("preview certificate: %v %v", leaf, err)
	}
}
