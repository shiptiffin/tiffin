// Small pieces the guides share: the list of guides (for the index and "more guides"),
// a static code block for files (the kit's Terminal is for shell transcripts), a step
// heading, and the guide's TechArticle data. Server components only. Styles in guides.css.
import Link from "next/link";
import type { ReactNode } from "react";
import { jsonLd } from "@/lib/meta";
import "./guides.css";

const SITE = "https://shiptiffin.com";

export const GUIDES: { slug: string; title: string; description: string; updated: string }[] = [
  {
    slug: "deploy-nextjs-to-hetzner",
    title: "Deploy Next.js to Hetzner, with Postgres, auth and HTTPS",
    description:
      "A Next.js app on your own Hetzner server: get a box (managed or free), deploy with tiffin, then add Postgres, sign-in and your own domain.",
    updated: "2026-10-10",
  },
  {
    slug: "move-from-vercel",
    title: "Move your Next.js apps off Vercel",
    description:
      "What carries over from Vercel unchanged (vercel.json, crons, env, monorepos), what to move by hand, what doesn't carry over, and a checklist.",
    updated: "2026-10-10",
  },
];

/** A file, shown as it is. `title` is the file's name. */
export function Code({ title, children }: { title: string; children: string }) {
  return (
    <div className="code guide-code" role="group" aria-label={title}>
      <div className="code-bar">
        <span>{title}</span>
      </div>
      <pre>{children}</pre>
    </div>
  );
}

/** One step of a guide: an H2 with an anchor, then its body. */
export function Step({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} className="guide-step" aria-labelledby={`${id}-title`}>
      <h2 id={`${id}-title`}>
        <a href={`#${id}`}>{title}</a>
      </h2>
      {children}
    </section>
  );
}

/** "On this page": the steps, as links. */
export function Toc({ items }: { items: [id: string, label: string][] }) {
  return (
    <nav className="guide-toc" aria-label="On this page">
      <p className="guide-toc-title">On this page</p>
      <ol>
        {items.map(([id, label]) => (
          <li key={id}>
            <a href={`#${id}`}>{label}</a>
          </li>
        ))}
      </ol>
    </nav>
  );
}

/** The other guides, at the foot of a guide. */
export function MoreGuides({ current }: { current: string }) {
  const others = GUIDES.filter((g) => g.slug !== current);
  if (others.length === 0) return null;
  return (
    <aside className="guide-more" aria-label="More guides">
      <p className="guide-toc-title">More guides</p>
      <ul>
        {others.map((g) => (
          <li key={g.slug}>
            <Link href={`/guides/${g.slug}`}>{g.title}</Link>
          </li>
        ))}
      </ul>
    </aside>
  );
}

/** TechArticle data for a guide (the BreadcrumbList comes from PageHead's crumbs). */
export function GuideLd({ slug }: { slug: string }) {
  const g = GUIDES.find((x) => x.slug === slug)!;
  const ld = {
    "@context": "https://schema.org",
    "@type": "TechArticle",
    headline: g.title,
    description: g.description,
    url: `${SITE}/guides/${g.slug}`,
    dateModified: g.updated,
    isPartOf: { "@type": "WebSite", name: "ShipTiffin", url: SITE },
    publisher: { "@type": "Organization", name: "ShipTiffin", url: SITE },
  };
  return <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: jsonLd(ld) }} />;
}
