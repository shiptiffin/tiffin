// Pricing: one plan, per box, plus the server the customer pays Hetzner for
// directly. Founding price for the first 100 customers, locked for 24 months.
// Monthly only at launch. Keep in step with the FAQ, the comparison and the terms.
import Link from "next/link";

const PRICE = "$19";
const FOUNDING = "$12";

/** What our fee pays for. Keep these true to what's built (docs/guide). */
const INCLUDED = [
  "Setup in your Hetzner account, in about 5 minutes",
  "The dashboard, and every part on the box",
  "Automatic updates",
  "Monitoring from outside the box",
  "A free yourname.shiptiffin.app address",
  "One-click upgrades to a bigger server",
  "Support from the people who build it",
];

const PARTS = [
  "Apps and previews",
  "Postgres, KV and files",
  "Sign-in for your apps' users",
  "Jobs, analytics and error tracking",
  "Restore to any moment in the last 7 days",
  "Your own domains, with HTTPS",
];

const TERMS = [
  ["Monthly", "Monthly billing only, for now."],
  ["14-day money-back", "Not for you? Full refund of what you paid us."],
  ["Cancel any time", "Your server and apps keep running. Updates and the managed extras stop."],
  ["No usage charges", "No seats, no per-project or per-request fees from us."],
] as const;

export function Pricing() {
  return (
    <section id="pricing" className="band" aria-labelledby="pricing-title">
      <div className="wrap">
        <div className="section-head">
          <h2 id="pricing-title" className="h2">
            One plan. Your server at Hetzner&rsquo;s price.
          </h2>
          <p className="section-sub">
            You pay us for the box&rsquo;s software and care, and Hetzner for the server, at their prices. Neither
            bill moves with your traffic.
          </p>
        </div>

        <div className="price2">
          <div className="plan plan-featured price2-main">
            <h3 className="plan-name">ShipTiffin</h3>
            <p className="plan-price">
              <span className="plan-amount">{PRICE}</span>
              <span className="plan-per">/month per box</span>
            </p>
            <p className="plan-founding">
              <strong>{FOUNDING}/month</strong> for our first 100 customers, locked for 24 months.
            </p>
            <div className="price2-lists">
              <div>
                <h4 className="price2-cap">What we do</h4>
                <ul className="price-list">
                  {INCLUDED.map((x) => (
                    <li key={x}>{x}</li>
                  ))}
                </ul>
              </div>
              <div>
                <h4 className="price2-cap">Every part, on every box</h4>
                <ul className="price-list">
                  {PARTS.map((x) => (
                    <li key={x}>{x}</li>
                  ))}
                </ul>
              </div>
            </div>
            <Link className="btn btn-primary" href="/start">
              Get started
            </Link>
          </div>

          <div className="plan price2-server">
            <h3 className="plan-name">Your server, from Hetzner</h3>
            <p className="plan-price">
              <span className="plan-amount">&euro;5&ndash;7</span>
              <span className="plan-per">/month</span>
            </p>
            <p className="plan-yearly">For a small server: 2 vCPU, 4 GB of memory.</p>
            <p className="price2-text">
              Billed by Hetzner to you, in your own account, at their prices. A small server fits about 3&ndash;5
              small apps; move to a bigger one in a click when you need more room, and pay Hetzner&rsquo;s price for
              that size.
            </p>
          </div>
        </div>

        <dl className="price2-terms">
          {TERMS.map(([k, v]) => (
            <div key={k}>
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>

        <p className="price-email">
          <strong>Email goes through your own provider.</strong> Connect SendGrid, Resend, Postmark, Amazon SES or any
          SMTP service in the dashboard by pasting its key; every project on the box can then send.
        </p>
      </div>
    </section>
  );
}
