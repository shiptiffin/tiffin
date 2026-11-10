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
import type { BetterAuthClientOptions, BetterAuthClientPlugin } from "better-auth/client";
/** The client plugins every Tiffin auth client gets. */
export type TiffinClientPlugins = [
    ReturnType<typeof magicLinkClient>,
    ReturnType<typeof emailOTPClient>,
    ReturnType<typeof organizationClient<{}>>,
    ReturnType<typeof twoFactorClient>
];
/** The client plugins every Tiffin auth client gets: magic links, email codes, organizations, two-step sign-in. */
export declare function tiffinClientPlugins(): TiffinClientPlugins;
export type TiffinAuthClientOptions<P extends BetterAuthClientPlugin[]> = Omit<BetterAuthClientOptions, "plugins" | "basePath"> & {
    /** More client plugins, after the box's (passkeyClient(), say). */
    plugins?: [...P];
};
/** What createAuthClient returns: Better Auth's client with the box's plugins and yours. */
export type TiffinAuthClient<P extends BetterAuthClientPlugin[] = []> = ReturnType<typeof betterAuthClient<{
    basePath: string;
    plugins: [...TiffinClientPlugins, ...P];
}>>;
/**
 * Better Auth's client for the box's /api/auth: basePath "/api/auth" on this
 * page's origin (or `baseURL`), with the magic-link, email-OTP, organization
 * and two-factor plugins, plus any you pass.
 */
export declare function createAuthClient<const P extends BetterAuthClientPlugin[] = []>(opts?: TiffinAuthClientOptions<P>): TiffinAuthClient<P>;
