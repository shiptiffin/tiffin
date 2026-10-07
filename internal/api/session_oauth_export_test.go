package api

import "net/http"

// UseFakeOAuthProviders points dashboard sign-in at fake Google and GitHub
// endpoints under base; the returned func restores the real ones.
func UseFakeOAuthProviders(base string, c *http.Client) func() {
	old := oauthURLs
	oauthURLs = oauthEndpoints{
		GoogleAuth: base + "/google/auth", GoogleToken: base + "/google/token",
		GitHubAuth: base + "/github/auth", GitHubToken: base + "/github/token", GitHubUser: base + "/github/user", GitHubEmails: base + "/github/emails",
		HTTP: c,
	}
	return func() { oauthURLs = old }
}
