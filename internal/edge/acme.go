package edge

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyevents"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	"github.com/caddyserver/certmagic"
	"github.com/libdns/libdns"
)

// Public ACME directories.
const (
	LetsEncrypt        = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
	ZeroSSL            = certmagic.ZeroSSLProductionCA
)

// ACME configures public certificates for a box on a public server.
//
// Which names get a certificate, and when:
//
//   - with Wildcard (a DNS provider): *.<Domain> through the DNS-01
//     challenge, which covers the dashboard, every app and every preview
//     at once (one certificate, one issuance);
//   - without it: the dashboard right away, and each other one-label host
//     under the box domain on its first visit (on-demand TLS, HTTP-01 or
//     TLS-ALPN), so previews nobody opens never use up rate limits;
//   - hosts outside the box domain (custom domains) right away, but only
//     those listed in Ready: their DNS points at this box. A route whose
//     DNS does not point here yet gets no certificate attempts at all, so a
//     waiting domain never burns Let's Encrypt's failed-validation limit.
//
// On-demand issuance is gated by the route table: a name gets a
// certificate only if the box serves it (see Allowed).
type ACME struct {
	// CA is the ACME directory URL. Empty: Let's Encrypt.
	CA string
	// Email is the ACME account contact (optional). With an email and the
	// default CA, ZeroSSL is tried when Let's Encrypt fails.
	Email string
	// TrustedRoots is a PEM file of roots for talking to a test CA (Pebble).
	TrustedRoots string
	// Ready are hosts outside the box domain whose DNS points at this box.
	// Nil: the ones the registered cert source gives (see SetCertSource).
	Ready []string
	// Wildcard obtains *.<Domain> through the DNS-01 challenge. Nil: the
	// one the registered cert source gives, if any.
	Wildcard *DNSChallenge
	// RenewInterval is how often certificates are checked for renewal.
	// 0: Caddy's default (10 minutes).
	RenewInterval time.Duration
}

// DNSChallenge solves ACME DNS-01 challenges with a DNS provider (libdns).
type DNSChallenge struct {
	// Name identifies the provider in the config, e.g. "cloudflare".
	Name string
	// Provider creates and deletes the _acme-challenge TXT records.
	Provider certmagic.DNSProvider
	// Resolvers are the DNS servers (host:port) used to find the zone and
	// check propagation. Empty: the system's.
	Resolvers []string
	// PropagationTimeout bounds the wait for the TXT record to appear.
	// 0: Caddy's default (2 minutes). Negative: do not check (tests).
	PropagationTimeout time.Duration
}

func (a *ACME) publicCA() bool {
	switch strings.TrimRight(a.CA, "/") {
	case "", LetsEncrypt, ZeroSSL:
		return true
	}
	return false
}

func (a *ACME) normalized() (*ACME, error) {
	n := *a
	n.CA = strings.TrimSpace(n.CA)
	if n.CA != "" && !strings.HasPrefix(n.CA, "https://") {
		return nil, fmt.Errorf("edge: ACME CA %q must be an https:// directory URL", n.CA)
	}
	ready := []string{}
	for _, h := range n.Ready {
		h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "."))
		if h == "" || slices.Contains(ready, h) {
			continue
		}
		if err := validHost(h); err != nil || strings.Contains(h, "*") {
			return nil, fmt.Errorf("edge: ready host %q is not a host name", h)
		}
		ready = append(ready, h)
	}
	sort.Strings(ready)
	n.Ready = ready
	if w := n.Wildcard; w != nil {
		if w.Name == "" || w.Provider == nil {
			return nil, errors.New("edge: a DNS challenge needs a provider name and a provider")
		}
		if err := validHost(w.Name); err != nil || strings.ContainsAny(w.Name, ".*") {
			return nil, fmt.Errorf("edge: DNS provider name %q must be a plain word", w.Name)
		}
	}
	return &n, nil
}

