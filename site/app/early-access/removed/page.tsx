import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "Removed from the list", robots: { index: false } };

export default function Removed() {
  return (
    <Notice title="You’re off the list.">
      <p>
        We&rsquo;ve deleted your address and your answers, and we won&rsquo;t email you again. Changed your mind?
        You can <a href="/start">join again</a> any time.
      </p>
      <div className="actions">
        <Link className="btn btn-quiet" href="/">
          Back to the home page
        </Link>
      </div>
    </Notice>
  );
}
