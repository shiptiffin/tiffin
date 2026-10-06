import { apiKeyClient } from "@better-auth/api-key/client";
import { passkeyClient } from "@better-auth/passkey/client";
import { emailOTPClient, magicLinkClient, organizationClient, twoFactorClient } from "better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";
import { type ReactNode } from "react";
type Plugins = [
    ReturnType<typeof organizationClient<{}>>,
    ReturnType<typeof magicLinkClient>,
    ReturnType<typeof emailOTPClient>,
    ReturnType<typeof twoFactorClient>,
    ReturnType<typeof passkeyClient>,
    ReturnType<typeof apiKeyClient>
];
/** Better Auth's React client with the plugins the box's engine serves. */
export type TiffinAuthClient = ReturnType<typeof createAuthClient<{
    plugins: Plugins;
}>>;
export declare function createTiffinAuth(opts?: {
    baseURL?: string;
}): TiffinAuthClient;
/** What the engine says this app supports (GET /api/auth/tiffin/config). */
export type AuthConfig = {
    appName: string;
    methods: string[];
    organizations: boolean;
    captcha: boolean;
    social: {
        google: {
            configured: boolean;
        } | null;
        github: {
            configured: boolean;
        } | null;
    };
};
type Ctx = {
    client: TiffinAuthClient;
    baseURL: string;
    config: AuthConfig | null;
    configError: string | null;
};
/**
 * Optional: share one client (and point components at another origin).
 * Without it, components talk to /api/auth on the current origin.
 */
export declare function TiffinAuthProvider({ baseURL, client, children }: {
    baseURL?: string;
    client?: TiffinAuthClient;
    children: ReactNode;
}): import("react").JSX.Element;
/** The shared client and the app's auth config. */
export declare function useTiffinAuth(): Ctx;
/**
 * Solves the engine's proof-of-work challenge in the background as soon as a
 * form mounts, so by the time someone presses the button it's usually done.
 * Each solution is good for one request.
 */
export declare function useCaptcha(baseURL: string, enabled: boolean): () => Promise<Record<string, string>>;
/**
 * The bot check for your own sign-in and sign-up forms that post to a Server
 * Action (signIn / signUp from tiffin-sdk/next/auth): a hidden `captcha`
 * field, solved in the background once the form mounts. Submitting before
 * it is ready waits for it; each submit gets a fresh one for the next try.
 *
 *   <form action={signInAction}><CaptchaField /> ...</form>
 */
export declare function CaptchaField({ name }: {
    name?: string;
}): import("react").JSX.Element;
/** Turns a Better Auth client error into words for people. */
export declare function errorText(e: unknown, fallback?: string): string;
export {};
