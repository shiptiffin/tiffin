package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// UserByEmail returns the ID of project's user whose email is email
// (case-insensitive), or "" when there is none. Analytics matches a
// payment's customer to an account with it.
func UserByEmail(ctx context.Context, project, email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", nil
	}
	var e enginePage[User]
	q := url.Values{"search": {email}, "limit": {"20"}}
	if err := defaultEngine.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/users", q, nil, &e); err != nil {
		return "", err
	}
	for _, u := range e.Users {
		if strings.EqualFold(u.Email, email) {
			return u.ID, nil
		}
	}
	return "", nil
}
