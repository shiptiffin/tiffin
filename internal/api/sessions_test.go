package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/btahir/tiffin/internal/api"
)

var (
	macUA   = map[string]string{"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"}
	phoneUA = map[string]string{"User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"}
)

// signIn redeems a sign-in code from ip with the user agent hdr and returns
// the session secret, its token ID and the device cookie.
func (e *mailEnv) signIn(code, ip string, hdr map[string]string, device ...*http.Cookie) (string, string, *http.Cookie) {
	e.t.Helper()
	res, out := e.send("/v1/session", ip, map[string]any{"code": code}, hdr, device...)
	s := cookieNamed(res, api.SessionCookie)
	if res.StatusCode != 200 || s == nil {
		e.t.Fatalf("sign in: %d %v", res.StatusCode, out)
	}
	return s.Value, out["tokenId"].(string), cookieNamed(res, api.DeviceCookie)
}

// person invites someone and returns their ID and the invite's code.
func (e *mailEnv) person(name, role string) (string, string) {
	e.t.Helper()
	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": name, "role": role})
	return inv["person"].(map[string]any)["id"].(string), codeOf(inv["url"].(string))
}

// link makes a fresh sign-in code for person.
func (e *mailEnv) link(person string) string {
	e.t.Helper()
	_, l, _ := e.call(e.owner, "POST", "/v1/people/"+person+"/login-link", nil)
	return codeOf(l["url"].(string))
}

