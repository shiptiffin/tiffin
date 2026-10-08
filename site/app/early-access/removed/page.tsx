import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "Request removed", robots: { index: false } };

export default function Removed() {
  return (
    <Notice title="Your request is removed.">
      <p>
        We&rsquo;ve deleted your address and your answers, and we won&rsquo;t email you about an invite. Changed
        your mind? You can request one again any time.
      </p>
      <div className="actions">
        <Link className="btn btn-quiet" href="/">
          Back to the home page
        </Link>
      </div>
    </Notice>
  );
}
