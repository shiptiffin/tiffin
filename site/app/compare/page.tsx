// /compare: the comparisons, one honest line each. Each page has its sources and dates.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, PageHead, Points, Section } from "../kit";
import { FEE, READ_ISO } from "./facts";
import "./compare.css";

export const metadata = pageMeta({
  title: "ShipTiffin vs Coolify, Vercel and Supabase",
  absoluteTitle: true,
  description:
    "How ShipTiffin compares with Coolify, Vercel and Supabase: prices read on each vendor's site and dated, and when the other tool is the better choice.",
  path: "/compare",
});
export const dynamic = "force-static";

const PAGES: { href: string; name: string; line: string }[] = [
  {
    href: "/compare/coolify",
    name: "ShipTiffin vs Coolify",
    line: "Coolify is a free deploy panel with templates you run yourself. ShipTiffin costs more and comes with sign-in, email, analytics, error tracking and point-in-time backups, kept up to date for you.",
  },
  {
    href: "/compare/vercel",
    name: "ShipTiffin vs Vercel",
    line: "For several Next.js apps: one flat bill on a server you own, against Vercel's global network and scaling. Costs for 1, 5 and 20 apps.",
  },
  {
    href: "/compare/supabase",
    name: "Self-hosted Supabase alternative",
    line: "Postgres, sign-in, files and KV for every project, with no per-project fee and no pausing. No realtime, edge functions or generated APIs.",
  },
];

export default function Compare() {
  return (
    <div className="compare-page">
      <PageHead kicker="Compare" title="How ShipTiffin compares" crumbs={[["Compare", "/compare"]]} updated={READ_ISO}>
        <p>The tools people use instead, side by side, with what each one does better.</p>
      </PageHead>

      <div className="wrap compare-answer">
        <Answer>
          <p>
            ShipTiffin puts every app and its backend on one server in your own Hetzner account, for ${FEE} a month per box
            plus the server. These pages set it against Coolify, Vercel and Supabase, with each vendor&rsquo;s prices read on
            its own site and dated, and a list of when the other tool is the better choice.
          </p>
        </Answer>
      </div>

      <Section id="pages" title="The comparisons">
        <ul className="compare-list">
          {PAGES.map((p) => (
            <li key={p.href}>
              <Link href={p.href}>{p.name}</Link>
              <p>{p.line}</p>
            </li>
          ))}
        </ul>
      </Section>

      <Section id="how" title="How these pages are written" tone="sunk">
        <Points
          items={[
            [
              "Prices from the vendor",
              "Every price for another tool links to that vendor's own page, with the day we read it. Prices change; check the link.",
            ],
            [
              "When the other tool fits better",
              "Each page has a list of the cases where you should pick the other tool. Some of them are common.",
            ],
            [
              "Our facts from our docs",
              <>
                What ShipTiffin does comes from the docs. Every known gap is on{" "}
                <Link href="/docs/limits">What works and what doesn&rsquo;t</Link>.
              </>,
            ],
            [
              "One server, said plainly",
              <>
                A box is one server, and ShipTiffin is before version 1.0. If the server has a hardware fault, its apps are
                down until it is back. <Link href="/docs/managed">Managed boxes</Link>.
              </>,
            ],
          ]}
        />
      </Section>

      <CtaBand title="All your apps. One box. One price.">
        Setup takes about five minutes in your own Hetzner account. 14-day money-back.
      </CtaBand>
    </div>
  );
}
