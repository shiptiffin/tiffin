// /guides: the index of hand-written tutorials. The list lives in guide-parts.tsx
// (GUIDES), which the guides' "More guides" and their article data read too.
import Link from "next/link";
import { longDate } from "@/lib/docs";
import { pageMeta } from "@/lib/meta";
import { CtaBand, PageHead } from "../kit";
import { GUIDES } from "./guide-parts";

export const dynamic = "force-static";

export const metadata = pageMeta({
  title: "Guides: Next.js on your own server",
  description:
    "Step-by-step guides for running Next.js apps on your own Hetzner server with Tiffin: deploying with Postgres, sign-in and HTTPS, and moving off Vercel.",
  path: "/guides",
});

export default function Guides() {
  const updated = GUIDES.map((g) => g.updated).sort().at(-1);
  return (
    <>
      <PageHead kicker="Guides" title="Guides" crumbs={[["Guides", "/guides"]]} updated={updated}>
        <p>
          Step-by-step, with the real commands. For every option and limit, see the{" "}
          <Link href="/docs">docs</Link>.
        </p>
      </PageHead>

      <div className="band">
        <div className="wrap">
          <ul className="guide-list">
            {GUIDES.map((g) => (
              <li key={g.slug}>
                <h2>
                  <Link href={`/guides/${g.slug}`}>{g.title}</Link>
                </h2>
                <p>{g.description}</p>
                <p className="guide-list-meta">
                  Updated <time dateTime={g.updated}>{longDate(g.updated)}</time>
                </p>
              </li>
            ))}
          </ul>
        </div>
      </div>

      <CtaBand title="Start with one box">
        $19 a month per box, plus your server at Hetzner&rsquo;s price. Or run it yourself, free.
      </CtaBand>
    </>
  );
}
