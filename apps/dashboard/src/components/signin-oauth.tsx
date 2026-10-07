import { queryOptions } from "@tanstack/react-query";
import { useState } from "react";
import { request } from "@/api/client";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

// "Sign in with Google" and "Sign in with GitHub" on the login page. The box
// offers a provider once its box-wide keys are set, and signs in the person
// on the box whose email the provider has verified. It never makes anyone.

export type OAuthProvider = { id: "google" | "github"; name: string };

export const oauthSignInQ = queryOptions({
  queryKey: ["session-oauth"],
  queryFn: () => request<{ providers: OAuthProvider[] | null }>("GET", "/v1/session/oauth").then((r) => r.providers ?? []),
  retry: false,
  staleTime: 60_000,
});

const startOAuth = (id: string, next?: string) =>
  request<{ url: string }>("POST", `/v1/session/oauth/${encodeURIComponent(id)}${next ? `?next=${encodeURIComponent(next)}` : ""}`);

/** The provider's own mark: Google's four-colour G (its brand rules ask for it as is), GitHub's in the text colour. */
export function ProviderMark({ id, className }: { id: string; className?: string }) {
  if (id === "google")
    return (
      <svg viewBox="0 0 48 48" aria-hidden className={className}>
        <path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z" />
        <path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z" />
        <path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z" />
        <path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z" />
      </svg>
    );
  return (
    <svg viewBox="0 0 16 16" aria-hidden className={className} fill="currentColor">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
    </svg>
  );
}

/** The buttons, one per provider with keys on the box. */
export function ProviderButtons({ providers, next, className }: { providers: OAuthProvider[]; next?: string; className?: string }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  if (providers.length === 0) return null;
  const go = async (p: OAuthProvider) => {
    setError(null);
    setBusy(p.id);
    try {
      const { url } = await startOAuth(p.id, next);
      window.location.assign(url);
    } catch (e) {
      setError(e);
      setBusy(null);
    }
  };
  return (
    <div className={className}>
      <div className={cn("grid gap-2", providers.length > 1 && "min-[420px]:grid-cols-2")}>
        {providers.map((p) => (
          <Button key={p.id} type="button" size="lg" className="w-full" disabled={busy !== null} onClick={() => go(p)}>
            <ProviderMark id={p.id} className={p.id === "google" ? "size-[17px]!" : "size-[17px]! text-ink"} />
            {busy === p.id ? `Opening ${p.name}…` : `Sign in with ${p.name}`}
          </Button>
        ))}
      </div>
      {!!error && <ProblemNote className="mt-3" error={error} />}
    </div>
  );
}

const names: Record<string, string> = { google: "Google", github: "GitHub" };

/**
 * Why a sign-in with a provider came back to the login page, from
 * ?reason=<provider>:<code>. Null for any other reason.
 */
export function oauthRefusal(reason?: string): { tone: "note" | "error"; text: string } | null {
  const m = /^(google|github):([a-z]+)$/.exec(reason ?? "");
  if (!m) return null;
  const name = names[m[1]];
  switch (m[2]) {
    case "unknown":
      return { tone: "error", text: `That ${name} account isn't on this box. Ask an owner to invite you.` };
    case "unverified":
      return {
        tone: "error",
        text:
          m[1] === "github"
            ? "That GitHub account's primary email isn't verified, so the box can't match it to anyone. Verify it on GitHub, or sign in another way."
            : "Google hasn't verified that account's email, so the box can't match it to anyone. Sign in another way.",
      };
    case "denied":
      return { tone: "note", text: `You cancelled signing in with ${name}.` };
    case "expired":
      return { tone: "error", text: `That sign-in with ${name} took too long or was already used. Try again.` };
    case "busy":
      return { tone: "error", text: "Too many sign-in attempts from your address. Wait a minute, then try again." };
    case "off":
      return { tone: "error", text: `Signing in with ${name} isn't set up on this box any more.` };
    default:
      return { tone: "error", text: `${name} sign-in didn't finish. Try again, or sign in another way.` };
  }
}
