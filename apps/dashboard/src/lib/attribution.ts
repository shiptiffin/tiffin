import type { Manifest } from "@/api/client";
import type { Attribution, Cookie } from "./privacy-text";
import type { StagedEdit } from "./staged";

/**
 * Marketing attribution's settings, as the manifest has them
 * (services.analytics.attribution; docs/guide/analytics.md, "Marketing
 * attribution"), and the privacy text that goes with them.
 *
 * The box writes the text (GET /v1/analytics/privacy) for the settings in
 * force. Before a change applies, the dashboard shows what the text will be:
 * privacyText mirrors internal/mod/analytics/privacy.go word for word. If the
 * two ever drift, nothing is lost: the copy remembered here is the preview's
 * hash, so the Privacy text panel says "Your privacy text changed" and the
 * person copies the box's own text.
 */

export { privacyHash, privacyText, type Attribution, type Cookie } from "./privacy-text";

export const OFF: Attribution = { on: false, campaigns: false, signups: false, cookie: "off" };

/** The manifest path every attribution edit starts from. */
export const ATTR = ["services", "analytics", "attribution"];

type Spec = { campaigns?: boolean; signups?: boolean; firstVisitCookie?: string };

export const asCookie = (v: unknown): Cookie => (v === "outside-eu" || v === "ask-eu" ? v : "off");

/** The settings from a raw attribution spec (undefined: off). Campaigns and sign-ups default to on. */
export function fromSpec(spec: Spec | undefined | null): Attribution {
  if (!spec) return OFF;
  return { on: true, campaigns: spec.campaigns ?? true, signups: spec.signups ?? true, cookie: asCookie(spec.firstVisitCookie) };
}

export function specOf(m: Manifest | undefined): Spec | undefined {
  const services = (m as { services?: { analytics?: { attribution?: Spec } } } | undefined)?.services;
  return services?.analytics?.attribution ?? undefined;
}

/** The settings with edits on their way laid over them, so controls show where they're going. */
export function withPending(m: Manifest | undefined, pending: StagedEdit[]): Attribution {
  let spec: Spec | undefined = specOf(m) ? { ...specOf(m) } : undefined;
  for (const e of pending) {
    if (e.kind !== "set" || e.path.slice(0, 3).join("/") !== ATTR.join("/")) continue;
    if (e.path.length === 3) spec = e.to === undefined ? undefined : { ...(e.to as Spec) };
    else if (spec) spec = { ...spec, [e.path[3]]: e.to };
  }
  return fromSpec(spec);
}

export const cookieChoices: Array<{ value: Cookie; label: string; line: string }> = [
  { value: "off", label: "Off", line: "No cookie. A sign-up is credited to the visit it happened in." },
  { value: "outside-eu", label: "Outside the EU", line: "Set for 90 days, never for visitors in the EU, EEA or UK. No banner needed." },
  { value: "ask-eu", label: "Ask EU visitors", line: "Visitors in the EU, EEA or UK get it only after they agree to a small prompt." },
];

export const cookieRank = (c: Cookie) => (c === "ask-eu" ? 2 : c === "outside-eu" ? 1 : 0);

/** The prompt an "ask-eu" project adds to its pages. */
export const CONSENT_SNIPPET = `<script defer src="/_tiffin/consent.js"></script>`;

// The hash of the privacy text a person last copied, per project, in this browser.
const KEY = (project: string) => `tiffin.privacy-copied.${project}`;

export function copiedHash(project: string): string | null {
  try {
    return localStorage.getItem(KEY(project));
  } catch {
    return null;
  }
}

export function rememberCopied(project: string, hash: string) {
  try {
    localStorage.setItem(KEY(project), hash);
  } catch {
    /* private window: the notice just shows again */
  }
}
