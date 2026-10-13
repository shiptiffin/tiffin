package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// The box remembers every record it writes through a connected DNS
// provider (createRecords, dns records set, email's records): that is what
// makes a record the box's own. When a name stops being served (domains
// remove, projects destroy), the records it created that still point at
// this box are deleted, so none is left pointing at an address the server
// may give up (a subdomain takeover once someone else gets that address).
// Records someone added by hand are never deleted, only listed.

// createdRecord is the values the box set for one name and type.
type createdRecord struct {
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Values []string  `json:"values"`
	At     time.Time `json:"at"`
	By     string    `json:"by,omitempty"`
}

func createdKey(name, typ string) string {
	return "created/" + strings.ToLower(name) + " " + strings.ToUpper(typ)
}

// noteCreated remembers records the box just set. SetRecords leaves each
// name and type with exactly the given values, so they replace what was
// remembered.
func (m *Module) noteCreated(ctx context.Context, recs []dnskit.Record, by string) {
	if m.p == nil || m.p.DB == nil {
		return
	}
	byKey := map[string]*createdRecord{}
	var keys []string
	for _, r := range recs {
		k := createdKey(r.Name, r.Type)
		c := byKey[k]
		if c == nil {
			c = &createdRecord{Name: strings.ToLower(r.Name), Type: strings.ToUpper(r.Type), At: m.now(), By: by}
			byKey[k] = c
			keys = append(keys, k)
		}
		c.Values = append(c.Values, r.Value)
	}
	for _, k := range keys {
		raw, _ := json.Marshal(byKey[k])
		_ = m.p.DB.KVPut(ctx, ns, k, raw)
	}
}

// forgetCreated forgets records: a record without a value forgets every
// value of its name and type.
func (m *Module) forgetCreated(ctx context.Context, recs []dnskit.Record) {
	if m.p == nil || m.p.DB == nil {
		return
	}
	all := m.created(ctx)
	for _, r := range recs {
		k := createdKey(r.Name, r.Type)
		c, ok := all[k]
		if !ok {
			continue
		}
		if r.Value != "" {
			c.Values = slices.DeleteFunc(c.Values, func(v string) bool { return dnskit.SameValue(r.Type, v, r.Value) })
		} else {
			c.Values = nil
		}
		if len(c.Values) == 0 {
			delete(all, k)
			_ = m.p.DB.KVDelete(ctx, ns, k)
			continue
		}
		raw, _ := json.Marshal(c)
		_ = m.p.DB.KVPut(ctx, ns, k, raw)
	}
}

// created is every record the box remembers creating, by createdKey.
func (m *Module) created(ctx context.Context) map[string]*createdRecord {
	out := map[string]*createdRecord{}
	if m.p == nil || m.p.DB == nil {
		return out
	}
	all, err := m.p.DB.KVList(ctx, ns)
	if err != nil {
		return out
	}
	for k, v := range all {
		if !strings.HasPrefix(k, "created/") {
			continue
		}
		var c createdRecord
		if json.Unmarshal(v, &c) == nil {
			out[k] = &c
		}
	}
	return out
}

// isCreated reports whether the box created r (its name, type and value).
func isCreated(all map[string]*createdRecord, r dnskit.Record) bool {
	c := all[createdKey(r.Name, r.Type)]
	return c != nil && slices.ContainsFunc(c.Values, func(v string) bool { return dnskit.SameValue(r.Type, v, r.Value) })
}

// ProjectHosts implements platform.DNSCleaner.
func (d *dnsManager) ProjectHosts(ctx context.Context, project string) []string {
	return d.m.projectHosts(ctx, d.m.p, project, "")
}

// CleanupDNS implements platform.DNSCleaner.
func (d *dnsManager) CleanupDNS(ctx context.Context, hosts []string, keep, apply bool, by string) *platform.DNSCleanup {
	return d.m.cleanupDNS(ctx, d.m.p, hosts, keep, apply, by)
}

