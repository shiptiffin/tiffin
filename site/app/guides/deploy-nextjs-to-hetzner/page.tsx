// /guides/deploy-nextjs-to-hetzner: a followable tutorial, from an empty Hetzner
// account to a Next.js app with Postgres, sign-in and a domain. Every command here is
// copied from docs/guide (quickstart.md, apps.md, data.md, auth.md, domains.md): change
// one only after checking it there. Transcripts show commands, and "#" lines say what
// the docs say happens; they are not captured output.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, FactTable, Fit, PageHead, Terminal } from "../../kit";
import { Code, GuideLd, MoreGuides, Step, Toc } from "../guide-parts";

export const dynamic = "force-static";

const SLUG = "deploy-nextjs-to-hetzner";
const UPDATED = "2026-10-10";

export const metadata = pageMeta({
  title: "Deploy Next.js to Hetzner, with Postgres, auth and HTTPS",
  description:
    "Deploy a Next.js app to your own Hetzner server: get a box (managed or free), run tiffin deploy, then add Postgres, sign-in and your own domain.",
  path: `/guides/${SLUG}`,
  absoluteTitle: true,
  type: "article",
});

const STEPS: [string, string][] = [
  ["before", "Before you start"],
  ["server", "1. Get a server with Tiffin on it"],
  ["init", "2. Add Tiffin to your app"],
  ["deploy", "3. Plan, apply and deploy"],
  ["logs", "4. Logs and rollbacks"],
  ["postgres", "5. Use the database"],
  ["sign-in", "6. Add sign-in"],
  ["domain", "7. Use your own domain"],
  ["github", "8. Deploy on every push (optional)"],
  ["vps", "What you get vs a plain VPS + Docker"],
];

const CONFIG = `import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "hello",
  apps: {
    web: { framework: "next" },
  },
  services: {
    postgres: {},
  },
});`;

const DB = `// db.ts
import postgres from "postgres";

const g = globalThis as { __sql?: postgres.Sql };
export const sql = (g.__sql ??= postgres(process.env.DATABASE_URL!, { prepare: false, max: 5, idle_timeout: 20 }));`;

const RELEASE = `apps: { web: { framework: "next", release: "bunx drizzle-kit migrate" } }`;

const AUTH_CONFIG = `services: {
  postgres: {},
  auth: { methods: ["email", "magic-link", "passkey", "google"] },
},`;

const PROXY = `// proxy.ts: only looks for the session cookie (no network)
import { authProxy } from "@shiptiffin/sdk/next/auth";
export const proxy = authProxy({ protect: ["/dashboard/:path*"], signIn: "/sign-in" });`;

const DAL = `// app/lib/dal.ts
import "server-only";
export { getSession, verifySession, requireRole, currentUser } from "@shiptiffin/sdk/next/auth";

// app/dashboard/page.tsx
const { user, organization } = await verifySession(); // signed out: to /sign-in?next=...`;

const ACTIONS = `// app/actions.ts
"use server";
import { signIn, signOut } from "@shiptiffin/sdk/next/auth";

export async function signInAction(_: unknown, form: FormData) {
  // email, password, captcha and next from the form; { ok: false, code, message } on failure
  return signIn(form, { redirectTo: "/dashboard" });
}
export async function signOutAction() {
  await signOut({ redirectTo: "/" });
}`;

