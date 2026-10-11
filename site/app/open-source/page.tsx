// /open-source: what's free, what the managed box adds, the self-host path, the licence
// and how to contribute. Facts from docs/guide/quickstart.md, managed.md, data.md,
// README.md (License) and CONTRIBUTING.md.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, FactTable, Faq, Fit, No, PageHead, Partly, Points, Section, Terminal, Yes } from "../kit";
import "./open-source.css";

export const metadata = pageMeta({
  title: "Tiffin: the open-source (AGPL) app server",
  description:
    "Tiffin is AGPL-3.0 and free to run on Hetzner or any Ubuntu server you can SSH into. What's free, what the $19 managed box adds, and how to contribute.",
  path: "/open-source",
});
export const dynamic = "force-static";

const UPDATED = "2026-10-10";
const REPO = "https://github.com/shiptiffin/tiffin";

/* Exactly as docs/guide/quickstart.md has them. */
const HETZNER = [
  "$ curl -fsSL https://shiptiffin.com/install.sh | sh",
  "$ export HCLOUD_TOKEN=...                               # the Hetzner token",
  "$ tiffin up --provider hetzner --name shop --dry-run    # what it makes, and the monthly price",
  "$ tiffin up --provider hetzner --name shop              # a few minutes",
];
const SSH = [
  "$ curl -fsSL https://shiptiffin.com/install.sh | sh",
  "$ tiffin up --provider ssh --name shop --host root@YOUR_SERVER_IP",
];
const BUILD = [
  "$ bun install     # the JS workspace (dashboard, SDK, auth engine, emails)",
  "$ make build      # bin/tiffin",
  "$ make test       # Go tests with the race detector, Bun tests, then make site-test",
  "$ make lint       # gofmt, go vet, staticcheck",
];

const FAQ: { q: string; a: React.ReactNode }[] = [
  {
    q: "Is the self-hosted version cut down?",
    a: (
      <p>
        No. It is the same software as on a managed box: every service, the dashboard, the CLI and the MCP server. What
        a managed box adds is service around it, listed above, not features in it.
      </p>
    ),
  },
  {
    q: "Do I need a ShipTiffin account to self-host?",
    a: (
      <p>
        No. Install the CLI and run <code>tiffin up</code>. You need a server (or a Hetzner token to make one) and SSH.
      </p>
    ),
  },
  {
    q: "Can I use it for a commercial product?",
    a: (
      <p>
        Yes. The AGPL lets you run Tiffin for any purpose, including paid products. Your apps import the SDK, which is
        Apache-2.0, so your own code stays under whatever licence you choose.
      </p>
    ),
  },
  {
    q: "Can I move between self-hosted and managed?",
    a: (
      <p>
        Yes, project by project. Export a project to a single <code>.tiffin</code> file with its code, data and files,
        and import it on the other box, or move it in one step. See{" "}
        <Link href="/docs/moving">copying and moving projects</Link>.
      </p>
    ),
  },
  {
    q: "What happens to a managed box if I stop paying?",
    a: (
      <p>
        It keeps running in your Hetzner account, as yours. Automatic updates pause, and off-box backup copies,
        monitoring emails and support stop; the <code>name.shiptiffin.app</code> address keeps working for 30 days.
        Details on <Link href="/docs/managed#if-you-stop-paying">managed boxes</Link>.
      </p>
    ),
  },
];

