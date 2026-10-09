// Invite requests: what a request holds, how the form is read, and what
// happens when someone asks for an invite, adds detail, confirms their email or
// asks to be removed. Storage and mail are passed in (lib/early-access-pg.ts and
// lib/early-access-box.ts on the box, fakes in the tests), so this file has no
// I/O of its own.
//
// The form has two steps. Step 1 (name, email, what you build) saves the
// request and sends the confirmation email. Its reply carries a short-lived
// details token; step 2 sends the optional answers with it. Without
// JavaScript, both steps arrive in one post.
import { createHash, randomBytes } from "node:crypto";
import { confirmEmail, ownerEmail, type Mail } from "./emails";

export * from "./form";
import {
  AGENT_CHOICES,
  DETAILS_FOR_MS,
  HONEYPOT,
  LIMITS,
  PROJECT_CHOICES,
  RESEND_AFTER_MS,
  ROLE_CHOICES,
  SPEND_CHOICES,
  TOOL_CHOICES,
  type Field,
  type LinkName,
} from "./form";

/** Step 2: everything optional. */
export type Details = {
  hostFirst: string | null;
  projects: string | null;
  tools: string[];
  spend: string | null;
  agents: string[];
  github: string | null;
  x: string | null;
  linkedin: string | null;
  site: string | null;
  note: string | null;
};

/** Step 1, plus whatever of step 2 came with it. */
export type Answers = Details & {
  email: string;
  name: string | null;
  role: string | null;
};

export type Status = "requested" | "invited" | "joined" | "declined";

export type InviteRequest = Answers & {
  id: number;
  status: Status;
  emailConfirmed: boolean;
  confirmSentAt: Date | null;
  createdAt: Date;
};

export type FieldErrors = Partial<Record<Field, string>>;

export type Parsed = { ok: true; answers: Answers; bot: boolean } | { ok: false; errors: FieldErrors };
export type ParsedDetails = { ok: true; details: Details } | { ok: false; errors: FieldErrors };

