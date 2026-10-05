package platform

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/dnskit"
	"github.com/btahir/tiffin/internal/state"
)

// Reach is how the world reaches the box: its public addresses, where its
// certificates come from and which DNS servers it asks about names.
type Reach struct {
	// PublicIPs are the server's public addresses (empty on a laptop VM).
	PublicIPs []netip.Addr
	// ACME: certificates come from a public CA (Let's Encrypt, ZeroSSL);
	// false: from the box's own internal CA.
	ACME bool
	// ACMEDirectory is the CA's directory URL ("" = Let's Encrypt).
	ACMEDirectory string
	// ACMERoots is a PEM file to trust a test CA's API (Pebble).
	ACMERoots string
	// ACMEEmail is the ACME account contact (also enables the ZeroSSL fallback).
	ACMEEmail string
	// Resolvers are the DNS servers (host:port) the box asks; empty means
	// public resolvers (dnskit.DefaultServers).
	Resolvers []string
	// DomainSource says how Platform.Domain was chosen: "set" (tiffin
	// domain set), "sslip" (automatic, from the public IPv4) or "flag" (the
	// service's --domain).
	DomainSource string
	// DefaultDomain is the domain the box uses without `tiffin domain set`.
	DefaultDomain string
	// Dashboard is the dashboard's first-level name ("dashboard").
	Dashboard string
	// AppsDomain is where apps and previews live when it is not the box
	// domain (`tiffin domain set --apps-domain`); empty means the box domain.
	AppsDomain string
}

// Resolver returns a resolver that asks the box's DNS servers.
func (r Reach) Resolver() *dnskit.Resolver { return &dnskit.Resolver{Servers: r.Resolvers} }

// CAAIdentities are the CAA "issue" values that let the box's CA issue.
// Empty for a test CA (no CAA check).
func (r Reach) CAAIdentities() []string {
	switch strings.TrimRight(r.ACMEDirectory, "/") {
	case "", "https://acme-v02.api.letsencrypt.org/directory", "https://acme-staging-v02.api.letsencrypt.org/directory":
		ids := []string{"letsencrypt.org"}
		if r.ACMEEmail != "" {
			ids = append(ids, "sectigo.com") // ZeroSSL issues through Sectigo
		}
		return ids
	case "https://acme.zerossl.com/v2/DV90":
		return []string{"sectigo.com"}
	}
	return nil
}

// BoxDomain is the box's own domain setting (`tiffin domain set`), kept in
// the state database so it survives restarts and updates.
type BoxDomain struct {
	// Domain is the domain set by the owner; empty means automatic.
	Domain string `json:"domain,omitempty"`
	// Dashboard is the dashboard's first-level name; empty means "dashboard".
	Dashboard string `json:"dashboard,omitempty"`
	// Apps is a separate domain for apps and previews (<project>.<apps>); empty
	// means Domain. A different registrable domain (example.app beside
	// example.com) keeps app code from setting cookies on the dashboard's.
	Apps string `json:"apps,omitempty"`
	// Email is the ACME account contact, if the owner gave one.
	Email string `json:"email,omitempty"`
	// Previous is the domain before the last switch. Its names keep
	// working until the new ones have certificates, then for an hour.
	Previous string `json:"previous,omitempty"`
	// PreviousApps is the apps domain before the last switch, when it was
	// not Previous; its names keep working for as long as Previous's.
	PreviousApps string `json:"previousApps,omitempty"`
	// PreviousUntil is when Previous stops being served; zero while the
	// new certificates are not live yet.
	PreviousUntil time.Time `json:"previousUntil,omitzero"`
	SetAt         time.Time `json:"setAt,omitzero"`
	SetBy         string    `json:"setBy,omitempty"`
}

const boxNS, boxDomainKey = "box", "domain"

// LoadBoxDomain reads the domain setting (zero value: none).
func LoadBoxDomain(ctx context.Context, db *state.DB) (BoxDomain, error) {
	var d BoxDomain
	raw, ok, err := db.KVGet(ctx, boxNS, boxDomainKey)
	if err != nil || !ok {
		return d, err
	}
	return d, json.Unmarshal(raw, &d)
}

// SaveBoxDomain stores the domain setting.
func SaveBoxDomain(ctx context.Context, db *state.DB, d BoxDomain) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return db.KVPut(ctx, boxNS, boxDomainKey, raw)
}

// IsLocalDomain reports whether a domain can only work on this machine
// (*.localhost and the reserved test/LAN suffixes): no public certificate
// can exist for it.
func IsLocalDomain(d string) bool {
	d = strings.ToLower(strings.TrimSuffix(d, "."))
	if d == "localhost" {
		return true
	}
	for _, s := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa", ".invalid"} {
		if strings.HasSuffix(d, s) {
			return true
		}
	}
	return false
}

// ChooseDomain picks the box's domain: the one set with `tiffin domain
// set`; else, on a box with a public IPv4 whose service domain is a local
// one, <ip-with-dashes>.sslip.io (real names with zero setup); else the
// service's --domain. It returns the domain, how it was chosen and the
// default (what it would be without a set domain).
func ChooseDomain(flag string, set BoxDomain, ips []netip.Addr) (domain, source, def string) {
	def, source = flag, "flag"
	if IsLocalDomain(flag) {
		for _, a := range ips {
			if s := dnskit.SslipDomain(a); s != "" {
				def, source = s, "sslip"
				break
			}
		}
	}
	if set.Domain != "" {
		return set.Domain, "set", def
	}
	return def, source, def
}

// DNSManager manages DNS records through the box's connected DNS provider
// (the domains module provides it as Platform.DNS). Modules use it to
// publish records they need, e.g. email's SPF, DKIM and DMARC.
type DNSManager interface {
	// Manages reports whether a connected provider holds name's zone.
	Manages(ctx context.Context, name string) bool
	// SetRecords creates or replaces records: each name and type keeps
	// exactly the given values. by is who asked (for the audit log).
	SetRecords(ctx context.Context, recs []dnskit.Record, by string) error
	// DeleteRecords removes records by name and type.
	DeleteRecords(ctx context.Context, recs []dnskit.Record, by string) error
}

// DashboardHost is the dashboard's host ("dashboard.<domain>" by default),
// always under the box domain.
func (p *Platform) DashboardHost() string {
	name := p.Reach.Dashboard
	if name == "" {
		name = "dashboard"
	}
	return name + "." + p.Domain
}

// AppsDomain is the domain apps and previews live under: the box domain,
// or a separate one set with `tiffin domain set --apps-domain`.
func (p *Platform) AppsDomain() string {
	if p.Reach.AppsDomain != "" {
		return p.Reach.AppsDomain
	}
	return p.Domain
}

// IsBoxHost reports whether host is a first-level name under the apps
// domain ("shop.<apps domain>"): covered by its wildcard DNS record and,
// with a DNS provider, by its wildcard certificate.
func (p *Platform) IsBoxHost(host string) bool {
	label, rest, ok := strings.Cut(strings.ToLower(host), ".")
	return ok && label != "" && rest == p.AppsDomain()
}
