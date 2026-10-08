import Link from "next/link";
import { DASHBOARD } from "./chrome";
import { EarlyAccessForm } from "./early-access-form";
import { EarlyAccessNext } from "./early-access-next";
import { Pricing } from "./pricing";

export const metadata = { alternates: { canonical: "/" } };
export const dynamic = "force-static";

/* The box, drawn: a stacked tin, one tier per project, each with its own limit.
   Illustrative projects, not customers. */
const TIERS = [
  { name: "shop", parts: "Web app · Database · Files · Sign-in", limit: "Up to 40%", enamel: "teal", fill: 0.4 },
  { name: "guestbook", parts: "Web app · Database · Email", limit: "512 MB", enamel: "leaf", fill: 0.18 },
  { name: "blog", parts: "Static site · Analytics", limit: "Automatic", enamel: "gold", fill: 0 },
  { name: "reports", parts: "Jobs · Database", limit: "Up to 10%", enamel: "plum", fill: 0.1 },
] as const;

function BoxDrawing() {
  return (
    <figure className="tin" aria-labelledby="tin-caption">
      <svg className="tin-handle" viewBox="0 0 220 64" aria-hidden="true">
        <path d="M30 64V40C30 18 46 6 70 6h80c24 0 40 12 40 34v24" />
      </svg>
      <div className="tin-lid">
        <span className="tin-lid-name">Your box</span>
        <span className="tin-lid-note">one server, yours alone</span>
      </div>
      <ol className="tin-tiers">
        {TIERS.map((t) => (
          <li key={t.name} className="tier" style={{ ["--enamel" as string]: `var(--enamel-${t.enamel})` }}>
            <div className="tier-top">
              <span className="tier-name">{t.name}</span>
              <span className="tier-limit">{t.limit}</span>
            </div>
            <div className="tier-parts">{t.parts}</div>
            <div className="tier-gauge" aria-hidden="true">
              {t.fill > 0 ? <span style={{ width: `${t.fill * 100}%` }} /> : <span className="auto" />}
            </div>
          </li>
        ))}
        <li className="tier tier-room">Room for the next one</li>
      </ol>
      <figcaption id="tin-caption" className="tin-caption">
        Each project is a tier with the limit you give it. One that hits its limit is held there; the rest keep
        running.
      </figcaption>
    </figure>
  );
}

/* What's in the box: the plain name, then what it is underneath. */
const PARTS = [
  ["Apps", "Next.js, Hono, FastAPI and more", "Deploy from GitHub or with git push. Roll back to any earlier deploy in one step."],
  ["Previews", "One per pull request", "Each pull request gets its own address and its own copy of the database."],
  ["Database", "Postgres 18", "One for each project, behind a connection pooler, with branches made in milliseconds."],
  ["KV", "Valkey, Redis-compatible", "For caches, sessions, rate limits and counters."],
  ["Files", "S3-compatible", "Private or public buckets, with image resizing."],
  ["Sign-in", "Better Auth", "Email and password, magic links, passkeys, and Google, GitHub and other providers."],
  ["Email", "SMTP and an API", "Send from any app. Until you connect a mail service, every message waits in a test inbox."],
  ["Jobs", "Queues, crons and workflows", "The box calls your app with each job and retries until it succeeds."],
  ["Analytics", "No cookies", "Visits, sources and your own events, counted on the box."],
  ["Error tracking", "Sentry-compatible", "Point any Sentry SDK at the box. Logs, traces and alerts sit beside it."],
  ["Backups", "Hourly", "Every database, file and setting, with restore drills that prove a backup works."],
  ["Domains", "HTTPS included", "Your own domain, with certificates issued and renewed for you."],
] as const;

/* What a project's limit holds back, besides its apps. From docs/guide/concepts.md. */
const HOLDS = [
  ["Apps", "Memory and CPU, across production and previews"],
  ["Database", "Its share of query time and connections"],
  ["KV", "Its share of memory"],
  ["Builds", "Their share of CPU; past it they slow down instead of failing"],
] as const;