// projectHosts are the names project serves outside the box's own: all of
// them, or with domain only that domain and the www name redirecting to it.
func (m *Module) projectHosts(ctx context.Context, p *platform.Platform, project, domain string) []string {
	var out []string
	for _, w := range m.scan(ctx, p) {
		if w.Project != project || (domain != "" && w.Host != domain && w.RedirectTo != domain) {
			continue
		}
		out = append(out, w.Host)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// pointing are the record types that point a name at an address.
var pointing = []string{"A", "AAAA", "CNAME"}

// cleanupDNS works out (and with apply, does) what happens to the records
// of hosts once they stop being served. See platform.DNSCleaner.
func (m *Module) cleanupDNS(ctx context.Context, p *platform.Platform, hosts []string, keep, apply bool, by string) *platform.DNSCleanup {
	out := &platform.DNSCleanup{Hosts: []string{}, KeepDNS: keep}
	for _, h := range hosts {
		h = strings.TrimSuffix(strings.ToLower(h), ".")
		// The box's own names keep their records (domain set made them).
		if h == "" || strings.HasPrefix(h, "*.") || h == p.Domain || h == p.AppsDomain() || h == p.DashboardHost() || p.IsBoxHost(h) {
			continue
		}
		if !slices.Contains(out.Hosts, h) {
			out.Hosts = append(out.Hosts, h)
		}
	}
	if len(out.Hosts) == 0 || len(p.Reach.PublicIPs) == 0 {
		out.Summary = describeCleanup(out)
		return out
	}
	_, ips, targets := m.boxNames(ctx, p)
	// A www CNAME to a name that stops being served still points here.
	targets = append(targets, out.Hosts...)
	here := func(r dnskit.Record) bool {
		switch r.Type {
		case "A", "AAAA":
			return slices.ContainsFunc(ips, func(ip string) bool { return dnskit.SameValue(r.Type, ip, r.Value) })
		case "CNAME":
			return slices.ContainsFunc(targets, func(t string) bool { return dnskit.SameValue("CNAME", t, r.Value) })
		}
		return false
	}
	created := m.created(ctx)
	type zoneState struct {
		st   *providerState
		recs []dnskit.Record
		err  error
		del  []dnskit.Record
	}
	zones := map[string]*zoneState{}
	var zoneOrder []string
	var errs []string
	var forget []dnskit.Record
	for _, h := range out.Hosts {
		st, zone := m.providerFor(h)
		if st == nil {
			// Kept at a DNS host the box cannot reach: what public DNS says.
			out.ByHand = append(out.ByHand, m.publicRecords(ctx, p, h, ips, here)...)
			continue
		}
		z := zones[zone]
		if z == nil {
			z = &zoneState{st: st}
			z.recs, z.err = dnskit.ListRecords(ctx, st.prov, zone)
			if z.err != nil {
				errs = append(errs, fmt.Sprintf("%s did not list the records of %s: %v", st.rec.Name, zone, z.err))
			}
			zones[zone] = z
			zoneOrder = append(zoneOrder, zone)
		}
		if z.err != nil {
			// Unverifiable: what public DNS says, and what the box set.
			byHand := m.publicRecords(ctx, p, h, ips, here)
			for _, typ := range pointing {
				if c := created[createdKey(h, typ)]; c != nil {
					for _, v := range c.Values {
						r := newRecordAt(h, typ, v)
						if !slices.ContainsFunc(byHand, func(b dnskit.Record) bool { return b.Type == r.Type && dnskit.SameValue(r.Type, b.Value, r.Value) }) {
							byHand = append(byHand, r)
						}
					}
				}
			}
			out.ByHand = append(out.ByHand, byHand...)
			continue
		}
		for _, typ := range pointing {
			mine := created[createdKey(h, typ)] != nil
			for _, r := range z.recs {
				if r.Name != h || r.Type != typ {
					continue
				}
				switch {
				case here(r) && isCreated(created, r) && keep:
					out.Kept = append(out.Kept, r)
				case here(r) && isCreated(created, r):
					out.Delete = append(out.Delete, r)
					z.del = append(z.del, r)
				case here(r):
					out.ByHand = append(out.ByHand, r)
				case mine:
					out.Kept = append(out.Kept, r) // re-pointed since the box set it
				}
			}
			if mine && !keep {
				forget = append(forget, dnskit.Record{Name: h, Type: typ})
			}
		}
	}
	if apply && !keep {
		out.Delete = nil
		for _, zone := range zoneOrder {
			z := zones[zone]
			if z.err != nil || len(z.del) == 0 {
				continue
			}
			if _, err := dnskit.DeleteValues(ctx, z.st.prov, zone, z.del); err != nil {
				errs = append(errs, fmt.Sprintf("%s refused to delete records in %s: %v", z.st.rec.Name, zone, err))
				out.ByHand = slices.Concat(z.del, out.ByHand)
				forget = slices.DeleteFunc(forget, func(f dnskit.Record) bool {
					return slices.ContainsFunc(z.del, func(d dnskit.Record) bool { return d.Name == f.Name })
				})
				continue
			}
			out.Deleted = append(out.Deleted, z.del...)
		}
		m.forgetCreated(ctx, forget)
		if len(out.Deleted) > 0 && p.DB != nil {
			_ = p.DB.Audit(ctx, by, "dns.records.delete", "box", out.Deleted)
		}
	}
	out.Error = strings.Join(errs, "; ")
	out.Summary = describeCleanup(out)
	return out
}

// publicRecords are the records public DNS has pointing h at this box, as
// they would be typed at a DNS host: the CNAME when h is an alias,
// otherwise its addresses that are this box's.
func (m *Module) publicRecords(ctx context.Context, p *platform.Platform, h string, ips []string, here func(dnskit.Record) bool) []dnskit.Record {
	var found []string
	cname := ""
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ans, err := p.Reach.Resolver().Lookup(cctx, h)
	cancel()
	if err == nil {
		found, cname = addrStrings(ans.Addrs), ans.CNAME
	} else {
		// DNS did not answer: what the box saw last.
		m.mu.Lock()
		if r := m.recs[h]; r != nil {
			found, cname = r.Found, r.CNAME
		}
		m.mu.Unlock()
	}
	var mine []string
	for _, a := range found {
		if slices.Contains(ips, a) {
			mine = append(mine, a)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	if cname != "" {
		return []dnskit.Record{newRecordAt(h, "CNAME", strings.TrimSuffix(cname, "."))}
	}
	var out []dnskit.Record
	for _, a := range mine {
		typ := "A"
		if strings.Contains(a, ":") {
			typ = "AAAA"
		}
		if r := newRecordAt(h, typ, a); here(r) {
			out = append(out, r)
		}
	}
	return out
}

func newRecordAt(name, typ, value string) dnskit.Record {
	return dnskit.Record{Name: name, Type: typ, Value: value, Host: dnskit.RelHost(name, dnskit.Zone(name))}
}

func recordList(recs []dnskit.Record) string {
	s := make([]string, len(recs))
	for i, r := range recs {
		s[i] = r.String()
	}
	return strings.Join(s, "; ")
}

// describeCleanup says in words what happens (or happened) to DNS.
func describeCleanup(c *platform.DNSCleanup) string {
	var s []string
	if len(c.Delete) > 0 {
		s = append(s, fmt.Sprintf("Once applied, the box deletes the %d DNS record(s) it created for these names: %s.", len(c.Delete), recordList(c.Delete)))
	}
	if len(c.Deleted) > 0 {
		s = append(s, fmt.Sprintf("Deleted the %d DNS record(s) the box created: %s.", len(c.Deleted), recordList(c.Deleted)))
	}
	if c.Error != "" {
		s = append(s, "The DNS provider failed ("+c.Error+").")
	}
	if len(c.ByHand) > 0 {
		s = append(s, fmt.Sprintf("Delete these at your DNS host, or they keep pointing at this box's address: %s.", recordList(c.ByHand)))
	}
	if len(c.Kept) > 0 {
		if c.KeepDNS {
			s = append(s, fmt.Sprintf("Kept, as asked (keepDns): %s.", recordList(c.Kept)))
		} else {
			s = append(s, fmt.Sprintf("Left alone, as they point elsewhere now: %s.", recordList(c.Kept)))
		}
	}
	if len(s) == 0 && len(c.Hosts) > 0 {
		s = append(s, "No DNS record points these names at this box.")
	}
	return strings.Join(s, " ")
}
