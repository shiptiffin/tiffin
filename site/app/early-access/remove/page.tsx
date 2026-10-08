import type { Metadata } from "next";
import Link from "next/link";
import { Notice } from "../../early-access-next";

export const metadata: Metadata = { title: "Remove your request", robots: { index: false }, referrer: "no-referrer" };

// A button, not the link itself, so a mail scanner opening the link removes nobody.
export default async function Remove({ searchParams }: { searchParams: Promise<{ t?: string }> }) {
  const { t } = await searchParams;
  return (
    <Notice title="Remove your invite request?">
      <p>We&rsquo;ll delete your address and your answers, and won&rsquo;t email you about an invite again.</p>
      <form action="/api/early-access/remove" method="post" className="actions">
        <input type="hidden" name="t" value={typeof t === "string" ? t : ""} />
        <button className="btn btn-primary" type="submit">
          Remove me
        </button>
        <Link className="btn btn-quiet" href="/">
          Keep me on it
        </Link>
      </form>
    </Notice>
  );
}
