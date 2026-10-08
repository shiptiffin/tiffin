// Command tiffin-cloud is ShipTiffin's control plane worker: the `cloud` app
// of the `website` project (see site/tiffin.config.ts). It runs the jobs the
// website queues in Postgres: creating managed boxes in customers' own
// Hetzner projects, resizing and deleting them, and keeping their
// <name>.shiptiffin.app records. It answers /health on $PORT for the box.
//
// Settings (project secrets and env):
//
//	DATABASE_URL (or DIRECT_DATABASE_URL)  the website's Postgres
//	CLOUD_KEK              base64 32 bytes: seals customers' Hetzner tokens
//	CLOUD_LICENCE_KEY      base64 32-byte ed25519 seed: signs box licences
//	CLOUDFLARE_API_TOKEN   Zone · DNS · Edit on the shiptiffin.app zone only
//	CLOUD_ZONE             default shiptiffin.app
//	CLOUD_CONTROL_URL      default https://shiptiffin.com (where boxes check in)
//	CLOUD_SSH_FROM         optional: this worker's public IPs (else asked of ipify)
//	CLOUD_RELEASE_SOURCE   optional: the signed release manifest URL ({channel})
//
// Until the required ones are set it waits, answering /health, and does nothing.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/cloud"
	"github.com/btahir/tiffin/internal/dnskit"
	"github.com/btahir/tiffin/internal/licence"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	port := env("PORT", "8080")
	srv := &http.Server{Addr: ":" + port, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("health server", "err", err)
		}
	}()
	defer srv.Close()

	w, missing := configure(ctx, log)
	if w == nil {
		log.Warn("tiffin-cloud is not configured yet; waiting (set the project secrets, then redeploy)", "missing", strings.Join(missing, ", "))
		<-ctx.Done()
		return
	}
	log.Info("tiffin-cloud running", "zone", w.DNS.Zone, "control", w.ControlURL)
	w.Run(ctx)
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// configure builds the worker, or names what is missing.
func configure(ctx context.Context, log *slog.Logger) (*cloud.Worker, []string) {
	var missing []string
	dbURL := env("DIRECT_DATABASE_URL", os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	kek, err := cloud.ParseKEK(os.Getenv("CLOUD_KEK"))
	if err != nil {
		missing = append(missing, "CLOUD_KEK")
	}
	key, err := licence.KeyFromSeed(os.Getenv("CLOUD_LICENCE_KEY"))
	if err != nil {
		missing = append(missing, "CLOUD_LICENCE_KEY")
	}
	cfToken := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN"))
	if cfToken == "" {
		missing = append(missing, "CLOUDFLARE_API_TOKEN")
	}
	if len(missing) > 0 {
		return nil, missing
	}
	var store *cloud.PG
	for {
		store, err = cloud.OpenPG(ctx, dbURL)
		if err == nil {
			break
		}
		log.Warn("database not ready; retrying in 10 s", "err", err)
		select {
		case <-ctx.Done():
			return nil, []string{"database"}
		case <-time.After(10 * time.Second):
		}
	}
	dns, err := dnskit.Open("cloudflare", map[string]string{"token": cfToken})
	if err != nil {
		return nil, []string{"CLOUDFLARE_API_TOKEN: " + err.Error()}
	}
	cache := filepath.Join(os.TempDir(), "tiffin-releases")
	rel := &cloud.ReleaseBinaries{Source: os.Getenv("CLOUD_RELEASE_SOURCE"), Dir: cache}
	return &cloud.Worker{
		Store:      store,
		KEK:        kek,
		Licence:    key,
		DNS:        cloud.DNS{P: dns, Zone: env("CLOUD_ZONE", "shiptiffin.app")},
		ControlURL: strings.TrimRight(env("CLOUD_CONTROL_URL", "https://shiptiffin.com"), "/"),
		Log:        log,
		EgressIPs:  cloud.EgressIPs(os.Getenv("CLOUD_SSH_FROM")),
		Binary:     rel.Get,
	}, nil
}
