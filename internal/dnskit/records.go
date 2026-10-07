package dnskit

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/libdns/libdns"
	"github.com/miekg/dns"
)

// ListRecords returns every record a provider holds in zone, as a DNS panel
// shows them: absolute names without the trailing dot, Host relative to the
// zone ("@" for the apex), values as zone-file data (CNAME, MX and NS
// targets without the trailing dot; TXT unquoted), TTL 0 for automatic.
// Sorted by name (the apex first), then type and value.
func ListRecords(ctx context.Context, p Provider, zone string) ([]Record, error) {
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	have, err := p.GetRecords(ctx, zone+".")
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(have))
	for _, h := range have {
		rr := h.RR()
		name := strings.TrimSuffix(strings.ToLower(libdns.AbsoluteName(rr.Name, zone+".")), ".")
		typ := strings.ToUpper(rr.Type)
		ttl := int(rr.TTL / time.Second)
		if ttl <= 1 {
			ttl = 0 // Cloudflare's "automatic" is 1
		}
		out = append(out, Record{Type: typ, Name: name, Host: RelHost(name, zone), Value: shownValue(typ, rr.Data), TTL: ttl})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Host == "@") != (b.Host == "@") {
			return a.Host == "@"
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Value < b.Value
	})
	return out, nil
}

// shownValue is a record's data as people type it: targets without the
// root dot ("10 mail.example.com", not "10 mail.example.com.").
func shownValue(typ, data string) string {
	data = strings.TrimSpace(data)
	switch typ {
	case "CNAME", "NS", "MX", "SRV", "PTR":
		return strings.TrimSuffix(data, ".")
	}
	return data
}

// SameValue reports whether two values of a type are the same record
// (case and the root dot don't matter, except in TXT).
func SameValue(typ, a, b string) bool {
	typ = strings.ToUpper(typ)
	a, b = shownValue(typ, a), shownValue(typ, b)
	if typ == "TXT" {
		return a == b
	}
	return strings.EqualFold(a, b)
}

// DeleteValues removes exactly the given records (name, type and value) from
// zone; other values of the same name and type stay. It returns how many it
// removed. Unlike DeleteRecords, a record without a value matches nothing.
func DeleteValues(ctx context.Context, p Provider, zone string, recs []Record) (int, error) {
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	have, err := p.GetRecords(ctx, zone+".")
	if err != nil {
		return 0, err
	}
	var del []libdns.Record
	for _, h := range have {
		rr := h.RR()
		name := strings.TrimSuffix(libdns.AbsoluteName(rr.Name, zone+"."), ".")
		for _, r := range recs {
			if r.Value != "" && strings.EqualFold(rr.Type, r.Type) && strings.EqualFold(name, strings.TrimSuffix(r.Name, ".")) && SameValue(rr.Type, rr.Data, r.Value) {
				del = append(del, h)
				break
			}
		}
	}
	if len(del) == 0 {
		return 0, nil
	}
	if _, err := p.DeleteRecords(ctx, zone+".", del); err != nil {
		return 0, err
	}
	return len(del), nil
}

// LookupTypes are the record types Records answers for.
var LookupTypes = []string{"A", "AAAA", "CNAME", "TXT", "MX", "CAA", "NS"}

// Records returns what DNS answers now for name and one type, values as
// ListRecords shows them. alias is the CNAME name points to, if it is an
// alias (then the values are the alias target's). A name that does not
// exist has no values and no error.
func (r *Resolver) Records(ctx context.Context, name, typ string) (values []string, alias string, err error) {
	typ = strings.ToUpper(strings.TrimSpace(typ))
	qt, ok := dns.StringToType[typ]
	if !ok || !slices.Contains(LookupTypes, typ) {
		return nil, "", fmt.Errorf("type %q is not one of %s", typ, strings.Join(LookupTypes, ", "))
	}
	in, err := r.query(ctx, name, qt)
	if err != nil {
		return nil, "", err
	}
	values = []string{}
	for _, rr := range in.Answer {
		if c, ok := rr.(*dns.CNAME); ok && qt != dns.TypeCNAME {
			if alias == "" && strings.EqualFold(dns.Fqdn(c.Hdr.Name), dns.Fqdn(name)) {
				alias = strings.TrimSuffix(strings.ToLower(c.Target), ".")
			}
			continue
		}
		if rr.Header().Rrtype != qt {
			continue
		}
		var v string
		switch x := rr.(type) {
		case *dns.TXT:
			v = strings.Join(x.Txt, "")
		case *dns.A:
			v = x.A.String()
		case *dns.AAAA:
			v = x.AAAA.String()
		default:
			v = strings.TrimSpace(strings.TrimPrefix(rr.String(), rr.Header().String()))
		}
		if v = shownValue(typ, v); !slices.Contains(values, v) {
			values = append(values, v)
		}
	}
	return values, alias, nil
}
