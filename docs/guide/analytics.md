# Analytics

Turn on `services: { analytics: {} }` and the box counts visits to every app of
the project. No script is needed for page views, nothing is sent to anyone else,
and no cookie banner is needed for it: there are no cookies.

```ts
// tiffin.config.ts
services: { analytics: { retentionDays: 365 } },  // how long visits are kept (default 365)
```

## How it counts

- **Page views come from the box's edge.** Every request to an app passes the
  edge, which logs it. A request counts as a page view when it is a `GET` for a
  top-level page (`Sec-Fetch-Dest: document`) that answered 2xx or 304 and is not
  a prefetch or prerender. Server-rendered and static pages count without any
  code, and ad blockers cannot hide them.
- **Bots are dropped**: crawlers, link previews, monitors, headless browsers and
  scripts, matched by user agent with the [isbot](https://github.com/omrilotan/isbot)
  list plus a few heuristics. `tiffin status` shows how many were dropped.
- **Visitors** are a hash of the app, the IP address and the user agent with a
  salt that changes every day and is deleted after 48 hours. The IP and user agent
  are never stored. A visitor on two different days counts as two visitors, by
  design: days cannot be linked.
- **Sessions** end after 30 minutes without a page view. Bounce rate is the share
  of sessions with one page view; visit duration is the time from a session's
  first to its last page view.
- Browsers that send [Global Privacy Control](https://globalprivacycontrol.org)
  are not counted at all (page views, script events and `track()` with a
  request). Do Not Track is not read: browsers have dropped it.
- Query strings are dropped, except `utm_*` and `ref`. Countries come from
  [DB-IP Lite](https://db-ip.com) (IP Geolocation by DB-IP, CC BY 4.0); browsers,
  systems and devices from [uap-core](https://github.com/ua-parser/uap-core).
- Days are UTC.

## The script (optional)

For single-page apps, custom events, outbound link clicks and file downloads, add
the 1.3 KB script to your pages. `tiffin analytics setup --project shop` prints
the exact tag:

```html
<script defer src="https://t.<box domain>/script.js"></script>
```

```js
tiffin.track("Signup", { plan: "pro" })
```

The script reports client-side navigations only; the first load of each page is
already counted at the edge. If a page is *not* served by the box (a static site
elsewhere), add `data-initial` to the tag so the script counts the first load too.
It uses no cookies and no storage.

## Server events

Apps of the project get `TIFFIN_ANALYTICS_URL`, `TIFFIN_ANALYTICS_KEY` and
`TIFFIN_ANALYTICS_SCRIPT`:

```ts
import { track } from "@shiptiffin/sdk/analytics";

await track("Signup", { plan: "pro" }, { request }); // joins the visitor's session
await track("Invoice paid", { amount: 49 });        // an event without a visitor
```

`track()` never throws and does nothing when analytics is off, so the same code
runs in development.

## Web Vitals

How fast pages feel to real visitors: Largest Contentful Paint (LCP), Interaction
to Next Paint (INP), Cumulative Layout Shift (CLS), First Contentful Paint (FCP)
and Time to First Byte (TTFB). In Next.js, render `<WebVitals />` once in the
root layout:

```tsx
// app/layout.tsx
import { WebVitals } from "@shiptiffin/sdk/next/vitals";

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <WebVitals />
        {children}
      </body>
    </html>
  );
}
```

It takes Next's own measurements (`useReportWebVitals`) and reports each page
under its route (`/products/[id]`, not `/products/42`). Anywhere else, call
`reportWebVitals()` from `@shiptiffin/sdk/vitals` in browser code; it measures with the
browser's performance observers, no library needed.

Both send one beacon per page load, when the page is hidden, to `/_tiffin/vitals`
on the page's own origin: the box answers that path on every app host, so there
is no extra host, no CORS and nothing for an ad blocker to match. A beacon is
JSON, `{"path": "/pricing", "metrics": {"LCP": 1840, "CLS": 0.02}}` (milliseconds;
CLS has no unit), at most 4 KB. Bots are dropped, and one address may send 60
beacons a minute (6,000 for the whole box). Query strings are dropped and IDs in
paths become `[id]`.

The box keeps no raw samples: each sample adds one to a bucket 5% wide, per app,
day, page and metric, so the 75th percentile (the figure Google rates) and the
share of good samples come from a few small rows a day, accurate to about 2.5%.
Each app keeps at most 200 pages a day; more count as `(other)`. They follow the
service's `retentionDays`.

```bash
tiffin analytics vitals --project shop --period 30d   # p75 and rating per metric, per page and per day
```

## Reading it

The dashboard's Analytics page shows a period (24 hours to 90 days, or days you
choose) against the one before it, by hour or by day, for the whole project or
one app. Choosing a row anywhere (a page, a source, a country, a browser)
filters the whole page; filters stay in the address, so a filtered view can be
shared. Filters select whole visits: a source or UTM tag by the visit's first
page view, entry and exit pages by its first and last page, a page by any page
it viewed (and then only that page's views are counted), and country, browser,
system and device by the visitor.

```bash
tiffin analytics overview --project shop --period 7d   # today, yesterday, 24h, 7d, 30d, 90d, 12mo, or --from/--to
tiffin analytics overview --project shop --period 30d --country DE --source Google --interval hour
tiffin analytics realtime --project shop               # the last 5 and 30 minutes
tiffin analytics events --project shop --period 30d    # custom events and their properties
```

Agents get the same as MCP tools (`analytics_overview`, `analytics_realtime`,
`analytics_events`, `analytics_vitals`, `analytics_setup`). Paths, referrers and event names come from
visitors, so tools mark their output as untrusted data.

## Privacy

You can link this section from your privacy policy, or copy it.

**What is collected.** For each page view: the page's address without its query
string (except `utm_*` and `ref` tags), the site that sent the visitor (its name
only, such as "news.ycombinator.com", never the full address), the country, the
kind of browser, operating system and device, and the time. Custom events add
their name and the properties the site's own code sends. Visitors are told apart
by a code computed from the site, the visitor's IP address and browser string,
and a secret that changes every day; the code can't be turned back into either,
and each day's secret is deleted within 48 hours, so visits on different days
can't be linked, even by us.

**What is not collected.** No cookies, and nothing else is stored on or read
back from the visitor's device. No IP addresses or browser strings are stored.
No location finer than the country. No names, email addresses or account IDs
(email addresses in page addresses are replaced with `[email]`; links followed
out of the site are kept without their query string or fragment). No tracking
across sites: the same person on two sites is two unrelated visitors. Bots are
not counted, and neither is anyone whose browser sends Global Privacy Control.

**Where it is kept, and for how long.** On the server that runs the site, and
nowhere else: nothing is sent to Tiffin or to any other company. Visits are kept
for `retentionDays` (365 by default) and then deleted; turning analytics off, or
destroying the project, deletes all of them.

**Your part.** Custom events hold whatever your code sends: don't send user IDs,
email addresses or anything else that identifies a person, unless your privacy
policy covers it. Separately from analytics, the box keeps request logs for
debugging and security (IP address, path without query, browser string) for 30
days by default (`logsRetention` in the box's observe settings); mention them
as you would any server's logs.

**How this fits EU rules.** Under the GDPR the IP address is processed for a
moment, in memory, to make the daily code and the country, which suits
legitimate interest (Article 6(1)(f)); what is stored is statistics about
visits, not about people. Page views counted at the edge read only what every
browser sends with every request. The optional script sends events from the
browser, which the ePrivacy rules (Article 5(3), as the EDPB reads it in its
Guidelines 2/2023) may treat as reaching into the device; it is built to meet
the conditions the French CNIL set for audience measurement without consent:
for the site's own statistics only, anonymous figures, no cross-site tracking,
no sharing, country-level location, kept well under 25 months, and an easy way
to object (Global Privacy Control). Say in your privacy policy that you
measure visits this way. This is how the design maps to the rules, not legal
advice; your own circumstances may differ.

**On Tiffin's hosted service** the server is run by Tiffin on your behalf, so
Tiffin is your processor: we provide a data processing agreement covering the
visit statistics and the request logs.

## Storage and limits

Events and daily rollups live in one SQLite file on the data disk
(`/var/lib/tiffin/analytics/analytics.db`), written in batches every second; the
realtime view is kept in memory and rebuilt from the file when the box restarts.
This comfortably handles side-project traffic (hundreds of thousands of events a
day). The store sits behind a small interface so a Postgres store (partitioned
events) can replace it for busier boxes. Removing the service deletes the
project's analytics data; shortening `retentionDays` deletes older events.

Not yet: funnels and retention, goals, share links, excluding your own
visits, and automatic events from sign-ups and deploys.
