"use client";

// The request-an-invite form, in two steps. Step 1 (name, email, what you
// build) saves the request and sends the confirmation email; step 2, "Help us
// get your box right", is optional and adds to the same request.
//
// Without JavaScript it is one long form that posts everything to
// /api/early-access and follows the redirect. The server renders that form;
// once the script runs it becomes the two steps (html.js hides step 2's
// fields until then, so they don't flash). Errors appear beside their field
// and are announced; the first one gets the focus.
import { useEffect, useId, useRef, useState } from "react";
import {
  AGENT_CHOICES,
  FIELDS,
  HONEYPOT,
  LIMITS,
  LINKS,
  MESSAGES,
  PROJECT_CHOICES,
  ROLE_CHOICES,
  SPEND_CHOICES,
  TOOL_CHOICES,
  type ErrorCode,
  type Field,
  type SignupReply,
} from "@/lib/form";
import { InviteSteps } from "./early-access-next";

type Step = "all" | "one" | "two" | "done";
type Errors = Partial<Record<Field, string>>;
type Problem = { message?: string; errors: Errors };

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/;

export function EarlyAccessForm({ initialError }: { initialError?: ErrorCode }) {
  const id = useId();
  const [step, setStep] = useState<Step>("all");
  const [sending, setSending] = useState(false);
  const [problem, setProblem] = useState<Problem | null>(
    initialError
      ? initialError === "email"
        ? { errors: { email: MESSAGES.email } }
        : { message: MESSAGES[initialError], errors: {} }
      : null,
  );
  const [who, setWho] = useState<{ email: string; name: string; details: string } | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);

  // The script is here: two steps instead of one long form.
  useEffect(() => setStep((s) => (s === "all" ? "one" : s)), []);

  // A new step moves the focus to its heading, so a screen reader hears it.
  useEffect(() => {
    if (step === "two" || step === "done") heading.current?.focus();
  }, [step]);

  const focusFirst = (form: HTMLFormElement, errors: Errors) => {
    const first = FIELDS.find((f) => errors[f]);
    if (first) (form.elements.namedItem(first) as HTMLElement | null)?.focus();
  };

  async function post(form: HTMLFormElement, url: string): Promise<SignupReply | null> {
    setSending(true);
    try {
      const r = await fetch(url, { method: "POST", body: new FormData(form), headers: { accept: "application/json" } });
      return (await r.json().catch(() => null)) as SignupReply | null;
    } catch {
      setProblem({ message: "We couldn't reach the server. Check your connection and try again.", errors: {} });
      return null;
    } finally {
      setSending(false);
    }
  }

  function refused(form: HTMLFormElement, reply: SignupReply | null) {
    if (!reply) return setProblem((p) => p ?? { message: MESSAGES.unavailable, errors: {} });
    if (reply.ok) return;
    const errors = reply.errors ?? {};
    setProblem({ message: Object.keys(errors).length ? undefined : reply.message, errors });
    focusFirst(form, errors);
  }

  async function submitOne(e: React.FormEvent<HTMLFormElement>) {
    if (step === "all") return; // no script yet: let the browser post it
    e.preventDefault();
    if (sending) return;
    const form = e.currentTarget;
    const data = new FormData(form);
    const email = String(data.get("email") ?? "").trim();
    const errors: Errors = {};
    if (!email) errors.email = "Enter your email address.";
    else if (!EMAIL_RE.test(email)) errors.email = MESSAGES.email;
    if (errors.email) {
      setProblem({ errors });
      return focusFirst(form, errors);
    }
    setProblem(null);
    const reply = await post(form, form.action);
    if (reply?.ok) {
      setWho({ email, name: String(data.get("name") ?? "").trim(), details: reply.details ?? "" });
      setStep("two");
    } else refused(form, reply);
  }

  async function submitTwo(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (sending) return;
    const form = e.currentTarget;
    setProblem(null);
    const reply = await post(form, form.action);
    if (reply?.ok) setStep("done");
    else refused(form, reply);
  }

  const clearOnInput = (e: React.FormEvent<HTMLFormElement>) => {
    const name = (e.target as HTMLInputElement).name as Field;
    if (problem?.errors[name]) setProblem({ ...problem, errors: { ...problem.errors, [name]: undefined } });
  };

  /* ---------- pieces ---------- */

  const err = (f: Field) => problem?.errors[f];
  const described = (f: Field, hint?: boolean) => (err(f) ? `${id}-${f}-err` : hint ? `${id}-${f}-hint` : undefined);
  const errLine = (f: Field) =>
    err(f) ? (
      <p id={`${id}-${f}-err`} className="field-err">
        {err(f)}
      </p>
    ) : null;
  const formError = problem?.message ?? (problem && Object.values(problem.errors).find(Boolean));
  const alert = (
    <p className={problem && !problem.message ? "form-err sr-only" : "form-err"} role="alert">
      {formError}
    </p>
  );

  const stepOne = (
    <>
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
      <fieldset className="field">
        <legend>What do you build?</legend>
        <div className="chips chips-role">
          {ROLE_CHOICES.map((c) => (
            <label key={c.value} className="chip chip-radio">
              <input type="radio" name="role" value={c.value} />
              <span>{c.label}</span>
            </label>
          ))}
        </div>
      </fieldset>
    </>
  );

  const stepTwo = (
    <>
      <div className="field">
        <label htmlFor={`${id}-hostFirst`}>What would you host first?</label>
        <input id={`${id}-hostFirst`} name="hostFirst" type="text" autoComplete="off" maxLength={LIMITS.hostFirst}
          placeholder="A Next.js shop and two side projects"
          aria-invalid={err("hostFirst") ? true : undefined} aria-describedby={described("hostFirst")} />
        {errLine("hostFirst")}
      </div>

      <fieldset className="field">
        <legend>How many projects would you bring?</legend>
        <div className="seg" style={{ ["--n" as string]: PROJECT_CHOICES.length }}>
          {PROJECT_CHOICES.map((c) => (
            <label key={c.value} className="seg-opt">
              <input type="radio" name="projects" value={c.value} />
              <span>{c.label}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <fieldset className="field" aria-describedby={`${id}-tools-hint`}>
        <legend>What do you use today?</legend>
        <p className="field-hint" id={`${id}-tools-hint`}>
          Pick any that apply.
        </p>
        <div className="chips chips-4">
          {TOOL_CHOICES.map((c) => (
            <label key={c.value} className="chip">
              <input type="checkbox" name="tools" value={c.value} />
              <span>{c.label}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <fieldset className="field">
        <legend>Roughly what do you spend on hosting a month?</legend>
        <div className="chips chips-spend">
          {SPEND_CHOICES.map((c) => (
            <label key={c.value} className="chip chip-radio">
              <input type="radio" name="spend" value={c.value} />
              <span>{c.label}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <fieldset className="field">
        <legend>Do you use AI coding agents?</legend>
        <div className="chips chips-agents">
          {AGENT_CHOICES.map((c) => (
            <label key={c.value} className="chip">
              <input type="checkbox" name="agents" value={c.value} />
              <span>{c.label}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <fieldset className="field" aria-describedby={`${id}-links-hint`}>
        <legend>Where can we see your work?</legend>
        <p className="field-hint" id={`${id}-links-hint`}>
          Any or none. A handle or a link both work.
        </p>
        <div className="links">
          {LINKS.map((l) => (
            <div key={l.name} className={err(l.name) ? "link link-bad" : "link"}>
              <label htmlFor={`${id}-${l.name}`}>{l.label}</label>
              <input id={`${id}-${l.name}`} name={l.name} type="text" inputMode="url" autoComplete="off"
                autoCapitalize="none" spellCheck={false} maxLength={LIMITS.link} placeholder={l.placeholder}
                aria-invalid={err(l.name) ? true : undefined} aria-describedby={described(l.name)} />
              {errLine(l.name)}
            </div>
          ))}
        </div>
      </fieldset>

      <div className="field">
        <label htmlFor={`${id}-note`}>
          Anything else? <span className="opt">optional</span>
        </label>
        <textarea id={`${id}-note`} name="note" rows={3} maxLength={LIMITS.note}
          aria-invalid={err("note") ? true : undefined} aria-describedby={described("note")} />
        {errLine("note")}
      </div>
    </>
  );

  const honeypot = (
    <div className="hp" aria-hidden="true">
      <label htmlFor={`${id}-hp`}>Leave this empty</label>
      <input id={`${id}-hp`} name={HONEYPOT} type="text" tabIndex={-1} autoComplete="off" />
    </div>
  );

  const fine = (
    <p className="ea-fine">
      One email to confirm, then nothing until your invite. Remove yourself any time.{" "}
      <a href="/privacy#invites">Privacy</a>
    </p>
  );

  /* ---------- the steps ---------- */

  if (step === "done" && who)
    return (
      <div className="ea-done">
        <p className="ea-step">Request saved</p>
        <h3 ref={heading} tabIndex={-1} className="ea-done-title">
          {who.name ? `Thanks, ${who.name.split(" ")[0]}. Now check your inbox.` : "Thanks. Now check your inbox."}
        </h3>
        <p>
          We sent a link to <strong>{who.email}</strong>. Click it to confirm your request; it can take a minute.
          Nothing there? Look in spam, or write to <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
        </p>
        <InviteSteps compact />
      </div>
    );

  if (step === "two" && who)
    return (
      <form className="ea-form" action="/api/early-access/details" method="post" onSubmit={submitTwo} onInput={clearOnInput} noValidate>
        <div className="ea-saved" role="status">
          <span className="ea-tick" aria-hidden="true" />
          <span>
            Request saved. We sent a confirmation link to <strong>{who.email}</strong>.
          </span>
        </div>
        <p className="ea-step">Step 2 of 2 · optional</p>
        <h3 ref={heading} tabIndex={-1} className="ea-step-title">
          Help us get your box right.
        </h3>
        <p className="ea-step-sub">A minute of answers helps us size your box and decide who to invite next.</p>
        <input type="hidden" name="d" value={who.details} />
        {stepTwo}
        <div className="ea-submit ea-submit-two">
          <button className="btn btn-primary" type="submit" aria-disabled={sending || undefined}>
            {sending ? "Saving…" : "Save my answers"}
          </button>
          <button className="btn btn-quiet" type="button" onClick={() => setStep("done")}>
            Skip
          </button>
        </div>
        {alert}
      </form>
    );

  return (
    <form className="ea-form" action="/api/early-access" method="post" onSubmit={submitOne} onInput={clearOnInput}
      noValidate={step !== "all"} data-step={step}>
      {step === "one" && <p className="ea-step">Step 1 of 2</p>}
      {stepOne}
      {step === "all" && (
        <div className="ea-more">
          <h3 className="ea-more-title">
            Help us get your box right <span className="opt">optional</span>
          </h3>
          {stepTwo}
        </div>
      )}
      {honeypot}
      <div className="ea-submit">
        <button className="btn btn-primary" type="submit" aria-disabled={sending || undefined}>
          {sending ? "Sending…" : "Request an invite"}
        </button>
        {fine}
      </div>
      {alert}
    </form>
  );
}
