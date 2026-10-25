package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

func signupFixture(t *testing.T, analytics string) (*Module, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	db, err := state.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := &platform.Platform{DB: db, Tokens: tokens.NewManager(db), Engine: change.NewEngine(db), Home: root, DataRoot: root, Domain: "box.test",
		PublicURL: "https://dashboard.box.test:8443", Log: slog.Default()}
	applyManifest(t, db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":`+analytics+`}}`)
	st, err := OpenSQLite(filepath.Join(root, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &Module{}
	m.setup(p, st, nil)
	srv := httptest.NewServer(m.collectorHandler())
	t.Cleanup(srv.Close)
	return m, srv
}

func postJSON(t *testing.T, url, key string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// A visitor from Hacker News signs up: the account keeps that source, a
// Signup event joins the visit, the app can read it, and deleting the
// account forgets it.
func TestSignupJoinsVisit(t *testing.T) {
	ctx := context.Background()
	m, srv := signupFixture(t, `{"attribution":{}}`)
	m.handleEdge(ctx, access("shop.box.test", "/pricing?utm_campaign=launch", human(1), 200, with(doc, "Referer", "https://news.ycombinator.com/item?id=9")))
	url, key, ok := m.authSink(ctx, "shop")
	if !ok || url != "http://"+CollectorAddr+"/signup" {
		t.Fatalf("sink %q %v", url, ok)
	}
	code, out := postJSON(t, srv.URL+"/signup", key, signupBody{UserID: "u1", Host: "shop.box.test", IP: "8.8.8.8", UA: human(1)})
	if code != http.StatusAccepted || out["matched"] != "visit" {
		t.Fatalf("signup %d %v", code, out)
	}
	su, _ := m.attr.signup(ctx, "shop", "u1")
	if su == nil || su.Visit.Source != "Hacker News" || su.Visit.UTMCampaign != "launch" || su.Visit.Landing != "/pricing" || su.App != "web" {
		t.Fatalf("saved %+v", su)
	}
	_ = m.pipe.Flush(ctx)
	evs, _ := m.store.Recent(ctx, timeZero)
	var signup, page *Event
	for i := range evs {
		switch evs[i].Name {
		case "Signup":
			signup = &evs[i]
		case "pageview":
			page = &evs[i]
		}
	}
	if signup == nil || page == nil || signup.Session != page.Session {
		t.Fatalf("the Signup event didn't join the visit: %+v %+v", signup, page)
	}

	// The app reads it with its own key; the engine's key can't, and an app key can't report sign-ups.
	appKey, _ := m.store.Key(ctx, "shop", "web")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/attribution?user=u1", nil)
	req.Header.Set("Authorization", "Bearer "+appKey)
	res, _ := http.DefaultClient.Do(req)
	var got struct{ Attribution *Signup }
	_ = json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if got.Attribution == nil || got.Attribution.Visit.Source != "Hacker News" {
		t.Fatalf("lookup %+v", got)
	}
	if code, _ := postJSON(t, srv.URL+"/signup", appKey, signupBody{UserID: "u2"}); code != http.StatusUnauthorized {
		t.Fatalf("an app key reported a sign-up: %d", code)
	}

	if code, _ := postJSON(t, srv.URL+"/signup/delete", key, map[string]string{"userId": "u1"}); code != http.StatusOK {
		t.Fatalf("delete %d", code)
	}
	if su, _ := m.attr.signup(ctx, "shop", "u1"); su != nil {
		t.Fatal("deleted account kept its source")
	}
}

// Without attribution (or with Global Privacy Control) the sign-up is only
// an event, linked to no account; with GPC not even that.
func TestSignupBasicAndGPC(t *testing.T) {
	ctx := context.Background()
	m, srv := signupFixture(t, `{}`)
	m.handleEdge(ctx, access("shop.box.test", "/", human(2), 200, doc))
	_, key, _ := m.authSink(ctx, "shop")
	postJSON(t, srv.URL+"/signup", key, signupBody{UserID: "u1", Host: "shop.box.test", IP: "8.8.8.8", UA: human(2)})
	postJSON(t, srv.URL+"/signup", key, signupBody{UserID: "u2", Host: "shop.box.test", IP: "8.8.8.8", UA: human(3), GPC: true})
	if rows, _ := m.attr.projectRows(ctx, "shop"); rows["signups"] != 0 {
		t.Fatalf("basic analytics saved sign-ups: %v", rows)
	}
	_ = m.pipe.Flush(ctx)
	evs, _ := m.store.Recent(ctx, timeZero)
	n := 0
	for _, e := range evs {
		if e.Name == "Signup" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Signup events %d, want 1 (GPC isn't counted)", n)
	}
}

var timeZero = time.Unix(0, 0)
