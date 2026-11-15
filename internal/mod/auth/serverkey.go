package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// BetterAuthVersion is the Better Auth release the box's engine runs
// (packages/auth-engine pins it; a test keeps the two equal). Apps install
// the same better-auth for their browser client: `tiffin auth show` prints it
// and the engine reports it at /api/auth/tiffin/config.
const BetterAuthVersion = "1.7.14"

// ServerKey is a project's server key, TIFFIN_AUTH_KEY: "tak_<project>_<mac>",
// an HMAC of the project's name under its engine secret. The project's apps
// send it (x-tiffin-auth-key) to find and create the project's users
// (getUserByEmail and createUser in @shiptiffin/sdk/auth). The engine derives
// the same from its config (serverKey in packages/auth-engine/src/server.ts)
// and answers those calls only with it, so another project's app on the box,
// which can name any host, still can't read or make this project's users.
func ServerKey(project, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("tiffin-auth server key:" + project))
	return "tak_" + project + "_" + hex.EncodeToString(m.Sum(nil))[:48]
}

// serverKeyFor is the project's server key, making the project's engine
// secret if it has none yet (the engine config takes the same one).
func serverKeyFor(ctx context.Context, p *platform.Platform, project string) (string, error) {
	secret, err := projectSecret(ctx, p, project)
	if err != nil {
		return "", err
	}
	return ServerKey(project, secret), nil
}