const FAQ: { q: string; a: React.ReactNode }[] = [
  {
    q: "How much will it cost?",
    a: (
      <p>
        One flat monthly price per box, announced at launch. You won&rsquo;t pay per request or per project, so a
        busy week doesn&rsquo;t change the bill. People on the early-access list get founding prices.
      </p>
    ),
  },
  {
    q: "What happens when one project gets busy?",
    a: (
      <p>
        Without a limit, it grows into what the box has free, and slows down rather than failing when the box is
        busy. With a limit, it&rsquo;s held at it: its apps, database, cache and builds. Either way the other
        projects keep running, and the project&rsquo;s Usage page shows when a limit held it back.
      </p>
    ),
  },
  {
    q: "Can I leave?",
    a: (
      <p>
        Yes, at any time. Export any project to a single file with its code, data and files. Everything inside is a
        standard piece: Postgres, S3-compatible storage, a Redis-compatible store and SMTP, so it moves to any host
        that runs them. If you close your account, you get at least 30 days to export first.
      </p>
    ),
  },
  {
    q: "How are backups done?",
    a: (
      <p>
        Postgres is backed up in full every day and incrementally every hour, along with KV, files, email and the
        box&rsquo;s settings. Restore drills prove a backup works without touching anything live. A restore takes a
        safety backup first, and a deleted database or bucket is kept for 7 days in case you change your mind.
      </p>
    ),
  },
  {
    q: "What uptime can I expect?",
    a: (
      <p>
        We aim for 99.9% each month, outside a weekly maintenance window you choose, when updates and restarts
        happen. Your box is one server, so a hardware fault means downtime until it&rsquo;s back. That&rsquo;s why
        we don&rsquo;t suggest it yet for apps that must never go down.
      </p>
    ),
  },
  {
    q: "Where is my data?",
    a: (
      <p>
        On your box: a server of your own in a Hetzner data centre in Germany. What your apps store stays on it
        and isn&rsquo;t copied to a central ShipTiffin database. The <Link href="/privacy">privacy policy</Link> has
        the details.
      </p>
    ),
  },
  {
    q: "Which frameworks work?",
    a: (
      <p>
        Next.js, Hono, FastAPI, TanStack Start, SvelteKit, Nuxt, React Router, Astro and static sites are
        first-class. Other Bun, Node.js and Python servers run too, and anything else from a Dockerfile.
      </p>
    ),
  },
  {
    q: "Will it be open source?",
    a: <p>We may open it up later. Right now we&rsquo;re focused on the hosted service.</p>,
  },
];

const CONFIG = [
  `<span class="t-k">export default</span> defineConfig({`,
  `  project: <span class="t-s">"shop"</span>,`,
  `  apps: { web: { framework: <span class="t-s">"next"</span> } },`,
  `  services: { auth: {} },`,
  `  resources: { maxSharePercent: <span class="t-n">40</span> },`,
  `});`,
].join("\n");

const TERMINAL = [
  `<span class="t-p">$</span> tiffin plan`,
  `<span class="t-p">$</span> git push tiffin main`,
].join("\n");

const MCP = `<span class="t-p">$</span> claude mcp add tiffin -- tiffin mcp`;

