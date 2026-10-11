// /compare/coolify: ShipTiffin vs Coolify. Coolify facts were read on coolify.io and its docs
// on READ (facts.ts); ours come from docs/guide (limits.md, data.md, managed.md, moving.md,
// concepts.md, quickstart.md). Answers the pains in notes/website/01-competitors-positioning.md §3.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, FactTable, Faq, Fit, No, PageHead, Points, Section, Yes } from "../../kit";
import { BOXES, FEE, FOUNDING, READ, READ_ISO, SRC } from "../facts";
import "../compare.css";

export const metadata = pageMeta({
  title: "ShipTiffin vs Coolify: the backend included, kept updated",
  absoluteTitle: true,
  description:
    "Coolify is a free deploy panel plus templates you run. ShipTiffin is $19 a month with sign-in, email, analytics, error tracking and PITR backups built in.",
  path: "/compare/coolify",
  type: "article",
});
export const dynamic = "force-static";

const UPDATED = READ_ISO;

function Src({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <a href={href} rel="noopener">
      {children}
    </a>
  );
}

export default function CompareCoolify() {
  return (
    <div className="compare-page">
      <PageHead
        kicker="Compare"
        title="ShipTiffin vs Coolify"
        crumbs={[
          ["Compare", "/compare"],
          ["Coolify", "/compare/coolify"],
        ]}
        updated={UPDATED}
      >
        <p>
          Both put your apps on a server you own, and both are open source. The difference is what comes with it, and who
          keeps it running.
        </p>
      </PageHead>

      <div className="wrap compare-answer">
        <Answer>
          <p>
            Coolify is a free, open-source deploy panel: builds, deploys and 280+ one-click services, which you add, wire up
            and maintain yourself. ShipTiffin is ${FEE} a month per box and comes with sign-in, email sending, analytics,
            error tracking, Postgres restore to any second of the last 7 days and a hard limit for each project, kept up to
            date for you.
          </p>
          <p>
            Pick Coolify for a free panel, many servers or any Docker Compose app. Pick ShipTiffin when you run many small
            apps that each need the same backend parts and you don&rsquo;t want to look after them.
          </p>
        </Answer>
      </div>

      <Section id="differences" title="The real differences" sub="What each one gives you on the day you start, side by side.">
        <FactTable
          caption="ShipTiffin and Coolify compared"
          highlight={1}
          head={["", "ShipTiffin", "Coolify"]}
          rows={[
            [
              "Price",
              <>
                ${FEE} a month per box (${FOUNDING} for the first 100 customers), plus the server at Hetzner&rsquo;s price.
                Self-hosting is free.
              </>,
              <>Self-hosted: free. Cloud: $5 a month for 2 servers, plus $3 for each more. Plus your servers.</>,
            ],
            [
              "Licence",
              <>AGPL-3.0 (the SDK is Apache-2.0)</>,
              <>Apache-2.0</>,
            ],
            [
              "Servers",
              "One box per subscription. Projects share it, and can move to another box.",
              "As many servers as you like, connected to one panel.",
            ],
            [
              "What you deploy",
              "Next.js first; also Hono, FastAPI, SvelteKit, Nuxt, React Router, Astro and static sites. Anything else from a Dockerfile. No Docker Compose.",
              "Nixpacks, Railpack, a Dockerfile or Docker Compose, plus 280+ one-click services.",
            ],
            [
              "Sign-in for your apps",
              <Yes key="y">Built in: passwords, magic links, passkeys, Google and more</Yes>,
              <No key="n">Not built in: add a service such as Logto or Authentik</No>,
            ],
            [
              "Email from your apps",
              <Yes key="y">Every project sends through your provider, with a test inbox</Yes>,
              <No key="n">Alerts about Coolify itself; app email is yours to set up</No>,
            ],
            [
              "Analytics and error tracking",
              <Yes key="y">Built in: cookieless analytics, Sentry-compatible errors, logs, alerts</Yes>,
              <No key="n">Services you add and run, such as Umami and GlitchTip</No>,
            ],
            [
              "Backups",
              "Daily, plus every 6 hours. Postgres back to any second of the last 7 days. A project exports to one file. Managed: copied off the server, encrypted with your key, kept 30 days.",
              "Scheduled database dumps and volume archives, sent to S3 if you set one up. Its backup docs don't mention point-in-time restore.",
            ],
            [
              "Limits",
              "One hard limit per project: its apps (production and previews), its share of Postgres, KV memory and builds.",
              "CPU and memory caps per container, off by default.",
            ],
            [
              "Updates",
              "Managed: the box installs signed releases within about an hour. Daily OS security updates on every box.",
              "Cloud: Coolify itself is updated for you. The servers you connect are yours to patch.",
            ],
            [
              "Coding agents",
              "MCP server. Every change is a plan, then apply; History records who and why; most changes can be undone.",
              "MCP server for Claude, Cursor or any MCP client.",
            ],
            ["Maturity", "New, before version 1.0.", "Mature: 62.8k GitHub stars, 3,641+ customers on Cloud."],
          ]}
          note={
            <>
              Coolify facts from <Src href={SRC.coolifyPricing}>coolify.io/pricing</Src>,{" "}
              <Src href={SRC.coolifyHome}>coolify.io</Src>, its docs on{" "}
              <Src href={SRC.coolifyDbBackups}>database backups</Src>,{" "}
              <Src href={SRC.coolifyVolumeBackups}>volume backups</Src> and{" "}
              <Src href={SRC.coolifyServices}>services</Src>, and the <Src href={SRC.coolifyRepo}>GitHub repository</Src>,
              all read {READ}. ShipTiffin facts from <Link href="/docs/limits">What works and what doesn&rsquo;t</Link>,{" "}
              <Link href="/docs/data#backups">Backups</Link> and <Link href="/docs/managed">Managed boxes</Link>.
            </>
          }
        />
      </Section>

      <Section
        id="cost"
        title="Why $19 when Coolify Cloud is $5"
        sub="Coolify Cloud runs the panel. Everything you deploy with it is still yours to assemble and look after."
        tone="sunk"
      >
        <div className="compare-prose">
          <p>
            On the smallest server, ShipTiffin is ${FEE} plus about ${BOXES.cx23.total} to Hetzner: about $
            {FEE + BOXES.cx23.total} a month. Coolify Cloud on the same server is about ${5 + BOXES.cx23.total}. Self-hosted
            Coolify is just the server, though Coolify itself needs 2 CPU cores and 2 GB of memory to run (
            <Src href={SRC.coolifyInstall}>its install docs</Src>, read {READ}).
          </p>
          <p>
            The $14 difference (<Link href="/pricing">what the fee covers</Link>) pays for parts you would otherwise add and
            maintain one by one:
          </p>
        </div>
        <div className="compare-after">
          <Points
            items={[
              [
                "Sign-in",
                <>
                  Better Auth on the box for every app, with users in the project&rsquo;s own Postgres.{" "}
                  <Link href="/docs/auth">How sign-in works</Link>.
                </>,
              ],
              [
                "Email",
                <>
                  Every project sends through the provider you connect once, with a test inbox until then.{" "}
                  <Link href="/docs/email">Sending email</Link>.
                </>,
              ],
              [
                "Analytics and error tracking",
                <>
                  Cookieless visits counted on the box, and a Sentry DSN for every app.{" "}
                  <Link href="/docs/analytics">Analytics</Link>, <Link href="/docs/observe">monitoring</Link>.
                </>,
              ],
              [
                "Backups you can go back in time with",
                <>
                  Postgres to any second of the last 7 days, and copies off the server every 6 hours on a managed box.{" "}
                  <Link href="/docs/data#backups">Backups</Link>.
                </>,
              ],
              [
                "A hard limit per project",
                <>
                  One runaway app slows down inside its own limit instead of taking the others with it.{" "}
                  <Link href="/docs/concepts#sharing-the-box">Sharing the box</Link>.
                </>,
              ],
              [
                "Someone else keeps it current",
                <>
                  Signed updates within about an hour, monitoring from outside the box and support by email.{" "}
                  <Link href="/security#updates">How updates reach your box</Link>.
                </>,
              ],
            ]}
          />
        </div>
      </Section>

      <Section
        id="pains"
        title="What people find hard with Coolify, and what ShipTiffin does"
        sub={
          <>
            The same four come up in Hacker News threads about Coolify on Hetzner (for example{" "}
            <Src href="https://news.ycombinator.com/item?id=43589794">on the dashboard</Src>,{" "}
            <Src href="https://news.ycombinator.com/item?id=43591646">on app backups</Src> and{" "}
            <Src href="https://news.ycombinator.com/item?id=46581078">on security</Src>). Here is what is different on a
            ShipTiffin box.
          </>
        }
      >
        <Points
          items={[
            [
              "“The dashboard is clunky”",
              <>
                Taste is personal, so judge for yourself. What differs: every change is a plan you read, then apply, and
                History shows who changed what and why. Most changes can be undone. The same actions work from the CLI and
                from your coding agent over MCP (<Link href="/agents">connect your agent</Link>). Deploys switch traffic
                only after the new version passes its health check, with no dropped requests.
              </>,
            ],
            [
              "“How do I back up and restore one app?”",
              <>
                A project exports to one <code>.tiffin</code> file with its code, database, files and KV, and imports on any
                box. Database snapshots are per project. One caveat: restoring Postgres to a moment takes every project
                back together, since they share one cluster. <Link href="/docs/moving">Copying and moving</Link>.
              </>,
            ],
            [
              "“I always worry about security”",
              <>
                The box allows SSH keys only, opens only SSH, HTTP and HTTPS, bans brute-force IPs with CrowdSec and rate
                limits every app. On a managed box, SSH is closed to everyone but your own key, if you add one.{" "}
                <Link href="/security">Your key, your server, your data</Link>.
              </>,
            ],
            [
              "“I log in once a month to patch it”",
              <>
                Every box turns on daily OS security updates. A managed box also installs signed Tiffin releases by itself.
                A kernel update never reboots the server unless you set a time for it. <Link href="/docs/quickstart">Quickstart</Link>.
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
              "you run many small apps and each needs a database, sign-in, email and analytics;",
              "you want one flat price and a hard limit on each project;",
              "you want updates, backups off the server and monitoring done for you;",
              "your apps are Next.js or another framework on the supported list;",
              "a coding agent does much of the work and you want plans, History and undo.",
            ],
          ]}
          theirs={[
            "Coolify is the better fit when",
            [
              "you want to pay nothing but the server;",
              "you run apps across many servers from one panel;",
              "you deploy Docker Compose stacks or one-click services ShipTiffin doesn't have;",
              "you want a large community and years of use behind the tool;",
              "you need it to run on more than Ubuntu.",
            ],
          ]}
        />
      </Section>

      <Section
        id="move"
        title="Moving from Coolify"
        sub="Move one app at a time. The Coolify server can keep running until the last one is across."
      >
        <Points
          numbered
          items={[
            [
              "Get a box",
              <>
                Managed from <Link href="/start">shiptiffin.com/start</Link>, or free with <code>tiffin up</code> on Hetzner
                or any Ubuntu 24.04 or 26.04 server you can SSH into. <Link href="/docs/quickstart">Quickstart</Link>.
              </>,
            ],
            [
              "Deploy the app",
              <>
                Connect GitHub, or run <code>tiffin deploy</code>. A supported framework is detected; anything else builds
                from its Dockerfile. Docker Compose files are not read. <Link href="/docs/apps">Apps and deploys</Link>.
              </>,
            ],
            [
              "Bring the data",
              <>
                Every project has its own Postgres and S3-compatible buckets, and apps get <code>DATABASE_URL</code>,{" "}
                <code>REDIS_URL</code> and <code>S3_*</code> without setup. On a box you set up yourself,{" "}
                <code>tiffin db tunnel</code> lets you load a Postgres dump from your computer.{" "}
                <Link href="/docs/data">Postgres, KV and backups</Link>.
              </>,
            ],
            [
              "Point the domain",
              <>
                Two DNS records per domain, with HTTPS from Let&rsquo;s Encrypt. <Link href="/docs/domains">Domains</Link>.
              </>,
            ],
          ]}
        />
      </Section>

      <Section id="faq" title="Questions" tone="sunk">
        <Faq
          items={[
            {
              q: "Is ShipTiffin open source like Coolify?",
              a: (
                <p>
                  Yes. Tiffin, the software on the box, is AGPL-3.0 and public at{" "}
                  <Src href={SRC.tiffinRepo}>github.com/shiptiffin/tiffin</Src>; the SDK is Apache-2.0. Coolify is
                  Apache-2.0. ShipTiffin is the paid service that sets Tiffin up in your Hetzner account and keeps it
                  running.
                </p>
              ),
            },
            {
              q: "Can I run it for free, the way I run Coolify?",
              a: (
                <p>
                  Yes. <code>tiffin up</code> installs it on Hetzner or any Ubuntu 24.04 or 26.04 server you can SSH into,
                  with every part on the box. The managed extras (updates, backups off the server, outside monitoring, the
                  shiptiffin.app address, support) are what the ${FEE} pays for.
                </p>
              ),
            },
            {
              q: "Can one ShipTiffin box span several servers?",
              a: (
                <p>
                  No. A box is one server. You can run several boxes and move a project between them, but one app
                  doesn&rsquo;t run across servers. If the server has a hardware fault, its apps are down until it is back.
                </p>
              ),
            },
            {
              q: "What if I stop paying?",
              a: (
                <p>
                  The server and every app keep running, untouched. Updates and the managed extras stop, and your
                  name.shiptiffin.app address keeps working for 30 days.{" "}
                  <Link href="/docs/managed#if-you-stop-paying">If you stop paying</Link>.
                </p>
              ),
            },
            {
              q: "Is it as mature as Coolify?",
              a: (
                <p>
                  No. ShipTiffin is new and before version 1.0. Every known gap is listed on{" "}
                  <Link href="/docs/limits">What works and what doesn&rsquo;t</Link>.
                </p>
              ),
            },
          ]}
        />
      </Section>

      <CtaBand title="Try it on one app first.">
        Setup takes about five minutes in your own Hetzner account. 14-day money-back.
      </CtaBand>
    </div>
  );
}
