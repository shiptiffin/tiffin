import Link from "next/link";
import { Art } from "./art";
import { Compare } from "./compare";
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
        <span className="tin-lid-note">your server, your Hetzner account</span>
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
  ["Apps", "Next.js first, Hono, FastAPI and more", "Deploy from GitHub or with git push. Every deploy keeps its own address, and rolling back takes one step."],
  ["Previews", "One per pull request", "Each pull request gets its own address and its own copy of the database."],
  ["Database", "Postgres 18", "One for each project, behind a connection pooler, with branches made in milliseconds."],
  ["KV", "Redis-compatible", "For caches, sessions, rate limits and counters."],
  ["Files", "S3-compatible", "Private or public buckets, with image resizing."],
  ["Sign-in", "Better Auth", "Email and password, magic links, passkeys, and Google, GitHub and other providers."],
  ["Email", "Your own provider", "Send from any app through SendGrid, Resend, Postmark, SES or any SMTP service. Until you connect one, mail waits in a test inbox."],
  ["Jobs", "Queues, crons and workflows", "The box calls your app with each job and retries until it succeeds."],
  ["Analytics", "No cookies", "Visits, sources and your own events, counted on the box."],
  ["Error tracking", "Sentry-compatible", "Point any Sentry SDK at the box. Logs, traces and alerts sit beside it."],
  ["Backups", "Restore to any moment", "Everything is backed up every 6 hours. Postgres goes back to any second of the last 7 days."],
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
    q: "What do I pay, and to whom?",
    a: (
      <p>
        Two bills. ShipTiffin is $19 a month per box ($12 for our first 100 customers, locked for 24 months). Your
        server is billed by Hetzner, to you, at their prices: the smallest is about $10 a month before VAT, with its IPv4 address and a 40 GB data volume. Neither
        changes with traffic: there are no usage charges, seats or per-project fees from us.
      </p>
    ),
  },
  {
    q: "Do I need a Hetzner account?",
    a: (
      <p>
        Yes. Sign-up asks for an API key from a Hetzner Cloud project, and your server lives in that account. If
        you don&rsquo;t have one yet, making one takes a few minutes at hetzner.com.
      </p>
    ),
  },
  {
    q: "What happens if I stop paying ShipTiffin?",
    a: (
      <p>
        Your server and your apps keep running in your Hetzner account. Updates and the managed extras stop:
        monitoring, one-click upgrades and support. Your shiptiffin.app address keeps working for 30 days, with
        an email when that starts, a week before it goes and when it goes, so you can point your own domain at
        the box. Your data was always on your server, in standard formats.
      </p>
    ),
  },
  {
    q: "When is something else cheaper?",
    a: (
      <p>
        One small app on a free tier can cost less. ShipTiffin pays off from the second app. And if you need servers
        in many regions, or an app that must stay up through a hardware fault, a bigger platform is the better fit.
      </p>
    ),
  },
  {
    q: "What happens when one project gets busy?",
    a: (
      <p>
        Without a limit, it grows into what the box has free, and slows down rather than failing when the box is
        busy. With a limit, it&rsquo;s held at it: its apps, database, cache and builds. Either way the other
        projects keep running, the bills stay the same, and the project&rsquo;s Usage page shows when a limit held
        it back. When the box itself is full, move to a bigger server in a click.
      </p>
    ),
  },
  {
    q: "Is there a free plan or a trial?",
    a: (
      <p>
        No. Instead there&rsquo;s a 14-day money-back guarantee on what you pay us. Your server is yours either way:
        if you change your mind, delete it in Hetzner and Hetzner stops billing.
      </p>
    ),
  },
  {
    q: "How does email work?",
    a: (
      <p>
        Your apps send through your own email provider: SendGrid, Resend, Postmark, Amazon SES or any SMTP service.
        Connect it once in the dashboard by pasting its key, and every project on the box can send. Until then,
        every message waits in a test inbox you can read, which is handy while you build.
      </p>
    ),
  },
  {
    q: "How are backups done?",
    a: (
      <p>
        Postgres is backed up in full every day and in part every 6 hours, and its log of changes is kept all the
        time, so you can restore your databases to any second in the last 7 days. KV, files, email and the
        box&rsquo;s settings come back from the nearest backup. A restore takes a safety backup first, and restore
        drills prove a backup works without touching anything live.
      </p>
    ),
  },
  {
    q: "What uptime can I expect?",
    a: (
      <p>
        Your box is one server. Updates and restarts happen in a weekly maintenance window you choose, and
        monitoring from outside the box tells you when something&rsquo;s wrong. A hardware fault means downtime
        until the server is back, which is why we don&rsquo;t suggest it yet for apps that must never go down.
      </p>
    ),
  },
  {
    q: "Can I take my projects elsewhere?",
    a: (
      <p>
        Yes. Export any project to a single file with its code, data and files. Everything inside is a standard
        piece: Postgres, S3-compatible storage, a Redis-compatible store and SMTP, so it moves to any host that runs
        them.
      </p>
    ),
  },
  {
    q: "Which frameworks work?",
    a: (
      <p>
        Next.js comes first. Hono, FastAPI, TanStack Start, SvelteKit, Nuxt, React Router, Astro and static sites
        work too, as do other Bun, Node.js and Python servers, and anything else from a Dockerfile.
      </p>
    ),
  },
];

