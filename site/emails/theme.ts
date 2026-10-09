// The emails' colours, type and pictures. One palette for every ShipTiffin
// email: the website's paper, ink and brass (app/globals.css, as hex: mail
// clients don't read oklch). The box's own emails (packages/emails) use the
// same values; lib/emails.test.ts checks both against globals.css.

export const light = {
  bg: "#f9f6f2", // --paper
  card: "#fefdfa", // --paper-raised
  rule: "#e8e2d8", // --rule on paper, flattened
  ink: "#231d18", // --ink
  ink2: "#564e48", // --ink-2
  ink3: "#6c6660", // --ink-3 (4.5:1 on bg and card)
  well: "#f2efe8", // --paper-sunk
  accent: "#f2b036", // --brass: the one button
  onAccent: "#25170c", // --on-brass
  link: "#825411", // --brass-ink
  danger: "#b6322b", // --danger
  ok: "#317a45",
};

export const dark: typeof light = {
  bg: "#141210",
  card: "#1d1b18",
  rule: "#34302b",
  ink: "#ece9e4",
  ink2: "#c4bfb8",
  ink3: "#a29d97",
  well: "#0f0d0b",
  accent: "#f7b83d",
  onAccent: "#1d140d",
  link: "#e0b771",
  danger: "#f68678",
  ok: "#6fc082",
};

/** Where the pictures and fonts live: always the public site, whatever SITE_URL says. */
export const ASSETS = "https://shiptiffin.com/email";

export const MARK = `${ASSETS}/mark.png`; // the mascot on a paper tile, 96px for 28px
export const READY = `${ASSETS}/box-ready.png`; // the open tin, 2x

// The site's faces where the mail app loads web fonts (Apple Mail, iOS Mail,
// Outlook for Mac), else the closest system ones.
export const SERIF = `Newsreader,Georgia,'Times New Roman',Times,serif`;
export const SANS = `'Instrument Sans',-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif`;
export const MONO = `ui-monospace,'SF Mono',SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace`;

export const fontFaces =
  `@font-face{font-family:Newsreader;font-style:normal;font-weight:200 800;font-display:swap;src:url(${ASSETS}/newsreader.woff2) format('woff2')}` +
  `@font-face{font-family:'Instrument Sans';font-style:normal;font-weight:400 700;font-display:swap;src:url(${ASSETS}/instrument-sans.woff2) format('woff2')}`;

/** Dark mode for the clients that ask (Apple Mail, iOS Mail, Outlook for Mac and Outlook.com). */
export function darkCss(): string {
  const d = dark;
  const rules: [string, string][] = [
    [".tf-bg", `background-color:${d.bg} !important`],
    [".tf-card", `background-color:${d.card} !important;border-color:${d.rule} !important`],
    [".tf-ink", `color:${d.ink} !important`],
    [".tf-ink2", `color:${d.ink2} !important`],
    [".tf-ink3", `color:${d.ink3} !important`],
    [".tf-rule", `border-color:${d.rule} !important`],
    [".tf-well", `background-color:${d.well} !important;border-color:${d.rule} !important`],
    [".tf-link", `color:${d.link} !important`],
    [".tf-danger", `color:${d.danger} !important`],
    [".tf-ok", `color:${d.ok} !important`],
    [".tf-btn", `background-color:${d.accent} !important;color:${d.onAccent} !important`],
  ];
  const block = (prefix: string) => rules.map(([sel, body]) => `${prefix}${sel}{${body}}`).join("");
  return `:root{color-scheme:light dark}@media (prefers-color-scheme:dark){${block("")}}${block("[data-ogsc] ")}`;
}

/** Phones: less padding, a smaller heading. Its own block, so Gmail keeps it if it drops the other. */
export const phoneCss =
  `@media only screen and (max-width:480px){.tf-pad{padding-left:12px !important;padding-right:12px !important}` +
  `.tf-cardpad{padding:30px 22px 26px !important}.tf-h1{font-size:26px !important;line-height:32px !important}` +
  `.tf-hero{font-size:30px !important;line-height:36px !important}}`;
