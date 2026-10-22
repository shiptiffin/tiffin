// /guides/move-from-vercel: moving Next.js apps from Vercel to a Tiffin box. Based on
// docs/guide/apps.md "Coming from Vercel" (vercel.json, monorepos), apps.md "What your
// app gets" and "Next.js" (env), data.md (Upstash/Vercel KV), queues.md (crons,
// Workflow DevKit) and limits.md (what doesn't carry over). Keep the vercel.json table
// in step with apps.md; every tiffin command is copied from the docs.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, FactTable, Fit, No, PageHead, Terminal } from "../../kit";
import { Code, GuideLd, MoreGuides, Step, Toc } from "../guide-parts";

export const dynamic = "force-static";

const SLUG = "move-from-vercel";
const UPDATED = "2026-10-10";

export const metadata = pageMeta({
  title: "Move your Next.js apps off Vercel",
  description:
    "Move Next.js apps from Vercel to your own server: what carries over unchanged (vercel.json, crons, env, monorepos), what to move by hand, and a checklist.",
  path: `/guides/${SLUG}`,
  type: "article",
});

const STEPS: [string, string][] = [
  ["map", "What maps to what"],
  ["box", "1. Get a box"],
  ["app", "2. Bring the app"],
  ["env", "3. Copy environment variables"],
  ["data", "4. Move your data"],
  ["test", "5. Test on the box's address"],
  ["dns", "6. Switch the domain"],
  ["gaps", "What doesn't carry over"],
  ["checklist", "Checklist"],
];

const ENV = `apps: {
  web: { framework: "next", env: { LOG_LEVEL: "info" } },
},`;

