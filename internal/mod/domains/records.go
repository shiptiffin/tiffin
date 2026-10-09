package domains

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// ZoneRecord is one record in a zone a connected provider holds, and
// whether the box relies on it.
type ZoneRecord struct {
	dnskit.Record
	Managed string `json:"managed,omitempty" enum:"tiffin,host" doc:"tiffin: the box relies on it (it points a name the box serves at this box, or it is a certificate challenge), so dns records delete refuses it; remove the domain first. host: the DNS host's own (NS). Absent: yours to change."`
	Why     string `json:"why,omitempty" doc:"Why it is managed, in plain words."`
}

// ZoneRecords is a zone's records as its DNS provider holds them.
type ZoneRecords struct {
	Zone          string       `json:"zone" doc:"The zone that holds the name asked about, e.g. example.com."`
	Provider      string       `json:"provider" doc:"The connected DNS provider that holds it, e.g. cloudflare."`
	ProviderLabel string       `json:"providerLabel"`
	Records       []ZoneRecord `json:"records"`
	Summary       string       `json:"summary"`
}

// DNSLookup is what public DNS answers for one name and type.
type DNSLookup struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Values  []string `json:"values" doc:"The answers, as zone-file data (TXT unquoted, targets without the root dot). Empty: no such record."`
	Alias   string   `json:"alias,omitempty" doc:"The CNAME the name points to, when it is an alias (the values are then the alias target's)."`
	Summary string   `json:"summary"`
}

var lookupName = regexp.MustCompile(`^(\*\.)?([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?\.)+[a-z][a-z0-9-]{0,62}$`)

// boxNames are the names the box serves (project domains and their www, the
// box domain, the apps domain and its wildcard, the dashboard), the
// addresses its records point at, and the names a CNAME may point at.
func (m *Module) boxNames(ctx context.Context, p *platform.Platform) (hosts map[string]bool, ips []string, targets []string) {
	hosts = map[string]bool{}
	for _, w := range m.scan(ctx, p) {
		hosts[w.Host] = true
	}
	apps := p.AppsDomain()
	for _, h := range []string{p.Domain, "*." + p.Domain, apps, "*." + apps, p.DashboardHost()} {
		hosts[strings.ToLower(h)] = true
	}
	for _, a := range p.Reach.PublicIPs {
		ips = append(ips, a.Unmap().String())
	}
	targets = []string{p.Domain, p.DashboardHost(), apps}
	return hosts, ips, targets
}

// mark says which records the box relies on.
func mark(recs []dnskit.Record, zone string, hosts map[string]bool, ips, targets []string) []ZoneRecord {
	out := make([]ZoneRecord, 0, len(recs))
	for _, r := range recs {
		z := ZoneRecord{Record: r}
		switch {
		case (r.Type == "A" || r.Type == "AAAA") && hosts[r.Name] && slices.ContainsFunc(ips, func(ip string) bool { return dnskit.SameValue(r.Type, ip, r.Value) }):
			z.Managed, z.Why = "tiffin", fmt.Sprintf("Points %s at this box.", r.Name)
		case r.Type == "CNAME" && hosts[r.Name] && slices.ContainsFunc(targets, func(t string) bool { return dnskit.SameValue("CNAME", t, r.Value) }):
			z.Managed, z.Why = "tiffin", fmt.Sprintf("Points %s at this box.", r.Name)
		case r.Type == "TXT" && strings.HasPrefix(r.Host, "_acme-challenge"):
			z.Managed, z.Why = "tiffin", "A certificate check (Let's Encrypt). The box adds and removes it."
		case r.Type == "SOA" || (r.Type == "NS" && r.Name == zone):
			z.Managed, z.Why = "host", "Your DNS host's own record for the zone."
		}
		out = append(out, z)
	}
	return out
}

