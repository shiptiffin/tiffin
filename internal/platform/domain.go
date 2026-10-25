package platform

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/state"
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

// SharedZones are ShipTiffin's own domains: managed boxes get names under
// them, and nobody else can set their DNS.
var SharedZones = []string{"shiptiffin.app"}

// SharedZone returns the shared zone d is in, or "".
func SharedZone(d string) string {
	d = strings.ToLower(strings.TrimSuffix(d, "."))
	for _, z := range SharedZones {
		if d == z || strings.HasSuffix(d, "."+z) {
			return z
		}
	}
	return ""
}

// HiddenFromSearch reports whether host is on a name nobody ranks on: a
// shared zone (shiptiffin.app), an automatic sslip.io name or a local one.
// Every app there says noindex, chosen names included; an app gets found on
// a domain of its own. A shared domain also shares its reputation: one
// project's spam would weigh on every other.
func HiddenFromSearch(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return SharedZone(h) != "" || h == "sslip.io" || strings.HasSuffix(h, ".sslip.io") || IsLocalDomain(h)
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

// DNSAppender is the part of Platform.DNS (the domains module) that adds a
// record beside the others at its name, such as a search engine's
// verification TXT next to SPF. The box does not count such records as its
// own: removing a domain leaves them, since the proof must stay.
type DNSAppender interface {
	AddRecords(ctx context.Context, recs []dnskit.Record, by string) error
}

// DNSCleaner is the part of Platform.DNS (the domains module) that tidies
// DNS when names stop being served (domains remove, projects destroy), so
// no record keeps pointing at this box's address after it is gone.
type DNSCleaner interface {
	// ProjectHosts are the names outside the box's own that project serves
	// (its domains, and the www names it redirects).
	ProjectHosts(ctx context.Context, project string) []string
	// CleanupDNS works out what happens to the records of hosts: those the
	// box created that still point here are deleted (unless keep), the rest
	// that point here are listed to delete by hand. It reads the DNS
	// provider each time. With apply it deletes; a provider that fails never
	// fails the caller: the records are listed under ByHand instead.
	CleanupDNS(ctx context.Context, hosts []string, keep, apply bool, by string) *DNSCleanup
}

// DNSCleanup is what happens to the DNS records of names that stop being
// served: in a plan, what will happen; after the change, what did.
type DNSCleanup struct {
	Hosts   []string        `json:"hosts" doc:"The names that stop being served."`
	Delete  []dnskit.Record `json:"delete,omitempty" doc:"In a plan: records the box created (createRecords) that still point at this box. They are deleted through the connected DNS provider once the change is applied."`
	Deleted []dnskit.Record `json:"deleted,omitempty" doc:"After the change: the records the box deleted."`
	ByHand  []dnskit.Record `json:"byHand,omitempty" doc:"Records that point these names at this box but that the box did not create (or that it could not delete): delete these at your DNS host."`
	Kept    []dnskit.Record `json:"kept,omitempty" doc:"Records the box created that it leaves alone: they point elsewhere now (you changed them), or keepDns was set."`
	KeepDNS bool            `json:"keepDns,omitempty" doc:"keepDns was set: no record is deleted."`
	Error   string          `json:"error,omitempty" doc:"Why the DNS provider could not be read or refused a deletion; those records are listed under byHand."`
	Summary string          `json:"summary"`
}

// Empty reports whether there is nothing to say about DNS.
func (c *DNSCleanup) Empty() bool {
	return c == nil || len(c.Delete)+len(c.Deleted)+len(c.ByHand)+len(c.Kept) == 0 && c.Error == ""
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
