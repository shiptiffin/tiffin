# Domains

## On a server, with no setup

A box on a server answers at once on a real name with a real certificate:

```
https://dashboard.203-0-113-7.sslip.io     the dashboard
https://shop.203-0-113-7.sslip.io          an app called shop
```

`203-0-113-7` is the server's IPv4 address with dashes. sslip.io answers every name
under it with that address, so there is nothing to set up. The certificates come from
Let's Encrypt. `tiffin domain` shows where you are. A managed box works the same way
under `<name>.shiptiffin.app`.

Any other name one level under the box's domain is free too, and works at once:
**Domains › Add domain** with `blog.<box domain>` (or `tiffin domains add <project>
--domain blog.<box domain> --app web`) adds `blog` to the app's routes, with no DNS to
set. Names directly under `shiptiffin.app` belong to other boxes and are refused.

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
Cloudflare token for *All zones* holds both). A project's own subdomain then points
at the box with A (and AAAA) records: the dashboard's host is never a CNAME target.

`tiffin domain set` is the whole setting: running it again without `--apps-domain`
puts the apps back on the box domain. Either way the old app names keep working until
the new certificates are live, then for an hour. Apps read the domain they live under
from `TIFFIN_DOMAIN`, and their own address from `TIFFIN_URL`.

## A domain for one project

Give an app its own name, `example.com` or `shop.example.com`:

```
tiffin domains add shop --domain example.com --app web --www
```

This is a normal change: you see the plan, then confirm it. The plan already lists the
DNS records to add, so you can set them before you confirm. It adds `"example.com"` to
the app's `routes` (and `www: "redirect"` under `domains`), so `tiffin pull` brings it
into your `tiffin.config.ts`:

```ts
apps: {
  web: { routes: ["shop", "example.com"] },
  api: { routes: ["example.com/api"] },   // only /api goes to the api app
},
domains: { "example.com": { www: "redirect" } },
```

### Before you start: remove your registrar's parking records

A domain you just bought usually points at the registrar's parking page. Delete those
records first, or they stay beside yours and the domain never points only at the box:

| Registrar | Where | Delete |
|---|---|---|
| Namecheap | Domain List › Manage › Advanced DNS | the URL Redirect Record on `@` and the CNAME Record for `www` (`parkingpage.namecheap.com`) |
| GoDaddy | My Products › your domain › DNS | the `@` A record to the parking page (3.33.130.190 or 15.197.148.33), and any forwarding |
| Porkbun | Domain Management › your domain › DNS | the ALIAS and CNAME records to `pixie.porkbun.com` |
| Squarespace | Domains › your domain › DNS › DNS Settings | the default Squarespace records on `@` and `www` |
| Gandi | Domain › your domain › DNS Records | the default `@` A record (217.70.184.38) and `www` CNAME (`webredir.vip.gandi.net`) |

If DNS still answers with one of these, the box says so: "example.com still points to
Namecheap's parking page. In Namecheap › Domain List › Manage › Advanced DNS, delete
…, then add: …". It also names where to click for Cloudflare, Hostinger, IONOS, Dynadot
and Name.com, from the domain's nameservers.

### The records

| Name | Record |
|---|---|
| `example.com` | A (and AAAA) to the server |
| `www.example.com`, when `example.com` is on the box | CNAME `example.com` (or the same A and AAAA records) |
| `shop.example.com` | A (and AAAA) to the server, or one CNAME to the box's domain |

The CNAME to the box's domain is only offered while apps live on the box domain; with a
separate apps domain, use the A and AAAA records.

### Watching it go live

`tiffin domains list shop` shows each name's state:

