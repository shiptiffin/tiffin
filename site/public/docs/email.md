# Email

Every app of every project can send mail: email is always there (list it in
`services` only to set `from`).
Until the box owner configures an SMTP relay, **nothing leaves the box**: every
message is captured in the project's dev inbox, where you (and your agents) can
read it, click its sign-in link and check how it looks. Preview deployments always
use the dev inbox, even with a relay, and so does mail addressed only to domains
reserved for examples and tests (`example.com`, `.test`, `.invalid`, `.localhost`...),
which no mail can reach: test sign-ups and invites never bounce off your relay.

```ts
// tiffin.config.ts
services: { email: { from: "hello@shop.example" } },  // from is optional
```

The default sender is `<project>@<box domain>`.

Every project sends through the box's one relay account, so the box checks who mail
claims to be from before it leaves: a project may send as `<project>@<box domain>` or as
any address at its own [sending domain](#send-from-your-own-domain) once that is verified
(or set up by hand, for providers without an API), and never as the box's own sender.
That goes for the envelope sender, `From` and `Sender`, through SMTP and the API alike;
anything else is refused (SMTP `550 5.7.1`, API 422). Mail kept in the dev inbox never
leaves the box, so it is not checked.

## Sending

Apps get `SMTP_URL` (`smtp://<project>:<password>@127.0.0.1:2525`), plus
`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD` and `EMAIL_FROM`. Any SMTP
library works; the SDK wraps it and renders [react-email](https://react.email)
templates:

```tsx
import { send, render } from "@shiptiffin/sdk/email";

await send({ to: user.email, subject: "Welcome", react: <Welcome name={user.name} /> });
await send({ to: "ops@example.com", subject: "Nightly report", text: "All good." });
```

Agents and scripts can send through the API or CLI:

```bash
tiffin email send shop --to ada@example.com --subject "Hi" --text "Hello Ada"
```

Sending needs a key with full access to the project.

## The dev inbox

```bash
tiffin email messages list shop               # newest first; --q to search, --all for relayed mail too
tiffin email messages get shop <msg_id>       # text, sanitised HTML, headers, attachments, links
tiffin email messages clear shop
```

`links` in a message lists its http(s) links, which is how an agent completes a
sign-up or magic-link flow. What a message says (subject, text, links, attachments, the
raw `.eml`, and searching them) needs full access to the project: mail carries reset
links, magic links and one-time codes, so read-only access sees each message's sender,
recipients, size and delivery only (`"hidden": true`). The dashboard shows new mail live
(`GET /v1/projects/<project>/email/stream`, server-sent events). Each project keeps
its newest 1,000 captured messages.

## Sending for real: the relay

One relay serves every project on the box. Connect it in the dashboard under
**Settings › Email**, or from the CLI. Pick the mail service and the box fills in
the host, port, security and username; you paste one key. Port 465 doesn't work on
Hetzner servers, and the box receives no mail yet: see
[What works and what doesn't](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/limits.md#email).

| Provider | Host | Port, security | Username | Password | Key needs |
| --- | --- | --- | --- | --- | --- |
| SendGrid | `smtp.sendgrid.net` | 587, STARTTLS | `apikey` | API key (`SG.…`) | Mail Send |
| Resend | `smtp.resend.com` | 587, STARTTLS | `resend` | API key (`re_…`) | Sending access |
| Postmark | `smtp.postmarkapp.com` | 587, STARTTLS | the server token | the server token | Server API token |
| Amazon SES | `email-smtp.<region>.amazonaws.com` | 587, STARTTLS | SMTP username | SMTP password | `ses:SendRawEmail` |
| Mailgun | `smtp.mailgun.org` (EU: `smtp.eu.mailgun.org`) | 587, STARTTLS | SMTP login, e.g. `postmaster@mg.example.com` | SMTP password | per-domain SMTP credentials |
| Brevo | `smtp-relay.brevo.com` | 587, STARTTLS | SMTP login | SMTP key (not the API key) | SMTP key |
| Cloudflare Email Service (beta) | `smtp.mx.cloudflare.net` | 465, TLS | `api_token` | account API token | Email Sending: Edit |

Any other SMTP server works too ("Other SMTP": host, port, security, username and
password by hand).

```bash
tiffin email providers                                   # the presets, with where to create each key
tiffin email relay set --provider sendgrid --password "$SENDGRID_KEY"
tiffin email relay set --provider resend --password "$RESEND_KEY"
tiffin email relay set --provider ses --region eu-west-1 --username "$SES_USER" --password "$SES_PASS"
tiffin email relay set --provider other --host smtp.example.com --port 587 --tls starttls --username me --password "$PASS"
tiffin email relay test --to you@example.com
tiffin email status
```

The key is stored encrypted and never shown. Omit `--password` to keep the stored
one. A relay test sends as the box's sender (`tiffin email box get`, or `--from`) and
reports what the server said and, when it fails, what that means
(a refused key, a blocked port, an unverified sender domain). Mail to the relay is
queued and retried with backoff (30 s, 1 min, 2 min ... up to 8 attempts).
`tiffin email relay delete` goes back to capturing everything.

Cloudflare Email Service is in beta. It needs the Workers Paid plan (3,000 emails a
month included, then $0.35 per 1,000), the sending domain must be onboarded in
Cloudflare, new accounts start with a small daily quota, and messages are limited to
5 MiB and 50 recipients.

Each provider only sends from domains you have verified with it. The project's
**Email settings** page checks the SPF, DKIM and DMARC records for its sender address.

## Mail from the box

The dashboard sends its own mail through the same relay: an invite with the person's
sign-in link (when you give their email address), a fresh link when you choose **Email a
new sign-in link**, a link people ask for on the login page, a note when someone
signs in from a new browser, a note when someone creates an API key in the dashboard, and
a note when a passkey is added to or removed from someone's sign-ins. The messages are
plain text and simple HTML, with no images or tracking (SendGrid's click and open
tracking is switched off for them).

The new-browser note goes out once per browser, never on someone's first sign-in. It
names the browser, the time (UTC), how they signed in and where from: the country, looked
up on the box in the analytics country database when it is there (nothing is sent
anywhere), then the address. Its button, **Review sign-ins**, opens **Settings › Sign-ins**,
where they can choose **Sign out everywhere else**; it also links their passkeys and, for
owners and admins, API keys.

The API key note goes to whoever made the key, every time: its name, projects and access,
when it expires, when, and the browser, address and country it came from. Its button,
**Review API keys**, opens **Settings › API keys**; "Wasn't you?" says to revoke it there
and sign out everywhere else.

The passkey notes go to the person whose passkeys changed, every time: *New passkey* names
the passkey, when, and the browser, address and country it was added from; its button,
**Review passkeys**, opens **Settings › Passkeys**, and "Wasn't you?" says to remove it
there and sign out everywhere else. *Passkey removed* says the same about a removal.

It comes from `Tiffin <hello@<box domain>>` until you change it under **Settings ›
Email › Mail from the box**, where you can also set a Reply-To. The relay's mail service
must accept the sender's domain.

```bash
tiffin email box get
tiffin email box set --from "ShipTiffin <hello@shiptiffin.com>" --reply-to help@shiptiffin.com
tiffin email box messages            # owners and admins: it holds invites
tiffin people email <usr_id> --email maya@example.com   # the owner token; API keys can't
```

Without a relay, box mail waits in the box's own dev inbox (the list under **Mail from
the box**), and the invite dialog says so: copy the link and send it yourself. The one
exception is a sign-in link someone asks for on the login page: it counts as proof they
read their inbox, so the box only makes one when the email leaves through the relay (one
that would stay in the dev inbox, say for an address at a reserved test domain, is
cancelled at once), and **Mail from the box** shows only that it was sent, never its text
or link, so no owner or admin can read it and sign in as that person.

## Send from your own domain

On a project's **Email settings** page, **Send from your domain** takes a domain and an
address (`hello@` by default) and does the rest. Only the box owner (or a key with full
access to all projects) can start it: it uses the box's mail-service and DNS accounts, and
nothing says the domain is the project's.

1. It sets the domain up with the relay's mail service through its API, using the
   relay key: SendGrid domain authentication (with automatic security: three CNAMEs),
   or a Resend domain. If the service already has the domain, it is reused.
2. When the domain's DNS is on a DNS provider connected to the box (Cloudflare), it
   writes the records itself (CNAMEs unproxied), plus a DMARC `p=none` policy when the
   domain has none. Otherwise the page lists the records to copy.
3. It asks the service to check: after 1, 2, 4, 8, 15 and 30 minutes, then hourly, for
   up to 48 hours. **Check now** asks at once; after 48 hours it stops and says so,
   and **Check again** starts another 48 hours.
4. Once verified, it makes `hello@<domain>` the project's sender, as a change in
   History you can undo. A sender already on that domain is kept.

The key needs more than sending for step 1. SendGrid: give the API key **Sender
Authentication** (Full Access) in Settings › API Keys. Resend: a **Full access** key
(a Sending access key can't manage domains); it sends mail too, so paste it as the
relay key. The page says exactly this when the key is short of it. For the other
services, the page lists the steps: add the domain in the service's dashboard, add its
records, then change the sender.

```bash
tiffin email sending-domain set shop --domain example.com --local hello
tiffin email sending-domain get shop
tiffin email sending-domain check shop
tiffin email sending-domain delete shop   # stops checking; the domain and records stay
```

## Delivery events

Without them, a relayed message stops at **Sent**: the relay accepted it. With them,
the provider tells the box what happened next, and each message shows **Delivered**,
**Bounced** or **Marked as spam**, with a timeline of what was reported (delays,
opens and clicks too, when the provider tracks them). Hard bounces, spam complaints
and unsubscribes add the address to the project's suppression list, with the reason.

The box reads events from SendGrid, Resend and Postmark. Each one POSTs to a public
address on the box:

```
https://<your dashboard>/v1/email/events/sendgrid
https://<your dashboard>/v1/email/events/resend
https://<your dashboard>/v1/email/events/postmark
```

The dashboard must be reachable from the internet (not `*.localhost`). Settings ›
Email shows the exact address, the events to turn on, and **Receiving events** once
the first verified request arrives.

**SendGrid.** Settings › Mail Settings › Event Webhooks › Create new webhook. Paste
the address as the Post URL; tick Delivered, Deferred, Bounced, Dropped, Spam
Reports and Unsubscribes (Opened and Clicked if you want them); turn on **Signed
Event Webhook** and save. Copy the verification key it then shows:

```bash
tiffin email webhooks set sendgrid --key "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE..."
```

**Resend.** Webhooks › Add webhook. Paste the address; pick `email.delivered`,
`email.delivery_delayed`, `email.bounced`, `email.complained`, `email.failed` and
`email.suppressed` (plus `email.opened` and `email.clicked` if you like). Copy the
signing secret from the webhook's page:

```bash
tiffin email webhooks set resend --key "whsec_..."
```

**Postmark.** Postmark has no signatures, so the box makes a password:

```bash
tiffin email webhooks set postmark    # prints the address with its password, once
```

Paste that address (it looks like `https://tiffin:<password>@.../v1/email/events/postmark`)
into the stream's Webhooks tab, with Delivery, Bounce and Spam Complaint ticked.
Running the command again makes a new password and retires the old one.

`tiffin email webhooks delete <provider>` turns events off. Amazon SES, Mailgun,
Brevo and Cloudflare are not read yet (Cloudflare sends its events to Cloudflare
Queues, not to a webhook).

### How events are checked and matched

- **SendGrid** signs each request with ECDSA (P-256, SHA-256 over the timestamp
  and the body). The box keeps the public verification key.
- **Resend** signs with Svix: HMAC-SHA256 over `id.timestamp.body` with the
  signing secret.
- **Postmark** sends the password in the address (HTTP basic auth), compared in
  constant time.

Unsigned or wrongly signed requests are refused with 401 before anything in them is
read, and the dashboard shows the last refusal and why. A Resend request more than
5 minutes old is refused. SendGrid retries for up to 24 hours, so its signed
timestamp may be up to 25 hours old; every event is recorded once by the provider's
own event ID, so a replayed request changes nothing. Keys and secrets are stored
encrypted with the box's other secrets and never returned.

When it relays a message, the box adds what lets the events find it again: an
`X-Tiffin-Message-Id` header, a `Message-ID` if the message has none, a SendGrid
`X-SMTPAPI` unique argument (`tiffin_id`, merged with any your app set), and Postmark
metadata (`X-PM-Metadata-tiffin-id`). Resend events are matched by the `Message-ID`,
then by Resend's own email ID; if Resend reports a different `Message-ID`, the first
event is matched by recipient and subject within an hour of sending, and only when
exactly one message fits. A status only moves forward (sent, delivered, bounced,
marked as spam), so a late "delivered" never hides a complaint.

## Suppressions and limits

A recipient the relay rejects permanently (a hard bounce) is added to the
project's suppression list, and Tiffin never sends to it again. With delivery events
on, bounces, spam complaints and unsubscribes reported by the provider are added too.
A message to a suppressed address is still accepted, through the API and SMTP alike: it
is logged with that recipient marked suppressed, and goes only to the others (if there
are none, nothing is sent). The list is checked again before every relay attempt, so
mail still queued when an address is suppressed (say during a relay outage) doesn't go
to it either. You can add and remove addresses yourself:

```bash
tiffin email suppressions add shop --address ada@example.com --reason unsubscribe
tiffin email suppressions list shop
tiffin email suppressions delete shop ada@example.com
```

Destroying a project removes its email too: the message log and raw files, delivery
events, suppressions, rate limit and sending-domain setup.

Each project may send 300 messages an hour (bursts of up to 60). The box owner can
change it: `tiffin email rate-limit set shop --per-hour 2000` (0 = unlimited).

## Under the hood

Tiffin runs its own SMTP submission server on `127.0.0.1:2525` (go-smtp; AUTH
PLAIN with the project's credentials; preview deployments log in as
`<project>+<preview>`). Captured messages are stored as raw `.eml` files under
`/var/lib/tiffin/email/messages/<project>/` with their metadata in the box's state
database; both are in every backup set.
