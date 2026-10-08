// What a box replaces: a typical indie setup assembled from separate
// services, against one Starter box. Every price was read on the vendor's own
// pricing page on 2026-10-08. Re-check
// each one, and AS_OF, before changing a number here.

const AS_OF = "October 2026";

type Line = { need: string; vendor: string; detail: string; lean: number; typical: number };

const LINES: Line[] = [
  {
    need: "Hosting for 4 apps",
    vendor: "Vercel Pro",
    detail: "One seat. Hobby is for non-commercial use only. Usage past the $20 credit is billed on top.",
    lean: 20,
    typical: 20,
  },
  {
    need: "Databases and sign-in",
    vendor: "Supabase Pro",
    detail: "$25 with one project, about $10 for each more. Lean: all four apps share one. Free allows two, paused after a quiet week.",
    lean: 25,
    typical: 55,
  },
  {
    need: "Cache",
    vendor: "Upstash Redis",
    detail: "Free up to 500K commands a month, or $10 for a fixed 250 MB.",
    lean: 0,
    typical: 10,
  },
  {
    need: "Error tracking",
    vendor: "Sentry",
    detail: "Free for one person and 5K errors, or Team at $26 billed yearly.",
    lean: 0,
    typical: 26,
  },
  {
    need: "Analytics",
    vendor: "PostHog or Plausible",
    detail: "PostHog free up to 1M events, or Plausible at $19 for up to 10 sites.",
    lean: 0,
    typical: 19,
  },
];

const LEAN = LINES.reduce((n, l) => n + l.lean, 0);
const TYPICAL = LINES.reduce((n, l) => n + l.typical, 0);

const SOURCES = [
  ["Vercel", "https://vercel.com/pricing"],
  ["Supabase", "https://supabase.com/pricing"],
  ["Upstash", "https://upstash.com/pricing/redis"],
  ["Sentry", "https://sentry.io/pricing/"],
  ["PostHog", "https://posthog.com/pricing"],
  ["Plausible", "https://plausible.io/#pricing"],
] as const;

const JUGGLE = [
  ["Dashboards", "5", "1"],
  ["Bills", "5", "2"],
  ["Sets of keys to copy around", "5", "0"],
  ["Places your data lives", "5", "1"],
] as const;

const ALSO = [
  ["No seats", "A teammate costs $20 a month more on Vercel Pro. On a box, people are free."],
  ["Hard caps per project", "A busy app is held at its limit. It can't slow the others or grow the bill."],
  ["Restore to any moment", "Your databases, to the second, from any point in the last 7 days."],
] as const;

const usd = (n: number) => `$${n}`;

export function Compare() {
  return (
    <section id="replaces" className="band replaces" aria-labelledby="replaces-title">
      <div className="wrap">
        <div className="section-head">
          <p className="kicker">What it replaces</p>
          <h2 id="replaces-title" className="h2">
            Five services and five bills, or one box.
          </h2>
          <p className="section-sub">
            A typical setup: four small Next.js apps, each with a database and sign-in, plus a cache, error tracking
            and analytics. Here it is from separate services, and on one ShipTiffin box in your Hetzner account.
          </p>
        </div>

        <div className="cmp">
          <div className="cmp-stack">
            <table className="cmp-table">
              <caption className="sr-only">
                The same setup from separate services, a month, lean and typical
              </caption>
              <thead>
                <tr>
                  <th scope="col">You need</th>
                  <th scope="col">From separate services</th>
                  <th scope="col" className="num">
                    Lean
                  </th>
                  <th scope="col" className="num">
                    Typical
                  </th>
                </tr>
              </thead>
              <tbody>
                {LINES.map((l) => (
                  <tr key={l.need}>
                    <th scope="row">{l.need}</th>
                    <td>
                      <span className="cmp-vendor">{l.vendor}</span>
                      <span className="cmp-detail">{l.detail}</span>
                    </td>
                    <td className="num" data-label="Lean">
                      {usd(l.lean)}
                    </td>
                    <td className="num" data-label="Typical">
                      {usd(l.typical)}
                    </td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr>
                  <th scope="row" colSpan={2}>
                    Separate services, a month
                  </th>
                  <td className="num" data-label="Lean">
                    {usd(LEAN)}
                  </td>
                  <td className="num" data-label="Typical">
                    {usd(TYPICAL)}+
                  </td>
                </tr>
              </tfoot>
            </table>
          </div>

          <aside className="cmp-box" aria-labelledby="cmp-box-title">
            <p className="cmp-box-label" id="cmp-box-title">
              On ShipTiffin
            </p>
            <p className="cmp-box-price">
              <span className="cmp-box-amount">$26</span>
              <span className="cmp-box-per">a month, for all four apps</span>
            </p>
            <p className="cmp-box-plan">
              $19 for ShipTiffin, plus about $7 for your server, billed by Hetzner. The first 100 customers pay $12
              instead of $19.
            </p>
            <ul className="cmp-box-list">
              <li>All four apps, with previews, on your own server</li>
              <li>A Postgres database and sign-in for each</li>
              <li>KV cache, files and jobs</li>
              <li>Error tracking and analytics</li>
              <li>Backups, restore to any moment</li>
            </ul>
            <p className="cmp-box-save">
              Less than the lean setup, about a fifth of the typical one, and the same every month.
            </p>
          </aside>
        </div>

        <p className="cmp-sources">
          Prices from each vendor&rsquo;s site, {AS_OF}:{" "}
          {SOURCES.map(([name, href], i) => (
            <span key={name}>
              <a href={href} rel="noopener">
                {name}
              </a>
              {i < SOURCES.length - 2 ? ", " : i === SOURCES.length - 2 ? " and " : "."}
            </span>
          ))}{" "}
          US dollars before tax, for one person. The Hetzner figure is for a small server (2 vCPU, 4 GB) and changes
          with Hetzner&rsquo;s prices. Email isn&rsquo;t counted on either side: on ShipTiffin you send through your
          own provider too.
        </p>

        <div className="juggle">
          <h3 className="juggle-title">What you stop juggling</h3>
          <dl className="juggle-grid">
            {JUGGLE.map(([what, before, after]) => (
              <div key={what} className="juggle-item">
                <dt>{what}</dt>
                <dd>
                  <span className="juggle-before">
                    <span className="sr-only">From </span>
                    {before}
                  </span>
                  <span className="juggle-arrow" aria-hidden="true">
                    →
                  </span>
                  <span className="juggle-after">
                    <span className="sr-only">to </span>
                    {after}
                  </span>
                </dd>
              </div>
            ))}
          </dl>
          <p className="juggle-note">
            On a box, every app gets its database, cache, storage, mail and sign-in settings as environment variables,
            set for you. There are no keys to copy between services.
          </p>
        </div>

        <dl className="also">
          {ALSO.map(([k, v]) => (
            <div key={k}>
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      </div>
    </section>
  );
}
