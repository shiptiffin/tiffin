package analytics

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/mod/observe/edgelog"
)

//go:embed script.js
var trackerJS []byte

// CollectorAddr serves the tracker script, browser beacons and server-side
// track() calls. The edge publishes it as t.<domain>.
const (
	CollectorPort = "7091"
	CollectorAddr = "127.0.0.1:" + CollectorPort
)

const maxBeacon = 8 << 10

// EdgePageview reports whether an access log entry is a page a person
// loaded: a GET with a 2xx or 304 response for a top-level document that
// is not a prefetch or prerender. Bots are filtered later by user agent.
func EdgePageview(e *edgelog.Entry) bool {
	if e.Method != http.MethodGet || !(e.Status >= 200 && e.Status < 300 || e.Status == http.StatusNotModified) {
		return false
	}
	if !strings.EqualFold(e.Header("Sec-Fetch-Dest"), "document") {
		return false
	}
	if m := e.Header("Sec-Fetch-Mode"); m != "" && !strings.EqualFold(m, "navigate") {
		return false
	}
	for _, h := range []string{"Sec-Purpose", "Purpose", "X-Purpose", "X-Moz"} {
		v := strings.ToLower(e.Header(h))
		if strings.Contains(v, "prefetch") || strings.Contains(v, "prerender") || strings.Contains(v, "preview") {
			return false
		}
	}
	return true
}

// handleEdge turns one access log line into a pageview when it is one.
func (m *Module) handleEdge(ctx context.Context, line []byte) {
	e, ok := edgelog.Parse(line)
	if !ok || !EdgePageview(&e) {
		return
	}
	path, _, _ := strings.Cut(e.URI, "?")
	site, ok := m.sites.Lookup(ctx, e.Host, path)
	if !ok || !site.Analytics {
		return
	}
	m.pipe.Add(ctx, Hit{At: e.Time, Project: site.Project, App: site.App, Kind: "pageview", URL: e.URI, Host: e.Host,
		Referrer: e.Header("Referer"), IP: e.ClientIP, UA: e.Header("User-Agent"), Src: "edge", GPC: e.Header("Sec-GPC") == "1"})
}

func (m *Module) collectorHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/script.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write(trackerJS)
	})
	mux.HandleFunc("/e", m.beacon)
	mux.HandleFunc("/track", m.serverTrack)
	mux.HandleFunc(VitalsPath, m.vitalsBeacon)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "Tiffin analytics collector.\n\nGET /script.js   the tracker; add <script defer src=\"/script.js on this host\"></script> to your pages\nPOST /e          browser beacons\nPOST /track      server-side track() (Authorization: Bearer $TIFFIN_ANALYTICS_KEY)\n"+
			"POST "+VitalsPath+" on an app's own host: Web Vitals {\"path\": \"/\", \"metrics\": {\"LCP\": 1840}}\n")
	})
	return mux
}

// clientIP is the visitor's address: the edge appends it to X-Forwarded-For.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type beaconBody struct {
	N string         `json:"n"`
	U string         `json:"u"`
	R string         `json:"r"`
	P map[string]any `json:"p"`
}

// beacon accepts events from the tracker script.
func (m *Module) beacon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "content-type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var b beaconBody
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBeacon)).Decode(&b); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON {n, u, r, p}"})
		return
	}
	u, err := url.Parse(b.U)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		reply(w, http.StatusBadRequest, map[string]string{"error": "u must be the page URL"})
		return
	}
	name := strings.TrimSpace(b.N)
	if name == "" || len(name) > 120 {
		reply(w, http.StatusBadRequest, map[string]string{"error": "n must be pageview or an event name (1-120 chars)"})
		return
	}
	site, ok := m.sites.Lookup(r.Context(), u.Host, u.EscapedPath())
	if !ok || !site.Analytics {
		reply(w, http.StatusNotFound, map[string]string{"error": "no app with analytics serves " + u.Host})
		return
	}
	h := Hit{Project: site.Project, App: site.App, Kind: "event", Name: name, URL: b.U, Referrer: b.R,
		IP: clientIP(r), UA: r.Header.Get("User-Agent"), Props: b.P, Src: "script", GPC: r.Header.Get("Sec-GPC") == "1"}
	if name == "pageview" {
		h.Kind = "pageview"
	}
	if ok, why := m.pipe.Add(r.Context(), h); !ok {
		reply(w, http.StatusAccepted, map[string]string{"dropped": why})
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

type trackBody struct {
	Name     string         `json:"name"`
	Props    map[string]any `json:"props"`
	URL      string         `json:"url"`
	Referrer string         `json:"referrer"`
	IP       string         `json:"ip"`
	UA       string         `json:"ua"`
	GPC      bool           `json:"gpc"`
	At       *time.Time     `json:"at"`
}

// serverTrack accepts events from app servers (tiffin-sdk/analytics track()).
func (m *Module) serverTrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	project, app, ok := m.store.LookupKey(r.Context(), key)
	if !ok {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "send Authorization: Bearer $TIFFIN_ANALYTICS_KEY"})
		return
	}
	var b trackBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&b); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON {name, props, url, referrer, ip, ua}"})
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 120 {
		reply(w, http.StatusBadRequest, map[string]string{"error": "name is required (1-120 chars)"})
		return
	}
	if !m.sites.AnalyticsEnabled(r.Context(), project) {
		reply(w, http.StatusConflict, map[string]string{"error": "analytics is not enabled for project " + project})
		return
	}
	h := Hit{Project: project, App: app, Kind: "event", Name: b.Name, URL: b.URL, Referrer: b.Referrer, IP: b.IP, UA: b.UA, Props: b.Props, Src: "server", GPC: b.GPC}
	if b.URL == "" {
		h.URL = "/"
	}
	if b.At != nil && time.Since(*b.At) < 24*time.Hour && time.Until(*b.At) < time.Minute {
		h.At = *b.At
	}
	if b.Name == "pageview" {
		h.Kind = "pageview"
	}
	if ok, why := m.pipe.Add(r.Context(), h); !ok {
		reply(w, http.StatusAccepted, map[string]string{"dropped": why})
		return
	}
	reply(w, http.StatusAccepted, map[string]bool{"ok": true})
}
