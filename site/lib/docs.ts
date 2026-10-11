// The guides as web pages: /docs/<name>, rendered at build time from the same
// Markdown the site publishes for agents (public/docs/<name>.md, see lib/llms.ts).
// No client JavaScript: code is coloured by Shiki while the page is built.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { Marked } from "marked";
import { createHighlighter, type Highlighter } from "shiki";
import DATES_JSON from "./docs-dates.json";
import { PAGES, SITE, type DocDates } from "./llms";

const DATES: DocDates = DATES_JSON;

export type DocPage = (typeof PAGES)[number];
export type Heading = { id: string; text: string; depth: 2 | 3 };
export type Doc = {
  page: DocPage;
  title: string;
  /** The day the guide last changed. */
  updated: string;
  html: string;
  headings: Heading[];
  markdown: string;
};

export const DOC_PAGES = PAGES;

const LANGS = ["bash", "ts", "tsx", "js", "json", "html", "text"] as const;
let shiki: Promise<Highlighter> | undefined;
function highlighter() {
  shiki ??= createHighlighter({ themes: ["github-light", "github-dark"], langs: [...LANGS] });
  return shiki;
}

const escape = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

/** The same ids GitHub gives headings, so links written for GitHub keep working. */
export function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/<[^>]+>/g, "")
    .replace(/[^\p{L}\p{N}\s_-]/gu, "")
    .trim()
    .replace(/\s/g, "-");
}

const plain = (s: string) =>
  s
    .replace(/`([^`]+)`/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/[*_]/g, "")
    .replace(/\s+/g, " ")
    .trim();

/** Links to another guide's Markdown copy become links to its page. */
function localHref(href: string): string {
  const m = href.match(/^(?:https:\/\/shiptiffin\.com)?\/docs\/([a-z0-9-]+)\.md(#.*)?$/);
  if (m) return `/docs/${m[1]}${m[2] ?? ""}`;
  if (href.startsWith(SITE)) return href.slice(SITE.length) || "/";
  return href;
}

export async function loadDoc(name: string): Promise<Doc | undefined> {
  const page = PAGES.find((p) => p.name === name);
  if (!page) return undefined;
  const markdown = readFileSync(join(process.cwd(), "public", "docs", `${name}.md`), "utf8");
  const hl = await highlighter();

  const headings: Heading[] = [];
  const seen = new Map<string, number>();
  let title = page.title;

  const marked = new Marked({ gfm: true });
  marked.use({
    renderer: {
      heading({ tokens, depth, text }) {
        const inner = this.parser.parseInline(tokens);
        if (depth === 1) {
          title = plain(text);
          return ""; // the page draws its own title
        }
        let id = slug(plain(text));
        const n = seen.get(id) ?? 0;
        seen.set(id, n + 1);
        if (n) id = `${id}-${n}`;
        if (depth === 2 || depth === 3) headings.push({ id, text: plain(text), depth });
        return `<h${depth} id="${id}"><a class="anchor" href="#${id}" aria-hidden="true" tabindex="-1">#</a>${inner}</h${depth}>\n`;
      },
      code({ text, lang }) {
        const l = (lang ?? "").split(/\s/)[0];
        const known = (LANGS as readonly string[]).includes(l) ? l : "text";
        const html = hl.codeToHtml(text.replace(/\n$/, ""), {
          lang: known,
          themes: { light: "github-light", dark: "github-dark" },
          defaultColor: false,
        });
        return `<div class="doc-code">${html}</div>\n`;
      },
      link({ href, title: t, tokens }) {
        const inner = this.parser.parseInline(tokens);
        const local = localHref(href);
        const ext = /^https?:\/\//.test(local);
        return `<a href="${escape(local)}"${t ? ` title="${escape(t)}"` : ""}${ext ? ' rel="noopener"' : ""}>${inner}</a>`;
      },
      table(token) {
        const head = token.header.map((c) => `<th${c.align ? ` style="text-align:${c.align}"` : ""}>${this.parser.parseInline(c.tokens)}</th>`).join("");
        const rows = token.rows
          .map((r) => `<tr>${r.map((c) => `<td${c.align ? ` style="text-align:${c.align}"` : ""}>${this.parser.parseInline(c.tokens)}</td>`).join("")}</tr>`)
          .join("\n");
        return `<div class="doc-table"><table><thead><tr>${head}</tr></thead><tbody>${rows}</tbody></table></div>\n`;
      },
      /* The guides are prose, not HTML: show a stray <tag> as text, drop comments. */
      html({ text }) {
        return text.trim().startsWith("<!--") ? "" : escape(text);
      },
    },
  });

  const html = await marked.parse(markdown);
  return { page, title, updated: DATES[name]?.updated ?? "", html, headings, markdown };
}

/** The guide before and after this one, in reading order. */
export function neighbours(name: string): { prev?: DocPage; next?: DocPage } {
  const i = PAGES.findIndex((p) => p.name === name);
  return { prev: PAGES[i - 1], next: PAGES[i + 1] };
}

/** "2026-10-09" as "9 October 2026". */
export function longDate(iso: string): string {
  return new Date(`${iso}T12:00:00Z`).toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" });
}
