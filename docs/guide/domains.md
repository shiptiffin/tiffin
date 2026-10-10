# Domains

## On a server, with no setup

A box on a server answers at once on a real name with a real certificate:

```
https://dashboard.203-0-113-7.sslip.io     the dashboard
https://shop.203-0-113-7.sslip.io          an app called shop
```

`203-0-113-7` is the server's IPv4 address with dashes. sslip.io answers every name
under it with that address, so there is nothing to set up. The certificates come from
Let's Encrypt. `tiffin domain` shows where you are.

(A local box in a VM, for trying Tiffin out, uses `*.tiffin.localhost` and its own
certificate authority: the CLI trusts it after `tiffin up`, browsers after `tiffin trust`.
Real domains need a server.)

## Your own domain, in two records

At your DNS host, point the domain and everything under it at the server:

| Type | Name | Value |
|---|---|---|
| A | `@` (example.com) | your server's IPv4 |
| A | `*` (*.example.com) | your server's IPv4 |

Add the same two as AAAA records if the server has IPv6 (`tiffin domain` lists its
addresses). Then:

```
tiffin domain check --domain example.com   # optional: what DNS says now
tiffin domain set example.com
```

`domain set` checks both records first. If they don't point at the box yet, nothing
changes and you get the exact records to add. When they do, the dashboard moves to
`dashboard.example.com` and apps to `<project>.example.com` (other apps of a project to
`<project>-<app>.example.com`). Want to keep `example.com`
itself for something else? Use a subdomain: `tiffin domain set apps.example.com`
(the records are then `apps` and `*.apps`).

`tiffin domain unset` goes back to the sslip.io name.

## The domain itself

`example.com` itself (not a name under it) sends visitors to the dashboard
(`https://dashboard.example.com/`) until an app uses it. The same goes for a managed
`<name>.shiptiffin.app` and for the automatic sslip.io name. It is a temporary redirect
(302), so browsers don't remember it.

To put your website there, give it to an app:

```
tiffin domains add shop --domain example.com
```

The app wins as soon as the change applies, and `tiffin domains remove shop example.com`
brings the redirect back. Settings › Domain in the dashboard shows which it is now, and so
does `tiffin domain`.

## Apps on a domain of their own

Like vercel.com and vercel.app, the dashboard can live on one domain and the apps on
another:

```
tiffin domain set example.com --apps-domain example.app
```

The dashboard, the API and webhooks stay at `dashboard.example.com`; apps, previews
and the box's own service names (`s3`, `files`, `t`, `errors`, `otel`) move to
`<name>.example.app`. App code then runs on a different registrable domain from the
dashboard, so it cannot set cookies on the dashboard's domain. (The dashboard's
sign-in cookie is host-only either way.) The records are:

| Type | Name | Value |
|---|---|---|
| A | `dashboard.example.com` | your server's IPv4 |
| A | `*.example.app` | your server's IPv4 |

