package edge

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/shiptiffin/tiffin/internal/mod/analytics/enrich"
	"go.uber.org/zap"
)

// The first-visit cookie. A project with marketing attribution may ask the
// box to remember each visitor's first visit for 90 days, so an account
// made days later is credited to the site that first sent the person. The
// edge sets the cookie on page loads (the app's code does nothing) and
// names it to the analytics collector in the access log (FirstVisitHeader);
// the collector keeps the visit's source under it, and the auth engine
// sends it along with a sign-up.
//
// It is never set for visitors in the EU, EEA or UK (an address the country
// database doesn't know counts as one of them), unless the project asks
// them (ask-eu) and they agreed (ConsentCookie=yes); never with Global
// Privacy Control; never after an opt-out (ConsentCookie=no, set at
// /_tiffin/opt-out).

const (
	// FirstVisitCookie holds a random id, 16 bytes in base64url.
	FirstVisitCookie = "tiffin_first_visit"
	// ConsentCookie is the visitor's choice: yes or no.
	ConsentCookie = "tiffin_consent"
	// FirstVisitHeader names the cookie's id to the app's request (the
	// edge drops any a client sends); the access log has it as first_visit.
	FirstVisitHeader = "X-Tiffin-First-Visit"
	firstVisitMaxAge = 90 * 24 * 60 * 60
)

// First-visit modes (manifest.FirstVisitOutsideEU, manifest.FirstVisitAskEU).
const (
	FirstVisitOutsideEU = "outside-eu"
	FirstVisitAskEU     = "ask-eu"
)

// FirstVisit is which hosts set the first-visit cookie, and how.
type FirstVisit struct {
	Hosts   map[string]string // host -> outside-eu or ask-eu
	GeoPath string            // the DB-IP country database
}

var (
	firstVisitMu     sync.Mutex
	firstVisitSource func() *FirstVisit
)

// SetFirstVisitSource registers the function the edge asks, on every Start
// and Reload of a Config without one, for the hosts that set the
// first-visit cookie. The analytics module registers it; settings changes
// reload the edge (Platform.RefreshRoutes). nil unregisters it.
func SetFirstVisitSource(fn func() *FirstVisit) {
	firstVisitMu.Lock()
	defer firstVisitMu.Unlock()
	firstVisitSource = fn
}

func currentFirstVisit() *FirstVisit {
	firstVisitMu.Lock()
	src := firstVisitSource
	firstVisitMu.Unlock()
	if src == nil {
		return nil
	}
	return src()
}

// mode is the first-visit mode of a route's hosts, or "".
func (f *FirstVisit) mode(hosts []string) string {
	if f == nil {
		return ""
	}
	for _, h := range hosts {
		if m := f.Hosts[strings.ToLower(h)]; m == FirstVisitOutsideEU || m == FirstVisitAskEU {
			return m
		}
	}
	return ""
}

// europe is where the cookie needs consent: the EU, the rest of the EEA and
// the UK.
var europe = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true, "DK": true, "EE": true, "FI": true, "FR": true,
	"DE": true, "GR": true, "HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true, "MT": true, "NL": true,
	"PL": true, "PT": true, "RO": true, "SK": true, "SI": true, "ES": true, "SE": true,
	"IS": true, "LI": true, "NO": true, // EEA
	"GB": true,                                     // UK
	"GG": true, "JE": true, "IM": true, "GI": true, // Crown Dependencies and Gibraltar follow UK rules
	"AX": true, "GF": true, "GP": true, "MQ": true, "RE": true, "YT": true, "MF": true, // EU outermost regions
}

// NeedsConsent reports whether a visitor from country (ISO code, "" for
// unknown) is asked before the cookie is set.
func NeedsConsent(country string) bool { return country == "" || europe[strings.ToUpper(country)] }

var (
	geoMu   sync.Mutex
	geoOpen = map[string]*enrich.Geo{}
)

func geoAt(path string) *enrich.Geo {
	geoMu.Lock()
	defer geoMu.Unlock()
	if g, ok := geoOpen[path]; ok {
		return g
	}
	g, _ := enrich.OpenGeo(path) // a missing database: every country is unknown, so nobody gets the cookie without consent
	geoOpen[path] = g
	return g
}

func init() { caddy.RegisterModule(FirstVisitHandler{}) }

// FirstVisitHandler is the Caddy module http.handlers.tiffin_first_visit.
type FirstVisitHandler struct {
	Mode string `json:"mode"` // outside-eu or ask-eu
	Geo  string `json:"geo"`  // the country database's path

	geo     *enrich.Geo
	country func(ip string) string // tests
}

// CaddyModule returns the Caddy module information.
func (FirstVisitHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.tiffin_first_visit", New: func() caddy.Module { return new(FirstVisitHandler) }}
}

// Provision opens the country database.
func (h *FirstVisitHandler) Provision(caddy.Context) error {
	h.geo = geoAt(h.Geo)
	return nil
}

// ValidFirstVisitID: 22 base64url characters.
func ValidFirstVisitID(s string) bool {
	if len(s) != 22 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

// ServeHTTP names the visitor's first-visit cookie to the access log, and
// sets one on a page load when the visitor may get it.
func (h *FirstVisitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	r.Header.Del(FirstVisitHeader)
	if r.Header.Get("Sec-GPC") == "1" {
		return next.ServeHTTP(w, r)
	}
	consent := ""
	if c, err := r.Cookie(ConsentCookie); err == nil {
		consent = c.Value
	}
	if consent == "no" {
		return next.ServeHTTP(w, r)
	}
	if c, err := r.Cookie(FirstVisitCookie); err == nil && ValidFirstVisitID(c.Value) {
		name(r, c.Value)
		return next.ServeHTTP(w, r)
	}
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document") {
		return next.ServeHTTP(w, r)
	}
	ip, _ := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey).(string)
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	lookup := h.geo.Country
	if h.country != nil {
		lookup = h.country
	}
	if NeedsConsent(lookup(ip)) && !(h.Mode == FirstVisitAskEU && consent == "yes") {
		return next.ServeHTTP(w, r)
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	id := base64.RawURLEncoding.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{Name: FirstVisitCookie, Value: id, Path: "/", MaxAge: firstVisitMaxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	name(r, id)
	return next.ServeHTTP(w, r)
}

// name puts the cookie's id in the access log (first_visit) and on the request.
func name(r *http.Request, id string) {
	r.Header.Set(FirstVisitHeader, id)
	if extra, ok := r.Context().Value(caddyhttp.ExtraLogFieldsCtxKey).(*caddyhttp.ExtraLogFields); ok {
		extra.Add(zap.String("first_visit", id))
	}
}
