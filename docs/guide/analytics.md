# Analytics

Turn on `services: { analytics: {} }` and the box counts visits to every app of
the project. No script is needed for page views, nothing is sent to anyone else,
and no cookie banner is needed for it: there are no cookies.

```ts
// tiffin.config.ts
services: { analytics: { retentionDays: 365 } },  // raw events; daily totals are kept until the service is removed
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
import { track } from "tiffin-sdk/analytics";

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
import { WebVitals } from "tiffin-sdk/next/vitals";

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
`reportWebVitals()` from `tiffin-sdk/vitals` in browser code; it measures with the
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

```bash
tiffin analytics overview --project shop --period 7d   # today, yesterday, 24h, 7d, 30d, 90d, 12mo, or --from/--to
tiffin analytics realtime --project shop               # the last 5 and 30 minutes
tiffin analytics events --project shop --period 30d    # custom events and their properties
```

Agents get the same as MCP tools (`analytics_overview`, `analytics_realtime`,
`analytics_events`, `analytics_vitals`, `analytics_setup`). Paths, referrers and event names come from
visitors, so tools mark their output as untrusted data.

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