/* The trust story: what happens to your Hetzner key. Keep each line true to the sign-up flow. */
const KEY = [
  ["A project just for ShipTiffin", "You make a separate Hetzner project for your box and a key for it, so the key can only see that project."],
  ["Used to build, then forgotten", "We use the key to create the server, its disk and its firewall, then forget it by default. Resizing later asks you to paste a key again for a minute."],
  ["Every call listed", "Each call we make to Hetzner with your key is listed in your account."],
  ["No way in left behind", "After install, our setup key is removed from the server. Updates are pulled by the box itself, and checked against our signature."],
  ["Revoke it any time", "Delete the key in Hetzner whenever you like. Your box keeps running."],
  ["Your data stays with you", "Your apps, databases and files live on your server. They never pass through us."],
] as const;

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
            <p className="kicker">Your server, in your own Hetzner account</p>
            <h1 className="display">
              All your apps.
              <br />
              One box.
              <br />
              One price.
            </h1>
            <p className="lede">
              ShipTiffin sets up a server in your Hetzner account with everything your apps need already on it:
              Postgres, sign-in, file storage, jobs, analytics, error tracking and backups. Run all your projects on
              it, each with a hard limit, for $19 a month plus the server, about $10 at Hetzner. No usage
              bills.
            </p>
            <div className="actions">
              <Link className="btn btn-primary" href="/start">
                Get started
              </Link>
              <a className="btn btn-quiet" href="#replaces">
                See what it replaces
              </a>
            </div>
            <p className="fine">
              Ready in about 5 minutes. The first 100 customers pay $12 a month, locked for 24 months.
            </p>
            <Art
              name="tin-on-server"
              className="art hero-art"
              alt="The ShipTiffin tin sitting on top of a rack server, plugged in."
              sizes="(min-width: 960px) 400px, (min-width: 560px) 360px, 260px"
              priority
            />
          </div>
          <BoxDrawing />
        </div>
      </section>

      <Compare />

      <section id="box" className="band" aria-labelledby="parts-title">
        <div className="wrap">
          <div className="section-head">
            <h2 id="parts-title" className="h2">
              Everything&rsquo;s already in the box.
            </h2>
            <p className="section-sub">
              No separate accounts for the database, storage, analytics or error tracking, and no separate bills.
              Each project gets its own, set up when you ask for it.
            </p>
          </div>
          <div className="with-art">
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
            <Art
              name="open-tin-parts"
              className="art side-art"
              alt="The tin opened up into its tiers, with a database, files, an envelope, a key, a clock and a chart inside."
              sizes="(min-width: 1040px) 300px, 200px"
            />
          </div>
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
                <h3>Paste a Hetzner key.</h3>
                <p>
                  Make a Hetzner Cloud project just for ShipTiffin, create an API key for it, and paste it in.
                </p>
              </li>
              <li>
                <h3>We set up your box in about 5 minutes.</h3>
                <p>
                  The server, its disk and firewall in your account, with every part installed. You get a dashboard
                  and a free <code>yourname.shiptiffin.app</code> address.
                </p>
              </li>
              <li>
                <h3>Deploy from GitHub, or let your agent.</h3>
                <p>
                  Every push to main goes live, with a preview for each pull request. Or deploy with{" "}
                  <code>git push</code>, the <code>tiffin</code> command line or your coding agent, and give each
                  project its limit in <code>tiffin.config.ts</code>.
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

      <section id="control" className="band" aria-labelledby="control-title">
        <div className="wrap">
          <div className="section-head">
            <h2 id="control-title" className="h2">
              Your key, your server, your control.
            </h2>
            <p className="section-sub">
              ShipTiffin needs a Hetzner API key to build your server. Here&rsquo;s how we handle it.
            </p>
          </div>
          <div className="with-art">
            <ol className="keys">
              {KEY.map(([k, v]) => (
                <li key={k}>
                  <h3>{k}</h3>
                  <p>{v}</p>
                </li>
              ))}
            </ol>
            <Art
              name="tin-returns-key"
              className="art side-art"
              alt="The ShipTiffin tin handing back a key with a tag on it."
              sizes="(min-width: 1040px) 300px, 200px"
            />
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
                Move to a bigger server in a click when you need one. Export any project to a single <code>.tiffin</code>{" "}
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
            ShipTiffin is young and made by a small team. Your databases can go back to any second of the last
            week, and you can leave any time with standard Postgres, S3 and Redis.
          </p>
        </div>
      </section>

      <Pricing />

      <section id="start" className="band cta" aria-labelledby="start-title">
        <div className="wrap cta-row">
          <div>
            <h2 id="start-title" className="h2">
              All your apps on one box, in about 5 minutes.
            </h2>
            <p className="section-sub">
              $19 a month plus your Hetzner server. The first 100 customers pay $12, locked for 24 months.
            </p>
          </div>
          <div className="actions">
            <Link className="btn btn-primary" href="/start">
              Get started
            </Link>
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

      <section id="about" className="band" aria-labelledby="about-title">
        <div className="wrap two-col">
          <h2 id="about-title" className="h2">
            What ShipTiffin is
          </h2>
          <div className="prose-lg">
            <p>
              ShipTiffin sets up and looks after servers for web apps. Each customer brings a Hetzner Cloud account;
              we create a server in it, which we call a box, install everything on it and keep it updated, and the
              customer runs their apps on it. The box comes with the parts most apps need: a Postgres database,
              sign-in for the app&rsquo;s users, email sending, file storage, background jobs, logs, error tracking
              and visit counts.
            </p>
            <p>
              Customers deploy their apps from GitHub, with <code>git push</code>, the <code>tiffin</code> command
              line or a coding agent, and manage them from the box&rsquo;s dashboard. It is built for developers and
              small teams who run several apps and want one flat price instead of a bill for each service.
            </p>
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
