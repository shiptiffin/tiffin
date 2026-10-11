import type { MetadataRoute } from "next";
import DATES from "@/lib/docs-dates.json";
import { PAGES } from "@/lib/llms";

export const dynamic = "force-static";

const SITE = "https://shiptiffin.com";

/* lastModified is the day a page's content last changed; Google ignores changefreq and priority. */
const PAGES_UPDATED: [string, string][] = [
  ["/", "2026-10-11"],
  ["/start", "2026-10-09"],
  ["/agents", "2026-10-10"],
  ["/open-source", "2026-10-10"],
  ["/security", "2026-10-10"],
  ["/compare", "2026-10-10"],
  ["/compare/coolify", "2026-10-10"],
  ["/compare/vercel", "2026-10-10"],
  ["/compare/supabase", "2026-10-10"],
  ["/pricing", "2026-10-10"],
  ["/guides", "2026-10-10"],
  ["/guides/deploy-nextjs-to-hetzner", "2026-10-10"],
  ["/guides/move-from-vercel", "2026-10-10"],
  ["/privacy", "2026-10-08"],
  ["/terms", "2026-10-08"],
];

export default function sitemap(): MetadataRoute.Sitemap {
  const dates = DATES as Record<string, { updated: string }>;
  const newest = Object.values(dates).map((d) => d.updated).sort().at(-1);
  return [
    ...PAGES_UPDATED.map(([path, lastModified]) => ({ url: `${SITE}${path}`, lastModified })),
    { url: `${SITE}/docs`, lastModified: newest },
    ...PAGES.map((p) => ({ url: `${SITE}/docs/${p.name}`, lastModified: dates[p.name]?.updated })),
  ];
}
