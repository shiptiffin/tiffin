// /compare/supabase: a self-hosted Supabase alternative. Supabase facts were read on supabase.com
// and its docs on READ (facts.ts); ours come from docs/guide (data.md, auth.md, storage.md,
// queues.md, limits.md). Say plainly what Supabase has that the box doesn't.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, FactTable, Faq, Fit, No, PageHead, Points, Section, Yes } from "../../kit";
import { BOXES, FEE, FOUNDING, READ, READ_ISO, SRC, usd } from "../facts";
import "../compare.css";

export const metadata = pageMeta({
  title: "Self-hosted Supabase alternative: Postgres, sign-in, files",
  absoluteTitle: true,
  description:
    "Postgres, sign-in, files and KV for every project on a server you own: $19 a month, no per-project fee, no pausing. Plus what Supabase has that we don't.",
  path: "/compare/supabase",
  type: "article",
});
export const dynamic = "force-static";

const UPDATED = READ_ISO;

/* Supabase Pro: $25 with $10 of compute credit, which covers one Micro project; each more
   Micro project adds $10. PITR: about $100 a month per project for 7 days. Read on READ. */
const PRO = 25;
const MICRO = 10;
const PITR = 100;
const supabase = (projects: number) => PRO + (projects - 1) * MICRO;

const SIZES = [
  { projects: 1, box: BOXES.cx23 },
  { projects: 5, box: BOXES.cx33 },
  { projects: 20, box: BOXES.cx43 },
] as const;

function Src({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <a href={href} rel="noopener">
      {children}
    </a>
  );
}

