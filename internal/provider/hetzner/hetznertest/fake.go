// Package hetznertest is an in-memory fake of the parts of the Hetzner Cloud
// API that Tiffin uses, served over httptest. hcloud-go talks to it through
// hcloud.WithEndpoint, so tests exercise the real client end to end without
// a token or an account.
package hetznertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud/schema"
	"golang.org/x/crypto/ssh"
)

// Token is the only token the fake accepts.
const Token = "fake-hcloud-token"

// Fake is a fake Hetzner Cloud project.
type Fake struct {
	*httptest.Server

	mu        sync.Mutex
	nextID    int64
	servers   map[int64]*schema.Server
	volumes   map[int64]*schema.Volume
	firewalls map[int64]*schema.Firewall
	keys      map[int64]*schema.SSHKey
	ips       map[int64]*schema.PrimaryIP
	actions   map[int64]*schema.Action
	// Mutations is every non-GET request ("POST /servers"), in order.
	Mutations []string
	// ChangeTypes records every server type change as "<type> upgrade_disk=<bool>".
	ChangeTypes []string
}

// Catalog.
var (
	Locations = []schema.Location{
		{ID: 1, Name: "fsn1", City: "Falkenstein", Country: "DE", NetworkZone: "eu-central"},
		{ID: 2, Name: "nbg1", City: "Nuremberg", Country: "DE", NetworkZone: "eu-central"},
		{ID: 3, Name: "hel1", City: "Helsinki", Country: "FI", NetworkZone: "eu-central"},
		{ID: 4, Name: "ash", City: "Ashburn, VA", Country: "US", NetworkZone: "us-east"},
	}
	// Prices (EUR, net per month) the fake quotes.
	ServerPrices = map[string]float64{"cax11": 4.49, "cx23": 4.99, "cax21": 7.99, "cax31": 15.99, "cax41": 31.49,
		"cx33": 7.99, "cpx31": 15.59, "ccx13": 14.49}
	IPv4Price   = 0.61
	VolumePerGB = 0.0572
	VAT         = 0.19
)

func price(net float64) schema.Price {
	return schema.Price{Net: strconv.FormatFloat(net, 'f', 4, 64), Gross: strconv.FormatFloat(net*(1+VAT), 'f', 4, 64)}
}

func serverTypes() []schema.ServerType {
	mk := func(id int64, name, arch string, cores int, mem float32, disk int, avail ...string) schema.ServerType {
		cpu := "shared"
		if strings.HasPrefix(name, "ccx") {
			cpu = "dedicated"
		}
		st := schema.ServerType{ID: id, Name: name, Description: strings.ToUpper(name), Cores: cores, Memory: mem, Disk: disk,
			StorageType: "local", CPUType: cpu, Architecture: arch, Category: "cost_optimized"}
		for _, l := range Locations {
			ok := slices.Contains(avail, l.Name)
			st.Locations = append(st.Locations, schema.ServerTypeLocation{ID: l.ID, Name: l.Name, Available: ok, Recommended: ok})
			if ok {
				st.Prices = append(st.Prices, schema.PricingServerTypePrice{Location: l.Name, PriceHourly: price(ServerPrices[name] / 730), PriceMonthly: price(ServerPrices[name])})
			}
		}
		return st
	}
	eu := []string{"fsn1", "nbg1", "hel1"}
	all := []string{"fsn1", "nbg1", "hel1", "ash"}
	return []schema.ServerType{
		mk(45, "cax11", "arm", 2, 4, 40, eu...),
		mk(108, "cx23", "x86", 2, 4, 40, all...),
		mk(93, "cax21", "arm", 4, 8, 80, eu...),
		mk(94, "cax31", "arm", 8, 16, 160, eu...),
		mk(95, "cax41", "arm", 16, 32, 320, "fsn1"),
		mk(109, "cx33", "x86", 4, 8, 80, all...),
		mk(110, "cpx31", "x86", 4, 8, 160, all...),
		mk(111, "ccx13", "x86", 2, 8, 80, all...),
	}
}

