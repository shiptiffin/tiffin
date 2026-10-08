// The sign-up list, shown on /start until sign-up opens (every secret set and
// the cloud worker's tables made): leave your email and we send your link.
import { EarlyAccessForm } from "../early-access-form";
import type { ErrorCode } from "@/lib/form";

export function Waitlist({ code }: { code?: ErrorCode }) {
  return (
    <section id="form" className="ea ea-page">
      <div className="wrap ea-grid">
        <div className="ea-copy">
          <p className="kicker">Get started</p>
          <h1 className="h2">Sign-up opens this week.</h1>
          <p className="section-sub">Leave your email and we&rsquo;ll send your link.</p>
          <ol className="ea-next">
            <li>
              <h2>Confirm your email</h2>
              <p>One click on the link we send, so we know the address is yours.</p>
            </li>
            <li>
              <h2>Get your sign-up link</h2>
              <p>As soon as sign-up opens. Nothing else in between.</p>
            </li>
            <li>
              <h2>Have a Hetzner Cloud account ready</h2>
              <p>
                Setup asks for an API key from a Hetzner project made just for ShipTiffin. Your box is ready about 5
                minutes later.
              </p>
            </li>
          </ol>
          <p className="founding-line">
            The first 100 customers pay <strong>$12 a month</strong> instead of $19, locked for 24 months.
          </p>
        </div>
        <div className="ea-panel">
          <EarlyAccessForm initialError={code} />
        </div>
      </div>
    </section>
  );
}
