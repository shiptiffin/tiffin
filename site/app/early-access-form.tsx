"use client";

// The sign-up list form on /start, until sign-up itself opens there: name,
// email and an optional note. Without JavaScript it is a plain form that posts
// to /api/early-access and follows the redirect. With it, the same request
// goes by fetch: errors appear beside their field (and are announced), and
// success replaces the form with what happens next.
import { useEffect, useId, useRef, useState } from "react";
import { FIELDS, HONEYPOT, LIMITS, MESSAGES, type ErrorCode, type Field, type SignupReply } from "@/lib/form";

type Errors = Partial<Record<Field, string>>;
type Problem = { message?: string; errors: Errors };

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/;

export function EarlyAccessForm({ initialError }: { initialError?: ErrorCode }) {
  const id = useId();
  const form = useRef<HTMLFormElement>(null);
  const done = useRef<HTMLHeadingElement>(null);
  const [sending, setSending] = useState(false);
  const [sent, setSent] = useState<{ email: string; name: string } | null>(null);
  const [problem, setProblem] = useState<Problem | null>(
    initialError
      ? initialError === "email"
        ? { errors: { email: MESSAGES.email } }
        : { message: MESSAGES[initialError], errors: {} }
      : null,
  );

  // The browser's own checks serve the no-script form; with script, ours do.
  useEffect(() => {
    if (form.current) form.current.noValidate = true;
  }, []);
  useEffect(() => {
    if (sent) done.current?.focus();
  }, [sent]);

  const fail = (f: HTMLFormElement, p: Problem) => {
    setProblem(p);
    const first = FIELDS.find((k) => p.errors[k]);
    if (first) (f.elements.namedItem(first) as HTMLElement | null)?.focus();
  };

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (sending) return;
    const f = e.currentTarget;
    const data = new FormData(f);
    const email = String(data.get("email") ?? "").trim();
    if (!email) return fail(f, { errors: { email: "Enter your email address." } });
    if (!EMAIL_RE.test(email)) return fail(f, { errors: { email: MESSAGES.email } });
    setProblem(null);
    setSending(true);
    try {
      const r = await fetch(f.action, { method: "POST", body: data, headers: { accept: "application/json" } });
      const reply = (await r.json().catch(() => null)) as SignupReply | null;
      if (reply?.ok) return setSent({ email, name: String(data.get("name") ?? "").trim() });
      if (reply && !reply.ok) {
        const errors = reply.errors ?? {};
        return fail(f, { message: Object.keys(errors).length ? undefined : reply.message, errors });
      }
      setProblem({ message: MESSAGES.unavailable, errors: {} });
    } catch {
      setProblem({ message: "We couldn't reach the server. Check your connection and try again.", errors: {} });
    } finally {
      setSending(false);
    }
  }

  if (sent)
    return (
      <div className="ea-done" role="status">
        <h2 ref={done} tabIndex={-1} className="ea-done-title">
          {sent.name ? `Thanks, ${sent.name.split(" ")[0]}. Now check your inbox.` : "Thanks. Now check your inbox."}
        </h2>
        <p>
          We sent a link to <strong>{sent.email}</strong>. Click it to confirm your address, and we&rsquo;ll send
          your sign-up link as soon as sign-up opens. Nothing there? Look in spam, or write to{" "}
          <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
        </p>
      </div>
    );

  const err = (f: Field) => problem?.errors[f];
  const described = (f: Field) => (err(f) ? `${id}-${f}-err` : undefined);
  const errLine = (f: Field) =>
    err(f) ? (
      <p id={`${id}-${f}-err`} className="field-err">
        {err(f)}
      </p>
    ) : null;
  const formError = problem?.message ?? (problem && Object.values(problem.errors).find(Boolean));

  return (
    <form
      ref={form}
      className="ea-form"
      action="/api/early-access"
      method="post"
      onSubmit={onSubmit}
      onInput={(e) => {
        const name = (e.target as HTMLInputElement).name as Field;
        if (problem?.errors[name]) setProblem({ ...problem, errors: { ...problem.errors, [name]: undefined } });
      }}
    >
      <div className="field-row">
        <div className="field">
          <label htmlFor={`${id}-name`}>
            Name <span className="opt">optional</span>
          </label>
          <input id={`${id}-name`} name="name" type="text" autoComplete="name" maxLength={LIMITS.name}
            aria-invalid={err("name") ? true : undefined} aria-describedby={described("name")} />
          {errLine("name")}
        </div>
        <div className="field">
          <label htmlFor={`${id}-email`}>Email</label>
          <input id={`${id}-email`} name="email" type="email" required autoComplete="email" inputMode="email"
            spellCheck={false} maxLength={LIMITS.email} aria-invalid={err("email") ? true : undefined}
            aria-describedby={described("email")} />
          {errLine("email")}
        </div>
      </div>
      <div className="field">
        <label htmlFor={`${id}-note`}>
          Anything we should know? <span className="opt">optional</span>
        </label>
        <textarea id={`${id}-note`} name="note" rows={3} maxLength={LIMITS.note}
          placeholder="What you'd run first, or a question"
          aria-invalid={err("note") ? true : undefined} aria-describedby={described("note")} />
        {errLine("note")}
      </div>

      {/* Only bots fill this in. */}
      <div className="hp" aria-hidden="true">
        <label htmlFor={`${id}-hp`}>Leave this empty</label>
        <input id={`${id}-hp`} name={HONEYPOT} type="text" tabIndex={-1} autoComplete="off" />
      </div>

      <div className="ea-submit">
        <button className="btn btn-primary" type="submit" aria-disabled={sending || undefined}>
          {sending ? "Sending…" : "Send me the link"}
        </button>
        <p className="ea-fine">
          One email to confirm, then your sign-up link. Remove yourself any time.{" "}
          <a href="/privacy#signup-list">Privacy</a>
        </p>
      </div>
      <p className={problem && !problem.message ? "form-err sr-only" : "form-err"} role="alert">
        {formError}
      </p>
    </form>
  );
}
