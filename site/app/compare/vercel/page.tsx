// /compare/vercel: ShipTiffin vs Vercel, for people with several Next.js apps. Vercel, Supabase
// and Sentry prices were read on their pricing pages on READ (facts.ts). The migration section
// follows docs/guide/apps.md "Coming from Vercel"; keep the two in step.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, FactTable, Faq, Fit, PageHead, Points, Section } from "../../kit";
import { BOXES, FEE, FOUNDING, READ, READ_ISO, SRC, usd } from "../facts";
import "../compare.css";

export const metadata = pageMeta({
  title: "ShipTiffin vs Vercel: all your Next.js apps, one flat bill",
  absoluteTitle: true,
  description:
    "Several Next.js apps on one server you own for $19 a month plus Hetzner, against Vercel Pro and a database per app. Costs for 1, 5 and 20 apps, sourced.",
  path: "/compare/vercel",
  type: "article",
});
export const dynamic = "force-static";

const UPDATED = READ_ISO;

/* Vercel Pro (one developer seat), Supabase Pro (first Micro project included, each more
   +$10 of compute), Sentry Team (billed yearly). All read on READ. */
const VERCEL_PRO = 20;
const SUPABASE_PRO = 25;
const SUPABASE_MICRO = 10;
const SENTRY_TEAM = 26;
const theirs = (apps: number) => VERCEL_PRO + SUPABASE_PRO + (apps - 1) * SUPABASE_MICRO + SENTRY_TEAM;
const LEAN = VERCEL_PRO + SUPABASE_PRO;

const SIZES = [
  { apps: 1, box: BOXES.cx23 },
  { apps: 5, box: BOXES.cx33 },
  { apps: 20, box: BOXES.cx43 },
] as const;

function Src({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <a href={href} rel="noopener">
      {children}
    </a>
  );
}