export default function CompareSupabase() {
  return (
    <div className="compare-page">
      <PageHead
        kicker="ShipTiffin vs Supabase"
        title="A self-hosted Supabase alternative"
        crumbs={[
          ["Compare", "/compare"],
          ["Supabase", "/compare/supabase"],
        ]}
        updated={UPDATED}
      >
        <p>For people with many small projects, each needing a database, sign-in and file storage.</p>
      </PageHead>

      <div className="wrap compare-answer">
        <Answer>
          <p>
            ShipTiffin gives every project its own Postgres 18 database, sign-in, S3-compatible file storage and a KV store,
            next to its apps, on one server in your own Hetzner account. It costs ${FEE} a month however many projects you
            run, and nothing pauses for inactivity.
          </p>
          <p>
            Supabase has more: realtime, edge functions, APIs generated from your schema and a large ecosystem. On its cloud,
            each Pro project adds its own compute cost, and free projects pause after a week without activity.
          </p>
        </Answer>
      </div>

      <Section id="differences" title="The real differences">
        <FactTable
          caption="ShipTiffin and Supabase compared"
          highlight={1}
          head={["", "ShipTiffin", "Supabase"]}
          rows={[
            [
              "Where it runs",
              "One server in your own Hetzner account.",
              "Supabase's cloud. It can also be self-hosted with Docker.",
            ],
            [
              "Price",
              <>
                ${FEE} a month per box (${FOUNDING} for the first 100 customers), plus the server at Hetzner&rsquo;s price.
                No per-project fee.
              </>,
              "Free for 2 active projects. Pro from $25 a month, with $10 of compute credit; each more project adds its own compute, from $10 a month.",
            ],
            [
              "Quiet projects",
              "Never paused. A production app sleeps only if you ask it to; its data stays up.",
              "Free projects are paused after 1 week of inactivity. Paid projects are not.",
            ],
            [
              "Database",
              "Postgres 18 per project, with pgvector and pg_cron, and a copy-on-write branch for every preview.",
              "Postgres per project, with pgvector and REST and GraphQL APIs generated from your schema.",
            ],
            [
              "Sign-in",
              "Better Auth: email and password, magic links, one-time codes, passkeys, two-step sign-in, organizations. Google is tested; GitHub, Apple and 9 more are wired up.",
              "Email and password, magic links, phone codes and social logins, with access control through Row Level Security.",
            ],
            [
              "Files",
              "S3-compatible buckets, uploads straight from the browser, image resizing.",
              "Object storage with a CDN and image transformations.",
            ],
            [
              "KV store",
              <Yes key="y">Valkey per project, with an Upstash-compatible REST endpoint</Yes>,
              <No key="n">Not among its products</No>,
            ],
            [
              "Realtime",
              <No key="n">No realtime service; a job&rsquo;s progress can stream to the browser</No>,
              <Yes key="y">Database changes, presence and messages over WebSockets</Yes>,
            ],
            [
              "Server code",
              "Your apps on the box (Next.js, Hono, FastAPI and more), plus queues, crons and durable workflows.",
              "Edge Functions: serverless TypeScript deployed globally.",
            ],
            [
              "App hosting",
              <Yes key="y">Included</Yes>,
              <No key="n">Host your apps elsewhere</No>,
            ],
            [
              "Restore to a moment",
              "Included: Postgres to any second of the last 7 days (every project on the box together).",
              "An add-on: about $100 a month per project for 7 days.",
            ],
            [
              "Reaching the database",
              "Only from apps on the box, or from your computer through an SSH tunnel on a box you set up yourself.",
              "From anywhere, including the browser through its client libraries and APIs.",
            ],
            ["Licence", "AGPL-3.0 (the SDK is Apache-2.0)", "Apache-2.0"],
          ]}
          note={
            <>
              Supabase facts from <Src href={SRC.supabasePricing}>supabase.com/pricing</Src>,{" "}
              <Src href="https://supabase.com">supabase.com</Src>, its docs on{" "}
              <Src href="https://supabase.com/docs/guides/platform/manage-your-usage/point-in-time-recovery">
                point-in-time recovery
              </Src>{" "}
              and <Src href={SRC.supabaseSelfHost}>self-hosting</Src>, and the{" "}
              <Src href={SRC.supabaseRepo}>GitHub repository</Src>, all read {READ}. ShipTiffin facts from{" "}
              <Link href="/docs/data">Postgres, KV and backups</Link>, <Link href="/docs/auth">Sign-in</Link>,{" "}
              <Link href="/docs/storage">Files</Link> and <Link href="/docs/limits">What works and what doesn&rsquo;t</Link>.
            </>
          }
        />
      </Section>

      <Section
        id="cost"
        title="What 1, 5 and 20 projects cost"
        sub="Each project with its own database. A month in US dollars before tax. The ShipTiffin column also hosts the apps."
        tone="sunk"
      >
        <FactTable
          caption="Monthly cost of Supabase Pro projects, and of one ShipTiffin box"
          highlight={2}
          head={["", "Supabase Pro", "ShipTiffin"]}
          rows={[
            ...SIZES.map(({ projects, box }) => [
              `${projects} project${projects > 1 ? "s" : ""}`,
              <>
                <span className="compare-total">{usd(supabase(projects))}</span>
                {projects > 1 ? `: $${PRO}, plus $${MICRO} for each of ${projects - 1} more Micro projects` : ": the first Micro project is covered by the credit"}
              </>,
              <>
                <span className="compare-total">about {usd(FEE + box.total)}</span>: ShipTiffin ${FEE}, Hetzner {box.name} (
                {box.spec}) about ${box.total}
              </>,
            ]),
            [
              "Restore to a moment, 7 days",
              <>About ${PITR} a month for each project that has it</>,
              "Included",
            ],
            [
              "Billed on top",
              "Usage past each quota: more than 100,000 monthly active users, 8 GB of disk per project, 250 GB of egress.",
              "Nothing from us. Hetzner includes 20 TB of traffic a month in Europe (at least 1 TB in the US) and charges past that.",
            ],
          ]}
          note={
            <>
              Read {READ} on <Src href={SRC.supabasePricing}>supabase.com/pricing</Src>: a Micro project has 1 GB of
              memory on shared compute; the free plan allows 2 active projects. Server prices at Hetzner&rsquo;s prices from
              15 June 2026 (<Src href={SRC.hetznerPrices}>Hetzner&rsquo;s price list</Src>), plus IPv4 $0.60 (
              <Src href={SRC.hetznerIpv4}>Hetzner</Src>) and the 40 GB data volume, before VAT. The server sizes are our
              estimate for small projects with one app each. Supabase&rsquo;s column has no app hosting; ShipTiffin&rsquo;s
              does. With the founding price, take ${FEE - FOUNDING} off each ShipTiffin total (<Link href="/pricing">pricing</Link>).
            </>
          }
        />
      </Section>

      <Section
        id="missing"
        title="What ShipTiffin doesn't have"
        sub="If your app depends on one of these, stay on Supabase or plan the work to replace it."
      >
        <Points
          items={[
            [
              "Realtime",
              "No database change feeds, presence or broadcast channels. The box can stream a job's or workflow's progress to the page; for anything else, run a WebSocket server as one of your apps.",
            ],
            [
              "APIs generated from your schema",
              "No REST or GraphQL layer over the database. Your app's server code queries Postgres with DATABASE_URL, the way a Next.js route handler or server action already does.",
            ],
            [
              "Client libraries in the browser",
              <>
                Supabase&rsquo;s client calls are not understood here. Sign-in has its own SDK, files speak the S3 API, and
                data goes through your app. <Link href="/docs/auth#in-your-app">Sign-in in your app</Link>.
              </>,
            ],
            [
              "A database reachable from anywhere",
              <>
                Postgres and KV answer only apps on the box for now; a public address per project is planned.{" "}
                <Link href="/docs/limits#databases-and-kv-from-outside-the-box">Databases from outside the box</Link>.
              </>,
            ],
            [
              "Phone sign-in and every provider tested",
              <>
                No SMS codes. Google is tested end to end; the other providers are wired up but not tested yet.{" "}
                <Link href="/docs/limits#sign-in">Sign-in limits</Link>.
              </>,
            ],
            [
              "Many servers and regions",
              "Everything runs on one server in one place. A hardware fault means downtime until it is back, and a point-in-time restore takes every project back together.",
            ],
          ]}
        />
      </Section>

      <Section id="fit" title="Which one fits" tone="sunk">
        <Fit
          ours={[
            "ShipTiffin is the better fit when",
            [
              "you have many small projects and a per-project bill;",
              "you want the apps and their backend on one server you own;",
              "your projects go quiet for weeks and must not pause;",
              "your app talks to its data from server code;",
              "you want restore to a moment without paying for it per project.",
            ],
          ]}
          theirs={[
            "Supabase is the better fit when",
            [
              "you use realtime, edge functions or the generated APIs;",
              "your frontend talks to the database from the browser;",
              "you want a managed database with no server of your own at all;",
              "you need several regions, or room to grow past one server;",
              "you want a large ecosystem of guides, integrations and AI app builders.",
            ],
          ]}
        />
      </Section>

      <Section
        id="move"
        title="Moving from Supabase"
        sub="It is a code change, not a settings change: plan it one project at a time."
      >
        <Points
          numbered
          items={[
            [
              "Bring the data",
              <>
                Every project already has its own Postgres; add extensions such as pgvector in{" "}
                <code>tiffin.config.ts</code>. On a box you set up with <code>tiffin up</code>,{" "}
                <code>tiffin db tunnel</code> lets you load a <code>pg_dump</code> from your computer. A managed box closes
                SSH after setup, so the tunnel doesn&rsquo;t reach it. <Link href="/docs/data#from-your-computer">From your
                computer</Link>.
              </>,
            ],
            [
              "Swap the client calls",
              "Replace Supabase client queries with your own server code using DATABASE_URL, and keep row-level security where you rely on it.",
            ],
            [
              "Set up sign-in",
              <>
                Add <code>services: {"{ auth: {} }"}</code> and use <code>@shiptiffin/sdk/auth</code>. Users live in the
                project&rsquo;s own database. There is no importer for Supabase Auth users, so plan how existing users sign
                in again. <Link href="/docs/auth">Sign-in</Link>.
              </>,
            ],
            [
              "Copy the files",
              <>
                Buckets speak the S3 API at <code>s3.&lt;your domain&gt;</code>, so an S3 tool or SDK can copy files across. Apps get <code>S3_*</code> variables
                without setup. <Link href="/docs/storage">Files</Link>.
              </>,
            ],
          ]}
        />
      </Section>

      <Section id="faq" title="Questions" tone="sunk">
        <Faq
          items={[
            {
              q: "Why not self-host Supabase?",
              a: (
                <p>
                  You can: it is open source. Supabase&rsquo;s Docker guide asks for at least 4 GB of memory and 2 cores, runs
                  about a dozen services, and leaves secrets, HTTPS, email and updates to you (
                  <Src href={SRC.supabaseSelfHost}>self-hosting docs</Src>, read {READ}). ShipTiffin runs the database,
                  sign-in, files and KV next to your apps, with updates and off-server backups done for you.
                </p>
              ),
            },
            {
              q: "Does the database pause when nobody uses it?",
              a: (
                <p>
                  No. Postgres stays up. A production app sleeps only if you set <code>sleepAfter</code>, and wakes on the
                  next request; previews sleep after 15 idle minutes. <Link href="/docs/limits#sleep-and-wake">Sleep and
                  wake</Link>.
                </p>
              ),
            },
            {
              q: "Can one project use up the server?",
              a: (
                <p>
                  Give it a limit and it can&rsquo;t. A project&rsquo;s limit holds its apps, its share of Postgres, its KV
                  memory and its builds; a storage limit caps its database and files together.{" "}
                  <Link href="/docs/concepts#sharing-the-box">Sharing the box</Link>.
                </p>
              ),
            },
            {
              q: "Is row-level security available?",
              a: (
                <p>
                  Yes, it is plain Postgres. The box also adds a helper that keys row-level security on the signed-in
                  user&rsquo;s organization. <Link href="/docs/data#postgres">Postgres</Link>.
                </p>
              ),
            },
            {
              q: "How do backups compare?",
              a: (
                <p>
                  Every box backs up daily and every 6 hours, and Postgres can go back to any second of the last 7 days. On
                  a managed box the backups are copied off the server every 6 hours, encrypted with a key only you hold, and
                  kept 30 days. A restore to a moment takes every project back together.{" "}
                  <Link href="/docs/data#backups">Backups</Link>.
                </p>
              ),
            },
          ]}
        />
      </Section>

      <CtaBand title="Put your next project on your own box.">
        Setup takes about five minutes in your own Hetzner account. 14-day money-back.
      </CtaBand>
    </div>
  );
}