// New starts a fake project.
func New() *Fake {
	f := &Fake{nextID: 1000, servers: map[int64]*schema.Server{}, volumes: map[int64]*schema.Volume{},
		firewalls: map[int64]*schema.Firewall{}, keys: map[int64]*schema.SSHKey{}, ips: map[int64]*schema.PrimaryIP{}, actions: map[int64]*schema.Action{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Count returns how many servers, volumes, firewalls and SSH keys exist.
func (f *Fake) Count() (servers, volumes, firewalls, keys int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.servers), len(f.volumes), len(f.firewalls), len(f.keys)
}

// Firewall returns the first firewall, if any.
func (f *Fake) Firewall() *schema.Firewall {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fw := range f.firewalls {
		c := *fw
		return &c
	}
	return nil
}

// Volume returns the first volume, if any.
func (f *Fake) Volume() *schema.Volume {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.volumes {
		c := *v
		return &c
	}
	return nil
}

// ServerByName returns a server, if it exists.
func (f *Fake) ServerByName(name string) *schema.Server {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.servers {
		if s.Name == name {
			c := *s
			return &c
		}
	}
	return nil
}

// AddForeignServer adds a server Tiffin did not make (no labels).
func (f *Fake) AddForeignServer(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newServer(name, serverTypes()[0], Locations[0], map[string]string{})
}

// AddHandmadeServer adds a server made in the console, the way the owner
// made shiptiffin-server: an x86 cx23 in nbg1 with its primary IPs
// (auto-delete on), a 40 GB XFS volume attached, no labels, no firewall.
// It returns the server's and the volume's IDs.
func (f *Fake) AddHandmadeServer(name string) (int64, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.newServer(name, serverTypes()[1], Locations[1], map[string]string{"owner": "me"})
	xfs := "xfs"
	v := &schema.Volume{ID: f.id(), Name: name + "-vol", Size: 40, Status: "available", Labels: map[string]string{}, Format: &xfs, Location: s.Location, Server: &s.ID}
	v.LinuxDevice = fmt.Sprintf("/dev/disk/by-id/scsi-0HC_Volume_%d", v.ID)
	f.volumes[v.ID] = v
	s.Volumes = append(s.Volumes, v.ID)
	return s.ID, v.ID
}

// Protect turns on a server's delete and rebuild protection (as adopt does).
func (f *Fake) Protect(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.servers[id]; s != nil {
		s.Protection.Delete, s.Protection.Rebuild = true, true
	}
}

// PrimaryIP returns a primary IP by ID.
func (f *Fake) PrimaryIP(id int64) *schema.PrimaryIP {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ip := f.ips[id]; ip != nil {
		c := *ip
		return &c
	}
	return nil
}

// IPCount is how many primary IPs exist.
func (f *Fake) IPCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.ips) }

func (f *Fake) newServer(name string, st schema.ServerType, l schema.Location, labels map[string]string) *schema.Server {
	s := &schema.Server{ID: f.id(), Name: name, Status: "running", Created: time.Now(), ServerType: st, Labels: labels, Location: l, PrimaryDiskSize: st.Disk}
	n := s.ID % 250
	v4 := &schema.PrimaryIP{ID: f.id(), IP: fmt.Sprintf("203.0.113.%d", n), Type: "ipv4", AutoDelete: true, Labels: map[string]string{}, Location: l, AssigneeID: &s.ID, AssigneeType: "server", Name: "primary_ip-" + strconv.FormatInt(n, 10)}
	v6 := &schema.PrimaryIP{ID: f.id(), IP: fmt.Sprintf("2001:db8:%x::/64", n), Type: "ipv6", AutoDelete: true, Labels: map[string]string{}, Location: l, AssigneeID: &s.ID, AssigneeType: "server", Name: "primary_ip6-" + strconv.FormatInt(n, 10)}
	f.ips[v4.ID], f.ips[v6.ID] = v4, v6
	s.PublicNet.IPv4 = schema.ServerPublicNetIPv4{ID: v4.ID, IP: v4.IP}
	s.PublicNet.IPv6 = schema.ServerPublicNetIPv6{ID: v6.ID, IP: v6.IP}
	f.servers[s.ID] = s
	return s
}

// AddKey adds an SSH key someone uploaded in the console (no labels).
func (f *Fake) AddKey(name, pub string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		panic(err)
	}
	id := f.id()
	f.keys[id] = &schema.SSHKey{ID: id, Name: name, PublicKey: pub, Fingerprint: ssh.FingerprintLegacyMD5(pk), Labels: map[string]string{}}
}

