// The early-access list: what a sign-up holds, how a form is read, and what
// happens when someone signs up, confirms or asks to be removed. Storage and
// mail are passed in (lib/early-access-pg.ts and lib/early-access-box.ts on the box,
// fakes in the tests), so this file has no I/O of its own.
import { createHash, randomBytes } from "node:crypto";
import { confirmEmail, ownerEmail, type Mail } from "./emails";

export * from "./form";
import { HONEYPOT, HOST_CHOICES, LIMITS, PROJECT_CHOICES, RESEND_AFTER_MS } from "./form";

export type Answers = {
  email: string;
  hosting: string | null;
  projects: string | null;
  currentHosts: string[];
  note: string | null;
};

export type Signup = Answers & {
  id: number;
  status: "pending" | "confirmed";
  confirmSentAt: Date | null;
  createdAt: Date;
};

export type Field = "email" | "hosting" | "note";
export type FieldErrors = Partial<Record<Field, string>>;

export type Parsed = { ok: true; answers: Answers; bot: boolean } | { ok: false; errors: FieldErrors };

const EMAIL_RE = /^[^\s@<>()[\]\\,;:"]+@[^\s@<>()[\]\\,;:"]+\.[^\s@<>()[\]\\,;:".]{2,}$/;

function text(v: FormDataEntryValue | null): string {
  // Control characters are never meant; newlines only make sense in the note.
  return typeof v === "string" ? v.replace(/\r\n?/g, "\n").replace(/[\u0000-\u0009\u000b-\u001f\u007f]/g, "").trim() : "";
}

/** Reads and checks a submitted form. Error messages say what to do. */
export function parseForm(form: FormData): Parsed {
  const email = text(form.get("email")).toLowerCase();
  const hosting = text(form.get("hosting")).replace(/\s+/g, " ");
  const note = text(form.get("note"));
  const projects = text(form.get("projects"));
  const currentHosts = form
    .getAll("current")
    .map((v) => text(v))
    .filter((v, i, all) => HOST_CHOICES.some((c) => c.value === v) && all.indexOf(v) === i);

  const errors: FieldErrors = {};
  if (!email) errors.email = "Enter your email address.";
  else if (email.length > LIMITS.email || !EMAIL_RE.test(email))
    errors.email = "Enter an email address like name@example.com.";
  if (hosting.length > LIMITS.hosting) errors.hosting = `Keep this under ${LIMITS.hosting} characters.`;
  if (note.length > LIMITS.note) errors.note = `Keep the note under ${LIMITS.note} characters.`;
  if (Object.keys(errors).length) return { ok: false, errors };

  return {
    ok: true,
    bot: text(form.get(HONEYPOT)) !== "",
    answers: {
      email,
      hosting: hosting || null,
      projects: PROJECT_CHOICES.some((c) => c.value === projects) ? projects : null,
      currentHosts,
      note: note || null,
    },
  };
}

/** A link token: the random part goes in the email, only its hash is stored. */
export function newToken(): { token: string; hash: string } {
  const token = randomBytes(32).toString("base64url");
  return { token, hash: hashToken(token) };
}

export function hashToken(token: string): string {
  return createHash("sha256").update(token).digest("hex");
}

export function looksLikeToken(t: unknown): t is string {
  return typeof t === "string" && /^[A-Za-z0-9_-]{43}$/.test(t);
}

/**
 * What a sign-up does, given who is already on the list:
 * - new: add them and send a confirmation;
 * - resend: not confirmed yet and the last email is old enough: new link, send again;
 * - quiet: nothing to send (confirmed already, or an email went out a moment ago).
 */
export function decide(existing: Signup | null, now: Date): "new" | "resend" | "quiet" {
  if (!existing) return "new";
  if (existing.status === "confirmed") return "quiet";
  const sent = existing.confirmSentAt?.getTime() ?? 0;
  return now.getTime() - sent >= RESEND_AFTER_MS ? "resend" : "quiet";
}

export interface Repo {
  /** Adds or updates a sign-up as decide() says; returns what it decided. */
  register(a: Answers, token: { hash: string }, now: Date): Promise<"new" | "resend" | "quiet">;
  /** Marks the sign-up with this token confirmed. first is false if it already was. */
  confirm(hash: string, now: Date): Promise<{ signup: Signup; first: boolean } | null>;
  /** Deletes the sign-up with this token. */
  remove(hash: string): Promise<boolean>;
}

export type Deps = {
  repo: Repo;
  send: (m: Mail) => Promise<void>;
  /** https://shiptiffin.com, without a trailing slash. */
  baseUrl: string;
  /** Where the owner hears about confirmed sign-ups; skipped when unset. */
  notifyTo?: string | null;
  now?: () => Date;
  log?: (msg: string, err?: unknown) => void;
};

export type Outcome = { kind: "added" | "resent" | "quiet" | "bot"; mailed: boolean };

/** Someone submitted the form. The answer to them is the same whatever happened. */
export async function signUp(p: Extract<Parsed, { ok: true }>, d: Deps): Promise<Outcome> {
  if (p.bot) return { kind: "bot", mailed: false };
  const now = d.now?.() ?? new Date();
  const t = newToken();
  const what = await d.repo.register(p.answers, t, now);
  if (what === "quiet") return { kind: "quiet", mailed: false };
  let mailed = false;
  try {
    await d.send(confirmEmail(p.answers.email, links(d.baseUrl, t.token)));
    mailed = true;
  } catch (err) {
    d.log?.("early access: confirmation email not sent", err);
  }
  return { kind: what === "new" ? "added" : "resent", mailed };
}

export async function confirm(token: string, d: Deps): Promise<"confirmed" | "already" | "invalid"> {
  if (!looksLikeToken(token)) return "invalid";
  const r = await d.repo.confirm(hashToken(token), d.now?.() ?? new Date());
  if (!r) return "invalid";
  if (r.first && d.notifyTo) {
    try {
      await d.send(ownerEmail(d.notifyTo, r.signup));
    } catch (err) {
      d.log?.("early access: owner notification not sent", err);
    }
  }
  return r.first ? "confirmed" : "already";
}

export async function remove(token: string, d: Pick<Deps, "repo">): Promise<boolean> {
  if (!looksLikeToken(token)) return false;
  return d.repo.remove(hashToken(token));
}

export function links(baseUrl: string, token: string) {
  return {
    confirm: `${baseUrl}/early-access/confirm?t=${token}`,
    remove: `${baseUrl}/early-access/remove?t=${token}`,
    unsubscribe: `${baseUrl}/api/early-access/remove?t=${token}`,
  };
}