- **waiting_for_dns**, with the reason ("points to 5.6.7.8, not this box", "no A or
  AAAA record yet", or the registrar's parking page). The box checks every 30 seconds for
  the first 30 minutes after you add a domain, and again for 30 minutes whenever its DNS
  answer changes; after that less and less often, up to every 30 minutes. Each name says
  when: "Checked 2 min ago · next check in 30 s". `tiffin domains check shop example.com`
  (or **Check now** in the dashboard) checks at once. When `example.com` starts pointing
  here, `www.example.com` is checked right away too, and the other way round.
- **issuing**: it points here; the certificate takes a few seconds.
- **live**. `tiffin deploy` then names the app by this domain ("web is live at
  https://example.com") and lists its other addresses below.
- **error**, with what to do: a CAA record that doesn't allow Let's Encrypt, a rate
  limit, ports 80 and 443 closed.

DNS checks are cheap. A certificate is only requested once DNS points here, so frequent
checks never use up Let's Encrypt's limits.

`tiffin domains remove shop example.com` stops serving it. The plan says what happens to
its DNS records (see [Removing a domain](#removing-a-domain-or-a-project)).

## Primary domain

Moving a site to its own domain? Make that domain the app's **primary**, and its old
addresses send visitors there:

```
tiffin domains add shop --domain example.com --app web --www --primary
tiffin domains edit shop example.com --primary          # a domain the app already has
tiffin domains edit shop example.com --primary=false    # every address serves again
```

```ts
domains: { "example.com": { www: "redirect", primary: true } },
```

Once `example.com` is live, the app's other addresses answer a permanent redirect
(308) to `https://example.com`, with the path and query kept: its name on the box
(`shop.<apps domain>`), its other domains, and their `www.` names. Search engines then
index one address. Plain HTTP goes straight to the primary in one hop.

- **Nothing redirects until the primary is live.** While it waits for DNS or its
  certificate, the old addresses keep serving, so visitors never land on a name without
  HTTPS. If it stops being live (its DNS moved away), they serve again.
  `tiffin domains list shop` says which addresses redirect and whether the redirect is on
  (`redirectFrom`, `redirecting`).
- **Previews and deploy addresses keep serving**, so you can still open them. So do the
  box's own names and the dashboard.
- **Only addresses the app serves whole redirect.** A name shared with another app (say
  `old.com/api` goes to the api app) keeps serving, so the other app's paths never move.
- **One primary per app.** Making another domain primary unsets the old one. The primary
  is a domain of your own that the app serves whole (no path).
- In the dashboard: open a domain's menu and choose **Make primary**. For agents: `primary`
  on `POST /v1/projects/{project}/domains`, and `POST
  /v1/projects/{project}/domains/{domain}/edit` (`primary`, `www`).

## Buying a domain

**Domains** in the sidebar searches for a name and buys it, without leaving the dashboard.
It's registered with Cloudflare Registrar **in your own Cloudflare account**, at Cloudflare's
price (no markup), and charged to the card on that account. ShipTiffin never holds or resells
it: the domain is yours in Cloudflare, whatever happens to the box.

1. Type a word or a name (`acme`, `acme.app`). The name you typed comes first, then other
   names, each with its first-year price and what it costs every year after.
2. **Buy** shows the facts once more: first year, renewal, auto-renew (on by default),
   contact details hidden from WHOIS, and which project it's for. **Buy for $14.20**
   registers it. In the dashboard that needs a sign-in in the last 10 minutes (Confirm it's you).
3. The same window follows it: registered, DNS ready, then added to the project with
   its records set by the box. HTTPS follows by itself.

Without a project, it waits under **Your domains** (the same page, with an empty search),
which lists every domain the box can manage, where each is used, and when registered ones
renew. **Use on a project** puts one on a project in a click.

Until the box can buy, **Domains** shows one thing to do instead of the search: **Connect
Cloudflare** (it opens Settings › DNS and comes back), or, when the connected token lacks the
permission, what to add to it in Cloudflare and **Check again**.

It needs the Cloudflare token from [Let Tiffin manage DNS](#let-tiffin-manage-dns-optional) with
two more things: **Account › Registrar Domains › Admin**, and **Zone Resources: All zones**
(so the box reaches the zone of a domain you just bought). Editing a token in Cloudflare keeps
its value, so there's nothing to paste again. Once, in Cloudflare's dashboard, the account needs
a card, a default contact and the registration agreement accepted (Domains › Registrations).

For agents and the CLI, nothing is bought without the price: `domains buy` without `price`, or
with a price that's no longer right, answers 428 with the quote. Show it to a person, then send
its `total` back.

```
tiffin domains search --q acme
tiffin domains buy --domain acme.app --auto-renew=true --price 14.20
tiffin domains buy-status acme.app
tiffin domains owned
```

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

The box remembers every record it creates. `tiffin dns records list --name example.com`
marks them `created`, and the ones the box relies on `managed: tiffin` (it refuses to
delete those until the domain is removed).

`tiffin dns records delete` takes a type and a name, and deletes every record of that
type at that name. Add a value to delete only that one. The answer lists what it deleted:

```
tiffin dns records delete --records '[{"type":"A","name":"old.example.com"}]'
tiffin dns records delete --records '[{"type":"TXT","name":"example.com","value":"google-site-verification=abc"}]'
```

## Removing a domain or a project

`tiffin domains remove shop example.com` and `tiffin projects destroy shop` tidy up DNS,
so no record is left pointing at the server's address. (If the server is deleted later,
its address can go to someone else, who would then get your name.)

- **Records the box created** (`--create-records`) that still point at this box are
  deleted once the change is applied. The plan lists them first (`dns.delete`). Add
  `--keep-dns` (API: `keepDns: true`) to keep them.
- **Records you added yourself** are never deleted. The plan and the answer list the ones
  that point at this box (`dns.byHand`): delete them at your DNS host.
- **Records you pointed elsewhere** after the box created them are left alone
  (`dns.kept`).
- **If the DNS provider fails**, the domain or project is still removed. The answer says
  so (`dns.error`) and lists the records to delete by hand.

The box reads the records again from the provider when it plans and when it applies.
Undo brings the domain back, but not deleted records: add the domain again with
`--create-records`. In the dashboard, *Remove domain* and *Delete project* list the same
records before you confirm.

## Getting found

A production app on a domain of your own is findable: the box tells search engines
about its pages after each deploy and shows which bots came. Names on `shiptiffin.app`,
`sslip.io` and local names are never indexed. See [Search engines](search.md).

## Behind the scenes

- The edge (Caddy) gets certificates from Let's Encrypt, with ZeroSSL as a fallback
  when you give an email (`--email`). They renew on their own, well before they expire.
- A certificate is only ever requested for a name the box serves. A project domain is
  handed over only once its DNS points here, so a domain you haven't set up yet never
  wastes Let's Encrypt's limits. Without a DNS provider, each app gets its certificate
  on its first visit. The domain itself gets one on its first visit too, for the redirect,
  and only while no app uses it; once an app does, it is checked like any project domain.
- Plain HTTP redirects to HTTPS. A name that only redirects (`www.`, or an app's old
  address once its primary domain is live) goes straight to its target in one hop. Let's
  Encrypt's HTTP challenges are answered before any redirect. Browsers are told to stay on HTTPS (HSTS, 30 days)
  only for certificates from a public CA.
- A domain switch restarts the service for a few seconds (apps keep running). The old
  names keep working until the new ones have certificates, then for another hour.
  Passkey sign-ins belong to the dashboard's address: add them again on the new one.

## In the dashboard

- **A project › Settings › Domains** (also linked from the project's overview): add a
  domain, pick the app that shows it and whether `www.` comes along. The dialog shows the
  exact records before you add it, with a copy button for each; then the page watches
  DNS until the domain is live with HTTPS, says when the box last checked and when it
  checks next, and has **Check now**. A domain still on its registrar's parking page
  says what to delete and where. With Cloudflare connected, one button adds the records.
- **Settings › Your box › Domain**: the box's address and where apps live, *Use your
  own domain* (optionally with *Put apps on a domain of their own*; check the two
  records, then switch; the page follows the dashboard to its new address) and *Go
  back to the automatic address*.
- **Settings › DNS**: connect Cloudflare with a token made from its *Edit zone DNS*
  template (All zones; add Registrar Domains › Admin to buy domains too), see which domains
  it can manage, or disconnect.

- **Domains** (the sidebar): search, buy, and every domain the box can manage
  ([Buying a domain](#buying-a-domain)).

For agents: `GET /v1/domain`, `POST /v1/domain`, `GET /v1/projects/{project}/domains`,
`POST /v1/projects/{project}/domains`, `GET /v1/domains/search`, `POST /v1/domains/buy`,
`GET /v1/domains/owned`; the same names as MCP tools.
