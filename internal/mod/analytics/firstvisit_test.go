package analytics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/edge"
)

// A visitor first comes from Bluesky with the cookie, comes back days later
// from Google and signs up: the account keeps both.
func TestFirstVisitCredit(t *testing.T) {
	ctx := context.Background()
	m, srv := signupFixture(t, `{"attribution":{"firstVisitCookie":"outside-eu"}}`)
	const id = "AbCdEfGhIjKlMnOpQrStUv"
	day1 := access("shop.box.test", "/blog/launch", human(5), 200, with(doc, "Referer", "https://bsky.app/profile/x"))
	day1 = []byte(strings.Replace(string(day1), `"level"`, `"first_visit":"`+id+`","level"`, 1))
	m.handleEdge(ctx, day1)
	if fv, _ := m.attr.firstVisit(ctx, "shop", id); fv == nil || fv.Source != "Bluesky" || fv.Landing != "/blog/launch" {
		t.Fatalf("first visit %+v", fv)
	}
	// A later visit under the same id doesn't replace the first.
	m.pipe.Prune()
	m.noteFirstVisit("shop", id, Touch{At: time.Now(), Source: "Google"})
	m.handleEdge(ctx, access("shop.box.test", "/pricing", human(6), 200, with(doc, "Referer", "https://www.google.com/")))
	_, key, _ := m.authSink(ctx, "shop")
	postJSON(t, srv.URL+"/signup", key, signupBody{UserID: "u7", Host: "shop.box.test", IP: "8.8.8.8", UA: human(6), FirstVisit: id})
	su, _ := m.attr.signup(ctx, "shop", "u7")
	if su == nil || su.Visit.Source != "Google" || su.First == nil || su.First.Source != "Bluesky" {
		t.Fatalf("signup %+v", su)
	}
	if fv := m.firstVisitHosts(); fv == nil || fv.Hosts["shop.box.test"] != "outside-eu" {
		t.Fatalf("edge hosts %+v", fv)
	}
}

func TestOptOutForgets(t *testing.T) {
	ctx := context.Background()
	m, srv := signupFixture(t, `{"attribution":{"firstVisitCookie":"ask-eu"}}`)
	const id = "AbCdEfGhIjKlMnOpQrStUv"
	m.noteFirstVisit("shop", id, Touch{At: time.Now(), Source: "Reddit"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+OptOutPath, nil)
	req.Host = "shop.box.test"
	req.AddCookie(&http.Cookie{Name: edge.FirstVisitCookie, Value: id})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var choice, cleared bool
	for _, c := range res.Cookies() {
		choice = choice || c.Name == edge.ConsentCookie && c.Value == "no"
		cleared = cleared || c.Name == edge.FirstVisitCookie && c.MaxAge < 0
	}
	if !choice || !cleared {
		t.Fatalf("opt-out cookies %v", res.Cookies())
	}
	if fv, _ := m.attr.firstVisit(ctx, "shop", id); fv != nil {
		t.Fatal("opt-out kept the first visit")
	}
	// The prompt asks an EU (here: unknown-country) visitor of an ask-eu project.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://shop.box.test"+ConsentPath, nil)
	m.consent(w, r)
	if !strings.Contains(w.Body.String(), `"ask":true`) {
		t.Fatalf("consent %s", w.Body.String())
	}
}
