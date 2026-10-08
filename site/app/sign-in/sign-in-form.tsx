"use client";
// Sign-in for shiptiffin.com accounts: a link by email, or Google or GitHub.
// No passwords. The box's own sign-in (Better Auth) serves /api/auth on this
// site; the bot check comes from the SDK.
import { useEffect, useRef, useState } from "react";
import { authConfig, prepareCaptcha } from "@shiptiffin/sdk/client";

type Social = { id: "google" | "github"; name: string };

export function SignInForm({ next }: { next: string }) {
  const captcha = useRef<null | (() => Promise<Record<string, string>>)>(null);
  const [social, setSocial] = useState<Social[]>([]);
  const [email, setEmail] = useState("");
  const [state, setState] = useState<"idle" | "sending" | "sent">("idle");
  const [error, setError] = useState("");

  useEffect(() => {
    captcha.current = prepareCaptcha();
    authConfig()
      .then((c) => {
        const out: Social[] = [];
        if (c.social.google?.configured) out.push({ id: "google", name: "Google" });
        if (c.social.github?.configured) out.push({ id: "github", name: "GitHub" });
        setSocial(out);
      })
      .catch(() => {});
  }, []);

  async function post(path: string, body: unknown) {
    const headers = { "Content-Type": "application/json", ...((await captcha.current?.()) ?? {}) };
    const res = await fetch(path, { method: "POST", headers, body: JSON.stringify(body), credentials: "same-origin" });
    const json = (await res.json().catch(() => ({}))) as { url?: string; message?: string; code?: string };
    if (!res.ok) throw new Error(json.message || "That didn't work. Try again in a minute.");
    return json;
  }

  async function sendLink(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setState("sending");
    try {
      await post("/api/auth/sign-in/magic-link", { email: email.trim(), callbackURL: next });
      setState("sent");
    } catch (err) {
      setError(err instanceof Error ? err.message : "That didn't work.");
      setState("idle");
    }
  }

  async function withProvider(p: Social["id"]) {
    setError("");
    try {
      const r = await post("/api/auth/sign-in/social", { provider: p, callbackURL: next });
      if (r.url) window.location.href = r.url;
    } catch (err) {
      setError(err instanceof Error ? err.message : "That didn't work.");
    }
  }

  if (state === "sent") {
    return (
      <div className="cp-card" role="status">
        <h2>Check your inbox</h2>
        <p className="cp-sub">
          We sent a sign-in link to <strong>{email}</strong>. It works once, for a few minutes. Nothing there? Look in spam, or{" "}
          <button className="cp-link" onClick={() => setState("idle")}>
            try again
          </button>
          .
        </p>
      </div>
    );
  }

  return (
    <div className="cp-card">
      {social.length > 0 && (
        <div className="cp-row">
          {social.map((s) => (
            <button key={s.id} type="button" className="btn btn-quiet" onClick={() => withProvider(s.id)}>
              Continue with {s.name}
            </button>
          ))}
        </div>
      )}
      <form onSubmit={sendLink} className="cp-field" noValidate>
        <label htmlFor="email">{social.length > 0 ? "Or get a link by email" : "Your email"}</label>
        <input id="email" type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
        <div className="cp-row">
          <button className="btn btn-primary" disabled={state === "sending" || !email.includes("@")}>
            {state === "sending" ? "Sending…" : "Email me a sign-in link"}
          </button>
        </div>
      </form>
      {error && (
        <p className="cp-err" role="alert">
          {error}
        </p>
      )}
      <p className="cp-hint">No password to remember. New here? The same link makes your account.</p>
    </div>
  );
}
