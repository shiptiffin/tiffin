import type { Metadata } from "next";

// One place for each page's title, description, canonical and share card. Next merges
// metadata shallowly, so a page that set only a title would otherwise share the
// homepage's openGraph (its title and url). Every indexable page uses this.

export const SITE_NAME = "ShipTiffin";

/** The one sentence that describes ShipTiffin everywhere (site, llms.txt, GitHub). */
export const ENTITY =
  "ShipTiffin sets up Tiffin, open-source (AGPL-3.0) software, on a server in your own Hetzner account: apps (Next.js first), Postgres, KV, files, sign-in, email, jobs, analytics, error tracking and backups on one box, run by a CLI, a dashboard and an MCP server for coding agents.";

export function pageMeta(p: {
  title: string;
  description: string;
  path: string;
  /** The page's Markdown twin, for agents. */
  md?: string;
  /** Use the title as is, without " · ShipTiffin". */
  absoluteTitle?: boolean;
  type?: "website" | "article";
}): Metadata {
  return {
    title: p.absoluteTitle ? { absolute: p.title } : p.title,
    description: p.description,
    alternates: { canonical: p.path, ...(p.md ? { types: { "text/markdown": p.md } } : {}) },
    openGraph: {
      type: p.type ?? "website",
      siteName: SITE_NAME,
      locale: "en_US",
      title: p.title,
      description: p.description,
      url: p.path,
    },
    twitter: { card: "summary_large_image", title: p.title, description: p.description },
  };
}

/** JSON-LD for a <script> tag, with "<" escaped so it can't close the tag. */
export function jsonLd(data: unknown): string {
  return JSON.stringify(data).replace(/</g, "\\u003c");
}