// issuer is one ACME issuer object; dns adds the DNS-01 challenge.
func (c Config) issuer(ca string, dns *DNSChallenge) obj {
	a := c.ACME
	iss := obj{"module": "acme"}
	if ca != "" {
		iss["ca"] = ca
	}
	if a.Email != "" {
		iss["email"] = a.Email
	}
	if a.TrustedRoots != "" {
		iss["trusted_roots_pem_files"] = []string{a.TrustedRoots}
	}
	ch := obj{}
	if dns != nil {
		d := obj{"provider": obj{"name": dnsModuleName, "key": dns.Name}}
		if len(dns.Resolvers) > 0 {
			d["resolvers"] = dns.Resolvers
		}
		switch {
		case dns.PropagationTimeout < 0:
			d["propagation_timeout"] = -1
		case dns.PropagationTimeout > 0:
			d["propagation_timeout"] = int64(dns.PropagationTimeout)
		}
		ch["dns"] = d
	} else {
		// Challenges arrive on 80/443; a box behind a port forward listens
		// elsewhere and gets them through the forward.
		if c.HTTPPort != 80 {
			ch["http"] = obj{"alternate_port": c.HTTPPort}
		}
		if c.HTTPSPort != 443 {
			ch["tls-alpn"] = obj{"alternate_port": c.HTTPSPort}
		}
	}
	if len(ch) > 0 {
		iss["challenges"] = ch
	}
	return iss
}

// issuers are the HTTP-01/TLS-ALPN issuers: the configured CA, plus
// ZeroSSL as a fallback when the CA is Let's Encrypt and there is an email.
func (c Config) issuers(dns *DNSChallenge) []obj {
	a := c.ACME
	out := []obj{c.issuer(a.CA, dns)}
	if a.Email != "" && (a.CA == "" || strings.TrimRight(a.CA, "/") == LetsEncrypt) {
		out = append(out, c.issuer(ZeroSSL, dns))
	}
	return out
}

// customHosts are the route hosts outside the box domain (and its aliases).
func (c Config) isBoxHost(h string) bool {
	if BoxLabel(h, c.Domain) != "" {
		return true
	}
	for _, a := range c.Aliases {
		if BoxLabel(h, a) != "" {
			return true
		}
	}
	return false
}

// proactiveHosts get certificates as soon as the config loads.
func (c Config) proactiveHosts() []string {
	var out []string
	if c.ACME.Wildcard == nil {
		out = append(out, c.DashboardHost())
	}
	for _, r := range c.Routes {
		if !c.isBoxHost(r.Host) && slices.Contains(c.ACME.Ready, r.Host) && !slices.Contains(out, r.Host) {
			out = append(out, r.Host)
		}
	}
	return out
}

// Allowed is the ask gate: every host the box serves and may get a
// certificate for. Names outside it never get one on demand.
func (c Config) Allowed() map[string]bool {
	out := map[string]bool{}
	for _, d := range c.dashboardHosts() {
		out[d] = true
	}
	for _, r := range c.Routes {
		if c.isBoxHost(r.Host) {
			for _, h := range c.hostsFor(r.Host) {
				out[h] = true
			}
		} else if c.ACME == nil || slices.Contains(c.ACME.Ready, r.Host) {
			out[r.Host] = true
		}
	}
	return out
}

// acmeTLS is the tls app for public certificates.
func (c Config) acmeTLS() obj {
	a := c.ACME
	var policies []obj
	var automate []string
	if a.Wildcard != nil {
		policies = append(policies, obj{"subjects": []string{"*." + c.Domain}, "issuers": c.issuers(a.Wildcard)})
		automate = append(automate, "*."+c.Domain)
	}
	if hosts := c.proactiveHosts(); len(hosts) > 0 {
		// A policy without subjects matches everything, so this one is only
		// emitted with some.
		policies = append(policies, obj{"subjects": hosts, "issuers": c.issuers(nil)})
		automate = append(automate, hosts...)
	}
	policies = append(policies, obj{"on_demand": true, "issuers": c.issuers(nil)})
	automation := obj{
		"policies":  policies,
		"on_demand": obj{"permission": obj{"module": permModuleName}},
	}
	if a.RenewInterval > 0 {
		automation["renew_interval"] = int64(a.RenewInterval)
	}
	t := obj{"automation": automation}
	if len(automate) > 0 {
		t["certificates"] = obj{"automate": automate}
	}
	return t
}

func certEvents() obj {
	return obj{"subscriptions": []obj{{
		"events":   []string{"cert_obtaining", "cert_obtained", "cert_failed"},
		"handlers": []obj{{"handler": eventsModuleName}},
	}}}
}

// ---- certificate source (the domains module) ----

// CertState is the part of certificate management that changes while the
// box runs: earlier domains still served, custom domains whose DNS points
// here and the DNS provider for wildcard certificates.
type CertState struct {
	Aliases  []string
	Ready    []string
	Wildcard *DNSChallenge
}

var (
	certMu     sync.Mutex
	certSource func() CertState
)

