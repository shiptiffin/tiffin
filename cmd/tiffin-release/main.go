// Command tiffin-release makes the signing key and the signed manifests of
// a Tiffin release (make release-sign runs it). The secret key never goes
// in the repository: it is read from $TIFFIN_RELEASE_KEY (the key file's
// contents, as CI holds it in a secret) or the file -key names.
//
//	tiffin-release keygen -out ~/tiffin-release.key   # prints the public key for internal/release/keys.go
//	tiffin-release manifest -dist dist -version 1.4.0 -key ~/tiffin-release.key
//	tiffin-release verify dist/stable/manifest.json      # against the keys this build trusts
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/release"
)

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: tiffin-release keygen|manifest|verify [flags]"))
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tiffin-release:", err)
	os.Exit(1)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "where to write the secret key (never inside the repository)")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("keygen needs -out")
	}
	k, err := release.GenerateKey()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(k.File()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(*out+".pub", []byte(k.Public().File()), 0o644); err != nil {
		return err
	}
	fmt.Printf("secret key: %s (keep it out of the repository; CI reads it from the TIFFIN_RELEASE_KEY secret)\npublic key: %s\n", *out, k.Public())
	return nil
}

// secretKey reads the signing key from $TIFFIN_RELEASE_KEY or a file.
func secretKey(path string) (release.SecretKey, error) {
	if s := os.Getenv("TIFFIN_RELEASE_KEY"); s != "" {
		return release.ParseSecretKey(s)
	}
	if path == "" {
		return release.SecretKey{}, errors.New("no signing key: set TIFFIN_RELEASE_KEY or pass -key <file>")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return release.SecretKey{}, err
	}
	return release.ParseSecretKey(string(raw))
}

// Platforms the box runs on: a manifest must list their builds.
var boxPlatforms = []string{"linux/amd64", "linux/arm64"}

// Platforms the CLI alone runs on (people's own computers): listed when
// their builds are in dist, so installs on a Mac are checked against the
// same signed manifest.
var cliPlatforms = []string{"darwin/arm64", "darwin/amd64"}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	dist := fs.String("dist", "dist", "directory with the builds (tiffin-linux-amd64, tiffin-linux-arm64; tiffin-darwin-* when present)")
	ver := fs.String("version", "", "the release's version, e.g. 1.4.0 (a leading v is dropped)")
	channels := fs.String("channels", "", "channels to publish to, comma-separated (default: stable,edge for a release, edge for a pre-release)")
	minVer := fs.String("min-version", "", "the oldest version that may update to this one directly")
	notes := fs.String("notes", "", "release notes URL")
	rollout := fs.Int("rollout", 100, "percentage of boxes that apply it automatically")
	edge := fs.Bool("edge-restart", false, "the edge's code changed: boxes restart tiffin-edge after updating")
	baseURL := fs.String("base-url", "", "URL the builds are downloaded from (default: next to the manifest)")
	keyFile := fs.String("key", "", "the secret key file (or $TIFFIN_RELEASE_KEY)")
	_ = fs.Parse(args)
	v := strings.TrimPrefix(*ver, "v")
	parsed, err := release.ParseVersion(v)
	if err != nil {
		return err
	}
	key, err := secretKey(*keyFile)
	if err != nil {
		return err
	}
	chs := strings.Split(*channels, ",")
	if *channels == "" {
		chs = []string{"stable", "edge"}
		if parsed.Pre != "" {
			chs = []string{"edge"}
		}
	}
	m := release.Manifest{Version: v, Date: time.Now().UTC().Truncate(time.Second), MinVersion: strings.TrimPrefix(*minVer, "v"),
		Notes: *notes, Rollout: *rollout, EdgeRestart: *edge, Artifacts: map[string]release.Artifact{}}
	for _, p := range boxPlatforms {
		name := "tiffin-" + strings.ReplaceAll(p, "/", "-")
		sum, size, err := hashFile(filepath.Join(*dist, name))
		if err != nil {
			return err
		}
		a := release.Artifact{Name: name, SHA256: sum, Size: size}
		if *baseURL != "" {
			a.URL = strings.TrimSuffix(*baseURL, "/") + "/" + name
		}
		m.Artifacts[p] = a
	}
	for _, p := range cliPlatforms {
		name := "tiffin-" + strings.ReplaceAll(p, "/", "-")
		sum, size, err := hashFile(filepath.Join(*dist, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		a := release.Artifact{Name: name, SHA256: sum, Size: size}
		if *baseURL != "" {
			a.URL = strings.TrimSuffix(*baseURL, "/") + "/" + name
		}
		m.Artifacts[p] = a
	}
	for _, ch := range chs {
		m.Channel = ch
		if err := m.Validate(); err != nil {
			return err
		}
		raw, _ := json.MarshalIndent(m, "", "  ")
		raw = append(raw, '\n')
		sig, err := release.Sign(key, raw, m.TrustedComment())
		if err != nil {
			return err
		}
		dir := filepath.Join(*dist, ch)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json.minisig"), sig, 0o644); err != nil {
			return err
		}
		fmt.Printf("%s/manifest.json: tiffin %s, signed by key %s\n", dir, v, key.Public().KeyID())
	}
	return nil
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pub := fs.String("pub", "", "public key (base64 line or .pub file contents); default: the keys this build trusts")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: tiffin-release verify [-pub <key>] manifest.json")
	}
	keys := release.TrustedKeys()
	if *pub != "" {
		k, err := release.ParsePublicKey(*pub)
		if err != nil {
			return err
		}
		keys = []release.PublicKey{k}
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(fs.Arg(0) + ".minisig")
	if err != nil {
		return err
	}
	trusted, _, err := release.Verify(keys, raw, sig)
	if err != nil {
		return err
	}
	if _, err := release.ParseManifest(raw); err != nil {
		return err
	}
	fmt.Println("ok:", trusted)
	return nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
