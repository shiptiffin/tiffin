// /security: what ShipTiffin (the company) can and can't reach, how the Hetzner key is
// used and forgotten, signed updates, what the box protects, backups and the honest
// limits. Facts from docs/guide/managed.md, security.md, protection.md, data.md,
// quickstart.md, limits.md and SECURITY.md. The step-by-step setup is on /managed.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, FactTable, Faq, No, PageHead, Partly, Points, Section } from "../kit";
import "./security.css";

export const metadata = pageMeta({
  title: "Security: your Hetzner key, server and data",
  description:
    "What ShipTiffin can and can't reach on your server: how your Hetzner key is used then forgotten, signed updates, encrypted backups, and the honest limits.",
  path: "/security",
});
export const dynamic = "force-static";

const UPDATED = "2026-10-10";

const FAQ: { q: string; a: React.ReactNode }[] = [
  {
    q: "Can ShipTiffin staff log in to my server?",
    a: (
      <p>
        Not after setup. The setup SSH key is removed, port 22 is closed to everyone but your own key (if you add
        one), and once you&rsquo;ve signed in we have no way into your dashboard. If you want us to look at the server,
        write to hello@shiptiffin.com: you add a temporary SSH key for us in your dashboard (Settings › Server access),
        and remove it afterwards.
      </p>
    ),
  },
  {
    q: "What do I trust ShipTiffin with?",
    a: (
      <p>
        During setup, this site and our setup worker see your Hetzner token and act as root on the new server, as any
        installer does. The SSH keys put on it are listed in the &ldquo;ready&rdquo; email and in your account, so one
        you don&rsquo;t recognise stands out. After setup we hold no key, no sign-in link and no token. Updates are
        signed releases your box pulls itself: whoever holds our release key is trusted, as with any software you
        install.
      </p>
    ),
  },
  {
    q: "What if my Hetzner token leaks?",
    a: (
      <p>
        Delete it in Hetzner (Security → API tokens). A token reaches only the Hetzner project it was made in, which is
        why you make a project just for ShipTiffin. It can&rsquo;t reach your Hetzner login, password or billing.
      </p>
    ),
  },
  {
    q: "Do you see my visitors or analytics?",
    a: (
      <p>
        No. Analytics are counted on your box. The six-hourly check-in carries the Tiffin version, uptime, the names of
        failing checks and whether you&rsquo;ve signed in, nothing about projects, data, logs or visitors.
      </p>
    ),
  },
  {
    q: "Can I read the code that handles my key?",
    a: (
      <p>
        Yes. The managed service&rsquo;s control plane is in the same public repository as the box:{" "}
        <a href="https://github.com/shiptiffin/tiffin/tree/main/internal/cloud">internal/cloud</a> and the worker in{" "}
        <a href="https://github.com/shiptiffin/tiffin/tree/main/cmd/tiffin-provisioner">cmd/tiffin-provisioner</a>.
      </p>
    ),
  },
  {
    q: "What if ShipTiffin goes away?",
    a: (
      <p>
        Your server is in your Hetzner account and keeps running. The software on it is open source, your data is in
        standard formats (Postgres, S3-compatible files, a Redis-compatible store), and any project exports to a single
        file. See <Link href="/open-source">running Tiffin yourself</Link>.
      </p>
    ),
  },
];