// SetCertSource registers the function the edge asks for the current
// CertState on every Start and Reload; fields the Config sets itself win.
// The domains module registers it and reloads the edge (via
// Platform.RefreshRoutes) when the state changes. nil unregisters it.
func SetCertSource(fn func() CertState) {
	certMu.Lock()
	defer certMu.Unlock()
	certSource = fn
}

// withCertSource fills Aliases, ACME.Ready and ACME.Wildcard from the
// registered source where the Config leaves them nil.
func withCertSource(c Config) Config {
	certMu.Lock()
	src := certSource
	certMu.Unlock()
	if src == nil {
		return c
	}
	st := src()
	if c.Aliases == nil {
		c.Aliases = st.Aliases
	}
	if c.ACME != nil && !c.Internal {
		a := *c.ACME
		if a.Ready == nil {
			a.Ready = st.Ready
		}
		if a.Wildcard == nil {
			a.Wildcard = st.Wildcard
		}
		c.ACME = &a
	}
	return c
}

// ---- state shared with the Caddy modules below ----

var (
	allowed     atomic.Pointer[map[string]bool]
	providersMu sync.Mutex
	providers   = map[string]certmagic.DNSProvider{}
)

// install publishes what the Caddy modules of the next config need: the
// ask gate's host set and the DNS provider. Call before caddy.Load.
func install(c Config) {
	set := c.Allowed()
	allowed.Store(&set)
	if c.ACME != nil && c.ACME.Wildcard != nil {
		providersMu.Lock()
		providers[c.ACME.Wildcard.Name] = c.ACME.Wildcard.Provider
		providersMu.Unlock()
	}
}

// ---- certificate events and status ----

// CertEvent is something that happened to a certificate.
type CertEvent struct {
	Host    string    // the name ("*.example.com" for a wildcard)
	Kind    string    // "obtaining", "obtained" or "failed"
	Renewal bool      // a renewal rather than the first issuance
	Error   string    // for "failed": the CA's or the solver's error
	At      time.Time //
}

var (
	eventsMu   sync.Mutex
	lastEvent  = map[string]CertEvent{}
	eventHooks []func(CertEvent)
)

// OnCertEvent registers fn to be called (in its own goroutine) on every
// certificate event. It returns a function that unregisters it.
func OnCertEvent(fn func(CertEvent)) (cancel func()) {
	eventsMu.Lock()
	defer eventsMu.Unlock()
	eventHooks = append(eventHooks, fn)
	idx := len(eventHooks) - 1
	return func() {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		if idx < len(eventHooks) {
			eventHooks[idx] = nil
		}
	}
}

func recordEvent(ev CertEvent) {
	eventsMu.Lock()
	if ev.Kind == "obtaining" {
		// Keep a failure visible while the retry runs.
		if prev, ok := lastEvent[ev.Host]; ok && prev.Kind == "failed" {
			ev.Error = prev.Error
		}
	}
	lastEvent[ev.Host] = ev
	hooks := slices.Clone(eventHooks)
	eventsMu.Unlock()
	for _, h := range hooks {
		if h != nil {
			go h(ev)
		}
	}
}