export default function OpenSource() {
  return (
    <div className="oss">
      <PageHead
        kicker="Open source"
        title="Run Tiffin yourself, free."
        crumbs={[["Open source", "/open-source"]]}
        updated={UPDATED}
        actions={
          <>
            <a className="btn btn-primary" href={REPO}>
              See the code on GitHub
            </a>
            <Link className="btn btn-quiet" href="/docs/quickstart">
              Read the quickstart
            </Link>
          </>
        }
      >
        <p>
          Tiffin, the software on every ShipTiffin box, is open source under AGPL-3.0. Install it on your own server
          and you get the same box: apps, Postgres, KV, files, sign-in, email, jobs, analytics, error tracking and
          backups, with nothing to pay us.
        </p>
      </PageHead>

      <div className="wrap oss-answer">
        <Answer>
          <p>
            All of Tiffin is free: the platform is AGPL-3.0 and the SDK your apps import is Apache-2.0. The code is at{" "}
            <a href={REPO}>github.com/shiptiffin/tiffin</a>, and it runs on Hetzner or on any Ubuntu 24.04 or 26.04
            server you can SSH into.
          </p>
          <p>
            The $19 managed box is the same software with service around it: setup in your own Hetzner account,
            monitoring from outside, encrypted backups off the server, a <code>name.shiptiffin.app</code> address,
            resizing from your account, and support.
          </p>
        </Answer>
      </div>

      <Section
        id="free"
        title="What's free: all of it."
        sub="There is no community edition and no paid tier of the software. A box you run yourself has every part a managed box has."
      >
        <Points
          items={[
            [
              "The whole platform",
              <p key="p">
                Apps (Next.js first), previews, Postgres with branches, KV, files, sign-in, email, jobs, analytics, error
                tracking, backups and domains with HTTPS.
              </p>,
            ],
            [
              "The tools to run it",
              <p key="t">
                The dashboard, the <code>tiffin</code> CLI and the MCP server for coding agents. One binary is all three,
                and the box itself.
              </p>,
            ],
            [
              "Updates",
              <p key="u">
                A box running a release installs new signed releases by itself, about every hour, after a backup. Turn it
                off or give it a window.
              </p>,
            ],
            [
              "Backups, on and off the box",
              <p key="b">
                Postgres goes back to any second of the last 7 days. Copy every backup, encrypted, to any S3-compatible
                bucket you choose.
              </p>,
            ],
          ]}
        />
      </Section>

      <Section
        id="managed"
        title="What the $19 managed box adds."
        sub="Self-hosting is the same software with the care left to you. Here is the difference, line by line."
        tone="sunk"
      >
        <FactTable
          caption="Self-hosted and managed boxes compared"
          head={["", "Self-hosted", "Managed by ShipTiffin"]}
          highlight={2}
          rows={[
            ["The software", <Yes key="a">The same</Yes>, <Yes key="b">The same</Yes>],
            [
              "Making the server",
              <Partly key="a">You run tiffin up, on Hetzner or any Ubuntu server</Partly>,
              <Yes key="b">We make it in your own Hetzner account, in about 5 minutes</Yes>,
            ],
            [
              "Updates",
              <Yes key="a">The box installs signed releases itself</Yes>,
              <Yes key="b">The same, while you&rsquo;re subscribed</Yes>,
            ],
            [
              "Monitoring from outside",
              <No key="a">Yours to set up</No>,
              <Yes key="b">Every 5 minutes, with an email when the box stops answering</Yes>,
            ],
            [
              "Backups off the server",
              <Partly key="a">To a bucket you set, encrypted</Partly>,
              <Yes key="b">Every 6 hours, encrypted with a key only you hold, kept 30 days</Yes>,
            ],
            [
              "An address",
              <Partly key="a">An sslip.io address until you set your own domain</Partly>,
              <Yes key="b">A free name.shiptiffin.app with HTTPS, plus your own domains</Yes>,
            ],
            [
              "Resizing",
              <Partly key="a">tiffin up with a new type</Partly>,
              <Yes key="b">From your account</Yes>,
            ],
            ["Support", <No key="a">Issues on GitHub</No>, <Yes key="b">By email</Yes>],
            [
              "Price",
              "Free. Your host bills you for the server.",
              "$19 a month per box ($12 for the first 100 customers, locked for 24 months), plus the server, billed by Hetzner to you at their prices.",
            ],
          ]}
          note={
            <>
              Managed: no usage charges, seats or per-project fees, a 14-day money-back guarantee, monthly only. How it
              works, and what we can and can&rsquo;t reach on your server: <Link href="/docs/managed">managed boxes</Link>{" "}
              and <Link href="/security">security</Link>.
            </>
          }
        />
      </Section>

      <Section
        id="self-host"
        title="Run it yourself."
        sub="Install the CLI on macOS or Linux (on Windows, inside WSL), then make a box. It prints the dashboard address and a one-time sign-in link."
      >
        <div className="oss-terms">
          <div>
            <h3 className="oss-h3">On Hetzner</h3>
            <p>
              Make a Read &amp; Write API token in the Hetzner Cloud console (your project → Security → API tokens). It
              makes the server, a 40 GB data volume, a firewall and an SSH key in your Hetzner project.
            </p>
            <Terminal title="Terminal" lines={HETZNER} />
          </div>
          <div>
            <h3 className="oss-h3">On any Ubuntu server</h3>
            <p>
              Any Ubuntu 26.04 server (24.04 also works) you can reach over SSH, as root or a user with passwordless
              sudo.
            </p>
            <Terminal title="Terminal" lines={SSH} />
          </div>
        </div>
        <p className="oss-more">
          Open the sign-in link and add a passkey in Settings. The <Link href="/docs/quickstart">quickstart</Link> covers
          the rest: connecting your agent, your first project, resizing and Tiffin&rsquo;s own updates. Or let your coding
          agent do it: the <Link href="/agents">setup prompt</Link> has a self-hosted path.
        </p>
      </Section>

      <Section id="licence" title="The licence, in plain words." sub="A summary, not legal advice. The LICENSE file is what counts.">
        <Points
          items={[
            [
              "Use it for anything",
              <p key="u">
                Run Tiffin for yourself, your company or your customers, free or paid. The platform is{" "}
                <code>AGPL-3.0-only</code>.
              </p>,
            ],
            [
              "Your apps stay yours",
              <p key="a">
                What ends up inside your apps is Apache-2.0: the SDK, the starters, templates and examples, and the
                analytics tracker. Your app&rsquo;s code can use any licence you choose.
              </p>,
            ],
            [
              "Change it freely",
              <p key="c">
                Read it, fork it and change it. If you run a changed copy as a service for others, offer them its source.
              </p>,
            ],
            [
              "Contributions keep the same terms",
              <p key="i">
                What you send is licensed under the licence of the part it touches: AGPL-3.0-only for the platform,
                Apache-2.0 for the parts above.
              </p>,
            ],
          ]}
        />
        <p className="oss-more">
          Read the <a href={`${REPO}/blob/main/LICENSE`}>LICENSE</a> and the README&rsquo;s{" "}
          <a href={`${REPO}#license`}>License section</a>, which lists every Apache-2.0 part.
        </p>
      </Section>

      <Section
        id="contribute"
        title="Contribute."
        sub="The guide is CONTRIBUTING.md at the root of the repository. The short version:"
        tone="sunk"
      >
        <div className="oss-terms">
          <div>
            <Points
              items={[
                [
                  "Open an issue first",
                  <p key="i">
                    For anything bigger than a fix, so we agree on the shape before you build it. Small fixes and doc
                    corrections can go straight to a pull request.
                  </p>,
                ],
                [
                  "Agent-written pull requests are welcome",
                  <p key="a">
                    Held to the same bar: you read the diff, <code>make test</code> and <code>make lint</code> pass, and
                    the description says what you checked by hand.
                  </p>,
                ],
                [
                  "Docs move with the code",
                  <p key="d">
                    A change people can see updates <code>docs/guide</code> in the same pull request, and a new gap goes
                    in <code>docs/guide/limits.md</code>.
                  </p>,
                ],
                [
                  "Security problems by email",
                  <p key="s">
                    Not in an issue: write to hello@shiptiffin.com with &ldquo;Security&rdquo; in the subject.
                  </p>,
                ],
              ]}
            />
          </div>
          <div>
            <h3 className="oss-h3">Build and test</h3>
            <p>You need Go 1.27 or later and Bun.</p>
            <Terminal title="Terminal" lines={BUILD} />
            <p className="oss-more">
              <a href={`${REPO}/blob/main/CONTRIBUTING.md`}>Read CONTRIBUTING.md</a>: where things are, the module
              contract, local and dev boxes, tests and releases.
            </p>
          </div>
        </div>
      </Section>

      <Section id="fit" title="Which one to pick.">
        <Fit
          ours={[
            "Managed is the better fit when",
            [
              "You want a box running in five minutes without touching SSH",
              "You'd rather someone else watched it from outside",
              "You want backups off the server without setting up a bucket",
              "You want a name.shiptiffin.app address and someone to email",
            ],
          ]}
          theirs={[
            "Self-hosting is the better fit when",
            [
              "You already run servers and watch them",
              "Your server isn't at Hetzner",
              "You want to change Tiffin itself",
              "You want to pay for nothing but the server",
            ],
          ]}
        />
      </Section>

      <Section id="faq" title="Questions">
        <Faq items={FAQ} />
        <p className="oss-more">
          Related: <Link href="/agents">what your coding agent can do on a box</Link> and{" "}
          <Link href="/docs/limits">what works and what doesn&rsquo;t yet</Link>.
        </p>
      </Section>

      <CtaBand title="Same box either way.">
        Run it yourself for free, or let us set it up in your Hetzner account for $19 a month.
      </CtaBand>
    </div>
  );
}