func (m *Module) zoneRecords(ctx context.Context, p *platform.Platform, name string) (*ZoneRecords, error) {
	st, zone := m.providerFor(name)
	if st == nil {
		pr := api.NewProblem(http.StatusPreconditionFailed, "precondition", "no connected DNS provider holds the zone of "+name+"; its records are kept at its DNS host")
		pr.Hint = "connect the provider (tiffin dns connect cloudflare --token ...) to list and edit them here, or use dns lookup to see what public DNS answers"
		return nil, pr
	}
	recs, err := dnskit.ListRecords(ctx, st.prov, zone)
	if err != nil {
		return nil, api.NewProblem(http.StatusBadGateway, "internal", fmt.Sprintf("%s did not list the records of %s: %v", st.rec.Name, zone, err))
	}
	hosts, ips, targets := m.boxNames(ctx, p)
	out := &ZoneRecords{Zone: zone, Provider: st.rec.Name, ProviderLabel: st.rec.Name, Records: mark(recs, zone, hosts, ips, targets)}
	if k, ok := dnskit.Lookup(st.rec.Name); ok {
		out.ProviderLabel = k.Label
	}
	n := 0
	for _, r := range out.Records {
		if r.Managed == "tiffin" {
			n++
		}
	}
	out.Summary = fmt.Sprintf("%d record(s) in %s at %s; the box relies on %d of them.", len(out.Records), zone, out.ProviderLabel, n)
	return out, nil
}

func cleanRecords(recs []dnskit.Record, needValue bool) error {
	for i := range recs {
		recs[i].Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(recs[i].Name)), ".")
		recs[i].Type = strings.ToUpper(strings.TrimSpace(recs[i].Type))
		recs[i].Value = strings.TrimSpace(recs[i].Value)
		if recs[i].Name == "" || (needValue && recs[i].Value == "") {
			return api.NewProblem(422, "validation", "every record needs a name, a type and a value")
		}
		recs[i].Host = dnskit.RelHost(recs[i].Name, dnskit.Zone(recs[i].Name))
	}
	return nil
}

