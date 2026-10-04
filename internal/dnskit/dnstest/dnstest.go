// Package dnstest is an in-process DNS server and a libdns provider that
// edits it, for tests: the server answers recursive-style queries for the
// zones it holds (A, AAAA, CNAME, TXT, CAA, SOA, with wildcards), and the
// provider stands in for Cloudflare and friends.
package dnstest

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"
	"github.com/miekg/dns"
)

// Server is an authoritative-and-recursive-looking DNS server on 127.0.0.1.
type Server struct {
	addr  string
	udp   *dns.Server
	tcp   *dns.Server
	mu    sync.Mutex
	zones map[string]bool                // "example.test."
	recs  map[string]map[uint16][]dns.RR // fqdn → type → records
	// Queries counts questions by "name type", for tests that check the
	// box does not hammer DNS.
	Queries map[string]int
}

// Start runs a server until the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		pc.Close()
		t.Fatal(err)
	}
	s := &Server{addr: pc.LocalAddr().String(), zones: map[string]bool{}, recs: map[string]map[uint16][]dns.RR{}, Queries: map[string]int{}}
	s.udp = &dns.Server{PacketConn: pc, Handler: s}
	s.tcp = &dns.Server{Listener: ln, Handler: s}
	go func() { _ = s.udp.ActivateAndServe() }()
	go func() { _ = s.tcp.ActivateAndServe() }()
	t.Cleanup(func() {
		_ = s.udp.Shutdown()
		_ = s.tcp.Shutdown()
	})
	return s
}

// Addr is host:port to query.
func (s *Server) Addr() string { return s.addr }

// AddZone makes the server authoritative for zone (answers SOA at its apex).
func (s *Server) AddZone(zone string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zones[dns.Fqdn(strings.ToLower(zone))] = true
}

// Set replaces name's records of one type ("A", "AAAA", "CNAME", "TXT",
// "CAA") with values (zone-file data, e.g. `0 issue "letsencrypt.org"`).
func (s *Server) Set(name, typ string, values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setLocked(name, typ, values)
}

func (s *Server) setLocked(name, typ string, values []string) {
	fq := dns.Fqdn(strings.ToLower(name))
	qt := dns.StringToType[strings.ToUpper(typ)]
	if s.recs[fq] == nil {
		s.recs[fq] = map[uint16][]dns.RR{}
	}
	var rrs []dns.RR
	for _, v := range values {
		if qt == dns.TypeTXT {
			v = fmt.Sprintf("%q", v)
		}
		rr, err := dns.NewRR(fmt.Sprintf("%s 60 IN %s %s", fq, strings.ToUpper(typ), v))
		if err != nil {
			panic(fmt.Sprintf("dnstest: %s %s %s: %v", name, typ, v, err))
		}
		rrs = append(rrs, rr)
	}
	if len(rrs) == 0 {
		delete(s.recs[fq], qt)
		return
	}
	s.recs[fq][qt] = rrs
}

// Get returns name's values of one type.
func (s *Server) Get(name, typ string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, rr := range s.recs[dns.Fqdn(strings.ToLower(name))][dns.StringToType[strings.ToUpper(typ)]] {
		out = append(out, rrValue(rr))
	}
	return out
}

func rrValue(rr dns.RR) string {
	switch v := rr.(type) {
	case *dns.TXT:
		return strings.Join(v.Txt, "")
	case *dns.A:
		return v.A.String()
	case *dns.AAAA:
		return v.AAAA.String()
	case *dns.CNAME:
		return v.Target
	}
	h := rr.Header().String()
	return strings.TrimSpace(strings.TrimPrefix(rr.String(), h))
}

