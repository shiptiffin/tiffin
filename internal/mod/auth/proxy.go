package auth

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// The one callback URL. A project signing in with the box-wide keys sends
// people to the provider with redirect_uri
// https://<dashboard host>/api/auth/callback/<provider>; the provider sends
// them back there; the box forwards that request to the engine, whose OAuth
// proxy endpoint exchanges the code, encrypts the profile with the proxy
// secret and redirects to the app host the sign-in started on, which makes
// the account and session in the project's own database.
//
// Only the callback (GET, or POST for Apple's form_post) and the error page
// pass. Cookies never do, either way: the dashboard's session stays out of
// the engine, and the engine sets nothing on the dashboard's origin.

// engineUpstream is where DashboardHandler forwards to (tests change it).
var engineUpstream = "http://" + EngineAddr

// callbackBodyLimit caps a callback POST (Apple's form_post is ~2 KB).
const callbackBodyLimit = 64 << 10

// DashboardSignIn, when set, gets the first look at a callback on the
// dashboard host and reports whether it was its own. It is for signing in
// to the dashboard itself with Google or GitHub (planned): the box's
// provider app may register the same /api/auth/callback/<provider> URL for
// both, and the box tells its own sign-ins apart (by their state) from the
// apps' sign-ins, which go on to the engine.
var DashboardSignIn func(w http.ResponseWriter, r *http.Request) bool

// DashboardHandler serves /api/auth/ on the dashboard host. The box's HTTP
// server mounts it next to /v1/.
func DashboardHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if DashboardSignIn != nil && strings.HasPrefix(r.URL.Path, CallbackPathPrefix) && DashboardSignIn(w, r) {
			return
		}
		if !dashboardPathAllowed(r.Method, r.URL.Path) {
			http.Error(w, "Not found. The dashboard only serves sign-in callbacks under /api/auth.", http.StatusNotFound)
			return
		}
		target, err := url.Parse(engineUpstream)
		if err != nil {
			http.Error(w, "auth engine address", http.StatusInternalServerError)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, callbackBodyLimit)
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Host = pr.In.Host // the engine picks its proxy endpoint by Host
				for _, h := range []string{"Cookie", "Authorization", "X-Tiffin-Host", "X-Api-Key", "X-Skip-Oauth-Proxy"} {
					pr.Out.Header.Del(h)
				}
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			},
			ModifyResponse: func(res *http.Response) error {
				res.Header.Del("Set-Cookie")
				res.Header.Set("Cache-Control", "no-store")
				res.Header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
				res.Header.Set("X-Content-Type-Options", "nosniff")
				res.Header.Set("Referrer-Policy", "no-referrer")
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "Sign-in can't finish right now: the box's auth engine isn't answering. Try again in a minute.", http.StatusServiceUnavailable)
			},
		}
		rp.ServeHTTP(w, r)
	})
}

// dashboardPathAllowed: the callback of a known provider, and the error page.
func dashboardPathAllowed(method, path string) bool {
	if path == PathPrefix+"/error" {
		return method == http.MethodGet
	}
	id, ok := strings.CutPrefix(path, CallbackPathPrefix)
	if !ok {
		return false
	}
	if _, known := ProviderByID(id); !known {
		return false
	}
	return method == http.MethodGet || method == http.MethodPost
}