// CertInfo is the certificate the edge serves for a host.
type CertInfo struct {
	Host string `json:"host"`
	// State: "live" (a valid certificate is served), "issuing" (one is
	// being obtained), "error" (the last attempt failed; it is retried
	// with backoff) or "none" (no certificate and no attempt yet; on-demand
	// names get theirs on the first visit).
	State     string    `json:"state"`
	Issuer    string    `json:"issuer,omitempty"`
	Subject   string    `json:"subject,omitempty" doc:"The name the certificate covers, e.g. *.example.com for a wildcard."`
	NotAfter  time.Time `json:"notAfter,omitzero"`
	Error     string    `json:"error,omitempty"`
	ErrorAt   time.Time `json:"errorAt,omitzero"`
	Renewing  bool      `json:"renewing,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// CertStatus reports the certificate the edge holds for host (exact or
// wildcard match) and the last thing that happened while obtaining one.
func CertStatus(host string) CertInfo {
	host = strings.ToLower(host)
	now := time.Now()
	info := CertInfo{Host: host, State: "none", CheckedAt: now}
	var best *x509.Certificate
	for _, cert := range caddytls.AllMatchingCertificates(host) {
		leaf := cert.Leaf
		if leaf == nil {
			continue
		}
		if now.After(leaf.NotAfter) || now.Before(leaf.NotBefore) {
			continue
		}
		if best == nil || leaf.NotAfter.After(best.NotAfter) {
			best = leaf
		}
	}
	eventsMu.Lock()
	ev, haveEv := lastEvent[host]
	if !haveEv {
		if label, rest, ok := strings.Cut(host, "."); ok && label != "" {
			ev, haveEv = lastEvent["*."+rest]
		}
	}
	eventsMu.Unlock()
	if best != nil {
		info.State, info.NotAfter = "live", best.NotAfter
		info.Issuer = issuerName(best)
		if len(best.DNSNames) > 0 {
			info.Subject = best.DNSNames[0]
			for _, n := range best.DNSNames {
				if n == host {
					info.Subject = n
				}
			}
		}
		if haveEv && ev.Renewal && ev.Kind != "obtained" {
			info.Renewing = true
			info.Error, info.ErrorAt = ev.Error, failedAt(ev)
		}
		return info
	}
	if haveEv {
		switch ev.Kind {
		case "failed":
			info.State, info.Error, info.ErrorAt = "error", ev.Error, ev.At
		case "obtaining":
			info.State = "issuing"
			if ev.Error != "" {
				info.State, info.Error = "error", ev.Error
			}
		}
	}
	return info
}

func failedAt(ev CertEvent) time.Time {
	if ev.Kind == "failed" {
		return ev.At
	}
	return time.Time{}
}

func issuerName(c *x509.Certificate) string {
	name := c.Issuer.CommonName
	if len(c.Issuer.Organization) > 0 && !strings.Contains(name, c.Issuer.Organization[0]) {
		name = c.Issuer.Organization[0] + " " + name
	}
	return strings.TrimSpace(name)
}

// ---- Caddy modules ----

const (
	permModuleName   = "tiffin"
	eventsModuleName = "tiffin_certs"
	dnsModuleName    = "tiffin"
)

func init() {
	caddy.RegisterModule(askGate{})
	caddy.RegisterModule(certEventSink{})
	caddy.RegisterModule(dnsProvider{})
}

// askGate is the on-demand TLS permission: a certificate only for hosts in
// the current route table (see Config.Allowed).
type askGate struct{}

func (askGate) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "tls.permission." + permModuleName, New: func() caddy.Module { return new(askGate) }}
}

// errNotServed is the gate's refusal.
var errNotServed = errors.New("not served by this box")

func (askGate) CertificateAllowed(_ context.Context, name string) error {
	set := allowed.Load()
	if set != nil && (*set)[strings.ToLower(name)] {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", name, caddytls.ErrPermissionDenied, errNotServed)
}

// certEventSink feeds certmagic's events into recordEvent.
type certEventSink struct{}

func (certEventSink) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "events.handlers." + eventsModuleName, New: func() caddy.Module { return new(certEventSink) }}
}

func (certEventSink) Handle(_ context.Context, e caddy.Event) error {
	ev := CertEvent{At: time.Now()}
	switch e.Name() {
	case "cert_obtaining":
		ev.Kind = "obtaining"
	case "cert_obtained":
		ev.Kind = "obtained"
	case "cert_failed":
		ev.Kind = "failed"
	default:
		return nil
	}
	ev.Host, _ = e.Data["identifier"].(string)
	ev.Renewal, _ = e.Data["renewal"].(bool)
	if err, ok := e.Data["error"].(error); ok && err != nil {
		ev.Error = err.Error()
	}
	if ev.Host != "" {
		recordEvent(ev)
	}
	return nil
}

// dnsProvider hands certmagic the libdns provider registered under Key.
type dnsProvider struct {
	Key string `json:"key"`
	p   certmagic.DNSProvider
}

func (dnsProvider) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "dns.providers." + dnsModuleName, New: func() caddy.Module { return new(dnsProvider) }}
}

func (d *dnsProvider) Provision(caddy.Context) error {
	providersMu.Lock()
	defer providersMu.Unlock()
	d.p = providers[d.Key]
	if d.p == nil {
		return fmt.Errorf("edge: no DNS provider %q registered", d.Key)
	}
	return nil
}

func (d *dnsProvider) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return d.p.AppendRecords(ctx, zone, recs)
}

func (d *dnsProvider) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return d.p.DeleteRecords(ctx, zone, recs)
}

// Interface guards
var (
	_ caddytls.OnDemandPermission = askGate{}
	_ caddyevents.Handler         = certEventSink{}
	_ certmagic.DNSProvider       = (*dnsProvider)(nil)
	_ caddy.Provisioner           = (*dnsProvider)(nil)
)
