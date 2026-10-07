// Templates are ordinary React Email components. The build renders each one
// with placeholder props instead of real values, then turns the placeholders
// into Go template actions or a TypeScript function (scripts/build.tsx). The
// preview server renders the same components with real PreviewProps.
//
// Placeholder grammar, written into the rendered HTML by the helpers below
// and nowhere else:
//   %%=name%%           a value
//   %%#name%%           if name is set (non-empty string, or true)
//   %%#name=value%%     if name equals value
//   %%:name=value%%     else if name equals value
//   %%:%%               else
//   %%/%%               end
//   %%!name%%           a fixed snippet the build pastes in (Outlook-only markup)
import type { ReactNode } from "react";

/** live: the preview server (real values). html/text: the build, with placeholders. */
export type Mode = "live" | "html" | "text";
let mode: Mode = "live";
export const setMode = (m: Mode) => {
  mode = m;
};
export const getMode = () => mode;
export const compiling = () => mode !== "live";

// ---- variables ----------------------------------------------------------

export type Var =
  | { kind: "text"; optional?: boolean; doc: string }
  | { kind: "url"; optional?: boolean; doc: string }
  | { kind: "color"; doc: string }
  | { kind: "flag"; doc: string }
  | { kind: "enum"; values: readonly string[]; doc: string };

export const v = {
  text: (doc: string) => ({ kind: "text", doc }) as const,
  optText: (doc: string) => ({ kind: "text", optional: true, doc }) as const,
  url: (doc: string) => ({ kind: "url", doc }) as const,
  optUrl: (doc: string) => ({ kind: "url", optional: true, doc }) as const,
  color: (doc: string) => ({ kind: "color", doc }) as const,
  flag: (doc: string) => ({ kind: "flag", doc }) as const,
  oneOf: <const T extends readonly string[]>(values: T, doc: string) => ({ kind: "enum", values, doc }) as const,
};

type ValueOf<V> = V extends { kind: "flag" } ? boolean : V extends { kind: "enum"; values: readonly (infer E)[] } ? E : string;
export type PropsOf<Vars extends Record<string, Var>> = { [K in keyof Vars]: ValueOf<Vars[K]> };

export type Family = "box" | "app";

export interface EmailSpec<Vars extends Record<string, Var>> {
  /** File and function name: "invite", "verify-email". */
  id: string;
  family: Family;
  /** What it is, for the generated docs. */
  title: string;
  vars: Vars;
  subject: (p: PropsOf<Vars>) => string;
  preview: PropsOf<Vars>;
}

export const defineEmail = <const Vars extends Record<string, Var>>(spec: EmailSpec<Vars>) => spec;

/** The props the build renders with: every value is its own placeholder. */
export function placeholderProps(vars: Record<string, Var>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(vars)) out[k] = `%%=${k}%%`;
  return out;
}

// ---- conditionals -------------------------------------------------------

type Branch = ReactNode;
const isText = (b: unknown): b is string => typeof b === "string";

function join(parts: Branch[], inline: boolean): Branch {
  if (parts.every(isText)) return (parts as string[]).join("");
  // In text mode a block's marker sits on a line of its own, so the build can
  // drop the marker together with the line break after it.
  const own = mode === "text" && !inline;
  return (
    <>
      {parts.map((x, i) => (own && isText(x) && /^%%[#:/]/.test(x) ? <div key={i}>{x}</div> : <Frag key={i}>{x}</Frag>))}
    </>
  );
}
const Frag = ({ children }: { children?: ReactNode }) => <>{children}</>;

/** Shows `then` when the variable is set (a non-empty string, or true), else `otherwise`. */
export function when<P extends Record<string, unknown>>(p: P, key: keyof P & string, then: Branch, otherwise?: Branch, opts?: { inline?: boolean }): Branch {
  if (!compiling()) return p[key] ? then : (otherwise ?? null);
  const parts: Branch[] = [`%%#${key}%%`, then];
  if (otherwise !== undefined && otherwise !== null) parts.push("%%:%%", otherwise);
  parts.push("%%/%%");
  return join(parts, !!opts?.inline);
}

/** Inside a sentence: `when` without a line of its own in the plain-text part. */
export const whenInline = <P extends Record<string, unknown>>(p: P, key: keyof P & string, then: Branch, otherwise?: Branch) =>
  when(p, key, then, otherwise, { inline: true });

/** Picks the branch for the variable's value, or `fallback`. */
export function choose<P extends Record<string, unknown>>(
  p: P,
  key: keyof P & string,
  cases: Record<string, Branch>,
  fallback?: Branch,
  opts?: { inline?: boolean },
): Branch {
  if (!compiling()) {
    const k = String(p[key]);
    return k in cases ? cases[k] : (fallback ?? null);
  }
  const parts: Branch[] = [];
  Object.entries(cases).forEach(([val, node], i) => {
    if (!/^[a-z0-9-]+$/.test(val)) throw new Error(`choose(${key}): case "${val}" must be lowercase letters, digits and dashes`);
    parts.push(`%%${i === 0 ? "#" : ":"}${key}=${val}%%`, node);
  });
  if (fallback !== undefined && fallback !== null) parts.push("%%:%%", fallback);
  parts.push("%%/%%");
  return join(parts, !!opts?.inline);
}

/** A fixed snippet the build pastes in after rendering (React can't write comments). */
export const raw = (name: keyof typeof RAW) => (mode === "html" ? `%%!${name}%%` : null);

/** Outlook for Windows ignores max-width, so it gets a fixed-width table instead. */
export const RAW = {
  msoOpen: `<!--[if mso]><table role="presentation" align="center" width="560" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`,
  msoClose: `<!--[if mso]></td></tr></table><![endif]-->`,
} as const;
