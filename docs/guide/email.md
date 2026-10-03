# Email

Turn on `services: { email: {} }` and every app of the project can send mail.
Until the box owner configures an SMTP relay, **nothing leaves the box**: every
message is captured in the project's dev inbox, where you (and your agents) can
read it, click its sign-in link and check how it looks. Preview deployments always
use the dev inbox, even with a relay.

```ts
// tiffin.config.ts
services: { email: { from: "hello@shop.example" } },  // from is optional
```

The default sender is `<project>@<box domain>`.

## Sending

Apps get `SMTP_URL` (`smtp://<project>:<password>@127.0.0.1:2525`), plus
`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD` and `EMAIL_FROM`. Any SMTP
library works; the SDK wraps it and renders [react-email](https://react.email)
templates:

```tsx
import { send, render } from "tiffin-sdk/email";

await send({ to: user.email, subject: "Welcome", react: <Welcome name={user.name} /> });
await send({ to: "ops@example.com", subject: "Nightly report", text: "All good." });
```

Agents and scripts can send through the API or CLI:

```bash
tiffin email send shop --to ada@example.com --subject "Hi" --text "Hello Ada"
```

Capturing needs `apply:reversible`; once a relay is set, real sending needs
`apply:outbound`.

## The dev inbox

```bash
tiffin email messages list shop               # newest first; --q to search, --all for relayed mail too
tiffin email messages get shop <msg_id>       # text, sanitised HTML, headers, attachments, links
tiffin email messages clear shop
```

`links` in a message lists its http(s) links, which is how an agent completes a
sign-up or magic-link flow. The dashboard shows new mail live
(`GET /v1/projects/<project>/email/stream`, server-sent events). Each project keeps
its newest 1,000 captured messages.

## Sending for real: the relay

Point the box at any SMTP relay (Resend, Postmark, SES, your provider):

```bash
tiffin email relay set --host smtp.resend.com --port 587 --tls starttls --username resend --password "$RESEND_KEY"
tiffin email relay test --to you@example.com
tiffin email status
```

The password is stored encrypted and never shown. Mail to the relay is queued and
retried with backoff (30 s, 1 min, 2 min ... up to 8 attempts). `tiffin email relay delete`
goes back to capturing everything.

## Suppressions and limits

A recipient the relay rejects permanently (a hard bounce) is added to the
project's suppression list, and Tiffin never sends to it again. Add unsubscribes
and complaints yourself:

```bash
tiffin email suppressions add shop --address ada@example.com --reason unsubscribe
tiffin email suppressions list shop
tiffin email suppressions delete shop ada@example.com
```

Each project may send 300 messages an hour (bursts of up to 60). The box owner can
change it: `tiffin email rate-limit set shop --per-hour 2000` (0 = unlimited).

## Under the hood

Tiffin runs its own SMTP submission server on `127.0.0.1:2525` (go-smtp; AUTH
PLAIN with the project's credentials; preview deployments log in as
`<project>+<preview>`). Captured messages are stored as raw `.eml` files under
`/var/lib/tiffin/email/messages/<project>/` with their metadata in the box's state
database; both are in every backup set.
