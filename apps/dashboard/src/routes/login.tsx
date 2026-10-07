import { takeLoginCode } from "@/lib/login-code";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError, api } from "@/api/client";
import { boxMailQ } from "@/api/modules";
import { Command } from "@/components/copy";
import { Wordmark } from "@/components/logo";
import { Mascot } from "@/components/mascot";
import { Button } from "@/components/ui/button";
import { ProblemNote } from "@/components/problem";
import { EmailSignIn } from "@/components/signin-email";
import { oauthRefusal, oauthSignInQ, ProviderButtons } from "@/components/signin-oauth";
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
  // With a mail service connected, the box can email people a sign-in link.
  const byEmail = useQuery({ ...boxMailQ.signIn, enabled: state === "no-code" || state === "bad-link" }).data?.available ?? false;
  // With the box-wide Google or GitHub keys set, people sign in with the account that has their email.
  const providers = useQuery({ ...oauthSignInQ, enabled: state === "no-code" || state === "bad-link" }).data ?? [];
  const refusal = oauthRefusal(reason);
  const providerList = providers.map((p) => p.name).join(" or ");
  // What counts as a fresh sign-in for sudo mode, besides the owner's own `tiffin login`.
  const strong = [canPasskey && words.name, ...providers.map((p) => p.name), byEmail && "an emailed link"].filter(Boolean);
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
      reason === "confirm"
        ? "Sign in again to confirm it’s you."
        : reason === "session"
        ? "Your session has ended."
        : reason === "signed-out"
          ? "Signed out. See you soon."
          : canPasskey || byEmail || providers.length > 0
            ? "Sign in to your box."
            : "Sign in with a link from your terminal.",
    "bad-link": "That link has been used, or it expired.",
    already: "You're already signed in.",
    offline: "Can't reach the box.",
  };

  const showCommand = state === "no-code" || state === "bad-link";
  const opening = state === "signing-in" || state === "success";

  return (
    <div className="grid min-h-dvh bg-paper max-md:grid-rows-[auto_1fr] md:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
      {/* The mascot on its own plate, the form on the page: the wordmark over the form is the name alone, so the tin shows once. */}
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
          <Wordmark bare />
        </header>
        <main className="flex w-full max-w-[25rem] flex-1 flex-col justify-start pt-8 pb-10 md:justify-center md:py-12">
          <h1 key={state} className="sentence animate-rise text-ink" aria-live="polite">
            {headline[state]}
          </h1>

          {showCommand && (
            <div className="animate-rise" style={{ animationDelay: "60ms" }}>
              {reason === "confirm" && (
                <p className="mt-2.5 text-md text-ink-2">
                  Adding a passkey, changing an email address or making an API key that outlives your session needs a sign-in from the last 10 minutes with{" "}
                  {strong.length > 0 ? (
                    <>
                      {strong.join(", ")}, or, if you’re the owner, a fresh <code className="ident text-ink">tiffin login</code>.
                    </>
                  ) : (
                    <>
                      a fresh <code className="ident text-ink">tiffin login</code> (the owner’s own).
                    </>
                  )}{" "}
                  A link someone else made doesn’t count.
                </p>
              )}
              {reason !== "confirm" && (canPasskey || providers.length > 0) && (
                <p className="mt-2.5 text-md text-ink-2">
                  {state === "bad-link"
                    ? `Use ${canPasskey ? words.how : providerList} instead, or get a fresh link.`
                    : canPasskey && providers.length > 0
                      ? `Use ${words.how}, or ${providerList} with your email on this box.`
                      : canPasskey
                        ? `Use ${words.how}, if you’ve set it up on this box.`
                        : `Use ${providerList} with your email on this box.`}
                </p>
              )}
              {refusal &&
                (refusal.tone === "error" ? (
                  <ProblemNote className="mt-5" error={new Error(refusal.text)} />
                ) : (
                  <p className="mt-5 text-[0.875rem] text-ink-3" role="status">
                    {refusal.text}
                  </p>
                ))}
              {canPasskey && (
                <>
                  <Button variant="primary" size="lg" className={providers.length > 0 ? "mt-5 w-full" : "mt-5"} onClick={passkey} disabled={passkeyBusy}>
                    <Fingerprint />
                    {passkeyBusy ? `Waiting for ${words.button}…` : `Sign in with ${words.button}`}
                  </Button>
                  {!!passkeyErr && <ProblemNote className="mt-4" error={passkeyErr} />}
                </>
              )}
              <ProviderButtons providers={providers} next={next} className={canPasskey ? "mt-2" : "mt-5"} />
              {byEmail && (
                <EmailSignIn
                  primary={!canPasskey && providers.length === 0}
                  className={canPasskey || providers.length > 0 ? "mt-7 border-t border-rule pt-6" : "mt-2.5"}
                />
              )}
              {(canPasskey || byEmail || providers.length > 0) && !linkOpen && (
                <button type="button" onClick={() => setLinkOpen(true)} className={cn("block text-[0.875rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink", byEmail ? "mt-6" : "mt-4")}>
                  {reason === "confirm" ? "or, as the owner, sign in from your terminal" : byEmail ? "or sign in from your terminal" : "or use a sign-in link"}
                </button>
              )}
              {((!canPasskey && !byEmail && providers.length === 0) || linkOpen) && (
                <>
                  <p className={cn("text-md text-ink-2", canPasskey || byEmail || providers.length > 0 ? "mt-8 text-[0.875rem]" : "mt-2.5")}>
                    {state === "bad-link"
                      ? "Sign-in links work once, for ten minutes. Get a fresh one where Tiffin is installed:"
                      : "Run this where Tiffin is installed. It prints a link that signs you in once, within ten minutes."}
                  </p>
                  <Command cmd="tiffin login" className={canPasskey || byEmail || providers.length > 0 ? "mt-3" : "mt-5"} />
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