export default function MoveFromVercel() {
  return (
    <>
      <GuideLd slug={SLUG} />
      <PageHead
        kicker="Guide"
        title="Moving your apps off Vercel"
        crumbs={[
          ["Guides", "/guides"],
          ["Move off Vercel", `/guides/${SLUG}`],
        ]}
        updated={UPDATED}
      >
        <p>
          A Tiffin box reads what your app already has, so most Next.js apps that deploy on Vercel deploy here unchanged.
          This guide covers what carries over, what you move by hand, and what stays behind. For prices side by side,
          see <Link href="/compare/vercel">ShipTiffin vs Vercel</Link>.
        </p>
      </PageHead>

      <div className="wrap guide-intro">
        <Answer>
          <p>
            Most Next.js apps move from Vercel to a Tiffin box with no code changes. The box reads{" "}
            <code>vercel.json</code> at every deploy (build commands, crons, headers, redirects), installs monorepos the
            way Vercel does, and gives the app the variables Next.js expects.
          </p>
          <p>
            You copy your environment variables, move your data, test on the box&rsquo;s own address, then point DNS at
            the server. Vercel&rsquo;s global edge network, regions and products with their own APIs, such as Blob,
            don&rsquo;t come along.
          </p>
        </Answer>
      </div>

      <div className="band">
        <div className="wrap guide-layout">
          <aside className="guide-side">
            <Toc items={STEPS} />
            <MoreGuides current={SLUG} />
          </aside>

          <article className="guide-body">
            <Step id="map" title="What maps to what">
              <FactTable
                caption="Vercel features and what replaces them on a Tiffin box"
                highlight={1}
                head={["On Vercel", "On the box"]}
                rows={[
                  ["Deploy on git push", <>A GitHub App per box: every push to the production branch deploys (<Link href="/docs/apps#deploy-from-github">GitHub</Link>)</>],
                  ["Preview deployments", "One per pull request at its own address, with its own copy of the database; removed when the pull request closes"],
                  ["Deployment URLs", <>Every production deploy keeps an address, <code>d-&lt;id&gt;--&lt;name&gt;</code>, read-only and behind your dashboard sign-in by default</>],
                  ["Instant Rollback", <><code>tiffin rollback &lt;app&gt;</code>, to any of the last 3 production deploys (up to 20 with <code>keepVersions</code>)</>],
                  ["Environment variables", <>Plain values in <code>tiffin.config.ts</code>, secrets with <code>tiffin secrets set</code> or the dashboard</>],
                  ["Cron jobs (vercel.json)", <>Run by the box: a <code>GET</code> to the path with <code>CRON_SECRET</code>, as Vercel calls it (<Link href="/docs/queues">jobs</Link>)</>],
                  ["Function duration", <><code>timeoutSeconds</code>: 15 minutes by default, up to 24 hours</>],
                  ["ISR and revalidateTag", "Next.js's cache, shared by every instance through the project's KV"],
                  ["Vercel Postgres, Neon", <>Postgres 18 for each project, behind a pooler, with branches (<Link href="/docs/data">data</Link>)</>],
                  ["Vercel KV, Upstash Redis", <><code>@vercel/kv</code> and <code>@upstash/redis</code> run unchanged against a compatible endpoint on the box</>],
                  ["Vercel Blob", <>S3-compatible buckets; code that calls <code>@vercel/blob</code> moves to an S3 client</>],
                  ["Workflow DevKit", "Runs unchanged, on the project's Postgres"],
                  ["Web Analytics", "Cookieless page views counted at the box's edge, with no script for page views"],
                  ["Observability", <>Sentry-compatible error tracking, logs and traces; <code>@vercel/otel</code> sends to the box</>],
                  ["Spend management", "A flat fee, and a hard limit per project for memory, CPU, database and KV"],
                  ["Team seats", "People are free"],
                  ["Domains and HTTPS", <><code>tiffin domains add</code>, with Let&rsquo;s Encrypt certificates (<Link href="/docs/domains">domains</Link>)</>],
                  ["Firewall", <>Rate limits, a proof-of-work bot check, CrowdSec and a firewall, on by default (<Link href="/docs/protection">protection</Link>)</>],
                  ["Edge network and regions", <No key="n">One server, in the place you picked</No>],
                ]}
                note={
                  <>
                    From <Link href="/docs/apps#coming-from-vercel">Coming from Vercel</Link> and the pages linked in
                    the table. The full list of gaps is <Link href="/docs/limits">what works and what doesn&rsquo;t</Link>.
                  </>
                }
              />
            </Step>

            <Step id="box" title="1. Get a box">
              <p>
                A box is one server in your own Hetzner account with Tiffin on it. Get one managed at{" "}
                <Link href="/start">shiptiffin.com/start</Link> ($19 a month, plus the server at Hetzner&rsquo;s price), or
                make one yourself for free with <code>tiffin up</code>. Both are step 1 of{" "}
                <Link href="/guides/deploy-nextjs-to-hetzner#server">Deploy Next.js to Hetzner</Link>. One box holds many
                projects, so several Vercel projects can move to the same one.
              </p>
            </Step>

            <Step id="app" title="2. Bring the app">
              <h3>From GitHub</h3>
              <p>
                In the dashboard, <strong>Settings › Git › Connect GitHub</strong>, then{" "}
                <strong>New project › Import from GitHub</strong>. Pick the repository, its production branch and the
                app&rsquo;s folder (monorepos list each app found, with the framework the box would use), add environment
                variables and create. From then on it works like Vercel: every push to the branch deploys, and every pull
                request gets a preview with one comment kept up to date.
              </p>
              <h3>From your computer</h3>
              <Terminal
                title="In the app's folder"
                lines={[
                  "$ tiffin init",
                  "$ tiffin plan",
                  '$ tiffin apply --confirm <hash> -m "Move from Vercel"',
                  "$ tiffin deploy",
                ]}
              />
              <h3>What the box reads</h3>
              <ul>
                <li>
                  <strong>vercel.json</strong> in the app&rsquo;s folder, at every deploy. The build log lists what was
                  taken and what wasn&rsquo;t used, and <code>tiffin plan</code> says the same. A broken vercel.json fails
                  the deploy, saying why; the live version keeps serving.
                </li>
                <li>
                  <strong>Monorepos.</strong> An app in a JavaScript workspace goes up with the whole workspace, as on
                  Vercel. Dependencies install at the top with the workspace&rsquo;s package manager, only for the app,
                  the workspace packages it uses and the root; then the app builds and starts in its own folder. If that
                  install fails, the box installs the whole workspace instead.
                </li>
                <li>
                  <strong>Static exports.</strong> A Next.js app with <code>output: &quot;export&quot;</code> builds with
                  its own <code>next build</code>, and the edge serves <code>out/</code> as a static site, with no
                  container.
                </li>
              </ul>
              <FactTable
                caption="vercel.json keys and what the box does with them"
                head={["vercel.json", "On the box"]}
                rows={[
                  [<><code>buildCommand</code>, <code>installCommand</code></>, "Replace the build's own: build in the app's folder, install at the top of its workspace"],
                  [<code key="o">outputDirectory</code>, "The folder a static site or export serves"],
                  [<code key="c">crons</code>, <>Crons that call the app with <code>GET</code> and its <code>CRON_SECRET</code>, as Vercel does</>],
                  [<code key="h">headers</code>, "Set at the edge on matching responses; they win over the app's own and the edge's defaults"],
                  [<code key="r">redirects</code>, <>Answered at the edge, query string kept; <code>permanent: false</code> is 307, <code>statusCode</code> is kept</>],
                  [<code key="w">rewrites</code>, "Static sites: another path of the site answers when no file matches"],
                  [<><code>cleanUrls</code>, <code>trailingSlash</code></>, <>Static sites: <code>/page.html</code> redirects to <code>/page</code>; paths get (or lose) their trailing slash</>],
                ]}
                note={
                  <>
                    Sources use Vercel&rsquo;s patterns (<code>/blog/:slug</code>, <code>/docs/:path*</code>). For a
                    Next.js server app, rewrites, <code>cleanUrls</code> and <code>trailingSlash</code> stay with the app,
                    and <code>next.config</code>&rsquo;s own redirects, rewrites and headers run inside it as before. From{" "}
                    <Link href="/docs/apps#coming-from-vercel">Coming from Vercel</Link>.
                  </>
                }
              />
            </Step>

            <Step id="env" title="3. Copy environment variables">
              <p>
                Read them from your Vercel project&rsquo;s settings and set each one on the box. Secrets are stored
                encrypted, and setting one is a change in History you can undo:
              </p>
              <Terminal
                title="Secrets"
                lines={[
                  '$ tiffin secrets set hello STRIPE_SECRET_KEY --value "$STRIPE_SECRET_KEY"',
                  "$ tiffin secrets list hello",
                  "# names only, never values",
                ]}
              />
              <p>Plain values that aren&rsquo;t secret can live in the app&rsquo;s config instead:</p>
              <Code title="tiffin.config.ts">{ENV}</Code>
              <ul>
                <li>
                  <strong>Leave out what the box sets.</strong> Every app gets <code>DATABASE_URL</code>,{" "}
                  <code>REDIS_URL</code>, the Upstash and Vercel KV variables, <code>S3_*</code>, <code>SMTP_URL</code>,{" "}
                  <code>SENTRY_DSN</code> and more. A value you set yourself wins over the box&rsquo;s, so don&rsquo;t copy
                  the old ones across. If your code reads <code>POSTGRES_URL</code>, point it at{" "}
                  <code>DATABASE_URL</code>.
                </li>
                <li>
                  <strong>Builds see the same env as the app</strong>, as on Vercel, so prerendered pages can query the
                  database. At build time the database login is read-only.
                </li>
                <li>
                  <strong>Browser variables</strong> (<code>NEXT_PUBLIC_*</code>) are built into client code, so changing
                  one rebuilds the app.
                </li>
                <li>
                  <strong>Vercel&rsquo;s own variables.</strong> The box sets <code>VERCEL_PROJECT_PRODUCTION_URL</code>{" "}
                  to the app&rsquo;s host, so <code>opengraph-image</code> and other metadata resolve without a{" "}
                  <code>metadataBase</code>. <code>VERCEL</code> and <code>VERCEL_URL</code> stay unset, since libraries
                  take them to mean the app runs on Vercel.
                </li>
              </ul>
              <p>
                The full list is in <Link href="/docs/apps#what-your-app-gets">what your app gets</Link>.
              </p>
            </Step>

            <Step id="data" title="4. Move your data">
              <ul>
                <li>
                  <strong>Postgres</strong> (Vercel Postgres, Neon, Supabase or another host): dump it with{" "}
                  <code>pg_dump</code>, open a tunnel with <code>tiffin db tunnel hello</code>, and restore into the{" "}
                  <code>postgresql://</code> URL it prints with <code>pg_restore</code> or <code>psql</code>. The tunnel
                  needs an API key with full access to the project.
                </li>
                <li>
                  <strong>KV</strong> (Vercel KV, Upstash): apps that use <code>@vercel/kv</code>,{" "}
                  <code>@upstash/redis</code> or <code>@upstash/ratelimit</code> run unchanged; <code>Redis.fromEnv()</code>{" "}
                  picks up the box&rsquo;s values. Delete the old Upstash values from your env, since yours would win. The
                  box&rsquo;s KV starts empty.
                </li>
                <li>
                  <strong>Files</strong> (Vercel Blob): every project has S3-compatible buckets. Copy your files with any
                  S3 tool and move upload code to <code>Bun.s3</code> or an AWS SDK. See{" "}
                  <Link href="/docs/storage">files</Link>.
                </li>
                <li>
                  <strong>Workflows</strong> built on the Workflow DevKit deploy unchanged and run on the project&rsquo;s
                  Postgres. See <Link href="/docs/queues">jobs</Link>.
                </li>
              </ul>
              <Terminal
                title="Check the data arrived"
                lines={['$ tiffin sql hello "select count(*) from users"', "# one read-only statement"]}
              />
            </Step>

            <Step id="test" title="5. Test on the box's address">
              <p>
                Before DNS moves, the app is already live at <code>https://hello.&lt;box domain&gt;</code>, with a real
                certificate. Sign in, run the flows that matter, and check the logs with{" "}
                <code>tiffin logs web -f</code>. <code>tiffin queue crons list</code> shows each cron and whether it came
                from <code>tiffin.config.ts</code> or <code>vercel.json</code>.
              </p>
              <p>
                If your app has its own sign-in (Auth.js, Clerk, your own OAuth apps), add the new host to each
                provider&rsquo;s allowed redirect URIs. If you turn on the box&rsquo;s sign-in instead, it answers{" "}
                <code>/api/auth/*</code> on every app address, so routes of your own there stop reaching your app.
              </p>
            </Step>

            <Step id="dns" title="6. Switch the domain">
              <Terminal
                title="Move the domain"
                lines={[
                  "$ tiffin domains add hello --domain example.com --app web --www",
                  "# shows the plan, then lists the DNS records to add",
                  "$ tiffin domains list hello",
                  "# waiting_for_dns, issuing, then live",
                ]}
              />
              <ol>
                <li>A day ahead, lower the TTL on the records you&rsquo;ll change, so the switch spreads quickly.</li>
                <li>
                  Add the domain on the box (above). It lists the records: an A (and AAAA) record to the server, or one
                  CNAME for a subdomain.
                </li>
                <li>
                  Change the records at your DNS host. The box checks again on its own and gets a Let&rsquo;s Encrypt
                  certificate a few seconds after the records point at it.
                </li>
                <li>
                  Leave the Vercel project running until the domain shows <code>live</code> and traffic has moved, then
                  remove the domain there.
                </li>
              </ol>
              <p>
                More in <Link href="/docs/domains">domains</Link>.
              </p>
            </Step>

            <Step id="gaps" title="What doesn't carry over">
              <ul>
                <li>
                  <strong>One server, one place.</strong> There is no global edge network and no choice of regions per
                  function. If the server has a hardware fault, your apps are down until it&rsquo;s back. Tiffin is
                  pre-1.0.
                </li>
                <li>
                  <strong>Some vercel.json is ignored</strong>, and the build log lists it: rules with <code>has</code> or{" "}
                  <code>missing</code>, rewrites to another site, and every other key (<code>functions</code>,{" "}
                  <code>regions</code>, <code>framework</code>...).
                </li>
                <li>
                  <strong>Vercel products with their own APIs</strong>, such as Blob and Edge Config, have no stand-in
                  that speaks those APIs. Move that code to buckets or KV.
                </li>
                <li>
                  <strong>Databases from outside.</strong> Postgres and KV are reachable only from inside the box (and
                  through <code>tiffin db tunnel</code>). A frontend that stays on Vercel can&rsquo;t connect to them
                  directly: run a small API app on the box instead.
                </li>
                <li>
                  <strong>Caching and images.</strong> Next.js&rsquo;s own cache works across instances; there is no
                  edge response cache for other frameworks yet, and each app optimises its own images.
                </li>
                <li>
                  <strong>Other frameworks&rsquo; Vercel adapters.</strong> Astro and SvelteKit with the Vercel adapter
                  aren&rsquo;t supported: switch to <code>@astrojs/node</code>, or a Node or Bun adapter.
                </li>
                <li>
                  <strong>Previews.</strong> Pull requests from forks aren&rsquo;t built unless the app says{" "}
                  <code>previews: &quot;forks&quot;</code>. A preview gets its own database copy but shares the cache,
                  buckets, secrets and real user accounts with production.
                </li>
              </ul>
              <Fit
                ours={[
                  "Moving makes sense when",
                  [
                    "You run several apps, and each needs a database, sign-in or jobs",
                    "Usage charges, seats or per-project fees keep growing",
                    "You want your apps and data in an account you own",
                  ],
                ]}
                theirs={[
                  "Vercel is the better fit when",
                  [
                    "Your users are worldwide and you need the edge network or many regions",
                    "An app must stay up through a hardware fault",
                    "You lean on Vercel-only products like Blob or Edge Config",
                    "You don't want a server at all, not even one we look after",
                  ],
                ]}
              />
            </Step>

            <Step id="checklist" title="Checklist">
              <ul className="guide-checklist">
                <li>
                  The box is ready and <code>tiffin whoami</code> answers.
                </li>
                <li>The app deploys on the box, and the build log shows what it took from vercel.json.</li>
                <li>Secrets are set, and old database, Upstash and KV values are left out.</li>
                <li>
                  The database is restored, and <code>tiffin sql</code> shows your rows.
                </li>
                <li>Files are in a bucket, and Blob code uses an S3 client.</li>
                <li>
                  Crons show in <code>tiffin queue crons list</code>.
                </li>
                <li>Sign-in works on the box&rsquo;s address, with redirect URIs updated.</li>
                <li>
                  An email provider is connected in <strong>Settings › Email</strong>, if the app sends mail.
                </li>
                <li>
                  The domain is added, DNS is changed, and <code>tiffin domains list</code> says <code>live</code>.
                </li>
                <li>The domain is removed from Vercel once traffic has moved.</li>
              </ul>
            </Step>
          </article>
        </div>
      </div>

      <CtaBand title="All your Vercel projects, on one box">
        $19 a month per box, plus your server at Hetzner&rsquo;s price. No usage bills or seats.
      </CtaBand>
    </>
  );
}
