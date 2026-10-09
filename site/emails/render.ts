// Turns an email component into what we send: HTML and its plain-text twin.
// React Email renders the HTML once; the text is that HTML without markup,
// read with a few rules of our own (below) so it reads like a written email.
import type { ReactElement } from "react";
import { render, toPlainText } from "react-email";
import { marker, MSO } from "./ui";

export type Rendered = { html: string; text: string };

type Builder = {
  openBlock(o?: { leadingLineBreaks?: number }): void;
  closeBlock(o?: { trailingLineBreaks?: number }): void;
  addInline(s: string, o?: { noWordTransform?: boolean }): void;
};
type Elem = { children: unknown[]; attribs: Record<string, string> };
type Walk = (nodes: unknown[], b: Builder) => void;

const options = {
  selectors: [
    // Headings keep their case.
    ...["h1", "h2", "h3"].map((selector) => ({ selector, options: { uppercase: false } })),
    // A fact per line: "Dashboard: dashboard.shop.shiptiffin.app".
    { selector: "tr.tf-fact", format: "block", options: { leadingLineBreaks: 1, trailingLineBreaks: 1 } },
    { selector: "td.tf-fl", format: "factLabel" },
    // Steps: "1. Add a passkey", the line under it right after.
    { selector: "td.tf-stepnum", format: "skip" },
    { selector: "p.tf-steptitle", format: "stepTitle" },
    { selector: "p.tf-stepbody", format: "block", options: { leadingLineBreaks: 1, trailingLineBreaks: 2 } },
    { selector: "hr", format: "skip" },
    { selector: "p.tf-item", format: "block", options: { leadingLineBreaks: 1, trailingLineBreaks: 1 } },
    // The button: "Open your dashboard: https://…".
    { selector: "a.tf-btn", format: "cta" },
    // Links in a sentence: "your account (https://…)"; the footer's show their words only.
    { selector: "a", options: { linkBrackets: ["(", ")"], hideLinkHrefIfSameAsText: true } },
    { selector: "a.tf-plain", format: "inline" },
  ],
  formatters: {
    factLabel: (elem: Elem, walk: Walk, b: Builder) => {
      walk(elem.children, b);
      b.addInline(": ");
    },
    stepTitle: (elem: Elem, walk: Walk, b: Builder) => {
      b.openBlock({ leadingLineBreaks: 2 });
      b.addInline(`${elem.attribs["data-n"]}. `);
      walk(elem.children, b);
      b.closeBlock({ trailingLineBreaks: 1 });
    },
    cta: (elem: Elem, walk: Walk, b: Builder) => {
      b.openBlock({ leadingLineBreaks: 2 });
      walk(elem.children, b);
      b.addInline(`: ${elem.attribs.href}`, { noWordTransform: true });
      b.closeBlock({ trailingLineBreaks: 2 });
    },
  },
};

export async function renderEmail(email: ReactElement): Promise<Rendered> {
  let html = await render(email);
  const text =
    toPlainText(html.replace(/%%mso:\w+%%/g, ""), options as Parameters<typeof toPlainText>[1])
      .replace(/[ \t]+\n/g, "\n")
      .replace(/\n{3,}/g, "\n\n")
      .trim() + "\n";
  for (const k of Object.keys(MSO) as (keyof typeof MSO)[]) html = html.replace(marker(k), MSO[k]);
  return { html, text };
}
