package dnskit

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libdns/cloudflare"
	"github.com/libdns/libdns"
)

// Provider is a DNS host the box can manage records with, through libdns.
// Any libdns provider fits (Namecheap, Route53, DigitalOcean, Hetzner DNS
// ...): register a Kind for it.
type Provider interface {
	libdns.RecordGetter
	libdns.RecordAppender
	libdns.RecordSetter
	libdns.RecordDeleter
	libdns.ZoneLister
}

// Field is one credential a provider needs.
type Field struct {
	Name   string `json:"name" doc:"The key in credentials, e.g. token."`
	Label  string `json:"label"`
	Help   string `json:"help,omitempty"`
	Secret bool   `json:"secret"`
}

// Kind is a supported DNS provider.
type Kind struct {
	Name   string  `json:"name" doc:"Its id in the API, e.g. cloudflare."`
	Label  string  `json:"label"`
	Help   string  `json:"help" doc:"How to make a credential for it."`
	Fields []Field `json:"fields"`
	// New builds a provider from credentials (keys are Field names).
	New func(creds map[string]string) (Provider, error) `json:"-"`
}

var (
	kindsMu sync.Mutex
	kinds   = map[string]Kind{}
)

// Register adds a provider kind. Call it from init().
func Register(k Kind) {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	kinds[k.Name] = k
}

// Kinds lists the supported providers by name.
func Kinds() []Kind {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	out := make([]Kind, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the provider kind with that name.
func Lookup(name string) (Kind, bool) {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	k, ok := kinds[name]
	return k, ok
}

// Open builds a provider, checking that every credential is present.
func Open(name string, creds map[string]string) (Provider, error) {
	k, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown DNS provider %q", name)
	}
	for _, f := range k.Fields {
		if strings.TrimSpace(creds[f.Name]) == "" {
			return nil, fmt.Errorf("%s needs %s (%s)", k.Label, f.Name, f.Label)
		}
	}
	return k.New(creds)
}

// Zones lists the zones a provider manages, as plain names ("example.com").
func Zones(ctx context.Context, p Provider) ([]string, error) {
	zs, err := p.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(zs))
	for _, z := range zs {
		n := strings.TrimSuffix(strings.ToLower(z.Name), ".")
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ZoneFor returns the zone among zones that holds name (the longest suffix).
func ZoneFor(zones []string, name string) (string, bool) {
	name = strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(name), "."), "*.")
	best := ""
	for _, z := range zones {
		if (name == z || strings.HasSuffix(name, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best, best != ""
}

// ErrNoZone is returned when no connected provider holds a name's zone.
var ErrNoZone = errors.New("no connected DNS provider manages this name")

// SetRecords creates or replaces records (each name+type keeps exactly the
// given values) in the zone that holds them. TTL 0 means automatic.
func SetRecords(ctx context.Context, p Provider, zone string, recs []Record) error {
	var lib []libdns.Record
	for _, r := range recs {
		rr, err := toLibdns(r, zone)
		if err != nil {
			return err
		}
		lib = append(lib, rr)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := p.SetRecords(ctx, zone+".", lib)
	return err
}

// DeleteRecords removes records by name and type (any value) from zone.
func DeleteRecords(ctx context.Context, p Provider, zone string, recs []Record) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	have, err := p.GetRecords(ctx, zone+".")
	if err != nil {
		return err
	}
	var del []libdns.Record
	for _, h := range have {
		rr := h.RR()
		for _, r := range recs {
			if strings.EqualFold(rr.Type, r.Type) && strings.EqualFold(libdns.AbsoluteName(rr.Name, zone+"."), strings.TrimSuffix(r.Name, ".")+".") {
				del = append(del, h)
			}
		}
	}
	if len(del) == 0 {
		return nil
	}
	_, err = p.DeleteRecords(ctx, zone+".", del)
	return err
}

func toLibdns(r Record, zone string) (libdns.Record, error) {
	name := libdns.RelativeName(strings.TrimSuffix(r.Name, ".")+".", zone+".")
	ttl := time.Duration(r.TTL) * time.Second
	switch strings.ToUpper(r.Type) {
	case "A", "AAAA":
		ip, err := netip.ParseAddr(r.Value)
		if err != nil {
			return nil, fmt.Errorf("record %s: %q is not an IP address", r.Name, r.Value)
		}
		return libdns.Address{Name: name, TTL: ttl, IP: ip}, nil
	case "CNAME":
		return libdns.CNAME{Name: name, TTL: ttl, Target: strings.TrimSuffix(r.Value, ".") + "."}, nil
	case "TXT":
		return libdns.TXT{Name: name, TTL: ttl, Text: r.Value}, nil
	case "MX", "CAA":
		return libdns.RR{Name: name, TTL: ttl, Type: strings.ToUpper(r.Type), Data: r.Value}.Parse()
	}
	return nil, fmt.Errorf("record %s: type %q is not supported (A, AAAA, CNAME, TXT, MX, CAA)", r.Name, r.Type)
}

func init() {
	Register(Kind{
		Name:  "cloudflare",
		Label: "Cloudflare",
		Help: "Create an API token at dash.cloudflare.com → My Profile (or Manage Account) → API Tokens → Create Token, " +
			"with the permission Zone · DNS · Edit on the zones you want the box to manage (adding Zone · Zone · Read helps it list them). " +
			"Account-owned and user tokens both work. The token is stored encrypted on the box and never shown again.",
		Fields: []Field{{Name: "token", Label: "API token", Help: "Zone · DNS · Edit", Secret: true}},
		New: func(c map[string]string) (Provider, error) {
			return &cloudflare.Provider{APIToken: strings.TrimSpace(c["token"])}, nil
		},
	})
}