func (m *Module) registerRecords(a huma.API, p *platform.Platform, ready func() error) {
	const tag = "dns"
	ls := api.Untrusted(api.Outbound(api.Op("dns-records-list", http.MethodGet, "/v1/dns/records", "dns records list", api.RiskRead,
		"List a zone's DNS records",
		"Every record in the zone that holds <name>, read from the connected DNS provider: type, name, value, TTL (0: automatic). "+
			"Records the box relies on are marked managed: tiffin (A/AAAA/CNAME pointing a served name at this box, certificate challenges); the DNS host's own are managed: host. "+
			"Status 412 when no connected provider holds the zone: then its records live at its DNS host (use dns lookup to see what public DNS answers). Box admins only.", tag)))
	ls.Errors = append(ls.Errors, 412)
	huma.Register(a, ls, api.Wrap(func(ctx context.Context, in *struct {
		Name string `query:"name" required:"true" maxLength:"253" doc:"A domain or any name in the zone, e.g. example.com or shop.example.com."`
	}) (*struct{ Body ZoneRecords }, error) {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		z, err := m.zoneRecords(ctx, p, strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Name)), "."))
		if err != nil {
			return nil, err
		}
		return &struct{ Body ZoneRecords }{*z}, nil
	}))

	del := api.Outbound(api.Op("dns-records-delete", http.MethodPost, "/v1/dns/records/delete", "dns records delete", api.RiskDestructive,
		"Delete DNS records",
		"Deletes exactly the given records (name, type and value) through the connected provider that holds each zone; other values of the same name and type stay. "+
			"Refuses (409) records the box relies on (see dns records list); remove the domain first. Status 412 when no connected provider holds a zone. Box admins only.", tag))
	del.Errors = append(del.Errors, 409, 412)
	huma.Register(a, del, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Records []dnskit.Record `json:"records" minItems:"1" maxItems:"50"`
		}
	}) (*struct {
		Body struct {
			Deleted int    `json:"deleted"`
			Summary string `json:"summary"`
		}
	}, error) {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		recs := in.Body.Records
		if err := cleanRecords(recs, true); err != nil {
			return nil, err
		}
		hosts, ips, targets := m.boxNames(ctx, p)
		byZone := map[string][]dnskit.Record{}
		provs := map[string]*providerState{}
		for _, r := range recs {
			st, zone := m.providerFor(r.Name)
			if st == nil {
				return nil, api.NewProblem(http.StatusPreconditionFailed, "precondition", fmt.Sprintf("%v: %s", dnskit.ErrNoZone, r.Name))
			}
			if z := mark([]dnskit.Record{r}, zone, hosts, ips, targets)[0]; z.Managed != "" {
				pr := api.NewProblem(http.StatusConflict, "conflict", fmt.Sprintf("%s %s %s: %s Not deleted", r.Name, r.Type, r.Value, z.Why))
				pr.Hint = "remove the domain from its project first (domains remove); its records can go after that"
				return nil, pr
			}
			byZone[zone] = append(byZone[zone], r)
			provs[zone] = st
		}
		zones := make([]string, 0, len(byZone))
		for z := range byZone {
			zones = append(zones, z)
		}
		sort.Strings(zones)
		total := 0
		for _, z := range zones {
			n, err := dnskit.DeleteValues(ctx, provs[z].prov, z, byZone[z])
			if err != nil {
				return nil, api.NewProblem(http.StatusBadGateway, "internal", fmt.Sprintf("%s (zone %s) refused: %v", provs[z].rec.Name, z, err))
			}
			total += n
		}
		_ = p.DB.Audit(ctx, actor(ctx), "dns.records.delete", "box", recs)
		out := &struct {
			Body struct {
				Deleted int    `json:"deleted"`
				Summary string `json:"summary"`
			}
		}{}
		out.Body.Deleted = total
		out.Body.Summary = fmt.Sprintf("Deleted %d record(s).", total)
		if total < len(recs) {
			out.Body.Summary += fmt.Sprintf(" %d were not there.", len(recs)-total)
		}
		return out, nil
	}))

	lk := api.Untrusted(api.Outbound(api.Op("dns-lookup", http.MethodGet, "/v1/dns/lookup", "dns lookup", api.RiskRead,
		"Look up a DNS record",
		"Read-only: what public DNS answers now for <name> and <type> (A, AAAA, CNAME, TXT, MX, CAA, NS), asked through the box's resolvers. "+
			"Use it to check a record you added at your DNS host, such as a verification TXT record.", tag)))
	huma.Register(a, lk, api.Wrap(func(ctx context.Context, in *struct {
		Name string `query:"name" required:"true" maxLength:"253" doc:"e.g. _github-pages-challenge-me.example.com"`
		Type string `query:"type" enum:"A,AAAA,CNAME,TXT,MX,CAA,NS" default:"TXT"`
	}) (*struct{ Body DNSLookup }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Name)), ".")
		if !lookupName.MatchString(name) {
			return nil, api.NewProblem(422, "validation", fmt.Sprintf("%q is not a DNS name", in.Name))
		}
		vals, alias, err := p.Reach.Resolver().Records(ctx, name, in.Type)
		if err != nil {
			return nil, api.NewProblem(http.StatusBadGateway, "internal", "DNS did not answer: "+err.Error())
		}
		out := DNSLookup{Name: name, Type: in.Type, Values: vals, Alias: alias}
		switch {
		case len(vals) == 0 && alias != "":
			out.Summary = fmt.Sprintf("%s is an alias of %s, which has no %s record.", name, alias, in.Type)
		case len(vals) == 0:
			out.Summary = fmt.Sprintf("Public DNS has no %s record for %s yet.", in.Type, name)
		default:
			out.Summary = fmt.Sprintf("%s %s: %s", name, in.Type, strings.Join(vals, " | "))
		}
		return &struct{ Body DNSLookup }{out}, nil
	}))
}
