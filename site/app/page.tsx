import { readFileSync } from "node:fs";
import { join } from "node:path";
import Link from "next/link";
import { ENTITY, jsonLd, pageMeta } from "@/lib/meta";
import { AgentSetup } from "./agent-setup";
import { Art } from "./art";
import { CopyPrompt } from "./copy-prompt";
import { PackingTin } from "./packing-tin";
import { Compare } from "./compare";
import { Pricing } from "./pricing";

export const metadata = pageMeta({
  title: "ShipTiffin: host all your apps on your own Hetzner server",
  description:
    "Run all your apps on one server in your Hetzner account, with Postgres, auth, files, jobs, analytics and backups built in. $19 a month, no usage bills.",
  path: "/",
  absoluteTitle: true,
});
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
        server is billed by Hetzner, to you, at their prices: the smallest is about $10 a month before VAT, with its IPv4 address and a 40 GB data volume. Our fee
        never changes with traffic: there are no usage charges, seats or per-project fees from us. Hetzner&rsquo;s
        price includes 20 TB of traffic a month in Europe (at least 1 TB in the US); past that, Hetzner charges
        for the extra.
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
        monitoring, backup copies off the server, resizing from your account, and support. Your shiptiffin.app address keeps working for 30 days, with
        an email when that starts, a week before it goes and when it goes, so you can point your own domain at
        the box. Your data was always on your server, in standard formats.
      </p>
    ),
  },
  {
    q: "What happens to my payment if I cancel or delete my box?",
    a: (
      <p>
        If you cancel and keep the server, the subscription runs to the end of the month you paid for, then stops.
        If you delete the server in your account, it ends immediately. Either way you won&rsquo;t be charged again,
        and the month already paid isn&rsquo;t refunded, as it&rsquo;s a monthly service. Within 14 days of your first
        payment you can ask for the money-back guarantee at hello@shiptiffin.com.
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
        Not for the managed service. Instead there&rsquo;s a 14-day money-back guarantee on what you pay us. Your
        server is yours either way: if you change your mind, delete it in Hetzner and Hetzner stops billing. Or run
        it yourself for free: it&rsquo;s open source.
      </p>
    ),
  },
  {
    q: "Is it open source?",
    a: (
      <p>
        Yes. The software on every box is on{" "}
        <a href="https://github.com/shiptiffin/tiffin">GitHub</a> under the AGPL-3.0 licence, and the SDK your apps
        use is Apache-2.0. You can run it yourself on Hetzner or any Ubuntu server for free; paying us gets you a box
        we set up, update, monitor and back up for you.
      </p>
    ),
  },
  {
    q: "What uptime can I expect?",
    a: (
      <p>
        Your box is one server. Tiffin updates itself without stopping your apps, server restarts happen in a maintenance window you choose, and
        monitoring from outside the box tells you when something&rsquo;s wrong. A hardware fault means downtime
        until the server is back, which is why we don&rsquo;t suggest it yet for apps that must never go down.
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
  {
    q: "What do you get from Sign in with Google?",
    a: (
      <p>
        Only your name, email address and profile picture (the <code>openid</code>, <code>email</code> and{" "}
        <code>profile</code> scopes), to sign you in to your box&rsquo;s dashboard or to an app on ShipTiffin that
        offers it. No access to Gmail, Drive, Calendar or any other Google data, and we never sell it, use it for ads
        or train AI on it. Details in our <Link href="/privacy#google">privacy policy</Link>.
      </p>
    ),
  },
  {
    q: "What can't I host?",
    a: (
      <p>
        Anything illegal or abusive, including apps that create or share AI-generated non-consensual intimate imagery
        (NCII) or any sexual content of people who haven&rsquo;t consented. We don&rsquo;t use Google APIs or Google
        user data for it either. We take such apps down and may suspend the box. The full rules are in our{" "}
        <Link href="/terms#use">terms</Link>.
      </p>
    ),
  },
];

/* Who we are, for search engines and AI answers. No ratings: we have none to show. */
const LD = [
  {
    "@context": "https://schema.org",
    "@type": "Organization",
    name: "ShipTiffin",
    url: "https://shiptiffin.com",
    logo: "https://shiptiffin.com/apple-icon.png",
    email: "hello@shiptiffin.com",
    description: ENTITY,
    sameAs: ["https://github.com/shiptiffin/tiffin"],
  },
  { "@context": "https://schema.org", "@type": "WebSite", name: "ShipTiffin", url: "https://shiptiffin.com" },
  {
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    name: "ShipTiffin",
    applicationCategory: "DeveloperApplication",
    operatingSystem: "Linux (Ubuntu)",
    description: ENTITY,
    url: "https://shiptiffin.com",
    license: "https://www.gnu.org/licenses/agpl-3.0.html",
    offers: { "@type": "Offer", price: "19", priceCurrency: "USD", url: "https://shiptiffin.com/pricing" },
  },
];

/* Plain facts under the hero, each true today. */
const PROOF = [
  ["About 5 minutes", "from a Hetzner key to a working box"],
  ["12 parts", "every app needs, on one server"],
  ["7 days", "of database history, restorable to the second"],
  ["AGPL-3.0", "open source; run it yourself for free"],
] as const;

/* The trust story, short. The long version is /security. Keep each line true to the sign-up flow. */
const YOURS_POINTS = [
  ["Your Hetzner account", "The server is yours and Hetzner bills you for it. Cancel us and it keeps running."],
  ["A key we use once", "We build the server with your key, then forget it. Every call we made is listed in your account."],
  ["Open source", "The software on every box is on GitHub under AGPL-3.0. Read it, change it, run it yourself."],
  ["A door out", "Export any project to one file with its code, data and files. Postgres, S3, Redis and SMTP move anywhere."],
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

/* The setup prompt, generated from docs/guide/agent-onboarding.md (site/lib/llms.ts). */
const PROMPT = readFileSync(join(process.cwd(), "public", "agent-setup.md"), "utf8");

/* What the agent stops and asks for, from the prompt's [Me] steps. */
const YOURS = [
  ["Sign in and pay", "It's your account and your card."],
  ["Your Hetzner key", "You paste it into our page, never into the chat."],
  ["A passkey", "It lives on your device."],
  ["The agent's key", "You create it in your dashboard and hand it over."],
] as const;

export default function Home() {
  return (
    <>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: jsonLd(LD) }} />
      <section className="hero">
        <div className="wrap hero-grid">
          <div className="hero-copy">
            <p className="kicker">Your own server, in your Hetzner account</p>
            <h1 className="display">
              All your apps.
              <br />
              <span className="hero-mark">One box.</span>
              <br />
              One price.
            </h1>
            <p className="lede">
              ShipTiffin sets up a server in your Hetzner account with everything your apps need already on it:
              Postgres, sign-in, files, email, jobs, analytics and backups. Run every project on it, each with a
              hard limit.
            </p>
            <p className="hero-price">
              <strong>$19 a month</strong> per box, plus about $10 for the server at Hetzner. No usage bills.
            </p>
            <div className="actions">
              <Link className="btn btn-primary" href="/start">
                Get started
              </Link>
              <CopyPrompt />
            </div>
            <p className="fine">
              Works with Claude Code, Codex and Cursor. Open source, so you can also{" "}
              <Link href="/docs/quickstart">run it yourself</Link> for free.
            </p>
          </div>
          <div className="hero-art">
            <PackingTin />
          </div>
        </div>
      </section>

      <section className="proof" aria-label="In numbers">
        <div className="wrap">
          <dl className="proof-row">
            {PROOF.map(([k, v]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </div>
            ))}
          </dl>
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
          <div>
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
        </div>
      </section>

      <section id="limits" className="band limits-band" aria-labelledby="limits-title">
        <div className="wrap limits-grid">
          <div>
            <p className="kicker">A hard limit on every project</p>
            <h2 id="limits-title" className="h2">
              Ship every side project. No surprise bills.
            </h2>
            <div className="prose-lg limits-prose">
              <p>
                You pay one flat price for the box. Projects share it on their own: each grows into what&rsquo;s
                free, and slows down rather than failing when the box is busy.
              </p>
              <p>
                Give a project a limit, a share of the box or an amount of memory and CPU, and it&rsquo;s held
                there. A side project that suddenly gets busy can&rsquo;t take the others down or run up a bill,
                and neither can an agent that deploys all day.
              </p>
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
            </div>
          </div>
          <div className="limits-art">
            <BoxDrawing />
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

      <section id="agent-setup" className="band" aria-labelledby="agent-setup-title">
        <div className="wrap two-col">
          <div>
            <h2 id="agent-setup-title" className="h2">
              Or let your coding agent set it up.
            </h2>
            <div className="prose-lg agent-intro">
              <p>
                Give this prompt to Claude Code, Codex or Cursor. It explains each step, connects itself to your box
                and deploys your first app. It stops and asks you for the parts that are yours:
              </p>
            </div>
            <ul className="agent-yours">
              {YOURS.map(([k, v]) => (
                <li key={k}>
                  <strong>{k}</strong>
                  <span>{v}</span>
                </li>
              ))}
            </ul>
            <p className="agent-more">
              <Link href="/agents">What an agent can and can&rsquo;t do on your box</Link>
            </p>
          </div>
          <AgentSetup prompt={PROMPT} />
        </div>
      </section>

      <section id="yours" className="band" aria-labelledby="yours-title">
        <div className="wrap">
          <div className="section-head">
            <h2 id="yours-title" className="h2">
              Your server, your key, your code.
            </h2>
            <p className="section-sub">
              ShipTiffin builds the box in your Hetzner account and then gets out of the way. Everything on it is
              open source and made of standard parts, so you can always leave.
            </p>
          </div>
          <ul className="yours">
            {YOURS_POINTS.map(([k, v]) => (
              <li key={k}>
                <h3>{k}</h3>
                <p>{v}</p>
              </li>
            ))}
          </ul>
          <div className="fit">
            <div>
              <h3 className="fit-title">Great for</h3>
              <ul>
                <li>Side projects, prototypes and internal tools</li>
                <li>Apps your coding agent builds and runs for you</li>
                <li>Many small apps, without paying for each one</li>
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
                <li>Anything that must stay up if one server fails: it&rsquo;s one machine</li>
                <li>Work that needs a stable platform today: ShipTiffin is before version 1.0</li>
              </ul>
            </div>
          </div>
          <p className="yours-links">
            <Link href="/security">How we handle your key and your data</Link>
            <Link href="/open-source">Run it yourself, free</Link>
            <Link href="/compare">Compare with Vercel, Supabase and Coolify</Link>
          </p>
        </div>
      </section>

      <Pricing />

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

      <section id="start" className="band cta" aria-labelledby="start-title">
        <div className="wrap cta-row">
          <Art
            name="tin-on-server"
            className="art cta-art"
            alt="The ShipTiffin tin sitting on top of a rack server, plugged in."
            sizes="(min-width: 960px) 280px, 200px"
          />
          <div className="cta-copy">
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
    </>
  );
}
