import { ACCESS_MAIL, DASHBOARD, EMAIL } from "./chrome";

export const metadata = { alternates: { canonical: "/" } };

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

const PARTS = [
  ["Apps", "Next.js, Hono, Bun and static sites. A preview for every pull request, rollbacks to earlier deploys."],
  ["Database", "Postgres 18 for each project, with branches for previews and regular restore points."],
  ["KV", "Valkey, Redis-compatible, for caches, sessions and counters."],
  ["Files", "S3-compatible buckets, private or public, with image resizing."],
  ["Sign-in", "Email and password, magic links, passkeys, and Google, GitHub and other providers."],
  ["Email", "Send from any app. Until you connect a mail service, every message waits in a test inbox."],
  ["Jobs", "Queues, crons and workflows. The box pushes each job to your app and retries until it succeeds."],
  ["Logs and analytics", "Logs, metrics, errors and visit counts without cookies, all kept on the box."],
  ["Domains", "Your own domain with HTTPS. Certificates are issued and renewed for you."],
] as const;

const CONFIG = [
  `<span class="t-k">export default</span> defineConfig({`,
  `  project: <span class="t-s">"shop"</span>,`,
  `  apps: { web: { framework: <span class="t-s">"next"</span> } },`,
  `  services: {`,
  `    postgres: {}, auth: {}, email: {},`,
  `  },`,
  `  resources: { maxSharePercent: <span class="t-n">40</span> },`,
  `});`,
].join("\n");

const TERMINAL = [
  `<span class="t-p">$</span> tiffin plan`,
  `<span class="t-p">$</span> git push tiffin main`,
].join("\n");

export default function Home() {
  return (
    <>
      <section className="hero">
        <div className="wrap hero-grid">
          <div className="hero-copy">
            <p className="kicker">Hosted Tiffin · early access</p>
            <h1 className="display">
              All your apps.
              <br />
              One server.
              <br />
              One price.
            </h1>
            <p className="lede">
              ShipTiffin gives you a server of your own with everything an app needs already on it: a database,
              sign-in, email, file storage and background jobs. Run as many projects as fit, deploy them with{" "}
              <code>git push</code> or let your coding agent do it, and give each one the limit you choose.
            </p>
            <div className="actions">
              <a className="btn btn-primary" href={ACCESS_MAIL}>
                Ask for early access
              </a>
              <a className="btn btn-quiet" href={DASHBOARD}>
                Sign in to your box <span aria-hidden="true">→</span>
              </a>
            </div>
            <p className="fine">
              Pricing isn&rsquo;t set yet. Early access: email <a href={`mailto:${EMAIL}`}>{EMAIL}</a>.
            </p>
          </div>
          <BoxDrawing />
        </div>
      </section>

      <section className="band" aria-labelledby="parts-title">
        <div className="wrap">
          <div className="section-head">
            <h2 id="parts-title" className="h2">
              Everything an app needs is already on the box.
            </h2>
            <p className="section-sub">
              No separate accounts for the database, storage, email or analytics. Each project gets its own, set up
              when you ask for it.
            </p>
          </div>
          <dl className="parts">
            {PARTS.map(([name, text]) => (
              <div key={name} className="part">
                <dt>{name}</dt>
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
                <h3>Describe a project.</h3>
                <p>
                  Pick its parts in the dashboard, or write them in <code>tiffin.config.ts</code> next to your
                  code. Both are the same thing underneath, so you can switch at any time.
                </p>
              </li>
              <li>
                <h3>Ship it.</h3>
                <p>
                  Push to git, run <code>tiffin deploy</code>, or ask your coding agent: every operation is also
                  an MCP tool. Each change is planned before it runs, recorded in History, and can be undone.
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

      <section className="band" aria-label="Limits and leaving">
        <div className="wrap pair">
          <div>
            <h2 className="h2">Limits per project, not surprise bills.</h2>
            <div className="prose-lg">
              <p>
                You pay one flat price for the box. Projects share it on their own: each grows into what&rsquo;s
                free, and slows down rather than failing when the box is busy.
              </p>
              <p>
                When you want a fixed share, give a project a limit: a share of the box, or an amount of memory,
                CPU or storage. Its database, cache and builds are held to it too, so a side project that suddenly
                gets busy can&rsquo;t take the others down, or run up a bill.
              </p>
            </div>
          </div>
          <div>
            <h2 className="h2">Room to grow, and a door out.</h2>
            <div className="prose-lg">
              <p>
                Move to a bigger server when you need one. Export any project to a single <code>.tiffin</code>{" "}
                file with its code, data and files, import it on another box, or move it there in one step.
              </p>
              <p>
                Everything on the box is a standard piece: Postgres, S3-compatible storage, a Redis-compatible
                store and SMTP. A project that outgrows one server can leave for any platform that speaks them.
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
              <h3 className="fit-title">Good for</h3>
              <ul>
                <li>Side projects, experiments and small products</li>
                <li>Apps your coding agent builds and runs for you</li>
                <li>Running many small apps without paying for each one</li>
              </ul>
            </div>
            <div>
              <h3 className="fit-title">Not yet for</h3>
              <ul>
                <li>Anything that must stay up if one server goes down: it&rsquo;s one machine</li>
                <li>Work that needs a stable platform today: Tiffin is before version 1.0</li>
              </ul>
            </div>
          </div>
        </div>
      </section>

      <section className="band cta" aria-labelledby="cta-title">
        <div className="wrap cta-row">
          <div>
            <h2 id="cta-title" className="h2">
              Want a box?
            </h2>
            <p className="section-sub">
              We&rsquo;re letting people in a few at a time. Tell us what you&rsquo;d run on it.
            </p>
          </div>
          <div className="actions">
            <a className="btn btn-primary" href={ACCESS_MAIL}>
              Ask for early access
            </a>
          </div>
        </div>
      </section>
    </>
  );
}
