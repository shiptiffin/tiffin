// The Better Auth release this engine runs. Apps' browser clients
// (better-auth/client) talk to it, so they should install the same version:
// /api/auth/tiffin/config reports it (authConfig() in @shiptiffin/sdk/client),
// and `tiffin auth show` prints the box's (internal/mod/auth BetterAuthVersion,
// kept equal by a test). package.json pins it exactly.
import pkg from "../package.json" with { type: "json" };

export const BETTER_AUTH_VERSION: string = pkg.dependencies["better-auth"];
