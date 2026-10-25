package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// authApp is the collector key name the auth engine sends for a project:
// not an app (app names are slugs, so none can be "_auth").
const authApp = "_auth"

func init() {
	collectorRoutes = append(collectorRoutes, func(m *Module, mux *http.ServeMux) {
		mux.HandleFunc("/signup", m.signupReport)
		mux.HandleFunc("/signup/delete", m.signupDeleted)
		mux.HandleFunc("/attribution", m.attributionLookup)
	})
}

// authSink gives the auth engine the collector's sign-up endpoint and the
// project's key.
func (m *Module) authSink(ctx context.Context, project string) (string, string, bool) {
	if m.store == nil {
		return "", "", false
	}
	key, err := m.store.Key(ctx, project, authApp)
	if err != nil {
		return "", "", false
	}
	return "http://" + CollectorAddr + "/signup", key, true
}

// engineProject is the project an auth-engine report is for (its key), or "".
func (m *Module) engineProject(w http.ResponseWriter, r *http.Request) string {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return ""
	}
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	project, app, ok := m.store.LookupKey(r.Context(), key)
	if !ok || app != authApp {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "the auth engine's key for the project"})
		return ""
	}
	return project
}

type signupBody struct {
	UserID     string     `json:"userId"`
	Host       string     `json:"host"`
	IP         string     `json:"ip"`
	UA         string     `json:"ua"`
	GPC        bool       `json:"gpc"`
	FirstVisit string     `json:"firstVisit"` // the tiffin_first_visit cookie, if the browser sent one
	At         *time.Time `json:"at"`
}

// signupReport is the auth engine telling the collector an account was
// made in a request: a Signup event joins the visit (counted in Basic
// analytics too, as an event with no account), and with attribution's
// signups the account keeps where that visit, and its first visit, came from.
func (m *Module) signupReport(w http.ResponseWriter, r *http.Request) {
	project := m.engineProject(w, r)
	if project == "" {
		return
	}
	var b signupBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&b); err != nil || b.UserID == "" || len(b.UserID) > 200 {
		reply(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON {userId, host, ip, ua}"})
		return
	}
	ctx := r.Context()
	if !m.sites.AnalyticsEnabled(ctx, project) || b.GPC {
		// Global Privacy Control objects to both the count and the link.
		reply(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	at := time.Now().UTC()
	if b.At != nil && time.Since(*b.At) < time.Hour && time.Until(*b.At) < time.Minute {
		at = b.At.UTC()
	}
	// Look the visit up before the event joins it: without a visit going on,
	// the event would start one of its own.
	visit, matched := m.pipe.Visit(ctx, project, b.IP, b.UA, at)
	app := ""
	if site, ok := m.sites.Lookup(ctx, b.Host, "/"); ok && site.Project == project {
		app = site.App
	}
	m.pipe.Add(ctx, Hit{At: at, Project: project, App: app, Kind: "event", Name: "Signup", URL: "https://" + b.Host + "/", IP: b.IP, UA: b.UA, Src: "server"})

	set := m.Settings(ctx, project)
	if !set.Attribution || !set.Signups {
		reply(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	su := Signup{UserID: b.UserID, At: at, App: app, Matched: "none"}
	if matched {
		su.Matched, su.Visit = "visit", visit
	}
	if set.FirstVisitCookie != manifest.FirstVisitOff && validFirstVisitID(b.FirstVisit) {
		if fv, err := m.attr.firstVisit(ctx, project, b.FirstVisit); err == nil && fv != nil {
			su.First = fv
		}
	}
	if err := m.attr.putSignup(ctx, project, su); err != nil {
		m.p.Log.Error("analytics sign-up", "project", project, "err", err)
		reply(w, http.StatusInternalServerError, map[string]string{"error": "couldn't save the sign-up"})
		return
	}
	reply(w, http.StatusAccepted, map[string]any{"ok": true, "matched": su.Matched})
}

// signupDeleted forgets a deleted account's attribution.
func (m *Module) signupDeleted(w http.ResponseWriter, r *http.Request) {
	project := m.engineProject(w, r)
	if project == "" {
		return
	}
	var b struct {
		UserID string `json:"userId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&b); err != nil || b.UserID == "" {
		reply(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON {userId}"})
		return
	}
	if err := m.attr.deleteSignup(r.Context(), project, b.UserID); err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "couldn't delete"})
		return
	}
	for _, f := range accountDeleted {
		f(r.Context(), m, project, b.UserID)
	}
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

// accountDeleted are called when an account is deleted, after its sign-up
// row goes (revenue unlinks its payments). Add from init.
var accountDeleted []func(ctx context.Context, m *Module, project, userID string)

// attributionLookup answers an app's attribution(userId) (SDK): where the
// account came from, or null.
func (m *Module) attributionLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	project, app, ok := m.store.LookupKey(r.Context(), key)
	if !ok || app == authApp {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "send Authorization: Bearer $TIFFIN_ANALYTICS_KEY"})
		return
	}
	user := r.URL.Query().Get("user")
	if user == "" || len(user) > 200 {
		reply(w, http.StatusBadRequest, map[string]string{"error": "?user=<account id>"})
		return
	}
	if !m.Settings(r.Context(), project).Attribution {
		reply(w, http.StatusOK, map[string]any{"attribution": nil, "off": true})
		return
	}
	su, err := m.attr.signup(r.Context(), project, user)
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "couldn't read"})
		return
	}
	reply(w, http.StatusOK, map[string]any{"attribution": su})
}

// validFirstVisitID: the cookie holds 22 base64url characters (16 random bytes).
func validFirstVisitID(s string) bool {
	if len(s) != 22 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
