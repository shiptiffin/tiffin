/**
 * `@shiptiffin/sdk/client/auth`: Better Auth's browser client, set up for the
 * box. Its own entry point, so apps that don't sign anyone in never load
 * better-auth; it needs `better-auth` installed (an optional peer).
 *
 *   import { createAuthClient } from "@shiptiffin/sdk/client/auth";
 *   export const authClient = createAuthClient();
 *   await authClient.signIn.magicLink({ email, callbackURL: "/app/" });
 *
 * It talks to /api/auth on the page's own host with the client plugins for
 * what the box serves: magic links, email codes, organizations and two-step
 * sign-in. Add passkeys (they live in their own package) with
 * `createAuthClient({ plugins: [passkeyClient()] })`, passkeyClient from
 * "@better-auth/passkey/client".
 *
 * Install the better-auth release the box runs: `authConfig()` from
 * @shiptiffin/sdk/client reports it (`betterAuthVersion`), and so does
 * `tiffin auth show <project>`.
 */
import { createAuthClient as betterAuthClient } from "better-auth/client";
import { emailOTPClient, magicLinkClient, organizationClient, twoFactorClient } from "better-auth/client/plugins";
/** The client plugins every Tiffin auth client gets: magic links, email codes, organizations, two-step sign-in. */
export function tiffinClientPlugins() {
    return [magicLinkClient(), emailOTPClient(), organizationClient(), twoFactorClient()];
}
/**
 * Better Auth's client for the box's /api/auth: basePath "/api/auth" on this
 * page's origin (or `baseURL`), with the magic-link, email-OTP, organization
 * and two-factor plugins, plus any you pass.
 */
export function createAuthClient(opts = {}) {
    const { plugins, ...rest } = opts;
    return betterAuthClient({
        ...rest,
        basePath: "/api/auth",
        plugins: [...tiffinClientPlugins(), ...(plugins ?? [])],
    });
}
