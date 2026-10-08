// What the request-an-invite form and its server agree on. Safe in the browser.

type Choice = { readonly value: string; readonly label: string };

/** "What do you build?" */
export const ROLE_CHOICES = [
  { value: "indie", label: "Indie maker" },
  { value: "company", label: "Developer at a company" },
  { value: "agency", label: "Agency or freelancer" },
  { value: "student", label: "Student" },
  { value: "other", label: "Something else" },
] as const satisfies readonly Choice[];

export const PROJECT_CHOICES = [
  { value: "1", label: "1" },
  { value: "2-5", label: "2–5" },
  { value: "6-20", label: "6–20" },
  { value: "more", label: "More" },
] as const satisfies readonly Choice[];

/** "What do you use today?" */
export const TOOL_CHOICES = [
  { value: "vercel", label: "Vercel" },
  { value: "netlify", label: "Netlify" },
  { value: "railway", label: "Railway" },
  { value: "render", label: "Render" },
  { value: "fly", label: "Fly" },
  { value: "supabase", label: "Supabase" },
  { value: "vps", label: "A VPS" },
  { value: "other", label: "Other" },
] as const satisfies readonly Choice[];

/** "Roughly what do you spend on hosting a month?" */
export const SPEND_CHOICES = [
  { value: "none", label: "Nothing" },
  { value: "under-25", label: "Under $25" },
  { value: "25-100", label: "$25–100" },
  { value: "100-300", label: "$100–300" },
  { value: "over-300", label: "Over $300" },
] as const satisfies readonly Choice[];

/** "Do you use AI coding agents?" */
export const AGENT_CHOICES = [
  { value: "claude-code", label: "Claude Code" },
  { value: "cursor", label: "Cursor" },
  { value: "codex", label: "Codex" },
  { value: "other", label: "Another" },
  { value: "none", label: "Not yet" },
] as const satisfies readonly Choice[];

/** The profile links, in the order the form shows them. All optional. */
export const LINKS = [
  { name: "github", label: "GitHub", placeholder: "username or link", hint: "a GitHub username or link" },
  { name: "x", label: "X", placeholder: "@handle or link", hint: "an X handle or link" },
  { name: "linkedin", label: "LinkedIn", placeholder: "linkedin.com/in/…", hint: "a LinkedIn profile link" },
  { name: "site", label: "Website", placeholder: "yoursite.com", hint: "a web address like yoursite.com" },
] as const;
export type LinkName = (typeof LINKS)[number]["name"];

export const LIMITS = { email: 254, name: 80, hostFirst: 200, note: 1000, link: 200 } as const;

/** A new confirmation email goes out at most this often per address. */
export const RESEND_AFTER_MS = 10 * 60 * 1000;

/** How long after step 1 its optional second step can still add to the request. */
export const DETAILS_FOR_MS = 60 * 60 * 1000;

/** The name of the hidden field only bots fill in. */
export const HONEYPOT = "url";

export type Field = "email" | "name" | "hostFirst" | "note" | LinkName;
/** In the order they appear, so the first error gets the focus. */
export const FIELDS: readonly Field[] = ["name", "email", "hostFirst", "github", "x", "linkedin", "site", "note"];

export type ErrorCode = "email" | "invalid" | "long" | "busy" | "unavailable";

/** Step 1's reply carries the token that lets step 2 add to the same request. */
export type SignupReply =
  | { ok: true; details?: string }
  | { ok: false; code: ErrorCode; errors?: Partial<Record<Field, string>>; message: string };

/** What the form says when a request is refused, by code (also ?error= on /early-access). */
export const MESSAGES: Record<ErrorCode, string> = {
  email: "Enter an email address like name@example.com.",
  invalid: "One of the answers needs another look.",
  long: "One of the answers is too long. Shorten it and send again.",
  busy: "That's a lot of requests from one place. Wait a few minutes and try again.",
  unavailable:
    "We couldn't save that just now. Try again in a minute, or email hello@shiptiffin.com and we'll add you by hand.",
};

export const label = (list: readonly Choice[], v: string | null | undefined): string | null =>
  v == null ? null : (list.find((c) => c.value === v)?.label ?? v);
