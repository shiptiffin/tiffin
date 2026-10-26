# Analytics

The box counts visits to every app of every project: analytics is always there
(list it in `services` only to set `retentionDays`). No script is needed for page views, nothing is sent to anyone else,
and no cookie banner is needed for it: there are no cookies.

```ts
// tiffin.config.ts
services: { analytics: { retentionDays: 365 } },  // how long visits are kept (default 365)
```

That is **Basic** analytics, on for every project. **Marketing attribution**, off until a
person turns it on, adds campaign tags, the source of each sign-up and, with Stripe
connected, of each payment: see [Marketing attribution](#marketing-attribution).

## How it counts

- **Page views come from the box's edge.** Every request to an app passes the
  edge, which logs it. A request counts as a page view when it is a `GET` for a
  top-level page (`Sec-Fetch-Dest: document`) that answered 2xx or 304 and is not
  a prefetch or prerender. Server-rendered and static pages count without any
  code, and ad blockers cannot hide them.
- **Bots are dropped**: crawlers, link previews, monitors, headless browsers and
  scripts, matched by user agent with the [isbot](https://github.com/omrilotan/isbot)
  list plus a few heuristics (a link in the user agent, no `Accept-Language`
  header, which every browser sends). So are requests from the IP ranges of cloud
  and hosting providers (AWS, Google Cloud, Azure, DigitalOcean, Hetzner, Linode,
  Oracle, OVH, Alibaba), where crawlers, AI agents and scrapers run and people
  rarely browse from; loopback and private addresses still count. Visits sent by
  sites on [Matomo's referrer spam list](https://github.com/matomo-org/referrer-spam-list)
  are dropped too. The script sends nothing from browsers driven by test tools
  (WebDriver, Selenium, Cypress). `tiffin status` shows how many were dropped.
- **Visitors** are a hash of the project, the IP address and the user agent with
  a salt that changes every day and is deleted after 48 hours, so a person on two
  apps of a project is one visitor of the project. The IP and user agent are
  never stored. A visitor on two different days counts as two visitors, by
  design: days cannot be linked, and a period's visitors are the sum of each
  day's.
- **Sessions** end after 30 minutes without a page view; one going on at
  midnight UTC carries on into the new day. Bounce rate is the share of sessions
  with one page view; visit duration is the time from a session's first to its
  last page view.
- **Sources**: a visit from another of the project's own apps or hosts is not a
  referral, and neither is coming back from a payment provider's checkout or billing
  page (Stripe, Lemon Squeezy, Polar, Paddle, PayPal, Dodo, GoCardless): the visit keeps
  the source that brought it. Google and Yandex mean their search pages;
  `mail.google.com` or `docs.google.com` show as themselves.
- Browsers that send [Global Privacy Control](https://globalprivacycontrol.org)
  are not counted at all (page views, script events and `track()` with a
  request). Do Not Track is not read: browsers have dropped it.
- Query strings are dropped. With marketing attribution, `utm_*` and `ref` tags are kept
  (Basic analytics keeps no campaign tags). Countries come from
  [DB-IP Lite](https://db-ip.com) (IP Geolocation by DB-IP, CC BY 4.0); browsers,
  systems and devices from [uap-core](https://github.com/ua-parser/uap-core).
- Days are UTC.

## The script (optional)

For single-page apps, custom events, outbound link clicks and file downloads, add
the 1.6 KB script to your pages. `tiffin analytics setup --project shop` prints
the exact tag:

```html
<script defer src="https://t.<box domain>/script.js"></script>
```

```js
tiffin.track("Signup", { plan: "pro" })
```

The script reports client-side navigations only; the first load of each page is
already counted at the edge. A navigation is a change of path: a change to the
query string alone (filters, search) is not a page view. If a page is *not*
served by the box (a static site elsewhere), add `data-initial` to the tag so the
script counts the first load too; a page the browser prerenders counts once the
visitor opens it. It uses no cookies and no storage.

## Server events

Apps of the project get `TIFFIN_ANALYTICS_URL`, `TIFFIN_ANALYTICS_KEY` and
`TIFFIN_ANALYTICS_SCRIPT`:

```ts
import { track } from "@shiptiffin/sdk/analytics";

await track("Signup", { plan: "pro" }, { request }); // joins the visitor's session
await track("Invoice paid", { amount: 49 });        // an event without a visitor
```

`track()` never throws and does nothing when analytics is off, so the same code
runs in development. An event sent without a request (no IP address or user
agent) counts as a visitor and a visit of its own.

## Sign-ups

Projects with sign-in (`services.auth`) count a `Signup` event for every new account,
with no code: the box's sign-in engine reports it, and it joins the visit it happened
in, so the events view and every filter (source, page, country) show where sign-ups
come from. In Basic analytics it is only an event, linked to no account.

## Marketing attribution

```ts
services: { analytics: { attribution: {} } },  // campaigns and signups on, no cookie
services: { analytics: { attribution: { signups: false, firstVisitCookie: "outside-eu" } } },
```

Attribution answers "which sources and campaigns bring us customers". With it on:

- **Campaigns** (`campaigns`, default on): `utm_source`, `utm_medium`, `utm_campaign`
  (and `utm_term`, `utm_content`) and `ref` tags are kept, and filter everything like any
  other dimension.
- **Sign-ups** (`signups`, default on): each new account keeps where the visit it signed
  up in came from: the source, the referrer site, the campaign tags, the first page and
  the country. A visit is found when the sign-up comes from the same browser and network
  within 30 minutes of its last page; moving between the project's apps is one visit. An
  account made with a sign-in link opened elsewhere (another device) has no visit. Your
  server code reads it with the app's analytics key:

  ```ts
  import { attribution } from "@shiptiffin/sdk/analytics";
  const a = await attribution(user.id); // { visit: { source, utmCampaign, landing, ... }, first?, matched } or null
  ```

  Deleting an account deletes its attribution.
- **Payments**: connect Stripe and every payment is linked to the account (or the visit)
  that paid: see [Revenue](#revenue).
- **First-visit cookie** (`firstVisitCookie`, default `"off"`): see
  [The first-visit cookie](#the-first-visit-cookie).

**Only a person turns it on.** It links visits to people, so API keys can plan the
change but not apply it (`403`, "an API key can't turn on marketing attribution"); people
in the dashboard and the owner token can. The plan step says it is turning
attribution on and names the privacy text's version, so History records what the person
was shown. Turning it off is irreversible: it deletes the sign-up sources, checkout
returns, first visits and payments it collected. Anyone whose browser sends Global
Privacy Control is left out of all of it.

**Your privacy policy has to say so.** Attribution is marketing measurement, which the
cookie-free exemption for audience measurement doesn't cover (CNIL, 2025). The box writes
the text for your settings:

```bash
tiffin analytics privacy --project shop   # Markdown, its version and a hash that changes with the text
```

The dashboard shows it when you turn attribution on, and again whenever a setting changes it.

### Revenue

Connect Stripe and every payment is linked to the account, or the visit, that paid, so
revenue, new customers, conversion rate and revenue per visitor break down by source,
campaign, first page and country. It needs attribution on.

1. In Stripe, create a **restricted key**: Developers › API keys › Create restricted key,
   then **Powering an integration you built** and **Choose your own**. Set these rows and
   leave everything else at None (Stripe asks you to confirm it's you before it makes the key):
   - **Read**: Balance, Charges and Refunds, Customers, Invoices, Subscriptions, Checkout
     Sessions
   - **Write**: Webhook Endpoints, Event Destinations

   Add **Products: Read** if the account sells more than one app's products (below).
2. Paste it:

   ```bash
   tiffin analytics stripe connect --project shop --key rk_live_...
   ```

That's all. The box keeps the key sealed, creates its own webhook in your Stripe account
at `https://<the project's address>/_tiffin/stripe` (beside any webhook your app has;
the edge answers that path before your app and checks Stripe's signature), and imports
the last 12 months of payments, refunds and subscriptions. When the project's address
changes, the webhook follows it (`tiffin analytics stripe repair` does it by hand);
`tiffin analytics stripe status` shows the connection, the import and any warning.
`tiffin analytics stripe disconnect` deletes the webhook, the key and the payments.

**One Stripe account, several apps.** Every project connected to a Stripe account sees
all of its payments. If the account sells more than this project's products, choose which
count: **Counts › Change** on the Revenue tab, or

```bash
tiffin analytics stripe products --project shop                                  # the products, their payments, which count
tiffin analytics stripe products set --project shop --products prod_123,prod_456 # only these (add --no-product for payments with none)
tiffin analytics stripe products set --project shop --all                        # every payment again (the default)
```

A payment's products come from its subscription, invoice or Checkout line items; a charge
made outside them has none. Refunds and MRR follow the choice, and changing it takes effect
at once: nothing is imported again. With **Products: Read** the list shows every product by
name; without it, only products sold through Checkout.

**How a payment finds its customer**, in order (each payment records which one worked):

1. `metadata.tiffin_user` on the Checkout Session and its subscription: the account id.
   Set it and matching is exact. `client_reference_id` counts too, but only when it is an
   account of the project: apps often keep their own ids there (an order, a cart).

   ```ts
   await stripe.checkout.sessions.create({
     ...,
     metadata: { tiffin_user: session.user.id },
     subscription_data: { metadata: { tiffin_user: session.user.id } },
   });
   ```

2. The Stripe customer's email, matched to an account of the project's sign-in.
3. The visit the buyer came back to from Checkout (a page with `?session_id=cs_...`, as
   Stripe's success URL sends): credited to that visit, with no account.
4. An earlier payment by the same Stripe customer: a renewal, credited like the first.

The rest show as unmatched, with the reason; `tiffin analytics revenue match` tries again
(after you add `metadata.tiffin_user`, say).

```bash
tiffin analytics revenue --project shop --period 30d                # last touch: the visit that paid
tiffin analytics revenue --project shop --period 30d --touch first  # first touch: where each customer first came from
```

Amounts are in the Stripe account's currency: a payment in another currency counts as
what Stripe settled it for (its balance transaction), so totals never mix currencies.
Refunds count against the payment they refund. MRR is the active subscriptions'
prices per month, less their discounts while those last: a subscription with a discount
counts at the share of its list price its latest invoice billed (a free plan counts
nothing). Agents get `analytics_revenue` and the `analytics_stripe_*` tools.

**Other payment providers**: send each payment from your server instead. It is ignored
while Stripe is connected, so nothing counts twice:

```ts
import { revenue } from "@shiptiffin/sdk/analytics";
await revenue({ id: order.id, amount: 1999, currency: "eur", user: user.id }, { request });
```

`amount` is in the currency's smallest unit; `refund: "<id of the payment>"` records a
refund, `renewal: true` a renewal. Without `user`, the request's visit gets the credit.

### The first-visit cookie

Without a cookie, a visitor who came from Hacker News on Monday and signs up on Thursday
is credited to Thursday's visit (often "direct" or Google). `firstVisitCookie` remembers
the first visit for 90 days:

- The box's edge sets one first-party cookie, `tiffin_first_visit` (a random code;
  `HttpOnly`, `Secure`, `SameSite=Lax`, 90 days), on page loads. Your app's code doesn't
  change. The account then keeps both: where its first visit came from (`first`) and where
  its sign-up visit came from (`visit`).
- **`"outside-eu"`**: never set for visitors in the EU, EEA or UK (or anywhere the country
  database doesn't know). No banner is needed.
- **`"ask-eu"`**: visitors there get it only once they agree. Add the prompt to your pages;
  it shows only to visitors who need asking and haven't answered, and
  `tiffinConsent.open()` shows it again for a "Change my choice" link:

  ```html
  <script defer src="/_tiffin/consent.js"></script>
  ```

- Never with Global Privacy Control. `/_tiffin/opt-out` on every host of the app stops it
  and deletes what it remembered; the privacy text links it.
- Turning it on needs a person, like attribution itself.

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
`analytics_events`, `analytics_vitals`, `analytics_setup`, `analytics_privacy`). Paths, referrers and event names come from
visitors, so tools mark their output as untrusted data.

## Privacy

You can link this section from your privacy policy, or copy it.

This section covers Basic analytics. With marketing attribution on, use the text
`tiffin analytics privacy` prints instead: it adds what attribution keeps.

**What is collected.** For each page view: the page's address without its query
string, the site that sent the visitor (its name
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
Guidelines 2/2023) may treat as reaching into the device. Basic analytics is
built to meet the conditions the French CNIL set for audience measurement
without consent (its 2025 self-assessment): for the site's own statistics only,
no campaign tags or customer IDs collected, referrers as a site name only,
anonymous figures, no cross-site tracking, no sharing, no third-party tools,
country-level location, kept well under 25 months, and an easy way to object
(Global Privacy Control). The CNIL asks for figures rounded to the nearest ten
or an analysis showing they are anonymous; the box shows exact counts, because
what it stores can't single anyone out: one-way daily visitor codes, no IP
addresses, no per-visitor views, no account links. Marketing attribution is
outside that exemption by design (it measures acquisition and links sign-ups
to accounts): turn it on only with the privacy text it comes with. Say in your privacy policy that you
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
events) can replace it for busier boxes. Deleting the project deletes its analytics
data; shortening `retentionDays` deletes older events.

What analytics doesn't do yet: see [What works and what doesn't](https://shiptiffin.com/docs/limits.md#analytics).
