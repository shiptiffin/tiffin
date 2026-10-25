package analytics

import (
	"context"
	_ "embed"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/manifest"
)

// The first-visit cookie's side in analytics: which hosts the edge sets it
// on, keeping each first visit's source, and the visitor's choices
// (/_tiffin/opt-out, and /_tiffin/consent for projects that ask visitors in
// the EU, EEA and UK). See internal/edge/firstvisit.go for the cookie.

const (
	OptOutPath  = "/_tiffin/opt-out"
	ConsentPath = "/_tiffin/consent"
	// ConsentScriptPath is the small prompt ask-eu projects add to their pages.
	ConsentScriptPath = "/_tiffin/consent.js"
	choiceMaxAge      = 365 * 24 * 60 * 60
)

//go:embed consent.js
var consentJS []byte

func init() {
	appPaths = append(appPaths, OptOutPath, ConsentPath, ConsentScriptPath)
	collectorRoutes = append(collectorRoutes, func(m *Module, mux *http.ServeMux) {
		mux.HandleFunc(OptOutPath, m.optOut)
		mux.HandleFunc(ConsentPath, m.consent)
		mux.HandleFunc(ConsentScriptPath, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(consentJS)
		})
	})
}

// firstVisitHosts is what the edge asks for: every host of the projects
// whose attribution sets the first-visit cookie.
func (m *Module) firstVisitHosts() *edge.FirstVisit {
	if m.p == nil || m.p.DB == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	projects, err := m.p.DB.ListProjects(ctx)
	if err != nil {
		return nil
	}
	root := m.p.DataRoot
	if root == "" {
		root = "/var/lib/tiffin"
	}
	out := &edge.FirstVisit{Hosts: map[string]string{}, GeoPath: geoPath(root)}
	for _, pr := range projects {
		s := m.Settings(ctx, pr)
		if !s.Attribution || s.FirstVisitCookie == manifest.FirstVisitOff || s.FirstVisitCookie == "" {
			continue
		}
		for _, h := range m.sites.Hosts(ctx, pr, "") {
			out.Hosts[strings.ToLower(h)] = s.FirstVisitCookie
		}
	}
	if len(out.Hosts) == 0 {
		return nil
	}
	return out
}

// noteFirstVisit keeps where a first visit came from under its cookie's id.
func (m *Module) noteFirstVisit(project, id string, t Touch) {
	if !edge.ValidFirstVisitID(id) {
		return
	}
	if err := m.attr.putFirstVisit(context.Background(), project, id, t); err != nil && m.p != nil {
		m.p.Log.Error("analytics first visit", "project", project, "err", err)
	}
}

// choose records the visitor's choice for the host they're on: yes lets an
// ask-eu project set the first-visit cookie, no stops it and forgets it.
func (m *Module) choose(w http.ResponseWriter, r *http.Request, choice string) {
	http.SetCookie(w, &http.Cookie{Name: edge.ConsentCookie, Value: choice, Path: "/", MaxAge: choiceMaxAge, Secure: true, SameSite: http.SameSiteLaxMode})
	if choice != "no" {
		return
	}
	if c, err := r.Cookie(edge.FirstVisitCookie); err == nil && edge.ValidFirstVisitID(c.Value) {
		if site, ok := m.sites.Lookup(r.Context(), r.Host, "/"); ok {
			m.attr.mu.Lock()
			_, _ = m.attr.db.ExecContext(r.Context(), `DELETE FROM first_visits WHERE project = ? AND id = ?`, site.Project, c.Value)
			m.attr.mu.Unlock()
		}
	}
	http.SetCookie(w, &http.Cookie{Name: edge.FirstVisitCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// optOut is the link in the privacy text: it stops the first-visit cookie
// on this site and forgets what it remembered. GET shows the page, POST
// (its button, or a link that wants no page) opts out.
func (m *Module) optOut(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	done := false
	switch r.Method {
	case http.MethodPost:
		m.choose(w, r, "no")
		done = true
	case http.MethodGet, http.MethodHead:
		if c, err := r.Cookie(edge.ConsentCookie); err == nil && c.Value == "no" {
			done = true
		}
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST"})
		return
	}
	host := html.EscapeString(strings.Split(r.Host, ":")[0])
	body := `<p>This site can remember, for 90 days, which site first sent you here, with a cookie that holds a random code and nothing else.</p>
<form method="post"><button type="submit">Don't remember me</button></form>`
	if done {
		body = `<p><strong>Done.</strong> ` + host + ` won't remember your first visit, and what it had remembered is deleted. This choice is kept in a cookie for a year; clearing your cookies undoes it.</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Visit tracking on `+host+`</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:4rem auto;padding:0 1rem;color:#222;background:#fff}button{font:inherit;padding:.5rem 1rem;border:1px solid #222;border-radius:6px;background:#fff;cursor:pointer}@media (prefers-color-scheme:dark){body{color:#eee;background:#111}button{color:#eee;background:#111;border-color:#eee}}</style>
<h1>Visit tracking on `+host+`</h1>`+body+`</html>`)
}

// consent answers the prompt: GET says whether to ask this visitor, POST
// {choice: "yes"|"no"} records the answer.
func (m *Module) consent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		choice := ""
		if c, err := r.Cookie(edge.ConsentCookie); err == nil {
			choice = c.Value
		}
		ask := false
		if site, ok := m.sites.Lookup(r.Context(), r.Host, "/"); ok && r.Header.Get("Sec-GPC") != "1" {
			s := m.Settings(r.Context(), site.Project)
			ask = s.Attribution && s.FirstVisitCookie == manifest.FirstVisitAskEU && choice == "" && edge.NeedsConsent(m.geo.Country(clientIP(r)))
		}
		reply(w, http.StatusOK, map[string]any{"ask": ask, "choice": choice})
	case http.MethodPost:
		var b struct {
			Choice string `json:"choice"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&b); err != nil || (b.Choice != "yes" && b.Choice != "no") {
			reply(w, http.StatusBadRequest, map[string]string{"error": `body must be {"choice": "yes"} or {"choice": "no"}`})
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			reply(w, http.StatusForbidden, map[string]string{"error": "only this site's own pages record a choice"})
			return
		}
		m.choose(w, r, b.Choice)
		reply(w, http.StatusOK, map[string]string{"choice": b.Choice})
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST"})
	}
}