(plus AAAA with IPv6). `example.com` itself is not needed, so it can stay your
website. If it points at the box, it behaves as [above](#the-domain-itself), and
`example.app` itself does too: it sends visitors to the dashboard, or to
`https://example.com/` when an app on the box serves `example.com`, until an app uses
`example.app` itself. `tiffin domain check --domain example.com --apps-domain example.app` lists
them and what DNS says now; with `--create-records`, the box adds the ones in zones
your connected DNS provider holds and tells you exactly which to add by hand (a
Cloudflare token for *All zones* holds both). A custom domain's CNAME then points at
`dashboard.example.com`.

`tiffin domain set` is the whole setting: running it again without `--apps-domain`
puts the apps back on the box domain. Either way the old app names keep working until
the new certificates are live, then for an hour. Apps read the domain they live under
from `TIFFIN_DOMAIN`, and their own address from `TIFFIN_URL`.

## A domain for one project

Give an app its own name, `example.com` or `shop.example.com`:

```
tiffin domains add shop --domain example.com --app web --www
```

This is a normal change: you see the plan, then confirm it. It adds `"example.com"` to
the app's `routes` (and `www: "redirect"` under `domains`), so `tiffin pull` brings it
into your `tiffin.config.ts`:

```ts
apps: {
  web: { routes: ["shop", "example.com"] },
  api: { routes: ["example.com/api"] },   // only /api goes to the api app
},
domains: { "example.com": { www: "redirect" } },
```

The answer lists the records to add: an A (and AAAA) record to the server, or, for a
subdomain, one CNAME to the box's own domain. `tiffin domains list shop` shows each
name's state:

- **waiting_for_dns**, with the reason ("points to 5.6.7.8, not this box", "no A or
  AAAA record yet"). The box checks again on its own: after 15 seconds, then less and
  less often, up to every 30 minutes. `tiffin domains check shop example.com` checks
  now.
- **issuing**: it points here; the certificate takes a few seconds.
- **live**.
- **error**, with what to do: a CAA record that doesn't allow Let's Encrypt, a rate
  limit, ports 80 and 443 closed.

`tiffin domains remove shop example.com` stops serving it. Your DNS records stay.

## Let Tiffin manage DNS (optional)

Connect a Cloudflare API token with **Zone · DNS · Edit** on your zones:

```
tiffin dns connect cloudflare --token <token>
```

The box checks the token by listing your zones and stores it encrypted. From then on:

- `tiffin domain set example.com --create-records` and `tiffin domains add ...
  --create-records` add the records for you (with the owner's or a box-wide key: the
  provider's zones are the box's, so a key for one project can't write to them);
- the box gets **one wildcard certificate** for `*.example.com` (DNS-01), so new apps
  and previews have HTTPS the moment they exist (for `*.example.app` with a separate
  apps domain, when the provider holds that zone; the dashboard then gets its own);
- records for email (SPF, DKIM, DMARC) can be set with `tiffin dns records set`.

Keep these records **DNS only** (grey cloud) in Cloudflare; the box serves HTTPS
itself. `tiffin dns disconnect cloudflare` forgets the token.

## Behind the scenes

- The edge (Caddy) gets certificates from Let's Encrypt, with ZeroSSL as a fallback
  when you give an email (`--email`). They renew on their own, well before they expire.
- A certificate is only ever requested for a name the box serves. A project domain is
  handed over only once its DNS points here, so a domain you haven't set up yet never
  wastes Let's Encrypt's limits. Without a DNS provider, each app gets its certificate
  on its first visit. The domain itself gets one on its first visit too, for the redirect,
  and only while no app uses it; once an app does, it is checked like any project domain.
- Plain HTTP redirects to HTTPS. Browsers are told to stay on HTTPS (HSTS, 30 days)
  only for certificates from a public CA.
- A domain switch restarts the service for a few seconds (apps keep running). The old
  names keep working until the new ones have certificates, then for another hour.
  Passkey sign-ins belong to the dashboard's address: add them again on the new one.

## In the dashboard

- **A project › Settings › Domains** (also linked from the project's overview): add a
  domain, pick the app that shows it and whether `www.` comes along. The page lists
  the exact records to add, with a copy button for each, and watches DNS until the
  domain is live with HTTPS. With Cloudflare connected, one button adds the records.
- **Settings › Your box › Domain**: the box's address and where apps live, *Use your
  own domain* (optionally with *Put apps on a domain of their own*; check the two
  records, then switch; the page follows the dashboard to its new address) and *Go
  back to the automatic address*.
- **Settings › DNS**: connect Cloudflare with a token made from its *Edit zone DNS*
  template (All zones), see which domains it can manage, or disconnect.

For agents: `GET /v1/domain`, `POST /v1/domain`, `GET /v1/projects/{project}/domains`,
`POST /v1/projects/{project}/domains`; the same names as MCP tools.
