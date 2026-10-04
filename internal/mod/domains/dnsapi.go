package domains

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/dnskit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/danielgtaylor/huma/v2"
)

// ConnectedProvider is a DNS provider the box holds credentials for.
type ConnectedProvider struct {
	Name        string    `json:"name"`
	Label       string    `json:"label"`
	Zones       []string  `json:"zones" doc:"The zones the credentials reach."`
	BoxDomain   bool      `json:"boxDomain" doc:"It holds the zone apps live under (the box domain, or the separate apps domain): the box uses one wildcard certificate (DNS-01) for every app."`
	ConnectedAt time.Time `json:"connectedAt"`
	ConnectedBy string    `json:"connectedBy"`
	Error       string    `json:"error,omitempty" doc:"Why the stored credentials cannot be used."`
}

// Providers is the DNS providers the box supports and the ones connected.
type Providers struct {
	Connected []ConnectedProvider `json:"connected"`
	Available []dnskit.Kind       `json:"available"`
	Summary   string              `json:"summary"`
}

func (m *Module) providers(p *platform.Platform) Providers {
	out := Providers{Connected: []ConnectedProvider{}, Available: dnskit.Kinds()}
	m.mu.Lock()
	names := make([]string, 0, len(m.provs))
	for n := range m.provs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := m.provs[n]
		c := ConnectedProvider{Name: n, Label: n, Zones: st.rec.Zones, ConnectedAt: st.rec.ConnectedAt, ConnectedBy: st.rec.ConnectedBy}
		if k, ok := dnskit.Lookup(n); ok {
			c.Label = k.Label
		}
		if st.err != nil {
			c.Error = st.err.Error()
		}
		if _, ok := dnskit.ZoneFor(st.rec.Zones, p.AppsDomain()); ok {
			c.BoxDomain = true
		}
		out.Connected = append(out.Connected, c)
	}
	m.mu.Unlock()
	switch {
	case len(out.Connected) == 0:
		out.Summary = "No DNS provider connected. Optional: connect one (tiffin dns connect cloudflare --token <token>) so the box can create records for you and use one wildcard certificate for every app."
	default:
		var parts []string
		for _, c := range out.Connected {
			parts = append(parts, fmt.Sprintf("%s (%d zone(s): %s)", c.Label, len(c.Zones), strings.Join(c.Zones, ", ")))
		}
		out.Summary = "Connected: " + strings.Join(parts, "; ")
	}
	return out
}

