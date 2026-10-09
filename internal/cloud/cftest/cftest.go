// Package cftest is an in-memory fake of the parts of Cloudflare's API the
// control plane uses: what libdns/cloudflare uses (zones, and listing,
// creating, patching and deleting DNS records), and R2's temporary access
// credentials. Hand Client() to cloudflare.Provider.HTTPClient (or the
// worker's R2): requests never leave the process.
package cftest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
)

// Token is the only API token the fake accepts.
const Token = "fake-cloudflare-token"

// Record is one DNS record in the fake.
type Record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	ZoneID  string `json:"zone_id"`
}

// Fake is a Cloudflare account with some zones.
type Fake struct {
	mu      sync.Mutex
	zones   map[string]string // name → id
	records map[string]*Record
	next    int
	// Requests is every request, "METHOD /path?query", in order.
	Requests []string
	// Fault, when set, fails the requests it returns true for (HTTP 500,
	// nothing changed): for "Cloudflare broke half way" tests.
	Fault func(method, path string) bool
	// Minted is every R2 temporary credentials request that succeeded.
	Minted []Mint
	// OnMint, when set, is told about each one with the credentials made
	// (a fake bucket can accept them).
	OnMint func(m Mint, accessKeyID, secretAccessKey, sessionToken string)
}

// Mint is a request for R2 temporary access credentials
// (POST /accounts/{account_id}/r2/temp-access-credentials).
type Mint struct {
	Account           string   `json:"-"`
	Bucket            string   `json:"bucket"`
	ParentAccessKeyID string   `json:"parentAccessKeyId"`
	Permission        string   `json:"permission"`
	TTLSeconds        int      `json:"ttlSeconds"`
	Prefixes          []string `json:"prefixes"`
	Objects           []string `json:"objects"`
}

// SetFault sets Fault under the fake's lock.
func (f *Fake) SetFault(fn func(method, path string) bool) { f.mu.Lock(); f.Fault = fn; f.mu.Unlock() }

// New returns a fake holding the given zones.
func New(zones ...string) *Fake {
	f := &Fake{zones: map[string]string{}, records: map[string]*Record{}}
	for i, z := range zones {
		f.zones[z] = fmt.Sprintf("zone%d", i+1)
	}
	return f
}

// Client is an HTTP client served by the fake.
func (f *Fake) Client() *Client { return &Client{f} }

// Client implements cloudflare.HTTPClient.
type Client struct{ f *Fake }

// Do serves req in process.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	c.f.ServeHTTP(w, req)
	return w.Result(), nil
}

// Records returns the records of a name ("shop.shiptiffin.app"), "TYPE value" each, sorted.
func (f *Fake) Records(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.records {
		if r.Name == name {
			out = append(out, r.Type+" "+r.Content)
		}
	}
	sort.Strings(out)
	return out
}

// Mints returns the R2 temporary credentials requests so far.
func (f *Fake) Mints() []Mint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Mint(nil), f.Minted...)
}

// Count is how many records the fake holds.
func (f *Fake) Count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.records) }

// Add puts a record in place (for "someone else's record" tests).
func (f *Fake) Add(zone, typ, name, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("rec%d", f.next)
	f.records[id] = &Record{ID: id, Type: typ, Name: name, Content: content, TTL: 1, ZoneID: f.zones[zone]}
}

type envelope struct {
	Result     any   `json:"result"`
	Success    bool  `json:"success"`
	Errors     []any `json:"errors"`
	ResultInfo any   `json:"result_info,omitempty"`
}

