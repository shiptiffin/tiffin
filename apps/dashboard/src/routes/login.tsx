import { useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError, api } from "@/api/client";
import { Command } from "@/components/copy";
import heroOpen from "@/assets/illustrations/carrier-hero-open.webp";
import heroClosed from "@/assets/illustrations/carrier-hero.webp";
import { Wordmark } from "@/components/logo";
import { Button } from "@/components/ui/button";
import { ProblemNote } from "@/components/problem";
import { cn } from "@/lib/cn";
import { getAssertion, webauthnSupported } from "@/lib/webauthn";
import { KeyRound } from "lucide-react";

type State = "checking" | "signing-in" | "success" | "no-code" | "bad-link" | "already" | "offline";

// The code is one-time: make sure we only ever spend it once, even if React
// mounts this page twice in development.
let spent: string | null = null;

function readCode() {
  const code = decodeURIComponent(location.hash.replace(/^#/, "")).trim();
  // Never leave the code in the address bar or history.
  if (code) history.replaceState(null, "", location.pathname + location.search);
  return code;
}

export function LoginPage({ reason, next }: { reason?: string; next?: string }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [code] = useState(readCode);
  const [state, setState] = useState<State>(code ? "signing-in" : "checking");

  // A second link pasted into the same tab only changes the hash: start over.
  useEffect(() => {
    const onHash = () => location.hash.length > 1 && location.reload();
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    document.title = "Sign in · Tiffin";
    if (code) {
      if (spent === code) return;
      spent = code;
      api
        .login(code)
        .then(() => {
          qc.clear();
          setState("success");
          setTimeout(() => navigate({ href: next ?? "/" }), 650);
        })
        .catch((e) => setState(e instanceof ApiError && e.status !== 0 ? "bad-link" : "offline"));
      return;
    }
    if (spent) return;
    api
      .whoami()
      .then(() => setState(reason ? "no-code" : "already"))
      .catch(() => setState("no-code"));
  }, [code, navigate, qc, reason, next]);

  // Passkey sign-in: options from the box → the OS prompt → the box checks it and sets the session.
  const [passkeyBusy, setPasskeyBusy] = useState(false);
  const [passkeyErr, setPasskeyErr] = useState<unknown>(null);
  const canPasskey = webauthnSupported();
  const passkey = async () => {
    setPasskeyErr(null);
    setPasskeyBusy(true);
    try {
      const credential = await getAssertion(await api.passkeyOptions());
      await api.passkeyLogin(credential);
      qc.clear();
      setState("success");
      setTimeout(() => navigate({ href: next ?? "/" }), 650);
    } catch (e) {
      // Closing the prompt is a choice, not an error.
      if (!(e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError"))) setPasskeyErr(e);
    } finally {
      setPasskeyBusy(false);
    }
  };

  const headline: Record<State, string> = {
    checking: "One moment…",
    "signing-in": "Opening your box…",
    success: "You're in.",
    "no-code":
      reason === "session"
        ? "Your session has ended."
        : reason === "signed-out"
          ? "Signed out. See you soon."
          : canPasskey
            ? "Sign in to your box."
            : "Sign in with a link from your terminal.",
    "bad-link": "That link has been used, or it expired.",
    already: "You're already signed in.",
    offline: "Can't reach the box.",
  };

  const showCommand = state === "no-code" || state === "bad-link";
  const opening = state === "signing-in" || state === "success";

  return (
    <div className="flex min-h-dvh flex-col bg-paper px-5">
      <main className="mx-auto flex w-full max-w-[25rem] flex-1 flex-col justify-center py-12">
        <div className="relative -ml-4 size-[184px] sm:size-[216px]" aria-hidden>
          <img src={heroClosed} alt="" width={216} height={216} className={cn("absolute inset-0 size-full transition-opacity duration-[600ms] ease-[var(--ease-out)]", opening && "opacity-0")} />
          <img src={heroOpen} alt="" width={216} height={216} className={cn("absolute inset-0 size-full opacity-0 transition-opacity duration-[600ms] ease-[var(--ease-out)]", opening && "opacity-100")} />
        </div>
        <Wordmark className="mt-4" />
        <h1 key={state} className="sentence mt-6 animate-rise text-ink" aria-live="polite">
          {headline[state]}
        </h1>

        {showCommand && (
          <div className="animate-rise" style={{ animationDelay: "60ms" }}>
            {canPasskey && (
              <>
                <p className="mt-2.5 text-md text-ink-2">
                  {state === "bad-link"
                    ? "Sign in with your passkey instead, or get a fresh link."
                    : reason === "session" || reason === "signed-out"
                      ? "Sign in again with the passkey you added in Settings, or a one-time link from your terminal."
                      : "With the passkey you added in Settings, or a one-time link from your terminal."}
                </p>
                <Button variant="primary" size="lg" className="mt-5" onClick={passkey} disabled={passkeyBusy}>
                  <KeyRound />
                  {passkeyBusy ? "Waiting for your passkey…" : "Sign in with a passkey"}
                </Button>
                {!!passkeyErr && <ProblemNote className="mt-4" error={passkeyErr} />}
                <p className="mt-8 text-[0.8125rem] font-[550] text-ink-2">Or with a link from your terminal</p>
              </>
            )}
            <p className={cn("text-md text-ink-2", canPasskey ? "mt-1 text-[0.875rem]" : "mt-2.5")}>
              {state === "bad-link"
                ? "Sign-in links work once, for ten minutes. Get a fresh one where Tiffin is installed:"
                : "Run this where Tiffin is installed. It prints a link that signs you in once, within ten minutes."}
            </p>
            <Command cmd="tiffin login" className={canPasskey ? "mt-3" : "mt-5"} />
            <p className="mt-4 text-sm text-ink-3">
              Signing in to a box on another machine? Set <code className="ident text-ink-2">TIFFIN_URL</code> and an owner{" "}
              <code className="ident text-ink-2">TIFFIN_TOKEN</code> first, or ask its owner to invite you.
            </p>
          </div>
        )}
        {state === "already" && (
          <Button asChild variant="primary" size="lg" className="mt-6 self-start">
            <Link to="/" search={{}}>
              Open the Box <ArrowRight />
            </Link>
          </Button>
        )}
        {state === "offline" && (
          <p className="mt-2.5 text-md text-ink-2">
            Check that <code className="ident text-ink">tiffin serve</code> is running, then reload this page.
          </p>
        )}
        {state === "signing-in" && <p className="mt-2.5 text-md text-ink-3">Checking the link with the box.</p>}
      </main>
      <footer className="mx-auto w-full max-w-[25rem] pb-8 text-sm text-ink-3">Your whole app in one box. Your box, your data.</footer>
    </div>
  );
}