const EMAIL_RE = /^[^\s@<>()[\]\\,;:"]+@[^\s@<>()[\]\\,;:"]+\.[^\s@<>()[\]\\,;:".]{2,}$/;

function text(v: FormDataEntryValue | null): string {
  // Control characters are never meant; newlines only make sense in the note.
  return typeof v === "string" ? v.replace(/\r\n?/g, "\n").replace(/[\u0000-\u0009\u000b-\u001f\u007f]/g, "").trim() : "";
}
const line = (v: FormDataEntryValue | null) => text(v).replace(/\s+/g, " ");

const one = (list: readonly { value: string }[], v: string) => (list.some((c) => c.value === v) ? v : null);

function many(form: FormData, key: string, list: readonly { value: string }[]): string[] {
  return form
    .getAll(key)
    .map((v) => text(v))
    .filter((v, i, all) => list.some((c) => c.value === v) && all.indexOf(v) === i);
}

/* ---------- profile links ---------- */

const HANDLE = {
  github: /^[A-Za-z0-9](?:[A-Za-z0-9]|-(?=[A-Za-z0-9])){0,38}$/,
  x: /^[A-Za-z0-9_]{1,15}$/,
  linkedin: /^[A-Za-z0-9\-_%.]{2,100}$/,
};

/** A web address, if the text is one: https:// is added when missing. */
function webUrl(raw: string): URL | null {
  if (/\s/.test(raw)) return null;
  const withScheme = /^[a-z][a-z0-9+.-]*:/i.test(raw) ? raw : `https://${raw.replace(/^\/+/, "")}`;
  try {
    const u = new URL(withScheme);
    if (u.protocol !== "https:" && u.protocol !== "http:") return null;
    if (u.username || u.password) return null;
    // A real host name: letters, a dot, and a top-level part of two or more letters.
    if (!/^(?:[a-z0-9-]+\.)+[a-z]{2,}$/i.test(u.hostname)) return null;
    return u;
  } catch {
    return null;
  }
}

const hostIs = (u: URL, ...names: string[]) =>
  names.some((n) => u.hostname.toLowerCase() === n || u.hostname.toLowerCase() === `www.${n}`);

/**
 * Reads a profile link as a handle or an address and returns the canonical
 * address, so the owner can click it: "@ada" in X becomes https://x.com/ada.
 * null means it isn't one; "" means the field was empty.
 */
export function normaliseLink(kind: LinkName, raw: string): string | null {
  const v = raw.trim();
  if (!v) return "";
  if (v.length > LIMITS.link) return null;
  if (kind === "site") {
    const u = webUrl(v);
    if (!u) return null;
    return u.href.replace(/\/$/, u.pathname === "/" && !u.search && !u.hash ? "" : "/");
  }
  const handle = v.replace(/^@/, "");
  if (kind !== "linkedin" && HANDLE[kind].test(handle)) return kind === "github" ? `https://github.com/${handle}` : `https://x.com/${handle}`;
  const u = webUrl(v);
  if (!u) return null;
  const parts = u.pathname.split("/").filter(Boolean);
  if (kind === "github" && hostIs(u, "github.com") && parts.length >= 1 && HANDLE.github.test(parts[0]!))
    return `https://github.com/${parts[0]}`;
  if (kind === "x" && hostIs(u, "x.com", "twitter.com") && parts.length >= 1 && HANDLE.x.test(parts[0]!))
    return `https://x.com/${parts[0]}`;
  if (kind === "linkedin" && hostIs(u, "linkedin.com") && (parts[0] === "in" || parts[0] === "company") && parts[1] && HANDLE.linkedin.test(parts[1]))
    return `https://www.linkedin.com/${parts[0]}/${parts[1]}`;
  return null;
}

const LINK_ERRORS: Record<LinkName, string> = {
  github: "Enter a GitHub username, or a link like github.com/you.",
  x: "Enter an X handle like @you, or a link like x.com/you.",
  linkedin: "Enter a LinkedIn link like linkedin.com/in/you.",
  site: "Enter a web address like yoursite.com.",
};

/* ---------- reading the form ---------- */

function readDetails(form: FormData, errors: FieldErrors): Details {
  const hostFirst = line(form.get("hostFirst"));
  const note = text(form.get("note"));
  if (hostFirst.length > LIMITS.hostFirst) errors.hostFirst = `Keep this under ${LIMITS.hostFirst} characters.`;
  if (note.length > LIMITS.note) errors.note = `Keep the note under ${LIMITS.note} characters.`;

  const links = {} as Record<LinkName, string | null>;
  for (const k of ["github", "x", "linkedin", "site"] as const) {
    const n = normaliseLink(k, line(form.get(k)));
    if (n === null) errors[k] = LINK_ERRORS[k];
    links[k] = n || null;
  }

  let agents = many(form, "agents", AGENT_CHOICES);
  // "Not yet" alongside a named agent: the named one is what they meant.
  if (agents.length > 1) agents = agents.filter((a) => a !== "none");

  return {
    hostFirst: hostFirst || null,
    projects: one(PROJECT_CHOICES, text(form.get("projects"))),
    tools: many(form, "tools", TOOL_CHOICES),
    spend: one(SPEND_CHOICES, text(form.get("spend"))),
    agents,
    ...links,
    note: note || null,
  };
}

/** Reads a request (step 1, or the whole form without JavaScript). Error messages say what to do. */
export function parseForm(form: FormData): Parsed {
  const errors: FieldErrors = {};
  const email = text(form.get("email")).toLowerCase();
  const name = line(form.get("name"));
  if (!email) errors.email = "Enter your email address.";
  else if (email.length > LIMITS.email || !EMAIL_RE.test(email)) errors.email = "Enter an email address like name@example.com.";
  if (name.length > LIMITS.name) errors.name = `Keep your name under ${LIMITS.name} characters.`;
  const details = readDetails(form, errors);
  if (Object.keys(errors).length) return { ok: false, errors };
  return {
    ok: true,
    bot: text(form.get(HONEYPOT)) !== "",
    answers: { email, name: name || null, role: one(ROLE_CHOICES, text(form.get("role"))), ...details },
  };
}

/** Reads step 2 on its own. */
export function parseDetails(form: FormData): ParsedDetails {
  const errors: FieldErrors = {};
  const details = readDetails(form, errors);
  return Object.keys(errors).length ? { ok: false, errors } : { ok: true, details };
}

/** Which error code a set of field errors maps to, for the no-script redirect. */
export function errorCode(errors: FieldErrors): "email" | "long" | "invalid" {
  if (errors.email) return "email";
  return Object.values(errors).some((m) => m?.startsWith("Keep")) ? "long" : "invalid";
}

/* ---------- tokens ---------- */

/** A link token: the random part goes to the person, only its hash is stored. */
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

/* ---------- what happens ---------- */

/**
 * What a request does, given who has already asked:
 * - new: add them and send a confirmation;
 * - resend: email not confirmed yet and the last email is old enough: new link, send again;
 * - quiet: nothing to send (confirmed already, or an email went out a moment ago).
 */
export function decide(existing: InviteRequest | null, now: Date): "new" | "resend" | "quiet" {
  if (!existing) return "new";
  if (existing.emailConfirmed) return "quiet";
  const sent = existing.confirmSentAt?.getTime() ?? 0;
  return now.getTime() - sent >= RESEND_AFTER_MS ? "resend" : "quiet";
}

export type DetailsToken = { hash: string; expiresAt: Date };

export interface Repo {
  /**
   * Adds or updates a request as decide() says and returns what it decided.
   * A request whose email isn't confirmed yet takes the newest answers and the
   * new details token; a confirmed one keeps its answers and gets no token.
   */
  register(a: Answers, token: { hash: string }, details: DetailsToken, now: Date): Promise<"new" | "resend" | "quiet">;
  /** Adds step 2's answers to the request holding this unexpired details token, once. */
  addDetails(detailsHash: string, d: Details, now: Date): Promise<boolean>;
  /** Marks the email of the request with this token confirmed. first is false if it already was. */
  confirm(hash: string, now: Date): Promise<{ request: InviteRequest; first: boolean } | null>;
  /** Deletes the request with this token. */
  remove(hash: string): Promise<boolean>;
}

export type Deps = {
  repo: Repo;
  send: (m: Mail) => Promise<void>;
  /** https://shiptiffin.com, without a trailing slash. */
  baseUrl: string;
  /** Where the owner hears about confirmed requests; skipped when unset. */
  notifyTo?: string | null;
  now?: () => Date;
  log?: (msg: string, err?: unknown) => void;
};

export type Outcome = { kind: "added" | "resent" | "quiet" | "bot"; mailed: boolean; details: string };

/**
 * Someone asked for an invite. The answer to them is the same whatever
 * happened, details token included (a bot's or a confirmed address's token
 * is stored nowhere), so the form can't be used to find out who has asked.
 */
export async function signUp(p: Extract<Parsed, { ok: true }>, d: Deps): Promise<Outcome> {
  const details = newToken();
  if (p.bot) return { kind: "bot", mailed: false, details: details.token };
  const now = d.now?.() ?? new Date();
  const t = newToken();
  const what = await d.repo.register(p.answers, t, { hash: details.hash, expiresAt: new Date(now.getTime() + DETAILS_FOR_MS) }, now);
  if (what === "quiet") return { kind: "quiet", mailed: false, details: details.token };
  let mailed = false;
  try {
    await d.send(await confirmEmail(p.answers.email, p.answers.name, links(d.baseUrl, t.token)));
    mailed = true;
  } catch (err) {
    d.log?.("invite request: confirmation email not sent", err);
  }
  return { kind: what === "new" ? "added" : "resent", mailed, details: details.token };
}

/**
 * Step 2. "dropped" means no request took the answers: the token expired, was
 * used already, or was never stored (a bot's, or one for an address that was
 * confirmed before). The person is told the same either way, so step 2 can't
 * reveal who has asked.
 */
export async function addDetails(token: string, p: Extract<ParsedDetails, { ok: true }>, d: Deps): Promise<"saved" | "dropped"> {
  if (!looksLikeToken(token)) return "dropped";
  return (await d.repo.addDetails(hashToken(token), p.details, d.now?.() ?? new Date())) ? "saved" : "dropped";
}

export async function confirm(token: string, d: Deps): Promise<"confirmed" | "already" | "invalid"> {
  if (!looksLikeToken(token)) return "invalid";
  const r = await d.repo.confirm(hashToken(token), d.now?.() ?? new Date());
  if (!r) return "invalid";
  if (r.first && d.notifyTo) {
    try {
      await d.send(await ownerEmail(d.notifyTo, r.request));
    } catch (err) {
      d.log?.("invite request: owner notification not sent", err);
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
