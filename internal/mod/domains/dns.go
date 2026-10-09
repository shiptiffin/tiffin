package domains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/edge"
)

// providerRecord is a connected DNS provider as stored: the credentials
// sealed with the box's key, the zones it saw.
type providerRecord struct {
	Name        string    `json:"name"`
	Sealed      []byte    `json:"sealed"`
	Zones       []string  `json:"zones"`
	ConnectedAt time.Time `json:"connectedAt"`
	ConnectedBy string    `json:"connectedBy"`
	ZonesAt     time.Time `json:"zonesAt"`
}

type providerState struct {
	rec  providerRecord
	prov dnskit.Provider
	err  error // opening it failed (credentials unreadable)
}

func (m *Module) loadProviders(ctx context.Context) error {
	all, err := m.p.DB.KVList(ctx, ns)
	if err != nil {
		return err
	}
	for k, v := range all {
		name, ok := strings.CutPrefix(k, "provider/")
		if !ok {
			continue
		}
		var rec providerRecord
		if json.Unmarshal(v, &rec) != nil {
			continue
		}
		st := &providerState{rec: rec}
		st.prov, st.err = m.open(rec)
		m.provs[name] = st
	}
	return nil
}

func (m *Module) open(rec providerRecord) (dnskit.Provider, error) {
	if m.p.Secrets == nil {
		return nil, errors.New("the box's secrets are not available")
	}
	raw, err := m.p.Secrets.Unseal(rec.Sealed)
	if err != nil {
		return nil, fmt.Errorf("read the stored credentials: %w", err)
	}
	var creds map[string]string
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, err
	}
	return dnskit.Open(rec.Name, creds)
}

// connect verifies credentials by listing the provider's zones, then
// stores them sealed. Listing zones is the check because it is what the box
// needs and it works with every kind of token (Cloudflare account-owned
// tokens fail the user token-verify endpoint, but list zones fine).
func (m *Module) connect(ctx context.Context, name string, creds map[string]string, by string) (*providerState, error) {
	prov, err := dnskit.Open(name, creds)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	zones, err := dnskit.Zones(cctx, prov)
	if err != nil {
		return nil, fmt.Errorf("the provider refused the credentials or could not list zones: %w", err)
	}
	raw, _ := json.Marshal(creds)
	sealed, err := m.p.Secrets.Seal(raw)
	if err != nil {
		return nil, err
	}
	now := m.now()
	rec := providerRecord{Name: name, Sealed: sealed, Zones: zones, ConnectedAt: now, ConnectedBy: by, ZonesAt: now}
	b, _ := json.Marshal(rec)
	if err := m.p.DB.KVPut(ctx, ns, "provider/"+name, b); err != nil {
		return nil, err
	}
	st := &providerState{rec: rec, prov: prov}
	m.mu.Lock()
	m.provs[name] = st
	m.mu.Unlock()
	return st, nil
}

func (m *Module) disconnect(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	_, ok := m.provs[name]
	delete(m.provs, name)
	m.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, m.p.DB.KVDelete(ctx, ns, "provider/"+name)
}

// providerFor finds the connected provider whose zones hold name.
func (m *Module) providerFor(name string) (st *providerState, zone string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providerForLocked(name)
}

func (m *Module) providerForLocked(name string) (*providerState, string) {
	names := make([]string, 0, len(m.provs))
	for n := range m.provs {
		names = append(names, n)
	}
	sort.Strings(names)
	var best *providerState
	bestZone := ""
	for _, n := range names {
		st := m.provs[n]
		if st.prov == nil {
			continue
		}
		if z, ok := dnskit.ZoneFor(st.rec.Zones, name); ok && len(z) > len(bestZone) {
			best, bestZone = st, z
		}
	}
	return best, bestZone
}

// wildcardLocked is the DNS-01 challenge for *.<apps domain>, when a
// connected provider holds the apps domain's zone.
func (m *Module) wildcardLocked() *edge.DNSChallenge {
	if m.p == nil || m.p.AppsDomain() == "" {
		return nil
	}
	st, _ := m.providerForLocked(m.p.AppsDomain())
	if st == nil {
		return nil
	}
	return &edge.DNSChallenge{Name: st.rec.Name, Provider: st.prov, Resolvers: m.p.Reach.Resolvers}
}

// setRecords writes records through whichever provider holds each zone.
func (m *Module) setRecords(ctx context.Context, recs []dnskit.Record, del bool) error {
	byZone := map[string][]dnskit.Record{}
	provs := map[string]*providerState{}
	for _, r := range recs {
		st, zone := m.providerFor(r.Name)
		if st == nil {
			return fmt.Errorf("%w: %s (connect one with `tiffin dns connect cloudflare --token ...`)", dnskit.ErrNoZone, r.Name)
		}
		byZone[zone] = append(byZone[zone], r)
		provs[zone] = st
	}
	zones := make([]string, 0, len(byZone))
	for z := range byZone {
		zones = append(zones, z)
	}
	sort.Strings(zones)
	for _, z := range zones {
		var err error
		if del {
			err = dnskit.DeleteRecords(ctx, provs[z].prov, z, byZone[z])
		} else {
			err = dnskit.SetRecords(ctx, provs[z].prov, z, byZone[z])
		}
		if err != nil {
			return fmt.Errorf("%s (zone %s): %w", provs[z].rec.Name, z, err)
		}
	}
	return nil
}

// dnsManager is Platform.DNS.
type dnsManager struct{ m *Module }

func (d *dnsManager) Manages(_ context.Context, name string) bool {
	st, _ := d.m.providerFor(name)
	return st != nil
}

func (d *dnsManager) SetRecords(ctx context.Context, recs []dnskit.Record, by string) error {
	if err := d.m.setRecords(ctx, recs, false); err != nil {
		return err
	}
	_ = d.m.p.DB.Audit(ctx, by, "dns.records.set", "box", recs)
	return nil
}

func (d *dnsManager) DeleteRecords(ctx context.Context, recs []dnskit.Record, by string) error {
	if err := d.m.setRecords(ctx, recs, true); err != nil {
		return err
	}
	_ = d.m.p.DB.Audit(ctx, by, "dns.records.delete", "box", recs)
	return nil
}
