package auth

import (
	"context"
	"errors"
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

// UserExists reports whether id is a user of project. Analytics checks that a
// payment's client_reference_id names an account before crediting it: apps
// often put their own ids there (an order, a box).
func UserExists(ctx context.Context, project, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 200 {
		return false, nil
	}
	err := defaultEngine.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/users/"+url.PathEscape(id), nil, nil, nil)
	var ee *EngineError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.Status == http.StatusNotFound:
		return false, nil
	}
	return false, err
}
