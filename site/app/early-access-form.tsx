"use client";

// The early-access form. Without JavaScript it is a plain form that posts to
// /api/early-access and follows the redirect. With it, the same request goes
// by fetch: errors appear beside the field they belong to (and are announced),
// and success replaces the form with what happens next.
import { useEffect, useId, useRef, useState } from "react";
import { HONEYPOT, HOST_CHOICES, LIMITS, MESSAGES, PROJECT_CHOICES, type ErrorCode, type SignupReply } from "@/lib/form";

type State =
  | { kind: "idle" }
  | { kind: "sending" }
  | { kind: "sent"; email: string }
  | { kind: "error"; message: string; field?: "email" | "hosting" | "note"; fieldMessage?: string };

export function EarlyAccessForm({ initialError }: { initialError?: ErrorCode }) {
  const id = useId();
  const form = useRef<HTMLFormElement>(null);
  const done = useRef<HTMLHeadingElement>(null);
  const [state, setState] = useState<State>(
    initialError
      ? initialError === "email"
        ? { kind: "error", message: "", field: "email", fieldMessage: MESSAGES.email }
        : { kind: "error", message: MESSAGES[initialError] }
      : { kind: "idle" },
  );

  // The browser's own checks serve the no-script form; with script, ours do.
  useEffect(() => {
    if (form.current) form.current.noValidate = true;
  }, []);

  useEffect(() => {
    if (state.kind === "sent") done.current?.focus();
  }, [state.kind]);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (state.kind === "sending") return;
    const f = e.currentTarget;
    const data = new FormData(f);
    const email = String(data.get("email") ?? "").trim();
    const fieldError = (field: "email" | "hosting" | "note", fieldMessage: string) => {
      setState({ kind: "error", message: "", field, fieldMessage });
      (f.elements.namedItem(field) as HTMLElement | null)?.focus();
    };
    if (!email) return fieldError("email", "Enter your email address.");
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(email)) return fieldError("email", MESSAGES.email);

    setState({ kind: "sending" });
    try {
      const r = await fetch(f.action, { method: "POST", body: data, headers: { accept: "application/json" } });
      const reply = (await r.json().catch(() => null)) as SignupReply | null;
      if (reply?.ok) return setState({ kind: "sent", email });
      if (reply && !reply.ok) {
        const field = (["email", "hosting", "note"] as const).find((k) => reply.errors?.[k]);
        if (field) return fieldError(field, reply.errors![field]!);
        return setState({ kind: "error", message: reply.message });
      }
      setState({ kind: "error", message: MESSAGES.unavailable });
    } catch {
      setState({ kind: "error", message: "We couldn't reach the server. Check your connection and try again." });
    }
  }

  if (state.kind === "sent") {
    return (
      <div className="ea-done" role="status">
        <h3 ref={done} tabIndex={-1} className="ea-done-title">
          Check your inbox.
        </h3>
        <p>
          We sent a link to <strong>{state.email}</strong>. Click it to confirm your place on the list. It can take a
          minute; if it doesn&rsquo;t arrive, look in spam, or write to{" "}
          <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
        </p>
      </div>
    );
  }

  const err = (field: "email" | "hosting" | "note") =>
    state.kind === "error" && state.field === field ? state.fieldMessage : undefined;
  const emailErr = err("email");
  const hostingErr = err("hosting");
  const noteErr = err("note");
  const formErr = state.kind === "error" ? state.message || state.fieldMessage : undefined;
  const fieldOnly = state.kind === "error" && !state.message;
  const sending = state.kind === "sending";

  return (
    <form ref={form} className="ea-form" action="/api/early-access" method="post" onSubmit={onSubmit}
      onInput={(e) => {
        // A field's error goes once its value changes.
        if (state.kind === "error" && state.field && (e.target as HTMLInputElement).name === state.field) setState({ kind: "idle" });
      }}
      aria-describedby={`${id}-req`}>
      <p id={`${id}-req`} className="ea-req">
        Only your email is required.
      </p>

      <div className="field">
        <label htmlFor={`${id}-email`}>Email</label>
        <input
          id={`${id}-email`}
          name="email"
          type="email"
          required
          autoComplete="email"
          inputMode="email"
          spellCheck={false}
          maxLength={LIMITS.email}
          aria-invalid={emailErr ? true : undefined}
          aria-describedby={emailErr ? `${id}-email-err` : undefined}
        />
        {emailErr && (
          <p id={`${id}-email-err`} className="field-err">
            {emailErr}
          </p>
        )}
      </div>

      <div className="field">
        <label htmlFor={`${id}-hosting`}>What would you host?</label>
        <input
          id={`${id}-hosting`}
          name="hosting"
          type="text"
          autoComplete="off"
          maxLength={LIMITS.hosting}
          aria-describedby={hostingErr ? `${id}-hosting-err` : `${id}-hosting-hint`}
          aria-invalid={hostingErr ? true : undefined}
        />
        {hostingErr ? (
          <p id={`${id}-hosting-err`} className="field-err">
            {hostingErr}
          </p>
        ) : (
          <p id={`${id}-hosting-hint`} className="field-hint">
            A few words is plenty: &ldquo;a Next.js shop and two side projects&rdquo;.
          </p>
        )}
      </div>

      <fieldset className="field">
        <legend>How many projects?</legend>
        <div className="seg">
          {PROJECT_CHOICES.map((c) => (
            <label key={c.value} className="seg-opt">
              <input type="radio" name="projects" value={c.value} />
              <span>{c.label}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <fieldset className="field" aria-describedby={`${id}-current-hint`}>
        <legend>What do you use today?</legend>
        <p className="field-hint" id={`${id}-current-hint`}>
          Pick any that apply.
        </p>
        <div className="chips">
          {HOST_CHOICES.map((c) => (
            <label key={c.value} className="chip">
              <input type="checkbox" name="current" value={c.value} />
              <span>{c.label}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <div className="field">
        <label htmlFor={`${id}-note`}>Anything else we should know?</label>
        <textarea
          id={`${id}-note`}
          name="note"
          rows={3}
          maxLength={LIMITS.note}
          aria-invalid={noteErr ? true : undefined}
          aria-describedby={noteErr ? `${id}-note-err` : undefined}
        />
        {noteErr && (
          <p id={`${id}-note-err`} className="field-err">
            {noteErr}
          </p>
        )}
      </div>

      {/* Only bots fill this in. */}
      <div className="hp" aria-hidden="true">
        <label htmlFor={`${id}-hp`}>Leave this empty</label>
        <input id={`${id}-hp`} name={HONEYPOT} type="text" tabIndex={-1} autoComplete="off" />
      </div>

      <div className="ea-submit">
        <button className="btn btn-primary" type="submit" aria-disabled={sending || undefined}>
          {sending ? "Sending…" : "Join the early-access list"}
        </button>
        <p className="ea-fine">
          One email to confirm, then nothing until your invite. Remove yourself any time. See our{" "}
          <a href="/privacy#early-access">privacy policy</a>.
        </p>
      </div>

      <p className={fieldOnly ? "form-err sr-only" : "form-err"} role="alert">
        {formErr}
      </p>
    </form>
  );
}