func sessionsOf(list []any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, s := range list {
		m := s.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}

func TestSessionsListEndAndEndOthers(t *testing.T) {
	e := newMailEnv(t)
	api.SetLocator(func(ip string) string {
		if ip == "198.51.100.77" {
			return "Canada"
		}
		return ""
	})
	t.Cleanup(func() { api.SetLocator(nil) })

	maya, invite := e.person("Maya", "member")
	mac, macID, device := e.signIn(invite, "203.0.113.5", macUA)
	phone, phoneID, _ := e.signIn(e.link(maya), "198.51.100.77", phoneUA)
	code, _, err := e.tm.EmailLoginLink(t.Context(), maya)
	if err != nil {
		t.Fatal(err)
	}
	mailed, mailedID, _ := e.signIn(code, "203.0.113.9", macUA, device)

	// Maya sees her three sessions: how, where, which one is this browser.
	c, _, list := e.call(mac, "GET", "/v1/sessions", nil)
	got := sessionsOf(list)
	if c != 200 || len(got) != 3 {
		t.Fatalf("list: %d %v", c, list)
	}
	if s := got[macID]; s["current"] != true || s["method"] != "link" || s["device"] != "Chrome on macOS" || s["ip"] != "203.0.113.5" ||
		s["state"] != "active" || s["lastSeenAt"] == nil || s["person"] != maya {
		t.Fatalf("this browser: %v", s)
	}
	if s := got[phoneID]; s["current"] != false || s["device"] != "Safari on iPhone" || s["country"] != "Canada" {
		t.Fatalf("phone: %v", s)
	}
	if s := got[mailedID]; s["method"] != "email" {
		t.Fatalf("emailed link: %v", s)
	}
	// The browsers she signed in from, this one marked.
	_, _, br := e.call(mac, "GET", "/v1/sessions/browsers", nil)
	if len(br) != 2 {
		t.Fatalf("browsers: %v", br)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/sessions/browsers", nil)
	req.Header.Set("Authorization", "Bearer "+mac)
	req.AddCookie(device)
	res, _ := http.DefaultClient.Do(req)
	var browsers []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&browsers)
	res.Body.Close()
	if i := slices.IndexFunc(browsers, func(b map[string]any) bool { return b["current"] == true }); i < 0 || browsers[i]["device"] != "Chrome on macOS" {
		t.Fatalf("current browser: %v", browsers)
	}

	// Ending one: refused on its very next request.
	if c, s, _ := e.call(mac, "DELETE", "/v1/sessions/"+phoneID, nil); c != 200 || s["state"] != "ended" {
		t.Fatalf("end one: %d %v", c, s)
	}
	if c, _, _ := e.call(phone, "GET", "/v1/whoami", nil); c != 401 {
		t.Fatalf("ended session still works: %d", c)
	}
	if c, _, _ := e.call(mac, "DELETE", "/v1/sessions/"+phoneID, nil); c != 404 {
		t.Fatalf("ending it twice: %d", c)
	}

	// Everywhere else: the emailed one goes, this browser stays.
	if c, out, _ := e.call(mac, "POST", "/v1/sessions/end-others", nil); c != 200 || out["ended"] != float64(1) {
		t.Fatalf("end others: %d %v", c, out)
	}
	if c, _, _ := e.call(mailed, "GET", "/v1/whoami", nil); c != 401 {
		t.Fatalf("other session still works: %d", c)
	}
	if c, _, _ := e.call(mac, "GET", "/v1/whoami", nil); c != 200 {
		t.Fatalf("this session ended too: %d", c)
	}
	_, _, list = e.call(mac, "GET", "/v1/sessions", nil)
	if len(list) != 1 {
		t.Fatalf("open after end-others: %v", list)
	}
	// History keeps the ended ones for 30 days.
	_, _, list = e.call(mac, "GET", "/v1/sessions?history=true", nil)
	got = sessionsOf(list)
	if len(got) != 3 || got[phoneID]["state"] != "ended" || got[phoneID]["endedAt"] == nil || got[macID]["state"] != "active" {
		t.Fatalf("history: %v", list)
	}

	// The audit log says who ended what.
	_, _, audit := e.call(e.owner, "GET", "/v1/audit?limit=100", nil)
	var ended, others, link bool
	for _, a := range audit {
		ev := a.(map[string]any)
		switch ev["action"] {
		case "session.end":
			ended = ev["target"] == phoneID && ev["actor"] == macID
		case "session.end_others":
			others = ev["target"] == maya && ev["actor"] == macID
		case "session.link":
			link = link || ev["target"] == maya
		}
	}
	if !ended || !others || !link {
		t.Fatalf("audit: end %v, end_others %v, link %v: %v", ended, others, link, audit)
	}
}

func TestSessionsAcrossPeople(t *testing.T) {
	e := newMailEnv(t)
	maya, invite := e.person("Maya", "member")
	mayaMac, mayaID, _ := e.signIn(invite, "203.0.113.5", macUA)
	ada, invite := e.person("Ada", "admin")
	adaMac, adaID, _ := e.signIn(invite, "203.0.113.6", macUA)

	// A member can't see or end anyone else's sessions.
	for _, r := range [][2]string{
		{"GET", "/v1/sessions?person=" + ada},
		{"GET", "/v1/sessions/browsers?person=" + ada},
		{"DELETE", "/v1/sessions/" + adaID},
		{"POST", "/v1/sessions/end-others?person=" + ada},
	} {
		if c, _, _ := e.call(mayaMac, r[0], r[1], nil); c != 403 {
			t.Fatalf("member %s %s: %d", r[0], r[1], c)
		}
	}
	if c, _, _ := e.call(adaMac, "GET", "/v1/whoami", nil); c != 200 {
		t.Fatalf("Ada was signed out by a member: %d", c)
	}

	// An admin can, and the session is refused at once.
	if c, _, list := e.call(adaMac, "GET", "/v1/sessions?person="+maya, nil); c != 200 || len(list) != 1 || list[0].(map[string]any)["current"] != false {
		t.Fatalf("admin lists: %d %v", c, list)
	}
	if c, _, _ := e.call(adaMac, "DELETE", "/v1/sessions/"+mayaID, nil); c != 200 {
		t.Fatalf("admin ends: %d", c)
	}
	if c, _, _ := e.call(mayaMac, "GET", "/v1/whoami", nil); c != 401 {
		t.Fatalf("ended by an admin, still works: %d", c)
	}
	mayaPhone, _, _ := e.signIn(e.link(maya), "203.0.113.7", phoneUA)
	if c, out, _ := e.call(adaMac, "POST", "/v1/sessions/end-others?person="+maya, nil); c != 200 || out["ended"] != float64(1) {
		t.Fatalf("admin ends all of Maya's: %d %v", c, out)
	}
	if c, _, _ := e.call(mayaPhone, "GET", "/v1/whoami", nil); c != 401 {
		t.Fatalf("still signed in: %d", c)
	}

	// Only the owner ends the owner's sessions; an admin may still see them.
	_, l, _ := e.call(e.owner, "POST", "/v1/login-links", nil)
	_, ownerID, _ := e.signIn(l["code"].(string), "203.0.113.8", macUA)
	if c, _, list := e.call(adaMac, "GET", "/v1/sessions?person=usr_owner", nil); c != 200 || len(list) != 1 {
		t.Fatalf("admin lists the owner's: %d %v", c, list)
	}
	if c, _, _ := e.call(adaMac, "DELETE", "/v1/sessions/"+ownerID, nil); c != 403 {
		t.Fatalf("admin ends the owner's: %d", c)
	}
	if c, _, _ := e.call(adaMac, "POST", "/v1/sessions/end-others?person=usr_owner", nil); c != 403 {
		t.Fatalf("admin ends all the owner's: %d", c)
	}
	if c, _, _ := e.call(e.owner, "DELETE", "/v1/sessions/"+ownerID, nil); c != 200 {
		t.Fatalf("owner ends own: %d", c)
	}

	// API keys are their own credentials: ending the session that made one
	// leaves it working. Nor does it end someone else's session signed in
	// with a link it sent. (A read-only key for a day needs no recent strong
	// sign-in; these sessions came from links.)
	adaPhone, _, _ := e.signIn(e.link(ada), "203.0.113.9", phoneUA)
	_, l, _ = e.call(adaMac, "POST", "/v1/people/"+maya+"/login-link", nil)
	mayaAgain, _, _ := e.signIn(codeOf(l["url"].(string)), "203.0.113.5", macUA)
	_, k, _ := e.call(adaMac, "POST", "/v1/tokens", map[string]any{"name": "ci", "projects": "all", "access": "read", "expiresInDays": 1})
	key := k["secret"].(string)
	_, k, _ = e.call(adaPhone, "POST", "/v1/tokens", map[string]any{"name": "phone", "projects": "all", "access": "read", "expiresInDays": 1})
	phoneKey := k["secret"].(string)
	if c, _, _ := e.call(adaPhone, "POST", "/v1/sessions/end-others", nil); c != 200 {
		t.Fatalf("Ada ends others: %d", c)
	}
	if c, _, _ := e.call(adaMac, "GET", "/v1/whoami", nil); c != 401 {
		t.Fatalf("ended session still works: %d", c)
	}
	if c, _, _ := e.call(key, "GET", "/v1/whoami", nil); c != 200 {
		t.Fatalf("key of an ended session stopped: %d", c)
	}
	if c, _, _ := e.call(adaPhone, "DELETE", "/v1/session", nil); c != 200 && c != 204 {
		t.Fatalf("Ada signs out: %d", c)
	}
	if c, _, _ := e.call(phoneKey, "GET", "/v1/whoami", nil); c != 200 {
		t.Fatalf("key of a signed-out session stopped: %d", c)
	}
	if c, _, _ := e.call(mayaAgain, "GET", "/v1/whoami", nil); c != 200 {
		t.Fatalf("Maya's session from Ada's link ended with Ada's: %d", c)
	}

	// An API key acts for nobody: it must name a person. Unknown sessions are 404.
	if c, _, _ := e.call(e.owner, "GET", "/v1/sessions?person="+maya, nil); c != 200 {
		t.Fatalf("owner token lists Maya's: %d", c)
	}
	if c, _, _ := e.call(phoneKey, "GET", "/v1/sessions", nil); c != 422 {
		t.Fatalf("key lists its own: %d", c)
	}
	if c, _, _ := e.call(phoneKey, "GET", "/v1/sessions?person="+maya, nil); c != 403 {
		t.Fatalf("read key lists Maya's: %d", c)
	}
	if c, _, _ := e.call(e.owner, "DELETE", "/v1/sessions/tok_NOPE", nil); c != 404 {
		t.Fatalf("unknown session: %d", c)
	}
}
