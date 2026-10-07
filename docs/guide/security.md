# Security model

Plainly, so you can decide what to trust it with.

- **The box is the boundary.** Tiffin runs as root on its own machine and manages
  system services. Apps run in containers. Do not share a box with people you don't trust.
- **App network.** App ports are reachable only from the box itself; the edge is the way
  in. Apps can't open connections to port 25 on other servers (the box's firewall refuses
  them): their mail goes through the box (`SMTP_URL`), which sends it with the mail
  service you connect, so an app can't hurt the box's sending reputation behind your back.
- **HTTPS everywhere.** Locally the box has its own certificate authority, created on
  the box and never shared. `tiffin trust` adds it to your Mac's keychain.
- **Only HTTPS leaves a local box,** and only to `127.0.0.1:8443` on your Mac. Postgres,
  Valkey and the rest are not reachable from outside the VM.
- **API keys** are random 200-bit secrets; only their SHA-256 is stored. Each reaches
  some projects (or all) with full or read access, and can expire after 30 or 90 days.
  Only a key with full access to all projects (or an owner or admin person) can create,
  list or revoke keys. See [API keys](agents.md#api-keys).
- **Agents and destructive changes.** By default, your agent can do what you can.
  Claude Code asks you before anything destructive (Tiffin marks those tools
  destructive); Tiffin records everything in History and can undo it. Give agents that
  run unattended, or that should only touch one project, a narrower key: read access,
  or only that project. Outside its reach a key gets `403 forbidden`; there is no
  approval step to get around it.
- **Secrets** (env vars) are encrypted with the box's own age key and are never shown
  after you set them.
- **App sign-in tokens.** The access, refresh and ID tokens that Google, GitHub and the
  other providers return when someone signs in to an app are encrypted (XChaCha20-Poly1305)
  with the project's auth key before they reach the app's database. The key stays in the
  auth engine's config, out of the database and the app's environment. Only the
  signed-in person gets them back: an app's API key (`tfk_`) is refused
  (`API_KEY_NOT_ALLOWED`) on `/get-access-token`, `/refresh-token` and `/account-info`.
  Each project's auth secret comes only from that config: the engine ignores
  `BETTER_AUTH_SECRET(S)`, `AUTH_SECRET`, `BETTER_AUTH_TRUSTED_ORIGINS` and
  `BETTER_AUTH_URL` in its environment. See [Sign-in providers](auth.md#sign-in-providers).
- **Dashboard sign-in** is a one-time link (`tiffin login`, an invite, or one emailed on
  request), a passkey, or Google or GitHub (see below).
  Each gives a 12-hour session with exactly that person's role, in an HttpOnly,
  Secure, SameSite=Strict cookie. Passkey sign-in needs user verification (Face ID,
  fingerprint or PIN), uses a single-use challenge that expires after 2 minutes, refuses
  people who were removed and passkeys whose signature counter goes backwards (a sign of
  a copied key), is limited to 10 attempts a minute per address, and is in the audit log.
- **Sign-in links by email.** With a mail service connected, the login page offers
  *Email me a sign-in link*. The answer is the same whether or not the address belongs
  to anyone, and the lookup and the mail happen after it. Each link works once, for 15
  minutes, and asking again cancels the previous one. Requests are limited to 5 per
  15 minutes per client address and 3 an hour per email address, and refused from other
  sites.
- **Google and GitHub sign-in** only signs in people already on the box, matched by an
  email the provider has verified. It never makes an account. See
  [Signing in with Google or GitHub](#signing-in-with-google-or-github).
- **New sign-in notices.** When someone with an email address signs in from a browser the
  box hasn't seen them use, it emails them (browser, time, address, how). Their first
  sign-in (the invite) and later sign-ins from the same browser are quiet. A random ID in
  an HttpOnly cookie (`tiffin_device`) is all the box keeps about a browser. The email's
  button, **Review sign-ins**, opens the page below.
- **Sign-ins and signing out.** **Settings › Sign-ins** lists where you're signed in, with
  **Sign out** on each and **Sign out everywhere else**. See
  [Seeing and ending sign-ins](#seeing-and-ending-sign-ins).
- **Every change and security event is logged** (changes with the key or person that
  made them, keys created and revoked, sign-ins, secrets).
- **Known gaps:** a person or agent with shell access to your Mac can read your local
  owner token in `~/.tiffin`. Backups stay on the box unless you set an off-box destination
  (`tiffin backups offsite set`); keep its passphrase off the box.

## Seeing and ending sign-ins

**Settings › Sign-ins** (also in your menu, bottom left) shows:

- **Where you're signed in:** each open session's browser and system ("Safari on iPhone"),
  the address it signed in from and its country (looked up on the box, when the analytics
  country database is there), how it signed in (sign-in link, emailed link, passkey, Google
  or GitHub), when, and when it was last active (to the minute). *This browser* marks yours.
  **Sign out** ends one; **Sign out everywhere else** ends all but this one.
- **Recent sign-ins:** every sign-in of the last 30 days, and whether it is still signed
  in, was signed out or expired.
- **Browsers this box knows:** up to 20 browsers you signed in from. Signing in from one of
  these sends no new sign-in email.

A session that is signed out is refused on its very next request, and any API keys made
while signed in there stop working too (a stolen session's keys go with it). Keys made in
another session, and other people's sessions, stay.

Owners and admins can do the same for anyone: **People › (their role menu) › End
sessions…** lists where that person is signed in, with **Sign out** on each and **Sign
out everywhere**. They keep their access and can sign in again; to take it away, remove
them. Only the owner can end the owner's sessions; admins can still see them.

Agents and scripts use API keys, not sessions, so they aren't listed here: see
**API keys**. Every sign-in is in the audit log (`session.link`, `session.passkey`,
`session.oauth`), and so is every sign-out from this page (`session.end` with the session,
`session.end_others` with the person, both with who did it).

For scripts and agents: `GET /v1/sessions` (`?person=usr_…` for someone else,
`&history=true` for the last 30 days), `DELETE /v1/sessions/{id}`,
`POST /v1/sessions/end-others` (`?person=usr_…`) and `GET /v1/sessions/browsers`; in the
CLI, `tiffin sessions list|end|end-others|browsers`. Sessions belong to people, so an API
key must name the person, and only a key with full access to all projects may.

## Signing in with Google or GitHub

When an owner sets the box-wide Google or GitHub keys (Box settings › Sign-in providers),
the login page shows **Sign in with Google** or **Sign in with GitHub** to the box's
people: owners, admins and members. It uses the same OAuth app and the same callback URL
as the apps' sign-in (`https://<dashboard host>/api/auth/callback/<provider>`), so there
is nothing more to register. Remove the keys and the button goes.

- **Who gets in.** The box asks the provider for the account's email and signs in the
  active person on the box with that address (compared without case). For Google that is
  the ID token's `email`, only when `email_verified` is true. For GitHub it is any
  verified address from `/user/emails` (the primary one is tried first); unverified
  addresses don't count. Nobody matches: *That Google account isn't on this box. Ask an
  owner to invite you.* Removed people never match. The box never makes an account, so
  invite someone (with their email) before they can sign in this way.
- **The session** is the same as a passkey's or a link's: 12 hours, that person's role,
  the same cookie, a new sign-in notice from a browser the box hasn't seen them use, and
  `session.oauth` in the audit log (refusals are `session.oauth_refused`, with why).
- **The flow** is the authorization code flow with PKCE (S256) and a random state; Google
  also gets an OpenID Connect nonce and `prompt=select_account`, GitHub `allow_signup=false`.
  The state, PKCE verifier, nonce and where to go next ride in a 10-minute
  `__Host-tiffin_oauth` cookie (HttpOnly, Secure, SameSite=Lax), HMAC-signed with a key the
  box makes when it starts. On the way back the box checks the signature, the age, the
  provider and the state (constant-time), and spends the state: each works once, and only
  in the browser that started it. The ID token comes straight from Google's token
  endpoint over TLS, so its issuer, audience, expiry and nonce are checked, not its
  signature (as Google's OpenID Connect guide allows). The provider's token is used for
  that one request and never kept. Where to go next is always a path on the dashboard.
- **Limits.** 10 starts and 10 returns a minute per client address; starts are refused
  from other sites.

For the dashboard: `GET /v1/session/oauth` lists the providers with keys set;
`POST /v1/session/oauth/{google|github}?next=/path` sets the state cookie and returns
`{url}` to open. The provider sends the browser back to the callback, which sets the
session and opens `next`, or goes to `/login?reason=<provider>:<why>` (`unknown`,
`unverified`, `expired`, `denied`, `failed`, `busy`, `off`).

## Signing in with a passkey

Set up a device once in **Sign in with Touch ID / Face ID** (your menu; sign in with a
link first). From then on, choose **Sign in with Touch ID** on the login page: no
username, no link. It is a passkey kept on that device, and every person can set up
their own.

The dashboard names it the way your device does: Touch ID / Face ID on a Mac, iPhone
or iPad, Windows Hello on Windows, fingerprint or face on Android, and a passkey
anywhere else.

Passkeys added before passkey sign-in existed were not required to be *discoverable*
(stored on the device so the browser can offer them without a username). Most phone and
laptop passkeys are anyway; if yours isn't offered at the login page, remove it and add
it again.

For the dashboard: `POST /v1/session/passkey/options` returns options for
`navigator.credentials.get()` (byte fields base64url, no `allowCredentials`); send
the result to `POST /v1/session/passkey` as `{"credential": ...}`. It sets the session
cookie and returns `{person, name, role, expiresAt}`. Failures are `401 unauthenticated`
with a `hint` (expired or replayed prompt, unknown passkey, removed person) or
`429 rate_limited` with `Retry-After`.
