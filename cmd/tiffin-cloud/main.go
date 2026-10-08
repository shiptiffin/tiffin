// Command tiffin-cloud is ShipTiffin's control plane worker, the `worker`
// app of the `cloud` project (cmd/tiffin-cloud/tiffin.config.ts): a project
// of its own, so its secrets never reach the website or its builds. It runs
// the jobs the website queues in the website's Postgres: creating managed
// boxes in customers' own Hetzner projects, resizing and deleting them, and
// keeping their <name>.shiptiffin.app records. It answers /health on $PORT.
//
// Settings (the cloud project's secrets):
//
//	CONTROL_DATABASE_URL   the website project's DATABASE_URL (the cloud_* tables)
//	CLOUD_SEAL_KEY         base64 X25519 private key: opens customers' Hetzner tokens
//	CLOUD_LICENCE_KEY      base64 32-byte ed25519 seed: signs box licences
//	CLOUDFLARE_API_TOKEN   Zone · DNS · Edit on the shiptiffin.app zone only
//	CLOUD_ZONE             default shiptiffin.app
//	CLOUD_CONTROL_URL      default https://shiptiffin.com (where boxes check in)
//	CLOUD_SSH_FROM         optional: this worker's public IPs (else asked of ipify)
//	CLOUD_RELEASE_SOURCE   optional: the signed release manifest URL ({channel})
//
// The website gets only the public halves: CLOUD_SEAL_PUBLIC and
// CLOUD_LICENCE_PUBLIC. `tiffin-cloud keygen` makes both pairs and prints
// which secret goes to which project.
//
// Until the required ones are set it waits, answering /health, and does nothing.
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
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
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		keygen(os.Stdout)
		return
	}
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
	log.Info("tiffin-cloud running", "zone", w.DNS.Zone, "control", w.ControlURL,
		"CLOUD_SEAL_PUBLIC", cloud.KeyText(w.SealKey.PublicKey().Bytes()),
		"CLOUD_LICENCE_PUBLIC", licence.PublicKeyText(w.Licence.Public().(ed25519.PublicKey)))
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
	dbURL := strings.TrimSpace(os.Getenv("CONTROL_DATABASE_URL"))
	if dbURL == "" {
		missing = append(missing, "CONTROL_DATABASE_URL")
	}
	sealKey, err := cloud.ParseSealKey(os.Getenv("CLOUD_SEAL_KEY"))
	if err != nil {
		missing = append(missing, "CLOUD_SEAL_KEY")
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
		SealKey:    sealKey,
		Licence:    key,
		DNS:        cloud.DNS{P: dns, Zone: env("CLOUD_ZONE", "shiptiffin.app")},
		ControlURL: strings.TrimRight(env("CLOUD_CONTROL_URL", "https://shiptiffin.com"), "/"),
		Log:        log,
		EgressIPs:  cloud.EgressIPs(os.Getenv("CLOUD_SSH_FROM")),
		Binary:     rel.Get,
	}, nil
}

// keygen prints fresh keys: the private halves for the cloud project, the
// public halves for the website project.
func keygen(w io.Writer) {
	seal, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		panic(err)
	}
	lic := ed25519.NewKeyFromSeed(seed)
	fmt.Fprintf(w, `# cloud project (the worker) only:
CLOUD_SEAL_KEY=%s
CLOUD_LICENCE_KEY=%s

# website project:
CLOUD_SEAL_PUBLIC=%s
CLOUD_LICENCE_PUBLIC=%s
`, cloud.KeyText(seal.Bytes()), base64.StdEncoding.EncodeToString(seed),
		cloud.KeyText(seal.PublicKey().Bytes()), licence.PublicKeyText(lic.Public().(ed25519.PublicKey)))
}