func (m *Module) registerDNS(a huma.API, p *platform.Platform, ready func() error) {
	const tag = "dns"
	huma.Register(a, api.Op("dns-providers", http.MethodGet, "/v1/dns/providers", "dns providers", api.RiskRead,
		"List DNS providers",
		"Connected DNS providers (never their credentials) with the zones they reach, and the providers the box supports with the credentials each needs. "+
			"A connected provider is optional: with it the box creates records for the box domain, project domains and email, and uses one wildcard certificate (DNS-01) for every app and preview.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Providers }, error) {
			if err := requireAdmin(ctx); err != nil {
				return nil, err
			}
			if err := ready(); err != nil {
				return nil, err
			}
			return &struct{ Body Providers }{m.providers(p)}, nil
		}))

	cn := api.Outbound(api.Op("dns-connect", http.MethodPut, "/v1/dns/providers/{provider}", "dns connect", api.RiskWrite,
		"Connect a DNS provider",
		"Stores a DNS provider's credentials, encrypted with the box's key, after checking them by listing the zones they reach. "+
			"Cloudflare: an API token with Zone · DNS · Edit on the zones (account-owned or user tokens both work). "+
			"If one of its zones holds the domain apps live under, the box switches to a wildcard certificate for *.<that domain>. Replaces earlier credentials for the same provider. Box admins only.", tag))
	cn.Errors = append(cn.Errors, 404)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *struct {
		Provider string `path:"provider" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"e.g. cloudflare"`
		Body     struct {
			Token       string            `json:"token,omitempty" maxLength:"500" doc:"The API token, for providers that take one (Cloudflare)."`
			Credentials map[string]string `json:"credentials,omitempty" doc:"Every credential by name, for providers that need several (see dns providers)."`
		}
	}) (*struct{ Body Providers }, error) {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		if _, ok := dnskit.Lookup(in.Provider); !ok {
			var names []string
			for _, k := range dnskit.Kinds() {
				names = append(names, k.Name)
			}
			return nil, api.NewProblem(404, "not_found", fmt.Sprintf("unknown DNS provider %q; supported: %s", in.Provider, strings.Join(names, ", ")))
		}
		creds := map[string]string{}
		for k, v := range in.Body.Credentials {
			creds[k] = v
		}
		if in.Body.Token != "" {
			creds["token"] = in.Body.Token
		}
		st, err := m.connect(ctx, in.Provider, creds, actor(ctx))
		if err != nil {
			pr := api.NewProblem(422, "validation", err.Error())
			if k, ok := dnskit.Lookup(in.Provider); ok {
				pr.Hint = k.Help
			}
			return nil, pr
		}
		_ = p.DB.Audit(ctx, actor(ctx), "dns.connect", in.Provider, map[string]any{"zones": st.rec.Zones})
		if err := p.RefreshRoutes(ctx); err != nil && p.Log != nil {
			p.Log.Error("domains: reload the edge", "err", err)
		}
		m.poke()
		return &struct{ Body Providers }{m.providers(p)}, nil
	}))

	dc := api.Op("dns-disconnect", http.MethodDelete, "/v1/dns/providers/{provider}", "dns disconnect", api.RiskWrite,
		"Disconnect a DNS provider",
		"Deletes the stored credentials. Records it created stay. The box goes back to per-name certificates (each app gets its own on its first visit). Box admins only.", tag)
	dc.Errors = append(dc.Errors, 404)
	huma.Register(a, dc, api.Wrap(func(ctx context.Context, in *struct {
		Provider string `path:"provider" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"e.g. cloudflare"`
	}) (*struct{ Body Providers }, error) {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		ok, err := m.disconnect(ctx, in.Provider)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, api.NewProblem(404, "not_found", in.Provider+" is not connected")
		}
		_ = p.DB.Audit(ctx, actor(ctx), "dns.disconnect", in.Provider, nil)
		if err := p.RefreshRoutes(ctx); err != nil && p.Log != nil {
			p.Log.Error("domains: reload the edge", "err", err)
		}
		return &struct{ Body Providers }{m.providers(p)}, nil
	}))

	rs := api.Outbound(api.Op("dns-records-set", http.MethodPut, "/v1/dns/records", "dns records set", api.RiskWrite,
		"Create or replace DNS records",
		"Sets records through the connected provider that holds each name's zone: every name and type ends up with exactly the given values (other records are untouched). "+
			"For what the box cannot infer itself, such as email's SPF (TXT), DKIM (TXT or CNAME from your mail relay) and DMARC (TXT _dmarc.<domain>). "+
			"Types: A, AAAA, CNAME, TXT, MX (\"10 mail.example.com.\"), CAA. Box admins only.", tag))
	rs.Errors = append(rs.Errors, 412)
	huma.Register(a, rs, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Records []dnskit.Record `json:"records" minItems:"1" maxItems:"50"`
		}
	}) (*struct {
		Body struct {
			Set     []dnskit.Record `json:"set"`
			Summary string          `json:"summary"`
		}
	}, error) {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		recs := in.Body.Records
		for i := range recs {
			recs[i].Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(recs[i].Name)), ".")
			recs[i].Type = strings.ToUpper(recs[i].Type)
			recs[i].Host = dnskit.RelHost(recs[i].Name, dnskit.Zone(recs[i].Name))
			if recs[i].Name == "" || recs[i].Value == "" {
				return nil, api.NewProblem(422, "validation", "every record needs a name and a value")
			}
		}
		if err := p.DNS.SetRecords(ctx, recs, actor(ctx)); err != nil {
			return nil, api.NewProblem(http.StatusPreconditionFailed, "precondition", err.Error())
		}
		out := &struct {
			Body struct {
				Set     []dnskit.Record `json:"set"`
				Summary string          `json:"summary"`
			}
		}{}
		out.Body.Set = recs
		out.Body.Summary = fmt.Sprintf("Set %d record(s); public DNS usually shows them within a minute.", len(recs))
		return out, nil
	}))
}