export default function Security() {
  return (
    <div className="sec">
      <PageHead
        kicker="Security"
        title="Your key, your server, your data."
        crumbs={[["Security", "/security"]]}
        updated={UPDATED}
        actions={
          <>
            <Link className="btn btn-primary" href="/docs/security">
              Read the security model
            </Link>
            <Link className="btn btn-quiet" href="/docs/limits">
              See the known limits
            </Link>
          </>
        }
      >
        <p>
          Your box runs in your own Hetzner account. Here is what ShipTiffin, the company, can reach, what it can&rsquo;t,
          and what the box does to protect your apps.
        </p>
      </PageHead>

      <div className="wrap sec-answer">
        <Answer>
          <p>
            ShipTiffin uses your Hetzner API key to build your server, then forgets it, and lists every call it made in
            your account. After setup it has no SSH access, never holds your box&rsquo;s owner token, and can&rsquo;t read
            your backups: your box encrypts them with a passphrase only you hold.
          </p>
          <p>
            The box pulls signed updates itself. The only thing of ours that connects to it is a monitor that loads its
            health page.
          </p>
        </Answer>
      </div>

      <Section
        id="reach"
        title="What ShipTiffin can and can't reach."
        sub="For a managed box. A box you run yourself has nothing of ours in it."
      >
        <FactTable
          caption="What ShipTiffin can reach on a managed box"
          head={["", "Can ShipTiffin reach it?", "How"]}
          rows={[
            [
              "Your Hetzner key",
              <Partly key="a">Only during a job</Partly>,
              "Sealed in your browser to our setup worker, used for setup (or a resize or delete), then forgotten. We keep a 12-character fingerprint so you can tell which key it was.",
            ],
            [
              "Your Hetzner login, password and billing",
              <No key="a" />,
              "A token reaches only the Hetzner project it was made in.",
            ],
            [
              "Your server over SSH",
              <No key="a" />,
              "After install, the setup key is removed from the server and from your project. Port 22 is closed to everyone but your own key, if you add one.",
            ],
            [
              "Your dashboard",
              <No key="a">No, once you sign in</No>,
              "We keep one sign-in link for your first sign-in. It works once, and your box refuses it 24 hours after making it. After you sign in, the box makes no more for us.",
            ],
            [
              "Your apps, databases and files",
              <No key="a" />,
              "They live on your server and never pass through us.",
            ],
            [
              "Your backups off the server",
              <Partly key="a">Stored, not readable</Partly>,
              "Copied to our storage, encrypted on your box with a passphrase we never see.",
            ],
            [
              "What your box tells us",
              <Partly key="a">Health only</Partly>,
              "Every 6 hours: Tiffin version, uptime, the names of failing checks, and whether you've signed in. No project names, data, logs or visitors.",
            ],
            [
              "Pushing anything to your box",
              <No key="a" />,
              "The box fetches signed releases itself. Our monitor only loads its health page, every five minutes.",
            ],
          ]}
          note={
            <>
              The long version, step by step: <Link href="/docs/managed">how managed boxes work</Link> and{" "}
              <Link href="/docs/managed#what-shiptiffin-can-and-cant-do-on-your-server">
                what ShipTiffin can and can&rsquo;t do on your server
              </Link>
              .
            </>
          }
        />
      </Section>

      <Section
        id="key"
        title="Your Hetzner key: used to build, then forgotten."
        sub="ShipTiffin needs a Hetzner API key to make your server. Here is everything that happens to it."
        tone="sunk"
      >
        <Points
          numbered
          items={[
            [
              "A project just for ShipTiffin",
              <p key="1">
                You make a new Hetzner project for your box and a Read &amp; Write key in it, so the key can only see that
                project.
              </p>,
            ],
            [
              "Sealed in your browser",
              <p key="2">
                The key stays in your browser until you click Create. The website then seals it to the setup
                worker&rsquo;s public key (X25519 and AES-256-GCM, bound to your box). The website can&rsquo;t open it;
                only the worker can, a separate service whose secrets the website never sees.
              </p>,
            ],
            [
              "Used once, then forgotten",
              <p key="3">
                The worker clears the key when the job ends, whether setup worked or not, and anything older than two hours
                is wiped regardless. A resize or a delete asks you for a key again, for that one job.
              </p>,
            ],
            [
              "Only what we made",
              <p key="4">
                Everything we create carries a label with your box&rsquo;s id. Nothing without that label is changed or
                deleted.
              </p>,
            ],
            [
              "Every call listed",
              <p key="5">
                Each request we make with your key is in your account: method, path, time and Hetzner&rsquo;s answer. The
                key itself is never logged.
              </p>,
            ],
            [
              "Revoke it any time",
              <p key="6">Delete the key in Hetzner whenever you like. Your box keeps running.</p>,
            ],
          ]}
        />
      </Section>

      <Section
        id="updates"
        title="Updates are signed, and pulled by the box."
        sub="Nothing is pushed to your server. Every box, managed or self-hosted, updates itself the same way."
      >
        <Points
          items={[
            [
              "Checked against a signature",
              <p key="s">
                About every hour the box reads its channel&rsquo;s release list and checks its signature against release
                keys built into Tiffin. A list or build that doesn&rsquo;t match is refused, and so is anything older than
                what runs.
              </p>,
            ],
            [
              "A backup first, a way back after",
              <p key="b">
                The box checks the build&rsquo;s sha256, takes a backup, then switches. Apps keep serving; if the new
                build isn&rsquo;t healthy within 90 seconds, the previous one comes back.
              </p>,
            ],
            [
              "When you want them",
              <p key="w">
                Give the box an update window to install only at a set time, or turn automatic updates off. Each update is
                in the audit log.
              </p>,
            ],
            [
              "The installer checks too",
              <p key="i">
                <code>install.sh</code> picks your build from the signed release list and checks its sha256 (and the
                list&rsquo;s signature when <code>minisign</code> is installed).
              </p>,
            ],
          ]}
        />
      </Section>

      <Section
        id="box"
        title="What the box protects."
        sub="On by default, on every box. The full list, with the mechanisms, is in the security model and protection docs."
        tone="sunk"
      >
        <Points
          items={[
            [
              "Sign-in",
              <p key="s">
                Passkeys, one-time links, or Google and GitHub for people already on the box. Sessions last 12 hours in a
                host-only, HttpOnly cookie. A new browser gets an email, and Settings › Sign-ins ends any session.
              </p>,
            ],
            [
              "Confirm it's you",
              <p key="c">
                Creating an API key that lasts over a day or has full access, adding a passkey, or changing an email address
                needs a strong sign-in in the last 10 minutes. Each new key or passkey is emailed to you.
              </p>,
            ],
            [
              "Keys and secrets",
              <p key="k">
                API keys are random 200-bit secrets; only their SHA-256 is stored. Secrets are encrypted with the
                box&rsquo;s own key and never shown after you set them.
              </p>,
            ],
            [
              "Network",
              <p key="n">
                The firewall lets in only the edge&rsquo;s ports, and SSH for keys only (on a managed box, once you add
                your key). App ports answer only the box, and apps can&rsquo;t send mail straight out on port 25.
              </p>,
            ],
            [
              "Apps kept apart",
              <p key="a">
                Apps run in containers without raw sockets. Every connection the box opens to an app is checked to reach
                that app&rsquo;s own project.
              </p>,
            ],
            [
              "Traffic protection",
              <p key="t">
                Rate limits per IP (strict on sign-in), security headers, CrowdSec banning scanners and brute-forcers, and
                cut-offs for slow clients. An under-attack mode and a WAF are there when you need them.
              </p>,
            ],
            [
              "The server itself",
              <p key="h">
                Daily security updates and SSH keys only. A kernel update reboots the server only at a time you choose.
              </p>,
            ],
            [
              "A record of everything",
              <p key="r">
                Every change and security event is logged with the key or person that made it: keys created and revoked,
                sign-ins, secrets.
              </p>,
            ],
          ]}
        />
        <p className="sec-more">
          Read <Link href="/docs/security">the security model</Link>, <Link href="/docs/protection">protection</Link>{" "}
          and, for coding agents, <Link href="/agents">what an agent can and can&rsquo;t do</Link>.
        </p>
      </Section>

      <Section
        id="backups"
        title="Backups you can restore, and only you can read."
        sub="Backups on the box undo mistakes. Copies off the box survive losing the server."
      >
        <Points
          items={[
            [
              "On the box",
              <p key="o">
                A full backup every day and an incremental one every 6 hours, of Postgres, KV, files, email, analytics and
                the box&rsquo;s settings. A restore takes a safety backup first.
              </p>,
            ],
            [
              "Any second of the last 7 days",
              <p key="p">
                Postgres keeps its log of changes, so it can go back to any moment of the last week. KV and files come
                back from the nearest backup.
              </p>,
            ],
            [
              "Off the box, managed",
              <p key="m">
                Every 6 hours, to our storage, encrypted with a passphrase your box makes and shows only to you. Kept 30
                days. The box reaches only its own folder, with keys that last 48 hours.
              </p>,
            ],
            [
              "Off the box, your own bucket",
              <p key="y">
                Set any S3-compatible bucket instead (Cloudflare R2, AWS S3, Hetzner Object Storage, MinIO), managed box
                or not. Keep the passphrase off the box: nothing restores without it.
              </p>,
            ],
          ]}
        />
        <p className="sec-more">
          Details: <Link href="/docs/data#backups">backups and restore</Link>.
        </p>
      </Section>

      <Section
        id="limits"
        title="The honest limits."
        sub="What this doesn't protect you from yet. Every gap is also in the limits page."
        tone="sunk"
      >
        <Points
          items={[
            [
              "It's one server",
              <p key="o">
                A hardware fault means downtime until the server is back. Copies off the box survive it, but bringing a
                lost managed box&rsquo;s copies to a new box goes through support for now.
              </p>,
            ],
            [
              "It's before version 1.0",
              <p key="v">ShipTiffin is young and made by a small team. Expect changes, and keep your passphrase safe.</p>,
            ],
            [
              "The box is the boundary",
              <p key="b">
                Apps share the server&rsquo;s network. The box checks every connection it opens to an app, but full
                isolation between apps isn&rsquo;t done yet. Don&rsquo;t share a box with people you don&rsquo;t trust.
              </p>,
            ],
            [
              "Sign in right away",
              <p key="s">
                Until your first sign-in, whoever controls your ShipTiffin account (or our website&rsquo;s database) could
                get an owner sign-in link. Sign in and add a passkey as soon as the box is ready.
              </p>,
            ],
            [
              "A full key needs no second yes",
              <p key="k">
                A key with full access applies changes without asking anyone; your agent&rsquo;s client is what asks you.
                Give unattended agents a read-only key for one project.
              </p>,
            ],
            [
              "Our side has gaps too",
              <p key="g">
                Monitoring runs from one place. shiptiffin.app isn&rsquo;t on the Public Suffix List yet, so don&rsquo;t
                set cookies on the parent domain. A compromised website could get credentials to delete your off-box
                copies, though not to read them.
              </p>,
            ],
          ]}
        />
        <p className="sec-more">
          The full list: <Link href="/docs/limits#managed-boxes-shiptiffin">managed boxes</Link> and{" "}
          <Link href="/docs/limits#isolation-between-apps">isolation between apps</Link> on the limits page.
        </p>
      </Section>

      <Section id="report" title="Found a problem?">
        <div className="sec-report">
          <p>
            Please don&rsquo;t open a public issue. Email hello@shiptiffin.com with &ldquo;Security&rdquo; in the subject:
            what you found, how to reproduce it, the version (<code>tiffin version</code>) and what someone could do with
            it. We aim to reply within a few working days and credit you in the release notes if you&rsquo;d like. There
            is no bug bounty.
          </p>
          <p>
            Only the latest release gets security fixes, and boxes install it by themselves within about an hour. Phishing
            or malware on a shiptiffin.app address is abuse: <Link href="/abuse">report it here</Link>.
          </p>
        </div>
      </Section>

      <Section id="faq" title="Questions">
        <Faq items={FAQ} />
      </Section>

      <CtaBand title="A server that stays yours.">
        $19 a month per box, plus the server, billed by Hetzner to you at their prices. Or{" "}
        <Link href="/open-source">run it yourself</Link>, free.
      </CtaBand>
    </div>
  );
}
