import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { DOC_PAGES, loadDoc, longDate, neighbours } from "@/lib/docs";
import { jsonLd, pageMeta } from "@/lib/meta";
import { DocsNav } from "../nav";

export const dynamic = "force-static";
export const dynamicParams = false;

export function generateStaticParams() {
  return DOC_PAGES.map((p) => ({ slug: p.name }));
}

export async function generateMetadata({ params }: PageProps<"/docs/[slug]">): Promise<Metadata> {
  const { slug } = await params;
  const doc = await loadDoc(slug);
  if (!doc) return {};
  return pageMeta({
    title: `${doc.title} · ShipTiffin docs`,
    absoluteTitle: true,
    description: doc.page.description,
    path: `/docs/${slug}`,
    md: `/docs/${slug}.md`,
    type: "article",
  });
}

export default async function DocPage({ params }: PageProps<"/docs/[slug]">) {
  const { slug } = await params;
  const doc = await loadDoc(slug);
  if (!doc) notFound();
  const { prev, next } = neighbours(slug);
  const ld = {
    "@context": "https://schema.org",
    "@type": "TechArticle",
    headline: doc.title,
    description: doc.page.description,
    url: `https://shiptiffin.com/docs/${slug}`,
    dateModified: doc.updated,
    isPartOf: { "@type": "WebSite", name: "ShipTiffin", url: "https://shiptiffin.com" },
    publisher: { "@type": "Organization", name: "ShipTiffin", url: "https://shiptiffin.com" },
  };
  const crumbs = {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    itemListElement: [
      { "@type": "ListItem", position: 1, name: "Docs", item: "https://shiptiffin.com/docs" },
      { "@type": "ListItem", position: 2, name: doc.title, item: `https://shiptiffin.com/docs/${slug}` },
    ],
  };

  return (
    <div className="wrap docs">
      <DocsNav current={slug} />
      <article className="doc" aria-labelledby="doc-title">
        <p className="doc-section">{doc.page.section}</p>
        <h1 id="doc-title" className="doc-title">
          {doc.title}
        </h1>
        <p className="doc-meta">
          Updated <time dateTime={doc.updated}>{longDate(doc.updated)}</time> ·{" "}
          <a href={`/docs/${slug}.md`}>View as Markdown</a>
        </p>
        <div className="doc-body" dangerouslySetInnerHTML={{ __html: doc.html }} />
        <nav className="doc-pager" aria-label="More guides">
          {prev ? (
            <Link href={`/docs/${prev.name}`} className="doc-pager-link">
              <span>Previous</span>
              {prev.title}
            </Link>
          ) : (
            <span />
          )}
          {next ? (
            <Link href={`/docs/${next.name}`} className="doc-pager-link doc-pager-next">
              <span>Next</span>
              {next.title}
            </Link>
          ) : null}
        </nav>
        <p className="doc-source">
          Something wrong or unclear?{" "}
          <a href={`https://github.com/shiptiffin/tiffin/blob/main/docs/guide/${slug}.md`}>Edit it on GitHub</a>.
        </p>
      </article>
      {doc.headings.length > 2 ? (
        <aside className="doc-toc" aria-labelledby="toc-title">
          <p id="toc-title" className="doc-toc-title">
            On this page
          </p>
          <ol>
            {doc.headings.map((h) => (
              <li key={h.id} className={h.depth === 3 ? "sub" : undefined}>
                <a href={`#${h.id}`}>{h.text}</a>
              </li>
            ))}
          </ol>
        </aside>
      ) : null}
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: jsonLd([ld, crumbs]) }} />
    </div>
  );
}
