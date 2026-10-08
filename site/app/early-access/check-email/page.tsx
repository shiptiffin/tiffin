import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "Check your inbox", robots: { index: false } };

export default function CheckEmail() {
  return (
    <Notice kicker="You're on the list" title="Now check your inbox.">
      <p>
        We sent a link to the address you gave. Click it to confirm, and we&rsquo;ll send your sign-up link as soon
        as sign-up opens. It can take a minute. Nothing there? Look in spam, or write to{" "}
        <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
      </p>
      <div className="actions">
        <Link className="btn btn-quiet" href="/">
          Back to the home page
        </Link>
      </div>
    </Notice>
  );
}