export default function CompareVercel() {
  return (
    <div className="compare-page">
      <PageHead
        kicker="Compare"
        title="ShipTiffin vs Vercel"
        crumbs={[
          ["Compare", "/compare"],
          ["Vercel", "/compare/vercel"],
        ]}
        updated={UPDATED}
      >
        <p>For people with several Next.js apps, each with its own database, sign-in and error tracking.</p>
      </PageHead>

      <div className="wrap compare-answer">
        <Answer>
          <p>
            On Vercel, each app adds to the bill: Vercel Pro plus a database project per app, plus usage. On ShipTiffin,
            every app and its Postgres, sign-in, files, KV, jobs, analytics and error tracking run on one server in your own
            Hetzner account, for ${FEE} a month plus the server. For 20 small apps that is about ${FEE + BOXES.cx43.total}{" "}
            against about ${theirs(20)}.
          </p>
          <p>
            You give up Vercel&rsquo;s global network and its scaling across many servers. The box reads your vercel.json,
            so most apps move without changes.
          </p>
        </Answer>
      </div>

      <Section id="differences" title="The real differences">
        <FactTable
          caption="ShipTiffin and Vercel compared"
          highlight={1}
          head={["", "ShipTiffin", "Vercel"]}
          rows={[
            [
              "Where your apps run",
              "One server in your own Hetzner account, in the location you pick.",
              "Vercel's own cloud, served from its global network.",
            ],
            [
              "Price",
              <>
                ${FEE} a month per box (${FOUNDING} for the first 100 customers), plus the server at Hetzner&rsquo;s price.
                No seats, no usage charges from us.
              </>,
              "Pro: $20 a month per developer seat, with $20 of usage included; usage past that is billed.",
            ],
            [
              "Database, sign-in, files, KV",
              "Included for every project: Postgres 18, Better Auth, S3-compatible buckets, Valkey.",
              "Not included: added from the Marketplace or other vendors, each billed on its own.",
            ],
            [
              "Error tracking and analytics",
              "Included: Sentry-compatible errors, logs, alerts, cookieless analytics.",
              "Observability and Web Analytics, with paid add-ons such as Observability Plus ($1.20 per 1M events) and Web Analytics Plus ($10 a month).",
            ],
            [
              "Spend control",
              "Our fee is flat. Each project can have a hard limit on memory, CPU, database share and KV.",
              "Spend management: new teams get a $200 on-demand budget by default, with alerts and an option to pause all projects at 100%.",
            ],
            [
              "Previews",
              "Every pull request gets its own address and its own copy-on-write database branch.",
              "Every branch and pull request gets a preview deployment.",
            ],
            [
              "Scale",
              "As far as one server goes. Resize to a bigger server from your account.",
              "Functions and the CDN scale across Vercel's infrastructure.",
            ],
            [
              "Open source",
              "Tiffin is AGPL-3.0. Self-host it free, or leave with your server and data.",
              "Next.js is open source; the Vercel platform is not.",
            ],
          ]}
          note={
            <>
              Vercel facts from <Src href={SRC.vercelPricing}>vercel.com/pricing</Src> and its docs on{" "}
              <Src href={SRC.vercelEnvironments}>environments</Src>, read {READ}. ShipTiffin facts from{" "}
              <Link href="/docs/limits">What works and what doesn&rsquo;t</Link>,{" "}
              <Link href="/docs/apps">Apps and deploys</Link> and <Link href="/docs/data">Postgres, KV and backups</Link>.
            </>
          }
        />
      </Section>

      <Section
        id="cost"
        title="What 1, 5 and 20 small apps cost"
        sub="A small Next.js app with its own database and sign-in, plus error tracking, a month in US dollars before tax."
        tone="sunk"
      >
        <FactTable
          caption="Monthly cost of small Next.js apps on Vercel with add-ons, and on one ShipTiffin box"
          highlight={2}
          head={["", "Vercel Pro and add-ons", "ShipTiffin"]}
          rows={[
            ...SIZES.map(({ apps, box }) => [
              `${apps} small app${apps > 1 ? "s" : ""}`,
              <>
                <span className="compare-total">{usd(theirs(apps))}</span>: Vercel Pro ${VERCEL_PRO}, Supabase Pro $
                {SUPABASE_PRO + (apps - 1) * SUPABASE_MICRO}
                {apps > 1 ? ` (${apps} projects)` : ""}, Sentry Team ${SENTRY_TEAM}
              </>,
              <>
                <span className="compare-total">about {usd(FEE + box.total)}</span>: ShipTiffin ${FEE}, Hetzner {box.name} (
                {box.spec}) about ${box.total}
              </>,
            ]),
            [
              "The leanest Vercel setup",
              <>
                <span className="compare-total">{usd(LEAN)}</span> at any count: every app shares one Supabase project, and
                Sentry&rsquo;s free plan (one user, 5k errors)
              </>,
              <>Every project keeps its own database, sign-in and limit</>,
            ],
            [
              "Billed on top",
              "Vercel usage past the $20 credit (data transfer past 1 TB from $0.15/GB, CDN requests past 10M from $2 per 1M), $20 per extra developer seat, Supabase usage past each quota.",
              "Nothing from us. Hetzner includes 20 TB of traffic a month in Europe (at least 1 TB in the US) and charges past that.",
            ],
          ]}
          note={
            <>
              Read {READ}: <Src href={SRC.vercelPricing}>Vercel</Src> (Pro, one developer seat, $20 included credit;
              Hobby is for non-commercial use only), <Src href={SRC.supabasePricing}>Supabase</Src> (Pro $25 with $10
              of compute credit, which covers one Micro project; each more Micro project adds $10),{" "}
              <Src href={SRC.sentryPricing}>Sentry</Src> (Team $26 a month billed yearly). Server prices at Hetzner&rsquo;s
              prices from 15 June 2026 (<Src href={SRC.hetznerPrices}>Hetzner&rsquo;s price list</Src>), plus IPv4 $0.60 (
              <Src href={SRC.hetznerIpv4}>Hetzner</Src>) and the 40 GB data volume, before VAT. The server sizes are our
              estimate: a small Next.js app settles around 270 MB of memory (<Link href="/docs/apps#nextjs">Next.js on the
              box</Link>), and 5 apps fit the smallest server but leave little room for previews. With the founding price,
              take ${FEE - FOUNDING} off each ShipTiffin total (<Link href="/pricing">pricing</Link>). Not counted on either side: email, KV and analytics.
            </>
          }
        />
      </Section>

      <Section
        id="give-up"
        title="What you give up"
        sub="One server in one place is a different trade from a global platform. Know it before you move."
      >
        <Points
          items={[
            [
              "A global network",
              "Vercel serves from many regions. Your box is in the one location you pick, so visitors far from it wait longer for each request.",
            ],
            [
              "Scale across many servers",
              "A traffic spike gets what one server has. You can resize to a bigger server, but one app never spreads across several.",
            ],
            [
              "One server, one point of failure",
              <>
                A hardware fault means downtime until the server is back. Backups are copied off a managed box every 6
                hours. <Link href="/docs/data#backups">Backups</Link>.
              </>,
            ],
            [
              "Vercel's polish",
              <>
                Vercel makes Next.js and has years of work in its dashboard and tooling. ShipTiffin is new and before
                version 1.0; every known gap is on <Link href="/docs/limits">What works and what doesn&rsquo;t</Link>.
              </>,
            ],
            [
              "An edge image optimizer",
              <>
                Each app optimizes its own images with sharp; there is no shared optimizer at the edge yet.{" "}
                <Link href="/docs/limits#caching-and-images">Caching and images</Link>.
              </>,
            ],
            [
              "Paying only for what runs",
              <>
                Apps on the box stay running unless you let them sleep, and a sleeping Next.js app takes about 0.9 s to
                wake. <Link href="/docs/limits#sleep-and-wake">Sleep and wake</Link>.
              </>,
            ],
          ]}
        />
      </Section>

      <Section id="fit" title="Which one fits" tone="sunk">
        <Fit
          ours={[
            "ShipTiffin is the better fit when",
            [
              "you have several small apps and a database bill per app;",
              "you want one flat price and a hard limit on each project;",
              "your visitors are mostly near one region;",
              "you want your apps and data on a server you own;",
              "a coding agent deploys for you and you want plans, History and undo.",
            ],
          ]}
          theirs={[
            "Vercel is the better fit when",
            [
              "your visitors are spread across the world and every millisecond counts;",
              "traffic can jump far past what one server handles;",
              "you want no server at all, not even one someone else runs for you;",
              "you rely on vercel.json settings the box doesn't read, such as functions or regions;",
              "you want a mature platform with years of production use behind it.",
            ],
          ]}
        />
      </Section>

      <Section
        id="move"
        title="Moving from Vercel"
        sub={
          <>
            The box reads what an app already has, so an app that deploys on Vercel deploys here unchanged in most cases.
            The details are in <Link href="/docs/apps#coming-from-vercel">Coming from Vercel</Link>.
          </>
        }
      >
        <Points
          numbered
          items={[
            [
              "Connect GitHub",
              <>
                Every push to the production branch goes live, every pull request gets a preview with one comment on it,
                and closing the pull request removes the preview. <Link href="/docs/apps#deploy-from-github">Deploy from
                GitHub</Link>.
              </>,
            ],
            [
              "Keep your vercel.json",
              <>
                It is read at every deploy: build and install commands, output folder, crons (called with{" "}
                <code>GET</code> and <code>CRON_SECRET</code>, as Vercel does), headers and redirects. Static sites also get
                rewrites, <code>cleanUrls</code> and <code>trailingSlash</code>. Not used: <code>has</code> and{" "}
                <code>missing</code> rules, rewrites to another site, <code>functions</code> and <code>regions</code>. The
                build log lists what was taken and what was not.
              </>,
            ],
            [
              "Monorepos work as on Vercel",
              "An app in a pnpm, Bun, npm or Yarn workspace installs with its workspace and builds in its own folder.",
            ],
            [
              "KV and workflows run unchanged",
              <>
                Apps using <code>@vercel/kv</code> or <code>@upstash/redis</code> get an Upstash-compatible endpoint on the
                box, and Vercel&rsquo;s Workflow DevKit runs on the project&rsquo;s Postgres.{" "}
                <Link href="/docs/data#moving-an-app-here-from-upstash-or-vercel-kv">Moving from Upstash or Vercel KV</Link>.
              </>,
            ],
            [
              "Move the database with the app",
              <>
                Postgres and KV are reachable only from apps on the box for now, so an app left on Vercel can&rsquo;t use
                them. Move an app and its data together.{" "}
                <Link href="/docs/limits#databases-and-kv-from-outside-the-box">Databases from outside the box</Link>.
              </>,
            ],
          ]}
        />
      </Section>

      <Section id="faq" title="Questions" tone="sunk">
        <Faq
          items={[
            {
              q: "Which Next.js versions work?",
              a: (
                <p>
                  Next.js is the first framework the box supports. With 16.2 or later the box adds its adapter: a cache
                  shared by every instance, so <code>revalidatePath</code> and <code>revalidateTag</code> reach all of them,
                  and client files served by the edge. Older versions build and run without it.{" "}
                  <Link href="/docs/apps#nextjs">Next.js on the box</Link>.
                </p>
              ),
            },
            {
              q: "Why not stay on Vercel's free Hobby plan?",
              a: (
                <p>
                  Vercel says its Hobby plan is for personal, non-commercial use, and Hobby accounts can&rsquo;t buy usage past
                  the caps (<Src href={SRC.vercelPricing}>vercel.com/pricing</Src>, read {READ}). For a side project that
                  stays personal, it may be all you need.
                </p>
              ),
            },
            {
              q: "Can I get a surprise bill?",
              a: (
                <p>
                  Not from us: the fee is flat, with no seats or usage charges. Hetzner bills the server, and charges for
                  traffic past what the server includes (20 TB a month in Europe, at least 1 TB in the US). Each project can
                  have a hard limit, so one busy app can&rsquo;t take the box from the others.
                </p>
              ),
            },
            {
              q: "What happens during a traffic spike or an attack?",
              a: (
                <p>
                  Every app has per-IP rate limits, CrowdSec bans scanners, and an under-attack mode puts a proof-of-work
                  challenge in front of every app. Past that, the box serves what one server can; there is no Cloudflare in
                  front of it yet. <Link href="/docs/protection">Protection</Link>.
                </p>
              ),
            },
            {
              q: "Can I leave later?",
              a: (
                <p>
                  Yes. The server is in your Hetzner account, Tiffin is open source, and a project exports to one file with
                  a <code>docker-compose.yml</code> and a plain SQL dump. <Link href="/docs/moving">Copying and moving</Link>.
                </p>
              ),
            },
          ]}
        />
      </Section>

      <CtaBand title="Move one app and compare the bill.">
        Setup takes about five minutes in your own Hetzner account. 14-day money-back.
      </CtaBand>
    </div>
  );
}
