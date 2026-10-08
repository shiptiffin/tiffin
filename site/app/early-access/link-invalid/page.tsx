import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "Link not valid", robots: { index: false } };

export default function LinkInvalid() {
  return (
    <Notice title="That link doesn’t work any more.">
      <p>
        It may be from an older email: signing up again sends a fresh link, and only the newest one works. Or the
        address was removed from the list. Join again and use the newest email, or write to{" "}
        <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
      </p>
      <div className="actions">
        <Link className="btn btn-primary" href="/early-access">
          Join the list
        </Link>
        <Link className="btn btn-quiet" href="/">
          Back to the home page
        </Link>
      </div>
    </Notice>
  );
}
