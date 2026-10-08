// Pricing. Until prices are set, PLANS is empty and the section says when
// they come. To publish them, add plans to PLANS: the section lays them out
// as columns (one to four), and the first with `featured` gets the brass button.

export type Plan = {
  /** "Small box" */
  name: string;
  /** "$12" */
  price: string;
  /** "a month" */
  per: string;
  /** One sentence: who it's for. */
  blurb: string;
  /** What the box has: "4 GB of memory", "40 GB of storage", ... */
  features: string[];
  /** Founding price note, e.g. "$9 for early access members". */
  founding?: string;
  featured?: boolean;
};

export const PLANS: Plan[] = [];

/** True of every box, whatever the plan. Keep these true to what's built. */
const EVERY_BOX = [
  "One flat monthly price for the whole box",
  "As many projects as fit, each with its own limit",
  "Database, KV, files, email, sign-in, jobs and analytics included",
  "No charges per request or per project",
];

export function Pricing() {
  return (
    <section id="pricing" className="band" aria-labelledby="pricing-title">
      <div className="wrap">
        <div className="section-head">
          <h2 id="pricing-title" className="h2">
            Pricing
          </h2>
          <p className="section-sub">
            {PLANS.length
              ? "One price per box, every month. Every part is included on every box."
              : "Simple monthly pricing, announced at launch. Early access members get founding prices."}
          </p>
        </div>
        {PLANS.length ? <Plans plans={PLANS} /> : <Placeholder />}
      </div>
    </section>
  );
}

function Placeholder() {
  return (
    <div className="price-soon">
      <ul className="price-every" aria-label="Every box includes">
        {EVERY_BOX.map((x) => (
          <li key={x}>{x}</li>
        ))}
      </ul>
      <div className="price-soon-cta">
        <p>Join the list and we&rsquo;ll tell you the price before you pay anything.</p>
        <a className="btn btn-quiet" href="#early-access">
          Get early access
        </a>
      </div>
    </div>
  );
}

function Plans({ plans }: { plans: Plan[] }) {
  return (
    <>
      <ol className="plans" style={{ ["--plans" as string]: Math.min(plans.length, 4) }}>
        {plans.map((p) => (
          <li key={p.name} className={p.featured ? "plan plan-featured" : "plan"}>
            <h3 className="plan-name">{p.name}</h3>
            <p className="plan-price">
              <span className="plan-amount">{p.price}</span> <span className="plan-per">{p.per}</span>
            </p>
            {p.founding && <p className="plan-founding">{p.founding}</p>}
            <p className="plan-blurb">{p.blurb}</p>
            <ul className="plan-features">
              {p.features.map((f) => (
                <li key={f}>{f}</li>
              ))}
            </ul>
            <a className={p.featured ? "btn btn-primary" : "btn btn-quiet"} href="#early-access">
              Get early access
            </a>
          </li>
        ))}
      </ol>
      <ul className="price-every price-every-row" aria-label="Every box includes">
        {EVERY_BOX.map((x) => (
          <li key={x}>{x}</li>
        ))}
      </ul>
    </>
  );
}