export default function Home() {
  return (
    <>
      <section className="hero">
        <div className="wrap hero-grid">
          <div className="hero-copy">
            <p className="kicker">Early access</p>
            <h1 className="display">
              All your apps.
              <br />
              One server.
              <br />
              One price.
            </h1>
            <p className="lede">
              ShipTiffin gives you a server of your own with everything an app needs already on it: a database,
              sign-in, email, file storage, background jobs, analytics and backups. Run all your projects on it for
              one flat monthly price, and give each one a limit, so a busy project never turns into a surprise bill.
            </p>
            <div className="actions">
              <a className="btn btn-primary" href="#early-access">
                Get early access
              </a>
              <a className="btn btn-quiet" href={DASHBOARD}>
                Sign in to your box <span aria-hidden="true">→</span>
              </a>
            </div>
            <p className="fine">
              Simple monthly pricing, announced at launch. Early access members get founding prices.
            </p>
          </div>
          <BoxDrawing />
        </div>
      </section>

      <section id="about" className="band" aria-labelledby="about-title">
        <div className="wrap two-col">
          <h2 id="about-title" className="h2">
            What ShipTiffin does
          </h2>
          <div className="prose-lg">
            <p>
              ShipTiffin is a hosting service for web apps. Each customer gets a server of their own, which we call
              a box, and runs their apps on it. The box comes with the parts most apps need: a Postgres database,
              sign-in for the app&rsquo;s users, email sending, file storage, background jobs, logs and visit
              counts.
            </p>
            <p>
              Customers deploy their apps from GitHub, with <code>git push</code>, the <code>tiffin</code> command
              line or a coding agent, and manage them from the box&rsquo;s dashboard. It is built for developers and
              small teams who run several apps and want one flat price instead of a bill for each service.
            </p>
          </div>
        </div>
      </section>

      <section id="box" className="band" aria-labelledby="parts-title">
        <div className="wrap">
          <div className="section-head">
            <h2 id="parts-title" className="h2">
              What&rsquo;s in the box.
            </h2>
            <p className="section-sub">
              No separate accounts for the database, storage, email or analytics, and no separate bills. Each
              project gets its own, set up when you ask for it.
            </p>
          </div>
          <dl className="parts">
            {PARTS.map(([name, tech, text]) => (
              <div key={name} className="part">
                <dt>
                  {name} <span className="part-tech">{tech}</span>
                </dt>
                <dd>{text}</dd>
              </div>
            ))}
          </dl>
        </div>
      </section>

      <section id="how" className="band" aria-labelledby="how-title">
        <div className="wrap how-grid">
          <div>
            <h2 id="how-title" className="h2">
              How it works
            </h2>
            <ol className="steps">
              <li>
                <h3>We set up your box.</h3>
                <p>
                  One server per customer. Nobody else&rsquo;s apps run on it, and you sign in to its dashboard
                  with your own account.
                </p>
              </li>
              <li>
                <h3>Connect GitHub, or let your agent deploy.</h3>
                <p>
                  Pick a repository and every push to its main branch goes live, with a preview for each pull
                  request. Or deploy with <code>git push</code>, the <code>tiffin</code> command line, or your
                  coding agent.
                </p>
              </li>
              <li>
                <h3>Every project gets its own limits.</h3>
                <p>
                  Projects share the box on their own. Give any of them a ceiling, a share of the box or an amount
                  of memory and CPU, in the dashboard or in <code>tiffin.config.ts</code> next to your code.
                </p>
              </li>
            </ol>
          </div>
          <div className="code" role="group" aria-label="Example project">
            <div className="code-bar">
              <span>tiffin.config.ts</span>
            </div>
            <pre dangerouslySetInnerHTML={{ __html: CONFIG }} />
            <div className="code-bar code-bar-mid">
              <span>Terminal</span>
            </div>
            <pre dangerouslySetInnerHTML={{ __html: TERMINAL }} />
          </div>
        </div>
      </section>

      <section id="limits" className="band" aria-labelledby="limits-title">
        <div className="wrap two-col">
          <div>
            <h2 id="limits-title" className="h2">
              Limits per project, not surprise bills.
            </h2>
            <div className="prose-lg limits-prose">
              <p>
                You pay one flat price for the box. Projects share it on their own: each grows into what&rsquo;s
                free, and slows down rather than failing when the box is busy.
              </p>
              <p>
                When you want a fixed share, give a project a limit: a share of the box, or an amount of memory and
                CPU. A side project that suddenly gets busy can&rsquo;t take the others down, or run up a bill.
              </p>
            </div>
          </div>
          <div className="holds">
            <h3 className="holds-title">A limit holds everything the project uses</h3>
            <dl>
              {HOLDS.map(([k, v]) => (
                <div key={k} className="hold">
                  <dt>{k}</dt>
                  <dd>{v}</dd>
                </div>
              ))}
            </dl>
            <p className="holds-note">
              A project at its limit is held there, and its Usage page says so. The rest of the box keeps running.
            </p>
          </div>
        </div>
      </section>

      <section className="band" aria-label="Agents and leaving">
        <div className="wrap pair">
          <div id="agents">
            <h2 className="h2">Your coding agent can run it, safely.</h2>
            <div className="prose-lg">
              <p>
                Claude Code and other agents connect over MCP. Everything you can do in the dashboard is a tool
                they can call: make a project, deploy it, read its logs, query its database.
              </p>
              <p>
                Every change is planned before it runs, recorded in History with who made it and why, and can be
                undone. Anything that destroys data asks you first.
              </p>
              <div className="code code-inline" role="group" aria-label="Connect Claude Code">
                <pre dangerouslySetInnerHTML={{ __html: MCP }} />
              </div>
            </div>
          </div>
          <div id="leave">
            <h2 className="h2">Room to grow, and a door out.</h2>
            <div className="prose-lg">
              <p>
                Move to a bigger server when you need one. Export any project to a single <code>.tiffin</code>{" "}
                file with its code, data and files, import it on another box, or move it there in one step.
              </p>
              <p>
                Everything on the box is a standard piece: Postgres, S3-compatible storage, a Redis-compatible
                store and SMTP. A project that outgrows one server can leave for any host that runs them.
              </p>
            </div>
          </div>
        </div>
      </section>

      <section className="band" aria-labelledby="honest-title">
        <div className="wrap">
          <div className="section-head">
            <h2 id="honest-title" className="h2">
              What it&rsquo;s for, honestly.
            </h2>
          </div>
          <div className="fit">
            <div>
              <h3 className="fit-title">Great for</h3>
              <ul>
                <li>Side projects, prototypes and experiments</li>
                <li>Internal tools</li>
                <li>Apps your coding agent builds and runs for you</li>
                <li>Running many small apps without paying for each one</li>
              </ul>
            </div>
            <div>
              <h3 className="fit-title">Fine for</h3>
              <ul>
                <li>Small real apps with a few thousand users</li>
                <li>Products that can take a short maintenance window</li>
              </ul>
            </div>
            <div>
              <h3 className="fit-title">Not yet for</h3>
              <ul>
                <li>Anything that must stay up if one server goes down: it&rsquo;s one machine</li>
                <li>Work that needs a stable platform today: ShipTiffin is before version 1.0</li>
              </ul>
            </div>
          </div>
          <p className="fit-line">
            ShipTiffin is young and made by a small team. Your box is backed up every hour, and you can leave any
            time with standard Postgres, S3 and Redis.
          </p>
        </div>
      </section>

      <Pricing />

      <section id="early-access" className="band ea" aria-labelledby="ea-title">
        <div className="wrap ea-grid">
          <div className="ea-copy">
            <h2 id="ea-title" className="h2">
              Get early access.
            </h2>
            <p className="section-sub">
              We&rsquo;re letting people in a few at a time. Tell us what you&rsquo;d run, and we&rsquo;ll invite
              you when there&rsquo;s a box for you.
            </p>
            <EarlyAccessNext />
          </div>
          <div className="ea-panel">
            <EarlyAccessForm />
          </div>
        </div>
      </section>

      <section id="faq" className="band" aria-labelledby="faq-title">
        <div className="wrap two-col">
          <h2 id="faq-title" className="h2">
            Questions
          </h2>
          <div className="faq">
            {FAQ.map(({ q, a }) => (
              <details key={q}>
                <summary>{q}</summary>
                <div className="faq-a">{a}</div>
              </details>
            ))}
          </div>
        </div>
      </section>

      <section id="google" className="band" aria-labelledby="trust-title">
        <div className="wrap">
          <div className="section-head">
            <h2 id="trust-title" className="h2">
              Your data, and what we don&rsquo;t allow.
            </h2>
          </div>
          <div className="notes">
            <div className="note">
              <h3 className="note-title">Sign in with Google</h3>
              <p>
                ShipTiffin uses Google for one thing: Sign in with Google, to sign you in to your box&rsquo;s
                dashboard, or to an app hosted on ShipTiffin that offers it. We ask only for the{" "}
                <code>openid</code>, <code>email</code> and <code>profile</code> scopes, which give us your name,
                email address and profile picture. We use them only to identify your account.
              </p>
              <p>
                We have no access to your Gmail, Drive, Calendar or any other Google data. We never sell Google user
                data, use it for ads, or use it to train AI models. Read the details in our{" "}
                <Link href="/privacy#google">privacy policy</Link>.
              </p>
            </div>
            <div className="note">
              <h3 className="note-title">Acceptable use</h3>
              <p>
                ShipTiffin does not use Google APIs or Google user data to create AI-generated non-consensual
                intimate imagery (NCII), and does not allow customers to host apps that create or share it, or any
                sexual content involving people who have not consented.
              </p>
              <p>
                We take down apps that do, and may suspend the box. The full rules are in the{" "}
                <Link href="/terms#use">acceptable use</Link> section of our terms.
              </p>
            </div>
          </div>
        </div>
      </section>
    </>
  );
}
