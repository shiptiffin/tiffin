// Pricing. The plans differ only in the size of the server; every part is on
// every box. To change a price, edit PLANS here (yearly = 11 months).
import { Fragment } from "react";
import { EMAIL } from "./chrome";

export type Plan = {
  name: string;
  /** "$29" or "from $249" */
  price: string;
  /** "/month" */
  per: string;
  /** ["2 vCPU", "4 GB memory", "80 GB disk"]: shown as one line, broken only between parts. */
  server: string[];
  /** "About 3–5 small apps" */
  fits: string;
  /** The early-access price for the first year, e.g. "$22". */
  founding?: string;
  /** Paid yearly: 11 months' price, e.g. "$319". */
  yearly?: string;
  cta: { label: string; href: string };
  featured?: boolean;
};

export const PLANS: Plan[] = [
  {
    name: "Starter",
    price: "$29",
    per: "/month",
    server: ["2 vCPU", "4 GB memory", "80 GB disk"],
    fits: "About 3–5 small apps",
    founding: "$22",
    yearly: "$319",
    cta: { label: "Get early access", href: "#early-access" },
    featured: true,
  },
  {
    name: "Plus",
    price: "$59",
    per: "/month",
    server: ["4 vCPU", "8 GB memory", "160 GB disk"],
    fits: "About 8–12 small apps",
    founding: "$44",
    yearly: "$649",
    cta: { label: "Get early access", href: "#early-access" },
  },
  {
    name: "Pro",
    price: "$119",
    per: "/month",
    server: ["8 vCPU", "16 GB memory", "320 GB disk"],
    fits: "About 15–25 small apps",
    founding: "$89",
    yearly: "$1,309",
    cta: { label: "Get early access", href: "#early-access" },
  },
  {
    name: "Dedicated",
    price: "from $249",
    per: "/month",
    server: ["Dedicated CPUs, sized with you"],
    fits: "A busy product, or many apps",
    cta: { label: "Talk to us", href: `mailto:${EMAIL}?subject=${encodeURIComponent("A dedicated box")}` },
  },
];

/** On every plan. Keep these true to what's built (docs/guide). */
const EVERY_BOX = [
  "As many projects as fit on the box",
  "Postgres, KV and files",
  "Email and sign-in",
  "Jobs, analytics and error tracking",
  "Databases restore to any moment in the last 7 days",
  "A preview for every pull request",
  "Your own domains, with HTTPS",
  "Limits per project",
  "No usage charges, ever",
];

const TERMS = [
  "14-day money-back guarantee",
  "Cancel any time",
  "Pay yearly and get 1 month free",
  "No free plan",
  "Servers in the EU (Germany or Finland)",
];

export function Pricing() {
  return (
    <section id="pricing" className="band" aria-labelledby="pricing-title">
      <div className="wrap">
        <div className="section-head">
          <h2 id="pricing-title" className="h2">
            One price for the whole box.
          </h2>
          <p className="section-sub">
            Every plan has every part. The plans differ only in the size of the server, and you can move up when you
            need more room.
          </p>
        </div>
        <ol className="plans">
          {PLANS.map((p) => (
            <li key={p.name} className={p.featured ? "plan plan-featured" : "plan"}>
              <h3 className="plan-name">{p.name}</h3>
              <p className="plan-price">
                <span className="plan-amount">{p.price}</span>
                <span className="plan-per">{p.per}</span>
              </p>
              <p className="plan-yearly">{p.yearly ? `or ${p.yearly} a year` : "Monthly or yearly"}</p>
              <dl className="plan-spec">
                <div>
                  <dt>Server</dt>
                  <dd>
                    {p.server.map((x, i) => (
                      <Fragment key={x}>
                        <span className="nw">{x}</span>
                        {i < p.server.length - 1 ? " · " : ""}
                      </Fragment>
                    ))}
                  </dd>
                </div>
                <div>
                  <dt>Room for</dt>
                  <dd>{p.fits}</dd>
                </div>
              </dl>
              <p className="plan-founding">
                {p.founding ? (
                  <>
                    <strong>{p.founding}/month</strong> for your first year with early access
                  </>
                ) : (
                  "Tell us what you run and we’ll size it."
                )}
              </p>
              <a className={p.featured ? "btn btn-primary" : "btn btn-quiet"} href={p.cta.href}>
                {p.cta.label}
              </a>
            </li>
          ))}
        </ol>
        <div className="price-more">
          <div>
            <h3 className="fit-title">Every plan includes</h3>
            <ul className="price-list price-list-2">
              {EVERY_BOX.map((x) => (
                <li key={x}>{x}</li>
              ))}
            </ul>
          </div>
          <div>
            <h3 className="fit-title">The terms</h3>
            <ul className="price-list">
              {TERMS.map((x) => (
                <li key={x}>{x}</li>
              ))}
            </ul>
            <p className="price-founding">
              <strong>Founding offer.</strong> People on the early-access list get 25% off their first year, and
              their price is locked for 24 months.
            </p>
          </div>
        </div>
      </div>
    </section>
  );
}
