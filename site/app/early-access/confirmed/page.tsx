import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "You're on the list", robots: { index: false } };

export default function Confirmed() {
  return (
    <Notice title="You’re on the list.">
      <p>
        Thanks for confirming. We&rsquo;re letting people in a few at a time. When it&rsquo;s your turn, we&rsquo;ll
        email you an invite with your founding price. Until then, we won&rsquo;t write.
      </p>
      <p>
        Want to tell us more about what you&rsquo;d run? Write to{" "}
        <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>. A person reads it.
      </p>
      <div className="actions">
        <Link className="btn btn-quiet" href="/">
          Back to the home page
        </Link>
      </div>
    </Notice>
  );
}
