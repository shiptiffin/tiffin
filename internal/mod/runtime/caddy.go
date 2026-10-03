package runtime

// Static deploys are edge routes with a FileRoot (and SPA routes rewrite to
// index.html). The Caddy modules for those must be compiled in; internal/edge
// does not import them yet, so the runtime module does (they register
// themselves with Caddy on init).
import (
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
)
