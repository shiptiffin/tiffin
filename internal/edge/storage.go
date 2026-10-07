package edge

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/certmagic"
)

const storageModuleName = "tiffin"

func init() { caddy.RegisterModule(fileStorage{}) }

// fileStorage is Caddy's file_system storage, except that it hides an ACME
// account without an email until both of its files are written.
//
// certmagic keeps the email-less account in a folder named "default". To
// find an email when none is configured, it lists the account folders and
// loads the newest; when that load fails (the account is being saved by a
// concurrent issuance, or a crash left only one of its two files), it makes
// up an account with the folder name as the contact: mailto:default. Every
// CA rejects that contact, so the issuance fails, and since the half-written
// account is never completed by a failed registration, every retry fails
// the same way. certmagic also remembers the discovered email for the rest
// of the process. Hiding the incomplete folder makes the discovery find
// nothing, so the issuance registers (or, under certmagic's registration
// lock, reloads) the email-less account as intended.
type fileStorage struct {
	Root string `json:"root,omitempty"`
}

func (fileStorage) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.storage." + storageModuleName, New: func() caddy.Module { return new(fileStorage) }}
}

func (s fileStorage) CertMagicStorage() (certmagic.Storage, error) {
	return accountGuard{&certmagic.FileStorage{Path: s.Root}}, nil
}

type accountGuard struct{ *certmagic.FileStorage }

func (g accountGuard) List(ctx context.Context, prefix string, recursive bool) ([]string, error) {
	keys, err := g.FileStorage.List(ctx, prefix, recursive)
	if err != nil {
		return keys, err
	}
	return slices.DeleteFunc(keys, func(k string) bool {
		if path.Base(k) != "default" || path.Base(path.Dir(k)) != "users" || !strings.HasPrefix(k, "acme/") {
			return false
		}
		return !g.Exists(ctx, k+"/default.json") || !g.Exists(ctx, k+"/default.key")
	}), nil
}