func (s *Server) zoneOf(fq string) string {
	best := ""
	for z := range s.zones {
		if (fq == z || strings.HasSuffix(fq, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best
}

// lookup finds records for fq (exact, then the closest wildcard).
func (s *Server) lookup(fq string) (map[uint16][]dns.RR, string) {
	if r, ok := s.recs[fq]; ok && len(r) > 0 {
		return r, fq
	}
	for n := fq; strings.Count(n, ".") > 1; {
		_, n, _ = strings.Cut(n, ".")
		if r, ok := s.recs["*."+n]; ok && len(r) > 0 {
			return r, "*." + n
		}
		if _, ok := s.recs[n]; ok {
			break // an existing name blocks wildcards above it
		}
	}
	return nil, ""
}

// ServeDNS implements dns.Handler.
func (s *Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative, m.RecursionAvailable = true, true
	if len(req.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(m)
		return
	}
	q := req.Question[0]
	fq := strings.ToLower(q.Name)
	s.mu.Lock()
	s.Queries[strings.TrimSuffix(fq, ".")+" "+dns.TypeToString[q.Qtype]]++
	zone := s.zoneOf(fq)
	if q.Qtype == dns.TypeSOA && zone == fq {
		m.Answer = append(m.Answer, soa(zone))
	} else {
		for range 8 { // follow CNAMEs inside the server
			recs, _ := s.lookup(fq)
			if recs == nil {
				break
			}
			rename := func(rr dns.RR) dns.RR {
				c := dns.Copy(rr)
				c.Header().Name = fq
				return c
			}
			if rrs, ok := recs[q.Qtype]; ok {
				for _, rr := range rrs {
					m.Answer = append(m.Answer, rename(rr))
				}
				break
			}
			if c, ok := recs[dns.TypeCNAME]; ok && q.Qtype != dns.TypeCNAME {
				m.Answer = append(m.Answer, rename(c[0]))
				fq = strings.ToLower(c[0].(*dns.CNAME).Target)
				continue
			}
			break
		}
		if len(m.Answer) == 0 && zone != "" {
			if recs, _ := s.lookup(strings.ToLower(q.Name)); recs == nil && strings.ToLower(q.Name) != zone {
				m.Rcode = dns.RcodeNameError
			}
			m.Ns = append(m.Ns, soa(zone))
		} else if len(m.Answer) == 0 && zone == "" {
			m.Rcode = dns.RcodeNameError
		}
	}
	s.mu.Unlock()
	_ = w.WriteMsg(m)
}

func soa(zone string) dns.RR {
	rr, _ := dns.NewRR(fmt.Sprintf("%s 60 IN SOA ns1.%s hostmaster.%s 1 7200 3600 1209600 60", zone, zone, zone))
	return rr
}

// Provider is a libdns provider whose zones live in a Server.
type Provider struct {
	S *Server
	// Fail, when set, makes every call fail with it (a revoked token).
	Fail error
	// Calls counts calls by method.
	Calls map[string]int
	mu    sync.Mutex
}

// NewProvider returns a provider for the server's zones.
func NewProvider(s *Server) *Provider { return &Provider{S: s, Calls: map[string]int{}} }

func (p *Provider) call(name string) error {
	p.mu.Lock()
	p.Calls[name]++
	p.mu.Unlock()
	return p.Fail
}

// ListZones implements libdns.ZoneLister.
func (p *Provider) ListZones(context.Context) ([]libdns.Zone, error) {
	if err := p.call("ListZones"); err != nil {
		return nil, err
	}
	p.S.mu.Lock()
	defer p.S.mu.Unlock()
	var out []libdns.Zone
	for z := range p.S.zones {
		out = append(out, libdns.Zone{Name: z})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetRecords implements libdns.RecordGetter.
func (p *Provider) GetRecords(_ context.Context, zone string) ([]libdns.Record, error) {
	if err := p.call("GetRecords"); err != nil {
		return nil, err
	}
	zone = dns.Fqdn(strings.ToLower(zone))
	p.S.mu.Lock()
	defer p.S.mu.Unlock()
	var out []libdns.Record
	for name, byType := range p.S.recs {
		if name != zone && !strings.HasSuffix(name, "."+zone) {
			continue
		}
		for qt, rrs := range byType {
			for _, rr := range rrs {
				out = append(out, libdns.RR{Name: libdns.RelativeName(name, zone), Type: dns.TypeToString[qt], Data: rrValue(rr), TTL: time.Minute})
			}
		}
	}
	return out, nil
}

func (p *Provider) edit(zone string, recs []libdns.Record, op func(name, typ, data string)) []libdns.Record {
	zone = dns.Fqdn(strings.ToLower(zone))
	for _, r := range recs {
		rr := r.RR()
		op(libdns.AbsoluteName(rr.Name, zone), rr.Type, rr.Data)
	}
	return recs
}

// AppendRecords implements libdns.RecordAppender.
func (p *Provider) AppendRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	if err := p.call("AppendRecords"); err != nil {
		return nil, err
	}
	p.S.mu.Lock()
	defer p.S.mu.Unlock()
	return p.edit(zone, recs, func(name, typ, data string) {
		var vals []string
		for _, rr := range p.S.recs[dns.Fqdn(name)][dns.StringToType[typ]] {
			vals = append(vals, rrValue(rr))
		}
		p.S.setLocked(name, typ, append(vals, data))
	}), nil
}

// SetRecords implements libdns.RecordSetter.
func (p *Provider) SetRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	if err := p.call("SetRecords"); err != nil {
		return nil, err
	}
	p.S.mu.Lock()
	defer p.S.mu.Unlock()
	byKey := map[string][]string{}
	var order []string
	p.edit(zone, recs, func(name, typ, data string) {
		k := name + "\x00" + typ
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], data)
	})
	for _, k := range order {
		name, typ, _ := strings.Cut(k, "\x00")
		p.S.setLocked(name, typ, byKey[k])
	}
	return recs, nil
}

// DeleteRecords implements libdns.RecordDeleter.
func (p *Provider) DeleteRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	if err := p.call("DeleteRecords"); err != nil {
		return nil, err
	}
	p.S.mu.Lock()
	defer p.S.mu.Unlock()
	return p.edit(zone, recs, func(name, typ, data string) {
		var keep []string
		for _, rr := range p.S.recs[dns.Fqdn(name)][dns.StringToType[typ]] {
			if v := rrValue(rr); data != "" && v != data {
				keep = append(keep, v)
			}
		}
		p.S.setLocked(name, typ, keep)
	}), nil
}

// Addr parses an address or panics (for test tables).
func Addr(s string) netip.Addr { return netip.MustParseAddr(s) }
