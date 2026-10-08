// What the early-access form and its server agree on. Safe in the browser.

export const PROJECT_CHOICES = [
  { value: "1", label: "1" },
  { value: "2-5", label: "2–5" },
  { value: "6-20", label: "6–20" },
  { value: "more", label: "More" },
] as const;

export const HOST_CHOICES = [
  { value: "vercel", label: "Vercel" },
  { value: "railway", label: "Railway" },
  { value: "render", label: "Render" },
  { value: "fly", label: "Fly" },
  { value: "vps", label: "A VPS" },
  { value: "other", label: "Other" },
] as const;

export const LIMITS = { email: 254, hosting: 200, note: 1000 } as const;

/** A new confirmation email goes out at most this often per address. */
export const RESEND_AFTER_MS = 10 * 60 * 1000;

/** The name of the hidden field only bots fill in. */
export const HONEYPOT = "website";

export type SignupReply = { ok: true } | { ok: false; code: ErrorCode; errors?: Record<string, string>; message: string };
export type ErrorCode = "email" | "long" | "busy" | "unavailable";

/** What the form says when a sign-up is refused, by code (also ?error= on /early-access). */
export const MESSAGES: Record<ErrorCode, string> = {
  email: "Enter an email address like name@example.com.",
  long: "One of the answers is too long. Shorten it and send again.",
  busy: "That's a lot of sign-ups from one place. Wait a few minutes and try again.",
  unavailable: "We couldn't save that just now. Try again in a minute, or email hello@shiptiffin.com and we'll add you by hand.",
};