func (f *Fake) id() int64 { f.nextID++; return f.nextID }

func (f *Fake) action(cmd string) schema.Action {
	now := time.Now()
	a := schema.Action{ID: f.id(), Status: "success", Command: cmd, Progress: 100, Started: now, Finished: &now}
	f.actions[a.ID] = &a
	return a
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, code int, errCode, msg string) {
	write(w, code, schema.ErrorResponse{Error: schema.Error{Code: errCode, Message: msg}})
}

// matches implements label selectors of the form "a=b,c=d".
func matches(labels map[string]string, sel string) bool {
	if sel == "" {
		return true
	}
	for _, term := range strings.Split(sel, ",") {
		k, v, ok := strings.Cut(term, "=")
		if !ok || labels[k] != v {
			return false
		}
	}
	return true
}

func sorted[T any](m map[int64]*T, keep func(*T) bool) []T {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := []T{}
	for _, id := range ids {
		if keep(m[id]) {
			out = append(out, *m[id])
		}
	}
	return out
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+Token {
		apiError(w, 401, "unauthorized", "unable to authenticate")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet {
		f.Mutations = append(f.Mutations, r.Method+" "+r.URL.Path)
	}
	q := r.URL.Query()
	sel := q.Get("label_selector")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var id int64
	if len(parts) > 1 {
		id, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	route := r.Method + " " + parts[0]
	if len(parts) > 1 {
		route += "/{id}"
	}
	if len(parts) > 3 {
		route += "/actions/" + parts[3]
	}
	decode := func(v any) bool {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			apiError(w, 400, "invalid_input", err.Error())
			return false
		}
		return true
	}
	switch route {
	case "GET locations":
		out := []schema.Location{}
		for _, l := range Locations {
			if n := q.Get("name"); n == "" || n == l.Name {
				out = append(out, l)
			}
		}
		write(w, 200, schema.LocationListResponse{Locations: out})
	case "GET server_types":
		out := []schema.ServerType{}
		for _, st := range serverTypes() {
			if n := q.Get("name"); n == "" || n == st.Name {
				out = append(out, st)
			}
		}
		write(w, 200, schema.ServerTypeListResponse{ServerTypes: out})
	case "GET images":
		arch := q.Get("architecture")
		name := q.Get("name")
		if name != "ubuntu-24.04" && name != "ubuntu-26.04" {
			write(w, 200, schema.ImageListResponse{Images: []schema.Image{}})
			return
		}
		imgID := int64(161547269)
		if arch == "x86" {
			imgID = 161547270
		}
		if name == "ubuntu-26.04" {
			imgID += 100
		}
		write(w, 200, schema.ImageListResponse{Images: []schema.Image{{ID: imgID, Name: &name, Type: "system", Status: "available", OSFlavor: "ubuntu", Architecture: arch}}})
	case "GET pricing":
		pr := schema.Pricing{Currency: "EUR", VATRate: strconv.FormatFloat(VAT*100, 'f', 2, 64)}
		pr.Volume.PricePerGBPerMonth = price(VolumePerGB)
		var ipPrices []schema.PricingPrimaryIPTypePrice
		for _, l := range Locations {
			ipPrices = append(ipPrices, schema.PricingPrimaryIPTypePrice{Location: l.Name, PriceHourly: price(IPv4Price / 730), PriceMonthly: price(IPv4Price)})
		}
		var ip6Prices []schema.PricingPrimaryIPTypePrice
		for _, l := range Locations {
			ip6Prices = append(ip6Prices, schema.PricingPrimaryIPTypePrice{Location: l.Name, PriceHourly: price(0), PriceMonthly: price(0)})
		}
		pr.PrimaryIPs = []schema.PricingPrimaryIP{{Type: "ipv4", Prices: ipPrices}, {Type: "ipv6", Prices: ip6Prices}}
		// Like the real API, floating IP prices come with them (hcloud-go
		// sizes its primary IP list by the floating IP list).
		pr.FloatingIPs = []schema.PricingFloatingIPType{{Type: "ipv4"}, {Type: "ipv6"}}
		for _, st := range serverTypes() {
			pr.ServerTypes = append(pr.ServerTypes, schema.PricingServerType{ID: st.ID, Name: st.Name, Prices: st.Prices})
		}
		write(w, 200, schema.PricingGetResponse{Pricing: pr})
	case "GET actions":
		out := []schema.Action{}
		for _, s := range q["id"] {
			n, _ := strconv.ParseInt(s, 10, 64)
			if a := f.actions[n]; a != nil {
				out = append(out, *a)
			}
		}
		write(w, 200, schema.ActionListResponse{Actions: out})
	case "GET primary_ips":
		write(w, 200, schema.PrimaryIPListResponse{PrimaryIPs: sorted(f.ips, func(ip *schema.PrimaryIP) bool { return matches(ip.Labels, sel) })})
	case "GET primary_ips/{id}":
		ip := f.ips[id]
		if ip == nil {
			apiError(w, 404, "not_found", "primary ip not found")
			return
		}
		write(w, 200, schema.PrimaryIPGetResponse{PrimaryIP: *ip})
	case "PUT primary_ips/{id}":
		ip := f.ips[id]
		var req schema.PrimaryIPUpdateRequest
		if ip == nil || !decode(&req) {
			if ip == nil {
				apiError(w, 404, "not_found", "primary ip not found")
			}
			return
		}
		if req.Labels != nil {
			ip.Labels = *req.Labels
		}
		if req.AutoDelete != nil {
			ip.AutoDelete = *req.AutoDelete
		}
		write(w, 200, schema.PrimaryIPUpdateResponse{PrimaryIP: *ip})
	case "POST primary_ips/{id}/actions/change_protection":
		ip := f.ips[id]
		var req struct {
			Delete bool `json:"delete"`
		}
		if ip == nil || !decode(&req) {
			if ip == nil {
				apiError(w, 404, "not_found", "primary ip not found")
			}
			return
		}
		ip.Protection.Delete = req.Delete
		write(w, 201, schema.ServerActionPoweronResponse{Action: f.action("change_protection")})
	case "DELETE primary_ips/{id}":
		ip := f.ips[id]
		if ip == nil {
			apiError(w, 404, "not_found", "primary ip not found")
			return
		}
		if ip.Protection.Delete {
			apiError(w, 403, "protected", "primary ip is protected")
			return
		}
		delete(f.ips, id)
		w.WriteHeader(204)
	case "PUT servers/{id}":
		s := f.servers[id]
		var req schema.ServerUpdateRequest
		if s == nil || !decode(&req) {
			if s == nil {
				apiError(w, 404, "not_found", "server not found")
			}
			return
		}
		if req.Labels != nil {
			s.Labels = *req.Labels
		}
		write(w, 200, schema.ServerUpdateResponse{Server: *s})
	case "POST servers/{id}/actions/change_protection":
		s := f.servers[id]
		var req struct {
			Delete  *bool `json:"delete"`
			Rebuild *bool `json:"rebuild"`
		}
		if s == nil || !decode(&req) {
			if s == nil {
				apiError(w, 404, "not_found", "server not found")
			}
			return
		}
		if req.Delete != nil {
			s.Protection.Delete = *req.Delete
		}
		if req.Rebuild != nil {
			s.Protection.Rebuild = *req.Rebuild
		}
		write(w, 201, schema.ServerActionPoweronResponse{Action: f.action("change_protection")})
	case "PUT volumes/{id}":
		v := f.volumes[id]
		var req schema.VolumeUpdateRequest
		if v == nil || !decode(&req) {
			if v == nil {
				apiError(w, 404, "not_found", "volume not found")
			}
			return
		}
		if req.Labels != nil {
			v.Labels = *req.Labels
		}
		write(w, 200, schema.VolumeUpdateResponse{Volume: *v})
	case "POST volumes/{id}/actions/change_protection":
		v := f.volumes[id]
		var req struct {
			Delete *bool `json:"delete"`
		}
		if v == nil || !decode(&req) {
			if v == nil {
				apiError(w, 404, "not_found", "volume not found")
			}
			return
		}
		if req.Delete != nil {
			v.Protection.Delete = *req.Delete
		}
		write(w, 201, schema.ServerActionPoweronResponse{Action: f.action("change_protection")})

	// ---- SSH keys ----
	case "GET ssh_keys":
		write(w, 200, schema.SSHKeyListResponse{SSHKeys: sorted(f.keys, func(k *schema.SSHKey) bool {
			return matches(k.Labels, sel) && (q.Get("fingerprint") == "" || q.Get("fingerprint") == k.Fingerprint) && (q.Get("name") == "" || q.Get("name") == k.Name)
		})})
	case "POST ssh_keys":
		var req schema.SSHKeyCreateRequest
		if !decode(&req) {
			return
		}
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
		if err != nil {
			apiError(w, 400, "invalid_input", "invalid public key")
			return
		}
		fp := ssh.FingerprintLegacyMD5(pk)
		for _, k := range f.keys {
			if k.Name == req.Name || k.Fingerprint == fp {
				apiError(w, 409, "uniqueness_error", "SSH key with the same fingerprint or name already exists")
				return
			}
		}
		k := &schema.SSHKey{ID: f.id(), Name: req.Name, PublicKey: req.PublicKey, Fingerprint: fp, Labels: deref(req.Labels), Created: time.Now()}
		f.keys[k.ID] = k
		write(w, 201, schema.SSHKeyCreateResponse{SSHKey: *k})
	case "DELETE ssh_keys/{id}":
		if f.keys[id] == nil {
			apiError(w, 404, "not_found", "ssh key not found")
			return
		}
		delete(f.keys, id)
		w.WriteHeader(204)

	// ---- firewalls ----
	case "GET firewalls":
		write(w, 200, schema.FirewallListResponse{Firewalls: sorted(f.firewalls, func(x *schema.Firewall) bool { return matches(x.Labels, sel) })})
	case "POST firewalls":
		var req schema.FirewallCreateRequest
		if !decode(&req) {
			return
		}
		for _, x := range f.firewalls {
			if x.Name == req.Name {
				apiError(w, 409, "uniqueness_error", "name is already used")
				return
			}
		}
		fw := &schema.Firewall{ID: f.id(), Name: req.Name, Labels: deref(req.Labels), Created: time.Now(), Rules: rules(req.Rules), AppliedTo: []schema.FirewallResource{}}
		f.firewalls[fw.ID] = fw
		write(w, 201, schema.FirewallCreateResponse{Firewall: *fw, Actions: []schema.Action{f.action("set_firewall_rules")}})
	case "POST firewalls/{id}/actions/set_rules":
		fw := f.firewalls[id]
		var req schema.FirewallActionSetRulesRequest
		if fw == nil || !decode(&req) {
			if fw == nil {
				apiError(w, 404, "not_found", "firewall not found")
			}
			return
		}
		fw.Rules = rules(req.Rules)
		write(w, 201, schema.FirewallActionSetRulesResponse{Actions: []schema.Action{f.action("set_firewall_rules")}})
	case "POST firewalls/{id}/actions/apply_to_resources":
		fw := f.firewalls[id]
		var req schema.FirewallActionApplyToResourcesRequest
		if fw == nil || !decode(&req) {
			if fw == nil {
				apiError(w, 404, "not_found", "firewall not found")
			}
			return
		}
		for _, res := range req.ApplyTo {
			if res.Server != nil {
				f.applyFirewall(fw, res.Server.ID)
			}
		}
		write(w, 201, schema.FirewallActionApplyToResourcesResponse{Actions: []schema.Action{f.action("apply_firewall")}})
	case "DELETE firewalls/{id}":
		fw := f.firewalls[id]
		if fw == nil {
			apiError(w, 404, "not_found", "firewall not found")
			return
		}
		if len(fw.AppliedTo) > 0 {
			apiError(w, 422, "resource_in_use", "firewall is still in use")
			return
		}
		delete(f.firewalls, id)
		w.WriteHeader(204)

	// ---- volumes ----
	case "GET volumes":
		write(w, 200, schema.VolumeListResponse{Volumes: sorted(f.volumes, func(v *schema.Volume) bool { return matches(v.Labels, sel) })})
	case "GET volumes/{id}":
		v := f.volumes[id]
		if v == nil {
			apiError(w, 404, "not_found", "volume not found")
			return
		}
		write(w, 200, schema.VolumeGetResponse{Volume: *v})
	case "POST volumes":
		var req schema.VolumeCreateRequest
		if !decode(&req) {
			return
		}
		if req.Location == nil {
			apiError(w, 400, "invalid_input", "location or server required")
			return
		}
		for _, x := range f.volumes {
			if x.Name == req.Name {
				apiError(w, 409, "uniqueness_error", "name is already used")
				return
			}
		}
		v := &schema.Volume{ID: f.id(), Name: req.Name, Size: req.Size, Status: "available", Labels: deref(req.Labels), Created: time.Now(), Format: req.Format}
		v.LinuxDevice = fmt.Sprintf("/dev/disk/by-id/scsi-0HC_Volume_%d", v.ID)
		v.Location = loc(req.Location.Name)
		if req.Location.ID != 0 {
			v.Location = loc(strconv.FormatInt(req.Location.ID, 10))
		}
		f.volumes[v.ID] = v
		write(w, 201, schema.VolumeCreateResponse{Volume: *v, Action: ptr(f.action("create_volume")), NextActions: []schema.Action{}})
	case "POST volumes/{id}/actions/attach":
		v := f.volumes[id]
		var req schema.VolumeActionAttachVolumeRequest
		if v == nil || !decode(&req) {
			if v == nil {
				apiError(w, 404, "not_found", "volume not found")
			}
			return
		}
		s := f.servers[req.Server]
		if s == nil || v.Server != nil {
			apiError(w, 422, "invalid_input", "cannot attach")
			return
		}
		v.Server = &s.ID
		s.Volumes = append(s.Volumes, v.ID)
		write(w, 201, schema.VolumeActionAttachVolumeResponse{Action: f.action("attach_volume")})
	case "POST volumes/{id}/actions/detach":
		v := f.volumes[id]
		if v == nil {
			apiError(w, 404, "not_found", "volume not found")
			return
		}
		if v.Server != nil {
			if s := f.servers[*v.Server]; s != nil {
				s.Volumes = slices.DeleteFunc(s.Volumes, func(x int64) bool { return x == v.ID })
			}
		}
		v.Server = nil
		write(w, 201, schema.VolumeActionDetachVolumeResponse{Action: f.action("detach_volume")})
	case "POST volumes/{id}/actions/resize":
		v := f.volumes[id]
		var req schema.VolumeActionResizeVolumeRequest
		if v == nil || !decode(&req) {
			if v == nil {
				apiError(w, 404, "not_found", "volume not found")
			}
			return
		}
		if req.Size < v.Size {
			apiError(w, 422, "invalid_input", "volumes cannot be shrunk")
			return
		}
		v.Size = req.Size
		write(w, 201, schema.VolumeActionResizeVolumeResponse{Action: f.action("resize_volume")})
	case "DELETE volumes/{id}":
		v := f.volumes[id]
		if v == nil {
			apiError(w, 404, "not_found", "volume not found")
			return
		}
		if v.Server != nil {
			apiError(w, 422, "resource_in_use", "volume is attached")
			return
		}
		if v.Protection.Delete {
			apiError(w, 403, "protected", "volume is protected")
			return
		}
		delete(f.volumes, id)
		w.WriteHeader(204)

	// ---- servers ----
	case "GET servers":
		write(w, 200, schema.ServerListResponse{Servers: sorted(f.servers, func(s *schema.Server) bool {
			return matches(s.Labels, sel) && (q.Get("name") == "" || q.Get("name") == s.Name)
		})})
	case "GET servers/{id}":
		s := f.servers[id]
		if s == nil {
			apiError(w, 404, "not_found", "server not found")
			return
		}
		write(w, 200, schema.ServerGetResponse{Server: *s})
	case "POST servers":
		var req schema.ServerCreateRequest
		if !decode(&req) {
			return
		}
		for _, x := range f.servers {
			if x.Name == req.Name {
				apiError(w, 409, "uniqueness_error", "server name is already used")
				return
			}
		}
		var st *schema.ServerType
		for _, t := range serverTypes() {
			if t.Name == req.ServerType.Name || t.ID == req.ServerType.ID {
				st = &t
			}
		}
		if st == nil || req.Location == "" {
			apiError(w, 400, "invalid_input", "server type and location required")
			return
		}
		s := f.newServer(req.Name, *st, loc(req.Location), deref(req.Labels))
		for _, vid := range req.Volumes {
			if v := f.volumes[vid]; v != nil {
				v.Server = &s.ID
				s.Volumes = append(s.Volumes, vid)
			}
		}
		for _, fwr := range req.Firewalls {
			if fw := f.firewalls[fwr.Firewall]; fw != nil {
				f.applyFirewall(fw, s.ID)
			}
		}
		write(w, 201, schema.ServerCreateResponse{Server: *s, Action: f.action("create_server"), NextActions: []schema.Action{f.action("start_server")}})
	case "POST servers/{id}/actions/shutdown", "POST servers/{id}/actions/poweroff":
		s := f.servers[id]
		if s == nil {
			apiError(w, 404, "not_found", "server not found")
			return
		}
		s.Status = "off"
		write(w, 201, schema.ServerActionPoweronResponse{Action: f.action(parts[3])})
	case "POST servers/{id}/actions/change_type":
		s := f.servers[id]
		var req schema.ServerActionChangeTypeRequest
		if s == nil || !decode(&req) {
			if s == nil {
				apiError(w, 404, "not_found", "server not found")
			}
			return
		}
		var st *schema.ServerType
		for _, t := range serverTypes() {
			if t.Name == req.ServerType.Name || t.ID == req.ServerType.ID {
				st = &t
			}
		}
		switch {
		case st == nil:
			apiError(w, 400, "invalid_input", "unknown server type")
		case s.Status != "off":
			apiError(w, 409, "conflict", "server must be powered off")
		case st.Architecture != s.ServerType.Architecture:
			apiError(w, 422, "invalid_input", "server type has a different architecture")
		case st.Disk < s.PrimaryDiskSize:
			apiError(w, 422, "invalid_server_type", "the disk of the server is too big for the new server type")
		default:
			s.ServerType = *st
			if req.UpgradeDisk {
				s.PrimaryDiskSize = st.Disk
			}
			f.ChangeTypes = append(f.ChangeTypes, st.Name+" upgrade_disk="+strconv.FormatBool(req.UpgradeDisk))
			write(w, 201, schema.ServerActionPoweronResponse{Action: f.action("change_server_type")})
		}
	case "POST servers/{id}/actions/poweron", "POST servers/{id}/actions/reboot":
		s := f.servers[id]
		if s == nil {
			apiError(w, 404, "not_found", "server not found")
			return
		}
		s.Status = "running"
		write(w, 201, schema.ServerActionPoweronResponse{Action: f.action(parts[3])})
	case "DELETE servers/{id}":
		s := f.servers[id]
		if s == nil {
			apiError(w, 404, "not_found", "server not found")
			return
		}
		if s.Protection.Delete {
			apiError(w, 403, "protected", "server is protected")
			return
		}
		for ipID, ip := range f.ips {
			if ip.AssigneeID != nil && *ip.AssigneeID == id {
				if ip.AutoDelete {
					delete(f.ips, ipID)
				} else {
					ip.AssigneeID = nil
				}
			}
		}
		for _, v := range f.volumes {
			if v.Server != nil && *v.Server == id {
				v.Server = nil
			}
		}
		for _, fw := range f.firewalls {
			fw.AppliedTo = slices.DeleteFunc(fw.AppliedTo, func(r schema.FirewallResource) bool { return r.Server != nil && r.Server.ID == id })
		}
		delete(f.servers, id)
		write(w, 200, schema.ServerDeleteResponse{Action: f.action("delete_server")})
	default:
		apiError(w, 404, "not_found", "the fake does not implement "+r.Method+" "+r.URL.Path)
	}
}

func (f *Fake) applyFirewall(fw *schema.Firewall, serverID int64) {
	for _, r := range fw.AppliedTo {
		if r.Server != nil && r.Server.ID == serverID {
			return
		}
	}
	fw.AppliedTo = append(fw.AppliedTo, schema.FirewallResource{Type: "server", Server: &schema.FirewallResourceServer{ID: serverID}})
	if s := f.servers[serverID]; s != nil {
		s.PublicNet.Firewalls = append(s.PublicNet.Firewalls, schema.ServerFirewall{ID: fw.ID, Status: "applied"})
	}
}

func rules(in []schema.FirewallRuleRequest) []schema.FirewallRule {
	out := []schema.FirewallRule{}
	for _, r := range in {
		out = append(out, schema.FirewallRule(r))
	}
	return out
}

func loc(name string) schema.Location {
	for _, l := range Locations {
		if l.Name == name || strconv.FormatInt(l.ID, 10) == name {
			return l
		}
	}
	return schema.Location{Name: name}
}

func deref(m *map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return *m
}

func ptr[T any](v T) *T { return &v }
