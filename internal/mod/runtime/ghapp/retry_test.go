package ghapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A status write GitHub fails with a 5xx is tried again; a 4xx is not.
func TestStatusWritesRetryServerErrors(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryDelays = old })

	var statuses, deployments atomic.Int32
	fails := map[string]int{"statuses": 1, "deployments": 5}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"token":"t","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		case strings.Contains(r.URL.Path, "/statuses/"):
			if int(statuses.Add(1)) <= fails["statuses"] {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(201)
		case strings.Contains(r.URL.Path, "/deployments/"):
			n := deployments.Add(1)
			if strings.Contains(r.URL.Path, "/deployments/2/") {
				w.WriteHeader(422) // a mistake of ours: repeating it won't help
				return
			}
			if int(n) <= fails["deployments"] {
				w.WriteHeader(502)
				return
			}
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	app, err := NewClient(Endpoints{API: srv.URL, Web: srv.URL}, srv.Client()).NewApp(Credentials{ID: 1, Slug: "a", PrivateKey: pemKey, WebhookSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	if err := app.SetStatus(ctx, 7, "o/r", sha, Status{State: "success", Context: "c"}); err != nil || statuses.Load() != 2 {
		t.Fatalf("a 500 then a 201: %v after %d tries", err, statuses.Load())
	}
	if err := app.SetDeploymentStatus(ctx, 7, "o/r", 1, DeploymentStatus{State: "success"}); !IsStatus(err, 502) || deployments.Load() != 3 {
		t.Fatalf("502 three times: %v after %d tries", err, deployments.Load())
	}
	deployments.Store(0)
	if err := app.SetDeploymentStatus(ctx, 7, "o/r", 2, DeploymentStatus{State: "success"}); !IsStatus(err, 422) || deployments.Load() != 1 {
		t.Fatalf("a 422 is not retried: %v after %d tries", err, deployments.Load())
	}
}
