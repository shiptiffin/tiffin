// What a box replaces: a typical indie setup assembled from separate
// services, priced from each vendor's own pricing page. Re-check every number
// (and AS_OF) before changing this section; link the page each one came from.

const AS_OF = "October 2026";

type Row = { what: string; how: React.ReactNode; lean: string; typical: string };

const SOURCES = [
  ["Vercel", "https://vercel.com/pricing"],
  ["Supabase", "https://supabase.com/pricing"],
  ["Upstash", "https://upstash.com/pricing/redis"],
  ["Resend", "https://resend.com/pricing"],
  ["Sentry", "https://sentry.io/pricing/"],
  ["PostHog", "https://posthog.com/pricing"],
  ["Plausible", "https://plausible.io/#pricing"],
] as const;

const ROWS: Row[] = [
  {
    what: "Hosting",
    how: "Vercel Pro, one seat. Usage past its $20 credit is billed on top, with no cap unless you set one.",
    lean: "$20",
    typical: "$20+",
  },
  {
    what: "Databases and sign-in",
    how: "Supabase Pro is $25 with one project, then about $10 for each more. Lean: all four apps share one.",
    lean: "$25",
    typical: "$55",
  },
  {
    what: "KV",
    how: "Upstash Redis: free up to 500K commands a month, or $10 for a fixed 250 MB.",
    lean: "$0",
    typical: "$10",
  },
  {
    what: "Email",
    how: "Resend: free up to 3,000 a month (100 a day), or Pro at $20 for 50,000.",
    lean: "$0",
    typical: "$20",
  },
  {
    what: "Error tracking",
    how: "Sentry: free for one person, or Team at $26.",
    lean: "$0",
    typical: "$26",
  },
  {
    what: "Analytics",
    how: "PostHog: free up to 1M events. Or Plausible at $19 for up to 10 sites.",
    lean: "$0",
    typical: "$19",
  },
];

const JUGGLE = [
  ["Six dashboards", "One dashboard"],
  ["Six bills, each with its own usage", "One bill, the same every month"],
  ["Six sets of API keys and env vars", "Connection details set on every app for you"],
  ["Your data in six places", "Plain Postgres, S3 and Redis on one server, exportable any time"],
  ["Usage that can run up a bill", "Hard limits per project, so nothing runs away"],
  ["Seats to pay for as the team grows", "No per-seat pricing"],
] as const;

export function Compare() {
  return (
    <section id="replaces" className="band" aria-labelledby="replaces-title">
      <div className="wrap">
        <div className="section-head">
          <h2 id="replaces-title" className="h2">
            What it replaces.
          </h2>
          <p className="section-sub">
            Four small Next.js apps, each with a database and sign-in, plus a cache, email, error tracking and
            analytics. Here is that setup from separate services, and on one box.
          </p>
        </div>

        <div className="cmp-grid">
          <div>
            <table className="cmp">
              <caption className="sr-only">Monthly cost of four small apps from separate services, in US dollars</caption>
              <thead>
                <tr>
                  <th scope="col">Part</th>
                  <th scope="col" className="num">
                    Lean
                  </th>
                  <th scope="col" className="num">
                    Typical
                  </th>
                </tr>
              </thead>
              <tbody>
                {ROWS.map((r) => (
                  <tr key={r.what}>
                    <th scope="row">
                      <span className="cmp-what">{r.what}</span>
                      <span className="cmp-how">{r.how}</span>
                    </th>
                    <td className="num">{r.lean}</td>
                    <td className="num">{r.typical}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr className="cmp-total">
                  <th scope="row">Separate services, a month</th>
                  <td className="num">$45</td>
                  <td className="num">$150+</td>
                </tr>
                <tr className="cmp-us">
                  <th scope="row">
                    <span className="cmp-what">ShipTiffin Starter</span>
                    <span className="cmp-how">All of the above for the four apps, on one server, flat.</span>
                  </th>
                  <td className="num" colSpan={2}>
                    $29
                  </td>
                </tr>
              </tfoot>
            </table>
            <p className="cmp-note">
              Lean counts one person and the free tiers where they cover four small apps. It doesn&rsquo;t count
              Vercel Hobby (for non-commercial use only) or Supabase Free (two active projects, paused after a week
              without use). Prices in US dollars before tax, from each vendor&rsquo;s pricing page as of {AS_OF}:{" "}
              {SOURCES.map(([name, href], i) => (
                <span key={name}>
                  <a href={href} rel="noopener">
                    {name}
                  </a>
                  {i < SOURCES.length - 1 ? ", " : "."}
                </span>
              ))}
            </p>
          </div>

          <div className="juggle">
            <h3 className="fit-title">What you stop juggling</h3>
            <dl>
              {JUGGLE.map(([them, us]) => (
                <div key={us} className="juggle-row">
                  <dt>{them}</dt>
                  <dd>{us}</dd>
                </div>
              ))}
            </dl>
          </div>
        </div>
      </div>
    </section>
  );
}
