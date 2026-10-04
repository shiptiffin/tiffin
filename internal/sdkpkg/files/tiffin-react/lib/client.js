"use client";
import { jsx as _jsx } from "react/jsx-runtime";
// The auth client every component shares: Better Auth's client with the
// plugins the box's engine serves, pointed at /api/auth on this app's host.
import { apiKeyClient } from "@better-auth/api-key/client";
import { passkeyClient } from "@better-auth/passkey/client";
import { emailOTPClient, magicLinkClient, organizationClient, twoFactorClient } from "better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";
import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import { solveChallenge } from "altcha-lib";
import { deriveKey } from "altcha-lib/algorithms/web/sha";
export function createTiffinAuth(opts = {}) {
    return createAuthClient({
        baseURL: opts.baseURL ?? (typeof window !== "undefined" ? window.location.origin : undefined),
        basePath: "/api/auth",
        plugins: [organizationClient(), magicLinkClient(), emailOTPClient(), twoFactorClient(), passkeyClient(), apiKeyClient()],
    });
}
const AuthContext = createContext(null);
let defaultClient = null;
const configCache = new Map();
function loadConfig(baseURL) {
    let p = configCache.get(baseURL);
    if (!p) {
        p = fetch(`${baseURL}/api/auth/tiffin/config`, { credentials: "include" }).then(async (r) => {
            if (!r.ok)
                throw new Error(`The auth endpoint answered ${r.status}. Is services.auth on in tiffin.config.ts?`);
            return (await r.json());
        });
        p.catch(() => configCache.delete(baseURL));
        configCache.set(baseURL, p);
    }
    return p;
}
/**
 * Optional: share one client (and point components at another origin).
 * Without it, components talk to /api/auth on the current origin.
 */
export function TiffinAuthProvider({ baseURL, client, children }) {
    const value = useAuthState(baseURL, client);
    return _jsx(AuthContext.Provider, { value: value, children: children });
}
function useAuthState(baseURL, client) {
    const url = (baseURL ?? (typeof window !== "undefined" ? window.location.origin : "")).replace(/\/+$/, "");
    const c = useMemo(() => {
        if (client)
            return client;
        if (baseURL)
            return createTiffinAuth({ baseURL: url });
        defaultClient ??= createTiffinAuth({ baseURL: url });
        return defaultClient;
    }, [client, baseURL, url]);
    const [config, setConfig] = useState(null);
    const [configError, setError] = useState(null);
    useEffect(() => {
        let live = true;
        loadConfig(url).then((v) => live && setConfig(v), (e) => live && setError(String(e?.message ?? e)));
        return () => {
            live = false;
        };
    }, [url]);
    return { client: c, baseURL: url, config, configError };
}
/** The shared client and the app's auth config. */
export function useTiffinAuth() {
    const ctx = useContext(AuthContext);
    const own = useAuthState();
    return ctx ?? own;
}
/**
 * Solves the engine's proof-of-work challenge in the background as soon as a
 * form mounts, so by the time someone presses the button it's usually done.
 * Each solution is good for one request.
 */
export function useCaptcha(baseURL, enabled) {
    const next = useRef(null);
    const solve = () => (async () => {
        const r = await fetch(`${baseURL}/api/auth/altcha/challenge`, { credentials: "include" });
        if (!r.ok)
            throw new Error("Couldn't load the bot check. Check your connection and try again.");
        const challenge = (await r.json());
        const solution = await solveChallenge({ challenge, deriveKey, timeout: 60_000 });
        if (!solution)
            throw new Error("The bot check took too long. Try again.");
        return btoa(JSON.stringify({ challenge, solution }));
    })();
    useEffect(() => {
        if (enabled && !next.current) {
            next.current = solve();
            next.current.catch(() => {
                next.current = null;
            });
        }
    }, [enabled, baseURL]);
    /** Headers for one protected request; starts solving the next one. */
    return async () => {
        if (!enabled)
            return {};
        const p = next.current ?? solve();
        next.current = solve();
        next.current.catch(() => {
            next.current = null;
        });
        return { "x-captcha-response": await p };
    };
}
/** Turns a Better Auth client error into words for people. */
export function errorText(e, fallback = "Something went wrong. Try again.") {
    const err = e;
    const code = err?.code ?? "";
    const known = {
        INVALID_EMAIL_OR_PASSWORD: "That email and password don't match. Try again, or reset your password.",
        EMAIL_NOT_VERIFIED: "Confirm your email first. We just sent you a new link.",
        USER_ALREADY_EXISTS: "There's already an account with this email. Sign in instead.",
        USER_ALREADY_EXISTS_USE_ANOTHER_EMAIL: "There's already an account with this email. Sign in instead.",
        PASSWORD_TOO_SHORT: "Use at least 8 characters for your password.",
        INVALID_OTP: "That code isn't right. Check the email and try again.",
        OTP_EXPIRED: "That code has expired. Ask for a new one.",
        TOO_MANY_ATTEMPTS: "Too many tries. Ask for a new code.",
        INVALID_CODE: "That code isn't right. Try the current one from your app.",
        BANNED: "This account is suspended. Contact the app's owner if you think this is a mistake.",
        CAPTCHA_EXPIRED: "The bot check expired. Press the button again.",
    };
    if (known[code])
        return known[code];
    if (err?.status === 429)
        return "Too many tries in a row. Wait a few seconds and try again.";
    return err?.message || fallback;
}
