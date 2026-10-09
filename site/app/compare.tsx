// What a box replaces: a typical indie setup assembled from separate
// services, against one Starter box. The two bills and their line items live
// in compare-ledger.tsx; every price there was read on the vendor's own
// pricing page on 2026-10-08. Re-check each one, and AS_OF, before changing it.
import { CompareLedger } from "./compare-ledger";

const AS_OF = "October 2026";

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
            Four small Next.js apps, each with a database and sign-in, plus a cache, error tracking and analytics.
            Here is the monthly bill for that setup bought separately, next to the same setup on one ShipTiffin box
            in your Hetzner account.
          </p>
        </div>

        <CompareLedger />

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
          US dollars before tax, for one person. The Hetzner figure is for the smallest server (2 vCPU, 4 GB) with its
          IPv4 address and a 40 GB data volume, and changes with Hetzner&rsquo;s prices. Email isn&rsquo;t counted on
          either side: on ShipTiffin you send through your own provider too.
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
