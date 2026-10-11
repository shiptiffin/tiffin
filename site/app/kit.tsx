// The building blocks of the site's own pages (pricing, compare, agents, guides...).
// Server components only: they ship no JavaScript. Styles in kit.css.
//
// House rules for every page built from these:
// - One H1 (PageHead). Sections are H2s with at most two sentences under them.
// - Every number from another vendor carries its source and the day it was read (FactTable note).
// - Plain words; show the mechanism, not adjectives. No "seamless", "supercharge", "elevate".
import Link from "next/link";
import type { ReactNode } from "react";
import { longDate } from "@/lib/docs";
import { jsonLd } from "@/lib/meta";
import "./kit.css";

const SITE = "https://shiptiffin.com";

/** Where the page sits, as a line above its title and as BreadcrumbList data. */
export function Crumbs({ items }: { items: [label: string, path: string][] }) {
  const all: [string, string][] = [["Home", "/"], ...items];
  const ld = {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    itemListElement: all.map(([name, path], i) => ({ "@type": "ListItem", position: i + 1, name, item: `${SITE}${path}` })),
  };
  return (
    <>
      <nav className="kit-crumbs" aria-label="Breadcrumb">
        <ol>
          {all.map(([label, path], i) => (
            <li key={path}>
              {i < all.length - 1 ? <Link href={path}>{label}</Link> : <span aria-current="page">{label}</span>}
            </li>
          ))}
        </ol>
      </nav>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: jsonLd(ld) }} />
    </>
  );
}

/** The top of a page: an optional kicker, the H1, a lede and optional actions. */
export function PageHead({
  kicker,
  title,
  children,
  actions,
  updated,
  crumbs,
}: {
  kicker?: string;
  title: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
  /** ISO date of the last real change to the page's facts. */
  updated?: string;
  crumbs?: [string, string][];
}) {
  return (
    <header className="kit-head">
      <div className="wrap">
        {crumbs ? <Crumbs items={crumbs} /> : null}
        {kicker ? <p className="kicker">{kicker}</p> : null}
        <h1 className="kit-title">{title}</h1>
        {children ? <div className="kit-lede">{children}</div> : null}
        {actions ? <div className="actions">{actions}</div> : null}
        {updated ? <Updated date={updated} /> : null}
      </div>
    </header>
  );
}

export function Updated({ date }: { date: string }) {
  return (
    <p className="kit-updated">
      Updated <time dateTime={date}>{longDate(date)}</time>
    </p>
  );
}

/** A band of the page with an H2. `wide` drops the reading-width limit on the body. */
export function Section({
  id,
  title,
  sub,
  kicker,
  children,
  tone,
}: {
  id?: string;
  title: ReactNode;
  sub?: ReactNode;
  kicker?: string;
  children?: ReactNode;
  tone?: "sunk";
}) {
  const hid = id ? `${id}-title` : undefined;
  return (
    <section id={id} className={`band kit-section${tone ? ` kit-${tone}` : ""}`} aria-labelledby={hid}>
      <div className="wrap">
        <div className="section-head">
          {kicker ? <p className="kicker">{kicker}</p> : null}
          <h2 id={hid} className="h2">
            {title}
          </h2>
          {sub ? <p className="section-sub">{sub}</p> : null}
        </div>
        {children}
      </div>
    </section>
  );
}

/** The short answer, first: what search engines and AI answers quote. Two to four sentences. */
export function Answer({ children, label = "In short" }: { children: ReactNode; label?: string }) {
  return (
    <div className="kit-answer">
      <p className="kit-answer-label">{label}</p>
      <div className="kit-answer-body">{children}</div>
    </div>
  );
}

/** A plain fact table. The first column is the row's name. `note` says where the facts come from. */
export function FactTable({
  caption,
  head,
  rows,
  note,
  highlight,
}: {
  caption: string;
  head: ReactNode[];
  rows: ReactNode[][];
  note?: ReactNode;
  /** Index of the column to mark as ours. */
  highlight?: number;
}) {
  return (
    <figure className="kit-table">
      <div className="kit-table-scroll" tabIndex={0} role="region" aria-label={caption}>
        <table>
          <caption className="sr-only">{caption}</caption>
          <thead>
            <tr>
              {head.map((h, i) => (
                <th key={i} scope="col" className={i === highlight ? "ours" : undefined}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                {r.map((c, j) =>
                  j === 0 ? (
                    <th key={j} scope="row">
                      {c}
                    </th>
                  ) : (
                    <td key={j} className={j === highlight ? "ours" : undefined}>
                      {c}
                    </td>
                  ),
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {note ? <figcaption className="kit-table-note">{note}</figcaption> : null}
    </figure>
  );
}

/** Marks for fact tables: yes, partly, no. Always with words, never a bare symbol. */
export function Yes({ children = "Yes" }: { children?: ReactNode }) {
  return <span className="kit-mark kit-yes">{children}</span>;
}
export function Partly({ children = "Partly" }: { children?: ReactNode }) {
  return <span className="kit-mark kit-partly">{children}</span>;
}
export function No({ children = "No" }: { children?: ReactNode }) {
  return <span className="kit-mark kit-no">{children}</span>;
}

/** Term and explanation pairs in a two-column grid: features, steps, promises. */
export function Points({ items, numbered }: { items: [ReactNode, ReactNode][]; numbered?: boolean }) {
  const List = numbered ? "ol" : "ul";
  return (
    <List className={`kit-points${numbered ? " kit-numbered" : ""}`}>
      {items.map(([k, v], i) => (
        <li key={i}>
          <h3>{k}</h3>
          <div>{v}</div>
        </li>
      ))}
    </List>
  );
}

/** Two lists side by side: when to pick us, when to pick the other. Honest pages get trusted. */
export function Fit({ ours, theirs }: { ours: [string, ReactNode[]]; theirs: [string, ReactNode[]] }) {
  return (
    <div className="kit-fit">
      {[ours, theirs].map(([title, items]) => (
        <div key={title}>
          <h3>{title}</h3>
          <ul>
            {items.map((x, i) => (
              <li key={i}>{x}</li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}

/** Code or a terminal transcript, static. Lines starting with "$ " are commands. */
export function Terminal({ title, lines }: { title: string; lines: string[] }) {
  return (
    <div className="code kit-terminal" role="group" aria-label={title}>
      <div className="code-bar">
        <span>{title}</span>
      </div>
      <pre>
        {lines.map((l, i) =>
          l.startsWith("$ ") ? (
            <span key={i} className="kit-cmd">
              <span className="t-p">$</span> {l.slice(2)}
              {"\n"}
            </span>
          ) : (
            <span key={i} className="kit-out">
              {l}
              {"\n"}
            </span>
          ),
        )}
      </pre>
    </div>
  );
}

/** Questions and answers, as <details>. */
export function Faq({ items }: { items: { q: string; a: ReactNode }[] }) {
  return (
    <div className="faq">
      {items.map(({ q, a }) => (
        <details key={q}>
          <summary>{q}</summary>
          <div className="faq-a">{a}</div>
        </details>
      ))}
    </div>
  );
}

/** The closing band: a title, a line, and the way in. */
export function CtaBand({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <section className="band cta kit-cta" aria-label="Get started">
      <div className="wrap cta-row">
        <div className="cta-copy">
          <h2 className="h2">{title}</h2>
          {children ? <p className="section-sub">{children}</p> : null}
        </div>
        <div className="actions">
          <Link className="btn btn-primary" href="/start">
            Get started
          </Link>
          <Link className="btn btn-quiet" href="/docs/quickstart">
            Read the quickstart
          </Link>
        </div>
      </div>
    </section>
  );
}
