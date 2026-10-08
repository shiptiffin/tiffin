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
              It stays in your browser until you click Create. Then it travels encrypted to our setup worker, which uses it and forgets it
              when setup ends, whether it worked or not. We keep only a fingerprint (12 characters of its hash) so you can tell which key it
              was.
            </li>
            <li>
              Tick &ldquo;Keep my key&rdquo; and we store it encrypted (envelope encryption, with the master key outside our database) for
              one-click resizes. Remove it any time in your account. Without it, a resize asks for a key, uses it and forgets it.
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
              that we have no way to log in.
            </li>
            <li>
              For a short while we keep the new box&rsquo;s setup sign-in key, so &ldquo;Open your dashboard&rdquo; signs you straight in. It
              goes a day after you first open the dashboard, after 7 days at most, or when you click Forget. Add a passkey on the box and you
              sign in on your own.
            </li>
            <li>
              Updates: the box fetches signed Tiffin releases itself, in its maintenance window. We never push anything to it; nothing of ours
              connects to it except monitoring, which only loads its health page.
            </li>
            <li>
              Once a day the box tells us its Tiffin version and the names of any failing checks. No data, no project names, no visitors.
            </li>
            <li>
              Support never logs in by default. If you want us to look at the server itself, you grant it: a temporary SSH key you add and
              open the firewall for, and remove when we&rsquo;re done.
            </li>
          </ul>
        </div>

        <div className="cp-card">
          <h2>If you stop paying</h2>
          <p className="cp-sub">
            Your server and every app on it keep running, untouched. Automatic updates, monitoring emails and support stop. Your{" "}
            <strong>name.shiptiffin.app</strong> address keeps working for 30 days, so you can point a domain of your own at the box. We
            never stop, slow or delete anything over billing. Deleting the server happens only when you ask, with a key you paste right then.
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
