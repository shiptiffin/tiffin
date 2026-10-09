// /managed: how a managed box works, what we do with the customer's Hetzner
// key, and what we can and can't do on their server. The long version is
// docs/guide/managed.md.
import type { Metadata } from "next";
import Link from "next/link";
import "../cloud.css";

export const metadata: Metadata = {
  title: "How managed boxes work",
  description: "Your server runs in your own Hetzner account. What ShipTiffin does with your key, and what it can and can't do on your server.",
  alternates: { canonical: "/managed" },
};

export default function Managed() {
  return (
    <section className="cp wrap">
      <div className="cp-col">
        <p className="kicker">How it works</p>
        <h1>Your server, in your Hetzner account</h1>
        <p className="cp-sub">
          ShipTiffin creates the server in your own Hetzner Cloud project and installs Tiffin on it. Hetzner bills you for the server; we
          charge $19 a month for keeping it managed. Your apps and data never run on anything of ours.
        </p>

        <div className="cp-card">
          <h2>Your Hetzner key</h2>
          <ul className="cp-guide">
            <li>Make it in a new Hetzner project just for ShipTiffin, so it sees only that project.</li>
            <li>
              It stays in your browser until you click Create. Then this website seals it to our setup worker&rsquo;s public key: the
              website can&rsquo;t open it; only the worker can, a separate service whose secrets the website never sees. The worker uses it
              and forgets it when setup ends, whether it worked or not. We keep only a fingerprint (12 characters of its hash) so you can
              tell which key it was.
            </li>
            <li>We never store your key. A resize (or deleting the server) asks for a key again, uses it for that one job and forgets it.</li>
            <li>
              We only ever touch what we create for your box: each server, volume, firewall and key carries a label with your box&rsquo;s
              id, and nothing without it is changed or deleted.
            </li>
            <li>Every request we make with your key is listed in your account: when, what, and what Hetzner answered.</li>
            <li>We never see your Hetzner password or payment details. Delete the token in Hetzner at any time; the box keeps running.</li>
          </ul>
        </div>

        <div className="cp-card">
          <h2>What we can and can&rsquo;t do on your server</h2>
          <ul className="cp-guide">
            <li>
              Setup logs in with a key made for that one setup, through a firewall opened to our setup worker only. When Tiffin is installed
              we delete that key from the server and from your Hetzner project, close SSH in the firewall, and check both are gone. After
              that we have no way to log in over SSH.
            </li>
            <li>
              We never hold your box&rsquo;s owner token; it stays on the box. At setup the box makes one sign-in link, which we keep so
              your first &ldquo;Open your dashboard&rdquo; signs you in. Your box enforces it: it works once, and the box refuses it 24
              hours after making it. We delete it when you use it (or click Forget). Add a passkey on the box then: after that we have no way to
              sign in to your box.
            </li>
            <li>
              Updates: the box fetches signed Tiffin releases itself, in its maintenance window. We never push anything to it; nothing of ours
              connects to it except monitoring, which only loads its health page.
            </li>
            <li>
              Every six hours the box tells us its Tiffin version and the names of any failing checks. No data, no project names, no
              visitors. If it stops checking in for 72 hours, we take its shiptiffin.app address off the server&rsquo;s IP (the server may
              be gone, and its IP someone else&rsquo;s); the next check-in brings it back.
            </li>
            <li>
              Backups are copied off your server every 6 hours to our storage, into a folder for your box alone, and kept 30 days. Your box
              encrypts them first with a passphrase it makes and shows only to you (in its dashboard, under Backups): we can&rsquo;t read
              them. It reaches its folder with short-lived keys we renew at each check-in, which reach nothing else. Prefer your own bucket?
              Set it in the dashboard instead.
            </li>
            <li>
              Support never logs in by default, and there&rsquo;s no button for it yet. If you want us to look at the server itself, write
              to hello@shiptiffin.com and we arrange it by email: a temporary SSH key you add and open the firewall for, and remove when
              we&rsquo;re done.
            </li>
          </ul>
        </div>

        <div className="cp-card">
          <h2>If you stop paying</h2>
          <p className="cp-sub">
            Your server and every app on it keep running, untouched. Automatic updates, backup copies off the server, monitoring emails
            and support stop. Your{" "}
            <strong>name.shiptiffin.app</strong> address keeps working for 30 days, with an email when that starts, a week before it goes and
            when it goes, so you can point a domain of your own at the box. Renew in your account and everything comes back. We never stop,
            slow or delete anything over billing. Deleting the server happens only when you ask, with a key you paste right then.
          </p>
        </div>

        <div className="cp-row" style={{ marginTop: 24 }}>
          <Link className="btn btn-primary" href="/start">
            Get started
          </Link>
          <Link className="btn btn-quiet" href="/abuse">
            Report abuse
          </Link>
        </div>
      </div>
    </section>
  );
}