export default function DeployNextToHetzner() {
  return (
    <>
      <GuideLd slug={SLUG} />
      <PageHead
        kicker="Guide"
        title="Deploy a Next.js app to a Hetzner server"
        crumbs={[
          ["Guides", "/guides"],
          ["Deploy Next.js to Hetzner", `/guides/${SLUG}`],
        ]}
        updated={UPDATED}
      >
        <p>
          From an empty Hetzner account to a Next.js app with its own Postgres database, sign-in, logs and a domain with
          HTTPS, on one server you own. Every command below is the real one, from the{" "}
          <Link href="/docs/quickstart">quickstart</Link>.
        </p>
      </PageHead>

      <div className="wrap guide-intro">
        <Answer>
          <p>
            Put Tiffin on a Hetzner Cloud server: managed through{" "}
            <Link href="/start">shiptiffin.com/start</Link> ($19 a month plus the server), or free with the open-source
            CLI (<code>tiffin up --provider hetzner</code>). Then, in your Next.js folder, run <code>tiffin init</code>,{" "}
            <code>tiffin plan</code>, <code>tiffin apply</code> and <code>tiffin deploy</code>.
          </p>
          <p>
            The app is served over HTTPS with its own Postgres database on the same server. Sign-in is one line of
            config, and your own domain is one command.
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
            <Step id="before" title="Before you start">
              <ul>
                <li>
                  A <a href="https://www.hetzner.com/cloud/">Hetzner Cloud</a> account. You&rsquo;ll make a new project
                  in it just for this server.
                </li>
                <li>
                  macOS or Linux for the <code>tiffin</code> CLI. On Windows, use it inside{" "}
                  <a href="https://learn.microsoft.com/windows/wsl/install">WSL</a>.
                </li>
                <li>
                  A Next.js app. Next.js 16.2 or later gets the box&rsquo;s adapter (a cache shared by every instance,
                  client files served by the edge); older versions build and run without it. No app yet? Step 2 makes
                  one.
                </li>
              </ul>
              <p>
                The server is billed by Hetzner to you. The smallest size a box uses came to about $10 a month before VAT
                on 10 October 2026, with its IPv4 address and a 40 GB data volume; see{" "}
                <Link href="/pricing#servers">server sizes and prices</Link>.
              </p>
            </Step>

            <Step id="server" title="1. Get a server with Tiffin on it">
              <p>
                A <strong>box</strong> is one Linux server with Tiffin and every service on it. Both ways below give you
                the same box, in your own Hetzner account.
              </p>
              <div className="guide-paths">
                <div>
                  <h3>Managed: ShipTiffin sets it up ($19 a month)</h3>
                  <ol>
                    <li>
                      In the Hetzner Cloud console, make a new project for ShipTiffin, then Security → API tokens →
                      Generate API token, with <strong>Read &amp; Write</strong>.
                    </li>
                    <li>
                      Go to <Link href="/start">shiptiffin.com/start</Link>. Sign in, pay, and paste the token. Pick a
                      name, a size and a place.
                    </li>
                    <li>
                      The box is built in your Hetzner account in about five minutes, at{" "}
                      <code>&lt;name&gt;.shiptiffin.app</code>. Click <strong>Open your dashboard</strong>, then add a
                      passkey in the dashboard&rsquo;s Settings.
                    </li>
                    <li>
                      Make an API key for your computer: <strong>Settings › API keys → Create key</strong>. It&rsquo;s
                      shown once.
                    </li>
                  </ol>
                  <Terminal
                    title="Connect the CLI to a managed box"
                    lines={[
                      "$ curl -fsSL https://shiptiffin.com/install.sh | sh",
                      "$ export TIFFIN_URL=https://dashboard.<name>.shiptiffin.app",
                      "$ export TIFFIN_TOKEN=<key>",
                      "$ tiffin whoami",
                      "# shows the key's name",
                    ]}
                  />
                  <p>
                    Put the two <code>export</code> lines in your shell profile (<code>~/.zshrc</code> or{" "}
                    <code>~/.bashrc</code>) to keep them. What we can and can&rsquo;t do with your Hetzner key is in{" "}
                    <Link href="/docs/managed">managed boxes</Link>.
                  </p>
                </div>
                <div>
                  <h3>Self-hosted: the free CLI does it</h3>
                  <p>
                    Make a <strong>Read &amp; Write</strong> API token in the Hetzner Cloud console (your project →
                    Security → API tokens). Then:
                  </p>
                  <Terminal
                    title="Make a box on Hetzner"
                    lines={[
                      "$ curl -fsSL https://shiptiffin.com/install.sh | sh",
                      "$ export HCLOUD_TOKEN=...",
                      "$ tiffin up --provider hetzner --name shop --dry-run",
                      "# what it makes, and the monthly price from Hetzner's price list",
                      "$ tiffin up --provider hetzner --name shop",
                      "# a few minutes; ends with the dashboard address and a one-time sign-in link",
                    ]}
                  />
                  <p>
                    It makes the server, a 40 GB data volume, a firewall and an SSH key in your Hetzner project. The
                    default is a <code>cax11</code> (Arm, 2 vCPU, 4 GB) in Falkenstein on Ubuntu 26.04; change it with{" "}
                    <code>--type</code> and <code>--location</code>. Open the sign-in link and add a passkey in Settings.
                  </p>
                  <p>
                    Until you give it a domain, the box answers at{" "}
                    <code>https://dashboard.&lt;server IPv4, dots as dashes&gt;.sslip.io</code>, with a real certificate.
                    The computer that ran <code>tiffin up</code> remembers the box, so there is nothing to connect.
                  </p>
                </div>
              </div>
            </Step>

            <Step id="init" title="2. Add Tiffin to your app">
              <p>
                In your app&rsquo;s folder, run <code>tiffin init</code>. No app yet? Make one first:
              </p>
              <Terminal
                title="Make a Next.js app and add Tiffin"
                lines={["$ npx create-next-app@latest hello --yes", "$ cd hello", "$ tiffin init"]}
              />
              <p>
                <code>tiffin init</code> writes <code>tiffin.config.ts</code>, plus <code>AGENTS.md</code> and an agent
                skill so your coding agent knows how to work with the box (see{" "}
                <Link href="/agents">connecting Claude Code, Codex or Cursor</Link>). The project is named after the folder:
              </p>
              <Code title="tiffin.config.ts">{CONFIG}</Code>
              <p>
                It also adds the SDK your app uses, <code>@shiptiffin/sdk</code>, as a file in <code>vendor/</code>, so
                no registry is needed: commit <code>vendor/</code> and run <code>npm install</code> or{" "}
                <code>bun install</code>.
              </p>
              <p>
                Database, KV, files, email and analytics are always there; list a service only to set its options. No{" "}
                <code>next.config</code> changes are needed. More in <Link href="/docs/apps">apps and deploys</Link>.
              </p>
            </Step>

            <Step id="deploy" title="3. Plan, apply and deploy">
              <Terminal
                title="Ship it"
                lines={[
                  "$ tiffin plan",
                  "# lists every step, its risk and why, and a plan hash; nothing changes yet",
                  '$ tiffin apply --confirm <hash> -m "Set up hello"',
                  "$ tiffin deploy",
                  "# builds on the box, waits for the health check, then switches traffic",
                ]}
              />
              <p>
                Nothing changes until you confirm with that exact plan&rsquo;s hash. <code>tiffin deploy</code> builds on
                the box, starts the new version, waits for its health check and switches traffic with no dropped
                requests. A failed build or health check leaves the old version serving.
              </p>
              <p>
                Your app is then live at <code>https://hello.&lt;box domain&gt;</code>: an app that sets no routes is
                served at its project&rsquo;s name. Next.js runs as a long-lived server (<code>next start</code>, on
                Bun); an app that needs Node.js sets <code>runtime: &quot;node&quot;</code>.
              </p>
            </Step>

            <Step id="logs" title="4. Logs and rollbacks">
              <Terminal
                title="Watch and undo"
                lines={[
                  "$ tiffin logs web",
                  "$ tiffin logs web -f",
                  "# follow them",
                  "$ tiffin rollback web",
                  "# back to an earlier production deploy (the last 5 are kept, up to 20 with keepVersions)",
                ]}
              />
              <p>
                Every production deploy also keeps an address of its own, so you can open an earlier version before you
                roll back to it. Error tracking (Sentry-compatible), traces and alerts are on the box too: see{" "}
                <Link href="/docs/observe">monitoring</Link>.
              </p>
            </Step>

            <Step id="postgres" title="5. Use the database">
              <p>
                Every project has its own Postgres 18 database. Your app gets <code>DATABASE_URL</code> (through the
                box&rsquo;s connection pooler) and <code>DIRECT_DATABASE_URL</code> (straight to Postgres), set for you.
                Connect with <a href="https://github.com/porsager/postgres">postgres.js</a> (
                <code>npm install postgres</code>), once per process:
              </p>
              <Code title="db.ts">{DB}</Code>
              <p>
                Keep <code>prepare: false</code> on <code>DATABASE_URL</code>: through the pooler, postgres.js can
                otherwise retry a query with its parameters encoded twice. Drizzle, Kysely and Prisma 7 sit on top of the
                same drivers.
              </p>
              <h3>Migrations</h3>
              <p>
                Give the app a <code>release</code> command. It runs once per deploy, after the build and before the new
                version takes traffic; if it fails, the old version keeps serving.
              </p>
              <Code title="tiffin.config.ts">{RELEASE}</Code>
              <h3>From your computer</h3>
              <Terminal
                title="Query it"
                lines={[
                  '$ tiffin sql hello "select now()"',
                  "# one read-only statement",
                  "$ tiffin db tunnel hello",
                  "# localhost:15432 over SSH, with a postgresql:// URL for psql or TablePlus",
                ]}
              />
              <p>
                Postgres listens only inside the box: there is no public address. Backups run every 6 hours, and Postgres
                restores to any second of the last 7 days. See <Link href="/docs/data">Postgres, KV and backups</Link>.
              </p>
            </Step>

            <Step id="sign-in" title="6. Add sign-in">
              <p>
                Add <code>auth</code> to the project&rsquo;s services, then run <code>tiffin plan</code> and{" "}
                <code>tiffin apply</code> again. The box then runs{" "}
                <a href="https://www.better-auth.com">Better Auth</a> for your app at <code>/api/auth/*</code> on its own
                hosts, with users and sessions in the project&rsquo;s own database.
              </p>
              <Code title="tiffin.config.ts">{AUTH_CONFIG}</Code>
              <p>
                In Next.js, <code>@shiptiffin/sdk/next/auth</code> follows the Next.js authentication guide: an
                optimistic check in <code>proxy.ts</code>, the real check next to the data, and Server Actions that sign
                in. There is no auth route or config to write.
              </p>
              <Code title="proxy.ts">{PROXY}</Code>
              <Code title="app/lib/dal.ts">{DAL}</Code>
              <Code title="app/actions.ts">{ACTIONS}</Code>
              <ul>
                <li>
                  <strong>Email sign-in needs a mail provider.</strong> Passwords, magic links and reset emails go
                  through your own provider (Resend, Postmark, SES or any SMTP), set once in the dashboard under{" "}
                  <strong>Settings › Email</strong>. Until then production refuses them; passkeys and Google keep
                  working, and previews use a test inbox.
                </li>
                <li>
                  <strong>Google sign-in needs an OAuth app</strong> in Google&rsquo;s console. Add its keys on the
                  project&rsquo;s <strong>Auth → Sign-in settings</strong> page, which shows the one redirect URI to
                  register.
                </li>
                <li>
                  Don&rsquo;t put your own routes under <code>/api/auth/</code>: those requests never reach your app.
                </li>
              </ul>
              <p>
                Sign-up forms, organizations and roles are in <Link href="/docs/auth">sign-in</Link>.
              </p>
            </Step>

            <Step id="domain" title="7. Use your own domain">
              <p>
                Give the app its own name. This is a normal change: you see the plan, then confirm it.
              </p>
              <Terminal
                title="A domain for the app"
                lines={[
                  "$ tiffin domains add hello --domain example.com --app web --www",
                  "# lists the DNS records to add: A (and AAAA) to the server, or one CNAME for a subdomain",
                  "$ tiffin domains list hello",
                  "# waiting_for_dns, issuing, then live",
                  "$ tiffin domains check hello example.com",
                  "# check DNS now instead of waiting",
                ]}
              />
              <p>
                Once the records point at the server, the box gets a Let&rsquo;s Encrypt certificate in a few seconds and
                renews it on its own. Plain HTTP redirects to HTTPS.
              </p>
              <p>
                To move the whole box (dashboard and every app) to your domain instead, point <code>@</code> and{" "}
                <code>*</code> at the server with two A records, then:
              </p>
              <Terminal
                title="A domain for the whole box"
                lines={["$ tiffin domain check --domain example.com", "$ tiffin domain set example.com"]}
              />
              <p>
                Apps then live at <code>&lt;project&gt;.example.com</code>. A managed box keeps its shiptiffin.app address
                as a second name. More in <Link href="/docs/domains">domains</Link>.
              </p>
            </Step>

            <Step id="github" title="8. Deploy on every push (optional)">
              <p>
                In the dashboard, <strong>Settings › Git › Connect GitHub</strong> makes a GitHub App for your box, then{" "}
                <strong>New project › Import from GitHub</strong> picks a repository, its branch and folder. Every push to
                the branch then deploys, and every pull request gets a preview at its own address with its own copy of
                the database, removed when the pull request closes.
              </p>
              <p>
                Prefer pushing to the box itself? Run <code>tiffin git-remote --add</code>, then{" "}
                <code>git push tiffin main</code>. See{" "}
                <Link href="/docs/apps#deploy-from-github">deploy from GitHub</Link>.
              </p>
            </Step>

            <Step id="vps" title="What you get vs a plain VPS + Docker">
              <p>
                The same Hetzner server, set up by hand with Docker, works too. Here is what you&rsquo;d build yourself.
              </p>
              <FactTable
                caption="A Tiffin box compared with a plain VPS and Docker"
                highlight={1}
                head={["", "Tiffin box", "VPS + Docker, by hand"]}
                rows={[
                  ["HTTPS", "Certificates issued and renewed for every app, preview and domain", "A proxy you add and configure (Caddy, Traefik, nginx)"],
                  ["Postgres", "One database per project, behind a pooler, with branches", "A container you configure, tune and upgrade"],
                  ["Backups", "Every 6 hours; Postgres to any second of the last 7 days", "Scripts you write, schedule and test"],
                  ["Deploys", "Health-checked switch with no dropped requests; rollback to any of the last 5 (up to 20)", "Yours to build, or a restart with a gap"],
                  ["Previews", "One per pull request, with its own database copy", "More tooling"],
                  ["Sign-in", "Passwords, magic links, passkeys, Google and more, on the box", "A library you add, host and keep patched"],
                  ["The server itself", "SSH keys only, a firewall, CrowdSec, daily security updates", "Yours to set up"],
                  ["One app using too much", "A limit per project: memory, CPU, database and KV", "Container limits you set by hand"],
                  ["Logs, errors, analytics", "On the box, nothing to install", "More services to run or pay for"],
                  ["Cost", "The server, plus $19 a month managed or nothing self-hosted", "The server"],
                ]}
                note={
                  <>
                    From <Link href="/docs/quickstart#what-tiffin-does-to-the-server">what Tiffin does to the server</Link>,{" "}
                    <Link href="/docs/apps">apps</Link>, <Link href="/docs/data">data</Link> and{" "}
                    <Link href="/docs/concepts">concepts</Link>.
                  </>
                }
              />
              <Fit
                ours={[
                  "When a Tiffin box fits",
                  [
                    "Several apps that each need a database, sign-in or jobs",
                    "You want deploys, previews and backups without writing them",
                    "You want a coding agent to run the server through plan and apply",
                  ],
                ]}
                theirs={[
                  "When a plain VPS fits better",
                  [
                    "One container that already runs the way you like",
                    "An app that needs ports other than HTTP and HTTPS (the box lets in only SSH, HTTP and HTTPS)",
                    "You want to choose and configure every piece yourself",
                    "You need something past pre-1.0 software on one server: Tiffin is pre-1.0, and a hardware fault means downtime until the server is back",
                  ],
                ]}
              />
            </Step>
          </article>
        </div>
      </div>

      <CtaBand title="Your Next.js app, on your own server">
        Managed for $19 a month plus the server, or free to run yourself. Coming from Vercel? Read{" "}
        <Link href="/guides/move-from-vercel">moving off Vercel</Link>.
      </CtaBand>
    </>
  );
}
