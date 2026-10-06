package release

import "strings"

// trustedKeys are the minisign public keys this build accepts release
// manifests from (the base64 line of each .pub file). To rotate: add the
// new key, ship a release signed with the old one, then sign with the new
// one; drop the old key once every box runs a build that trusts the new.
// Empty: the build checks no releases (boxes update with tiffin up).
var trustedKeys = []string{}

// extraKeys adds keys at link time, for test builds:
//
//	-ldflags "-X github.com/btahir/tiffin/internal/release.extraKeys=<key>,<key>"
var extraKeys string

// TrustedKeys returns the keys release manifests must be signed with.
func TrustedKeys() []PublicKey {
	var out []PublicKey
	for _, s := range append(append([]string(nil), trustedKeys...), strings.Split(extraKeys, ",")...) {
		if k, err := ParsePublicKey(s); err == nil {
			out = append(out, k)
		}
	}
	return out
}
