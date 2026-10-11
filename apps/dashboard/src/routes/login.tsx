import { takeLoginCode } from "@/lib/login-code";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError, api } from "@/api/client";
import { boxMailQ } from "@/api/modules";
import { access, aq } from "@/api/access";
import { afterLinkSignIn } from "@/lib/recovery";
import { Command } from "@/components/copy";
import { AuthFrame } from "@/components/auth-frame";
import { Button } from "@/components/ui/button";
import { ProblemNote } from "@/components/problem";
import { EmailSignIn } from "@/components/signin-email";
import { oauthRefusal, oauthSignInQ, ProviderButtons } from "@/components/signin-oauth";
import { cn } from "@/lib/cn";
import { getAssertion, passkeyWords, webauthnSupported } from "@/lib/webauthn";
import { Fingerprint } from "lucide-react";

type State = "checking" | "signing-in" | "success" | "no-code" | "bad-link" | "already" | "offline";

// The code is one-time: spend it once, even if React mounts this page twice
// in development; a second mount for the same code waits on the same exchange.
let exchange: { code: string; done: Promise<unknown> } | null = null;

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
    let live = true;
    if (code) {
      if (exchange?.code !== code) exchange = { code, done: api.login(code) };
      exchange.done
        .then(async () => {
          if (!live) return;
          qc.clear();
          setState("success");
          // Someone with no passkey yet gets the step that offers one first (not mid-task, when confirming it's them).
          const [recovery] = await Promise.all([
            reason === "confirm" ? undefined : access.recovery().catch(() => undefined),
            new Promise((r) => setTimeout(r, 650)),
          ]);
          if (live) void navigate({ href: afterLinkSignIn(recovery, webauthnSupported(), next) });
        })
        .catch((e) => {
          if (!live) return;
          if (!(e instanceof ApiError) || e.status === 0) return setState("offline");
          // A used link from a browser that's already signed in (Open dashboard again): just go in.
          api
            .whoami()
            .then(() => {
              if (live) void navigate({ href: next ?? "/", replace: true });
            })
            .catch(() => live && setState("bad-link"));
        });
      return () => {
        live = false;
      };
    }
    // No code (a later visit, e.g. "Sign in again" to confirm it's you).
    api
      .whoami()
      // Already signed in and nothing to confirm: go straight to the dashboard.
      .then(() => {
        if (!live) return;
        if (reason) setState("no-code");
        else void navigate({ href: next ?? "/", replace: true });
      })
      .catch(() => live && setState("no-code"));
    return () => {
      live = false;
    };
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
  // A box ShipTiffin set up has no "computer that ran tiffin up": the way back is the owner's SSH key, or Hetzner's console.
  const box = useQuery({ ...aq.box, enabled: state === "no-code" || state === "bad-link" }).data;
  const host = box?.managed ? box.host : "";
  const managed = !!host;
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
          : canPasskey || byEmail || providers.length > 0 || managed
            ? "Sign in to your box."
            : "Sign in with a link from your terminal.",
    "bad-link": "That link has been used, or it expired.",
    already: "You're already signed in.",
    offline: "Can't reach the box.",
  };

  const showCommand = state === "no-code" || state === "bad-link";
  const opening = state === "signing-in" || state === "success";

  return (
    <AuthFrame lit={opening}>
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
            <button
              type="button"
              onClick={() => setLinkOpen(true)}
              className={cn("block text-[0.875rem] underline decoration-rule-3 underline-offset-4 hover:text-ink", managed ? "text-ink-2" : "text-ink-3", byEmail ? "mt-6" : "mt-4")}
            >
              {managed
                ? reason === "confirm"
                  ? "or, as the owner, use your SSH key"
                  : "Can’t sign in?"
                : reason === "confirm"
                  ? "or, as the owner, sign in from your terminal"
                  : byEmail
                    ? "or sign in from your terminal"
                    : "or use a sign-in link"}
            </button>
          )}
          {managed && ((!canPasskey && !byEmail && providers.length === 0) || linkOpen) && (
            <ServerSignIn host={host} bad={state === "bad-link"} className={canPasskey || byEmail || providers.length > 0 ? "mt-8" : "mt-6"} />
          )}
          {!managed && ((!canPasskey && !byEmail && providers.length === 0) || linkOpen) && (
            <>
              <p className={cn("text-md text-ink-2", canPasskey || byEmail || providers.length > 0 ? "mt-8 text-[0.875rem]" : "mt-2.5")}>
                {state === "bad-link"
                  ? "Sign-in links work once, for ten minutes. Get a fresh one on the computer that ran tiffin up:"
                  : "Run this on the computer that ran tiffin up. It prints a link that signs you in once, within ten minutes."}
              </p>
              <Command cmd="tiffin login" className={canPasskey || byEmail || providers.length > 0 ? "mt-3" : "mt-5"} />
              <p className="mt-4 text-sm text-ink-3">
                On the box’s server instead, as root: <code className="ident text-ink-2">tiffin login --home /var/lib/tiffin/platform</code>. Not the owner? Ask
                an owner or admin to invite you.
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
    </AuthFrame>
  );
}

/**
 * Can't sign in, on a box ShipTiffin set up: the owner's SSH key prints a
 * fresh link from the server; without one, Hetzner's console gets there too.
 */
function ServerSignIn({ host, bad, className }: { host: string; bad: boolean; className?: string }) {
  return (
    <section className={cn("text-[0.875rem]", className)} aria-label="Other ways to sign in">
      <h2 className="font-[550] text-ink">With your SSH key</h2>
      <p className="mt-1 text-ink-2">
        {bad ? "Sign-in links work once, for ten minutes. Run this on your computer for a fresh one, then open it:" : "Run this on your computer, then open the link it prints:"}
      </p>
      <Command cmd={`ssh root@${host} tiffin login`} className="mt-3" wrap />
      <details className="group mt-5 border-t border-rule pt-4">
        <summary className="cursor-pointer list-none font-[550] text-ink select-none hover:text-ink-2 [&::-webkit-details-marker]:hidden">
          <span className="inline-block text-ink-3 transition-transform group-open:rotate-90">›</span> Through Hetzner’s console
        </summary>
        <ol className="mt-3 list-decimal space-y-2 pl-5 text-ink-2 marker:text-ink-3">
          <li>
            In the Hetzner Cloud Console, open your server, then <span className="text-ink">Rescue › Reset root password</span>. Copy the password it shows.
          </li>
          <li>
            Open <span className="text-ink">Console</span> (the <code className="ident text-ink">&gt;_</code> button) and log in as <code className="ident text-ink">root</code> with that
            password.
          </li>
          <li>
            Run <code className="ident text-ink">tiffin login</code>, then open the link it prints.
          </li>
        </ol>
      </details>
      <p className="mt-5 text-sm text-ink-3">Not the owner? Ask an owner or admin to invite you.</p>
    </section>
  );
}
