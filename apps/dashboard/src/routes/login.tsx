import { takeLoginCode } from "@/lib/login-code";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError, api } from "@/api/client";
import { Command } from "@/components/copy";
import { Wordmark } from "@/components/logo";
import { Mascot } from "@/components/mascot";
import { Button } from "@/components/ui/button";
import { ProblemNote } from "@/components/problem";
import { cn } from "@/lib/cn";
import { getAssertion, passkeyWords, webauthnSupported } from "@/lib/webauthn";
import { Fingerprint } from "lucide-react";

type State = "checking" | "signing-in" | "success" | "no-code" | "bad-link" | "already" | "offline";

// The code is one-time: make sure we only ever spend it once, even if React
// mounts this page twice in development.
let spent: string | null = null;

const readCode = takeLoginCode;

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
  const words = passkeyWords();
  // With a passkey on offer, the terminal link is the small fallback (open at once after a bad link).
  const [linkOpen, setLinkOpen] = useState(false);
  if (state === "bad-link" && !linkOpen) setLinkOpen(true);
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
    <div className="grid min-h-dvh bg-paper md:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
      {/* The object on its own plate, the form on the page: the line mark leads the form, the drawing never sits beside it. */}
      <aside
        aria-hidden
        className="relative flex items-center justify-center overflow-hidden border-rule bg-paper-sunk max-md:h-[13.5rem] max-md:border-b md:border-r"
      >
        <figure className="m-0 flex flex-col items-center md:-mt-6">
          {/* The mascot waits; once the link is good it lights up (steam), cross-fading in place. */}
          <Mascot state={opening ? "live" : "base"} className="size-[168px] md:size-[min(26rem,36vw)]" />
          <figcaption className="mt-5 text-center text-[0.8125rem] leading-5 text-ink-3 max-md:hidden">
            Apps, Postgres, files, mail, jobs and sign-in,
            <br />
            stacked in one machine you own.
          </figcaption>
        </figure>
      </aside>
      <div className="flex min-w-0 flex-col px-5 sm:px-12 lg:px-16">
        <header className="pt-6 md:pt-10">
          <Wordmark />
        </header>
        <main className="flex w-full max-w-[25rem] flex-1 flex-col justify-start pt-8 pb-10 md:justify-center md:py-12">
          <h1 key={state} className="sentence animate-rise text-ink" aria-live="polite">
            {headline[state]}
          </h1>

          {showCommand && (
            <div className="animate-rise" style={{ animationDelay: "60ms" }}>
              {canPasskey && (
                <>
                  <p className="mt-2.5 text-md text-ink-2">
                    {state === "bad-link" ? `Use ${words.how} instead, or get a fresh link.` : `Use ${words.how}, if you’ve set it up on this box.`}
                  </p>
                  <Button variant="primary" size="lg" className="mt-5" onClick={passkey} disabled={passkeyBusy}>
                    <Fingerprint />
                    {passkeyBusy ? `Waiting for ${words.button}…` : `Sign in with ${words.button}`}
                  </Button>
                  {!!passkeyErr && <ProblemNote className="mt-4" error={passkeyErr} />}
                  {!linkOpen && (
                    <button type="button" onClick={() => setLinkOpen(true)} className="mt-4 block text-[0.875rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                      or use a sign-in link
                    </button>
                  )}
                </>
              )}
              {(!canPasskey || linkOpen) && (
                <>
                  <p className={cn("text-md text-ink-2", canPasskey ? "mt-8 text-[0.875rem]" : "mt-2.5")}>
                    {state === "bad-link"
                      ? "Sign-in links work once, for ten minutes. Get a fresh one where Tiffin is installed:"
                      : "Run this where Tiffin is installed. It prints a link that signs you in once, within ten minutes."}
                  </p>
                  <Command cmd="tiffin login" className={canPasskey ? "mt-3" : "mt-5"} />
                  <p className="mt-4 text-sm text-ink-3">
                    Signing in to a box on another machine? Set <code className="ident text-ink-2">TIFFIN_URL</code> and an owner{" "}
                    <code className="ident text-ink-2">TIFFIN_TOKEN</code> first, or ask its owner to invite you.
                  </p>
                </>
              )}
            </div>
          )}
          {state === "already" && (
            <Button asChild variant="primary" size="lg" className="mt-6 self-start">
              <Link to="/" search={{}}>
                Open your projects <ArrowRight />
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
        <footer className="max-w-[25rem] pb-8 text-sm text-ink-3">Your whole app in one box. Your box, your data.</footer>
      </div>
    </div>
  );
}