func reply(w http.ResponseWriter, code int, result any, info any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	e := envelope{Result: result, Success: code < 400, Errors: []any{}, ResultInfo: info}
	if code >= 400 {
		e.Errors = []any{map[string]any{"code": code, "message": fmt.Sprint(result)}}
		e.Result = nil
	}
	_ = json.NewEncoder(w).Encode(e)
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path+qs(r))
	if f.Fault != nil && f.Fault(r.Method, r.URL.Path) {
		reply(w, 500, "injected failure", nil)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+Token {
		reply(w, 403, "Invalid API token", nil)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	q := r.URL.Query()
	switch {
	case len(parts) == 4 && parts[0] == "accounts" && parts[2] == "r2" && parts[3] == "temp-access-credentials" && r.Method == http.MethodPost:
		var m Mint
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.Bucket == "" || m.ParentAccessKeyID == "" || m.TTLSeconds <= 0 || m.TTLSeconds > 604800 {
			reply(w, 400, "bucket, parentAccessKeyId, permission and ttlSeconds (at most 604800) are required", nil)
			return
		}
		switch m.Permission {
		case "admin-read-write", "admin-read-only", "object-read-write", "object-read-only":
		default:
			reply(w, 400, "unknown permission "+m.Permission, nil)
			return
		}
		m.Account = parts[1]
		f.next++
		access, secret, token := fmt.Sprintf("tmpkey%d", f.next), fmt.Sprintf("tmpsecret%d", f.next), fmt.Sprintf("tmptoken%d", f.next)
		f.Minted = append(f.Minted, m)
		if f.OnMint != nil {
			f.OnMint(m, access, secret, token)
		}
		reply(w, 200, map[string]string{"accessKeyId": access, "secretAccessKey": secret, "sessionToken": token}, nil)
	case len(parts) == 1 && parts[0] == "zones" && r.Method == http.MethodGet:
		var out []map[string]any
		for name, id := range f.zones {
			if n := strings.TrimSuffix(q.Get("name"), "."); n != "" && n != name {
				continue
			}
			out = append(out, map[string]any{"id": id, "name": name})
		}
		reply(w, 200, out, map[string]int{"page": 1, "per_page": 50, "count": len(out), "total_count": len(out)})
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		zoneID := parts[1]
		if !f.hasZone(zoneID) {
			reply(w, 404, "zone not found", nil)
			return
		}
		f.records2(w, r, zoneID, parts[3:])
	default:
		reply(w, 404, "no route "+r.Method+" "+path, nil)
	}
}

func qs(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

// absolute makes a record name absolute as Cloudflare does: "@" is the
// zone, a name outside the zone gets the zone appended.
func (f *Fake) absolute(name, zoneID string) string {
	name = strings.TrimSuffix(name, ".")
	for z, id := range f.zones {
		if id != zoneID {
			continue
		}
		if name == "@" || name == "" {
			return z
		}
		if name != z && !strings.HasSuffix(name, "."+z) {
			return name + "." + z
		}
	}
	return name
}

func (f *Fake) hasZone(id string) bool {
	for _, z := range f.zones {
		if z == id {
			return true
		}
	}
	return false
}

func (f *Fake) records2(w http.ResponseWriter, r *http.Request, zoneID string, rest []string) {
	q := r.URL.Query()
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		out := []Record{}
		for _, rec := range f.records {
			if rec.ZoneID != zoneID ||
				q.Get("type") != "" && rec.Type != q.Get("type") ||
				q.Get("name") != "" && rec.Name != f.absolute(q.Get("name"), zoneID) ||
				q.Get("content.exact") != "" && rec.Content != q.Get("content.exact") {
				continue
			}
			out = append(out, *rec)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		reply(w, 200, out, map[string]int{"page": 1, "per_page": 100, "count": len(out), "total_count": len(out)})
	case len(rest) == 0 && r.Method == http.MethodPost:
		var rec Record
		if err := decode(r.Body, &rec); err != nil || rec.Type == "" || rec.Name == "" || rec.Content == "" {
			reply(w, 400, "bad record", nil)
			return
		}
		f.next++
		rec.ID, rec.ZoneID, rec.Name = fmt.Sprintf("rec%d", f.next), zoneID, f.absolute(rec.Name, zoneID)
		f.records[rec.ID] = &rec
		reply(w, 200, rec, nil)
	case len(rest) == 1 && r.Method == http.MethodPatch:
		rec, ok := f.records[rest[0]]
		if !ok || rec.ZoneID != zoneID {
			reply(w, 404, "record not found", nil)
			return
		}
		var upd Record
		if err := decode(r.Body, &upd); err != nil {
			reply(w, 400, "bad record", nil)
			return
		}
		if upd.Content != "" {
			rec.Content = upd.Content
		}
		if upd.TTL != 0 {
			rec.TTL = upd.TTL
		}
		reply(w, 200, *rec, nil)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		rec, ok := f.records[rest[0]]
		if !ok || rec.ZoneID != zoneID {
			reply(w, 404, "record not found", nil)
			return
		}
		delete(f.records, rest[0])
		reply(w, 200, map[string]string{"id": rec.ID}, nil)
	default:
		reply(w, 404, "no route", nil)
	}
}

func decode(r io.Reader, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<16))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
