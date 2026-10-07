// Colours for both families, as hex (mail clients don't read oklch).
//
// box: ShipTiffin / Tiffin's paper, ink and brass, converted from
// apps/dashboard/src/styles/tokens.css. Brass is the one accent: the button.
// app: a neutral stone palette around the app's own name and logo. Its
// button is the same brass button as the dashboard's, unless the app sets
// its own colour (auth.emailAccent in tiffin.config.ts).
//
// The values that come from tokens.css are listed in `fromDashboard`;
// test/brand.test.ts converts the oklch there and fails on any drift.
//
// Every family has the same token names, so the shared parts in parts.tsx
// work in both. Dark values feed the prefers-color-scheme block (Apple Mail,
// iOS Mail, Outlook for Mac and Outlook.com); Gmail's apps invert colours
// themselves, so the light palette is chosen to survive that too.
import type { Family } from "./compile";

export type Tokens = {
  bg: string; // the page around the card
  card: string;
  rule: string; // hairlines and the card's edge
  ink: string; // headings, sentences
  ink2: string; // secondary sentences
  ink3: string; // labels, footers (4.5:1 on bg and card)
  well: string; // the code box
  accent: string; // the button
  onAccent: string;
  link: string;
  danger: string;
  ok: string;
};

/** The dashboard's primary button: brass, with its text colour. */
export const brass = {
  light: { accent: "#f2b036", onAccent: "#25170c" }, // --brass, --on-brass
  dark: { accent: "#f7b83d", onAccent: "#1d140d" }, // the same, dark theme
} as const;

export const light: Record<Family, Tokens> = {
  box: {
    bg: "#f9f6f2", // --paper
    card: "#fefdfa", // --paper-raised
    rule: "#e8e2d8",
    ink: "#231d18", // --ink
    ink2: "#564e48", // --ink-2
    ink3: "#6c6660", // --ink-3
    well: "#f2efe8", // --paper-sunk
    ...brass.light,
    link: "#825411", // --brass-ink
    danger: "#b82f2b",
    ok: "#317a45",
  },
  app: {
    bg: "#f5f5f4",
    card: "#ffffff",
    rule: "#e7e5e4",
    ink: "#1c1917",
    ink2: "#44403c",
    ink3: "#6b6661",
    well: "#f5f5f4",
    ...brass.light, // the default; the app's accent replaces it at send time

    link: "#1c1917",
    danger: "#b42318",
    ok: "#2f7a43",
  },
};

export const dark: Record<Family, Tokens> = {
  box: {
    bg: "#141210", // --paper (dark)
    card: "#1d1b18", // --paper-raised (dark)
    rule: "#34302b",
    ink: "#ece9e4",
    ink2: "#c4bfb8",
    ink3: "#a29d97",
    well: "#0f0d0b",
    ...brass.dark,
    link: "#e0b771",
    danger: "#f06c61",
    ok: "#6fc082",
  },
  app: {
    bg: "#121110",
    card: "#1c1a19",
    rule: "#34312e",
    ink: "#f2f1ef",
    ink2: "#cfccc8",
    ink3: "#a8a29e",
    well: "#121110",
    accent: "", // the button stays as sent: brass, or the app's own accent
    onAccent: "",
    link: "#f2f1ef",
    danger: "#f97066",
    ok: "#6fc082",
  },
};

/** Which tokens.css variable each colour is, per theme (checked by test/brand.test.ts). */
export const fromDashboard: Record<"light" | "dark", { family: Family; token: keyof Tokens; css: string }[]> = {
  light: [
    ...(["box", "app"] as const).flatMap((family) => [
      { family, token: "accent" as const, css: "--brass" },
      { family, token: "onAccent" as const, css: "--on-brass" },
    ]),
    { family: "box", token: "bg", css: "--paper" },
    { family: "box", token: "card", css: "--paper-raised" },
    { family: "box", token: "well", css: "--paper-sunk" },
    { family: "box", token: "ink", css: "--ink" },
    { family: "box", token: "ink2", css: "--ink-2" },
    { family: "box", token: "ink3", css: "--ink-3" },
    { family: "box", token: "link", css: "--brass-ink" },
    { family: "box", token: "danger", css: "--danger" },
    { family: "box", token: "ok", css: "--ok" },
  ],
  dark: [
    { family: "box", token: "accent", css: "--brass" },
    { family: "box", token: "onAccent", css: "--on-brass" },
    { family: "box", token: "bg", css: "--paper" },
    { family: "box", token: "card", css: "--paper-raised" },
    { family: "box", token: "well", css: "--paper-sunk" },
    { family: "box", token: "ink", css: "--ink" },
    { family: "box", token: "link", css: "--brass-ink" },
    { family: "box", token: "danger", css: "--danger" },
    { family: "box", token: "ok", css: "--ok" },
  ],
};

export const SANS = `-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif`;
export const SERIF = `Georgia,'Times New Roman',Times,serif`;
export const MONO = `ui-monospace,'SF Mono',SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace`;

/** Tailwind v4 CSS-first theme for a family (the <Tailwind theme> prop). */
export function themeCss(f: Family): string {
  const t = light[f];
  const colours = Object.entries(t)
    .map(([k, val]) => `--color-${k.replace(/[A-Z0-9]/g, (c) => "-" + c.toLowerCase())}: ${val};`)
    .join(" ");
  return `@theme { ${colours} --font-sans: ${SANS}; --font-serif: ${SERIF}; --font-mono: ${MONO}; }`;
}

/**
 * The dark-mode rules. Class names (tf-*) are set by the
 * layouts and parts; everything else is inline. [data-ogsc] is how
 * Outlook.com marks its dark mode.
 */
export function headCss(f: Family): string {
  const d = dark[f];
  const rules: [string, string][] = [
    [".tf-bg", `background-color:${d.bg} !important`],
    [".tf-card", `background-color:${d.card} !important;border-color:${d.rule} !important`],
    [".tf-ink", `color:${d.ink} !important`],
    [".tf-ink2", `color:${d.ink2} !important`],
    [".tf-ink3", `color:${d.ink3} !important`],
    [".tf-rule", `border-color:${d.rule} !important`],
    [".tf-well", `background-color:${d.well} !important;border-color:${d.rule} !important;color:${d.ink} !important`],
    [".tf-link", `color:${d.link} !important`],
    [".tf-danger", `color:${d.danger} !important`],
    [".tf-ok", `color:${d.ok} !important`],
  ];
  if (d.accent) rules.push([".tf-btn", `background-color:${d.accent} !important;color:${d.onAccent} !important`]);
  const block = (prefix: string) => rules.map(([sel, body]) => `${prefix}${sel}{${body}}`).join("");
  return `:root{color-scheme:light dark}@media (prefers-color-scheme:dark){${block("")}}${block("[data-ogsc] ")}`;
}

/** Small screens. A block of its own: Gmail keeps it even if it drops the dark-mode block. */
export const phoneCss =
  `@media only screen and (max-width:480px){.tf-pad{padding-left:12px !important;padding-right:12px !important}` +
  `.tf-cardpad{padding:28px 22px 26px !important}.tf-h1{font-size:23px !important;line-height:30px !important}}`;
