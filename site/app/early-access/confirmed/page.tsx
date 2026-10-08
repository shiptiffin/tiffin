import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "You're on the list", robots: { index: false } };

export default function Confirmed() {
  return (
    <Notice kicker="Email confirmed" title="You’re on the list.">
      <p>Thanks for confirming. Here&rsquo;s what happens next.</p>
      <ol className="ea-next ea-next-done">
        <li>
          <h2>Your sign-up link, this week</h2>
          <p>One email, as soon as sign-up opens. Until then, we won&rsquo;t write.</p>
        </li>
        <li>
          <h2>Your box, about 5 minutes later</h2>
          <p>
            Make a Hetzner Cloud project just for ShipTiffin and paste an API key for it. We create the server in
            your account and set up every part on it.
          </p>
        </li>
        <li>
          <h2>The founding price</h2>
          <p>
            The first 100 customers pay $12 a month instead of $19, locked for 24 months, plus your server at
            Hetzner&rsquo;s prices. You have 14 days to change your mind.
          </p>
        </li>
      </ol>
      <p>
        Questions in the meantime? Write to <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>. A person
        reads it.
      </p>
      <div className="actions">
        <Link className="btn btn-quiet" href="/">
          Back to the home page
        </Link>
      </div>
    </Notice>
  );
}
