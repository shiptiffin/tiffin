package main

import (
	"bytes"
	"crypto/ed25519"
	"log/slog"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/cloud"
	"github.com/shiptiffin/tiffin/internal/licence"
)

// keygen's halves belong together: what the website seals the worker opens,
// and what the worker signs the website verifies.
func TestKeygen(t *testing.T) {
	var b bytes.Buffer
	keygen(&b)
	v := map[string]string{}
	for _, line := range strings.Split(b.String(), "\n") {
		if k, val, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "#") {
			v[k] = val
		}
	}
	priv, err := cloud.ParseSealKey(v["CLOUD_SEAL_KEY"])
	if err != nil {
		t.Fatal(err)
	}
	pub, err := cloud.ParseSealPublic(v["CLOUD_SEAL_PUBLIC"])
	if err != nil {
		t.Fatal(err)
	}
	s, _ := cloud.Seal(pub, []byte("x"), "a")
	if got, err := cloud.Open(priv, s, "a"); err != nil || string(got) != "x" {
		t.Fatal("seal pair does not match")
	}
	key, err := licence.KeyFromSeed(v["CLOUD_LICENCE_KEY"])
	if err != nil {
		t.Fatal(err)
	}
	lpub, err := licence.ParsePublicKey(v["CLOUD_LICENCE_PUBLIC"])
	if err != nil || !lpub.Equal(key.Public().(ed25519.PublicKey)) {
		t.Fatal("licence pair does not match")
	}
}

// Off-site backups stay off until all three CLOUD_R2_* settings are there.
func TestOffsiteSettings(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for _, k := range []string{"CLOUD_R2_ACCOUNT_ID", "CLOUD_R2_API_TOKEN", "CLOUD_R2_ACCESS_KEY_ID", "CLOUD_R2_BUCKET", "CLOUD_R2_ENDPOINT"} {
		t.Setenv(k, "")
	}
	if offsite(log) != nil {
		t.Fatal("on without settings")
	}
	t.Setenv("CLOUD_R2_ACCOUNT_ID", "acct")
	t.Setenv("CLOUD_R2_API_TOKEN", "tok")
	if offsite(log) != nil {
		t.Fatal("on without the parent access key")
	}
	t.Setenv("CLOUD_R2_ACCESS_KEY_ID", "parent")
	o := offsite(log)
	if o == nil || o.R2.Bucket != "shiptiffin-customer-backups" || o.R2.S3Endpoint() != "https://acct.r2.cloudflarestorage.com" {
		t.Fatalf("settings: %+v", o)
	}
}
