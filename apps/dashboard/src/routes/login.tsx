import { useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError, api } from "@/api/client";
import { Command } from "@/components/copy";
import { TiffinMark } from "@/components/logo";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

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

export function LoginPage({ reason }: { reason?: string }) {
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
          setTimeout(() => navigate({ to: "/", search: {} }), 650);
        })
        .catch((e) => setState(e instanceof ApiError && e.status !== 0 ? "bad-link" : "offline"));
      return;
    }
    if (spent) return;
    api
      .whoami()
      .then(() => setState(reason ? "no-code" : "already"))
      .catch(() => setState("no-code"));
  }, [code, navigate, qc, reason]);

  const headline: Record<State, string> = {
    checking: "One moment…",
    "signing-in": "Opening your box…",
    success: "You're in.",
    "no-code":
      reason === "session"
        ? "Your session has ended."
        : reason === "signed-out"
          ? "Signed out. See you soon."
          : "Sign in with a link from your terminal.",
    "bad-link": "That link has been used, or it expired.",
    already: "You're already signed in.",
    offline: "Can't reach the box.",
  };

  const showCommand = state === "no-code" || state === "bad-link";

  return (
    <div className="grain flex min-h-dvh flex-col bg-paper px-5">
      <main className="mx-auto flex w-full max-w-[26rem] flex-1 flex-col justify-center py-16">
        <div
          className={cn(
            "mb-10 grid size-14 place-items-center rounded-2xl border border-rule bg-raised shadow-pop transition-transform duration-500",
            state === "success" && "scale-105",
          )}
        >
          <TiffinMark className="size-8 text-brass" lid={state === "signing-in" || state === "success"} />
        </div>
        <h1 key={state} className="display animate-rise text-[2rem] leading-[2.5rem] text-ink" aria-live="polite">
          {headline[state]}
        </h1>

        {showCommand && (
          <div className="animate-rise" style={{ animationDelay: "80ms" }}>
            <p className="mt-3 text-md text-ink-2">
              {state === "bad-link"
                ? "Login links work once, for ten minutes. Get a fresh one:"
                : "Run this where Tiffin is installed. It prints a link that works once, for ten minutes."}
            </p>
            <Command cmd="tiffin login" className="mt-5" />
            <p className="mt-4 text-sm text-ink-3">
              Pointing at a remote box? Set <code className="font-mono text-ink-2">TIFFIN_URL</code> and an owner{" "}
              <code className="font-mono text-ink-2">TIFFIN_TOKEN</code> first.
            </p>
          </div>
        )}
        {state === "already" && (
          <Button asChild variant="primary" size="lg" className="mt-6 self-start">
            <Link to="/" search={{}}>
              Go to activity <ArrowRight />
            </Link>
          </Button>
        )}
        {state === "offline" && (
          <p className="mt-3 text-md text-ink-2">
            Check that <code className="font-mono">tiffin serve</code> is running, then reload this page.
          </p>
        )}
      </main>
      <footer className="mx-auto w-full max-w-[26rem] pb-8 text-sm text-ink-4">Tiffin is young and made with care. Your box, your data.</footer>
    </div>
  );
}
