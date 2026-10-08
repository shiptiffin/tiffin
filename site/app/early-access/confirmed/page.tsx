import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "Your request is in", robots: { index: false } };

export default function Confirmed() {
  return (
    <Notice kicker="Email confirmed" title="You’re on the list.">
      <p>
        Thanks for confirming. Here&rsquo;s what happens next, and roughly when.
      </p>
      <ol className="ea-next ea-next-done">
        <li>
          <h2>Invites go out weekly</h2>
          <p>
            We let people in in small groups, so every new box gets our full attention. We read every request, and
            the answers you gave help us size your box. We write the week yours is ready.
          </p>
        </li>
        <li>
          <h2>Your invite</h2>
          <p>
            One email with a link to set up your account. Your box is set up for you, with every part already on
            it, ready for your first project. Until then, we won&rsquo;t write.
          </p>
        </li>
        <li>
          <h2>Your founding price</h2>
          <p>
            25% off your first year: Starter is $22 a month instead of $29, Plus $44 instead of $59, Pro $89 instead
            of $119. After that, your price doesn&rsquo;t go up for 24 months. You see it before you pay, and you
            have 14 days to change your mind.
          </p>
        </li>
      </ol>
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
