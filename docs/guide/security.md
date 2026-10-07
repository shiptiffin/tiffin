# Security model

Plainly, so you can decide what to trust it with.

- **The box is the boundary.** Tiffin runs as root on its own machine and manages
  system services. Apps run in containers. Do not share a box with people you don't trust.
- **App network.** App ports are reachable only from the box itself; the edge is the way
  in. Apps can't open connections to port 25 on other servers (the box's firewall refuses
  them): their mail goes through the box (`SMTP_URL`), which sends it with the mail
  service you connect, so an app can't hurt the box's sending reputation behind your back.
  Each project may only send as its own addresses (`<project>@<box domain>` or its
  verified sending domain), never as the box or another project.
- **HTTPS everywhere.** A server with a public address gets certificates from a public CA
  (Let's Encrypt). Locally the box has its own certificate authority, created on the box
  and never shared. `tiffin trust` adds it to your Mac's keychain.
- **Your config is code from your repository**, which a pull request can change. `tiffin
  plan` and `apply` run `tiffin.config.ts` on your computer the way a push runs it on the
  box: no file, network or environment access (`process.env` is empty) and imports only
  from its git repository, never `~/.tiffin` where your owner tokens are.
- **Git pushes.** `tiffin git-remote --add` installs a credential helper that hands a box's
  token only to that box's address, never to another remote.
- **SSH to servers.** Tiffin pins each server's SSH host key the first time it connects
  and refuses a different one after. For a server you bring (`--provider ssh`) it also
  honours the keys in your own `~/.ssh/known_hosts`; see [limits](limits.md#servers).
- **Builds run your code in containers.** BuildKit runs the build steps; a static site
  builds in a container capped in memory and tasks. Your env reaches a build only inside
  those containers (as BuildKit secrets, build args or container env), never the
  environment of the box's own tools (Railpack, BuildKit's client, nerdctl), which run
  as root: a variable such as `PATH`, `DOCKER_CONFIG` or `LD_PRELOAD` can't reconfigure
  them. Each app's build caches are its own. What a build writes is checked before the
  box uses it (links must stay inside, no FIFOs or devices) and read with size caps.
  Known gap: Railpack plans on the host (see [limits](limits.md#builds)).
- **Only HTTPS leaves a local box,** and only to `127.0.0.1:8443` on your Mac. Postgres,
  Valkey and the rest are not reachable from outside the VM.
- **API keys** are random 200-bit secrets; only their SHA-256 is stored. Each reaches
  some projects (or all) with full or read access, and works for 30 days, 90 days (the
  default), a year, or until revoked. A key is its own credential: the dashboard session
  that made it can end or expire and the key keeps working, like a GitHub or Vercel
  personal access token. Only a key with full access to all projects (or an owner or
  admin person) can create, list or revoke keys. See [API keys](agents.md#api-keys) and
  [Creating API keys in the dashboard](#creating-api-keys-in-the-dashboard).
- **Agents and destructive changes.** By default, your agent can do what you can.
  Claude Code asks you before anything destructive (Tiffin marks those tools
  destructive); Tiffin records everything in History and can undo it. Give agents that
  run unattended, or that should only touch one project, a narrower key: read access,
  or only that project. Outside its reach a key gets `403 forbidden`; there is no
  approval step to get around it. A key for some projects also gets no box-wide reports
  (the disk breakdown, the box's resources, backups and restore drills), and the
  observe overview, alerts and alert rules show it only its own projects' containers
  and alerts.
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
- **App sign-in is kept apart per app.** The engine's cookies are `__Host-` (only the
  app's own host can set them, so another app on the box can't plant a session), each
  project has its own rate-limit counters, one project's sign-in settings or provider
  answers can't stop the engine (a project with invalid settings is left out; a
  provider's answer is capped at 1 MB and 15 s), and `@shiptiffin/sdk/auth` checks a
  session against its own app's project (`TIFFIN_AUTH_HOST`) whatever host a request
  names. Nothing in the auth tables works as a credential: reset and magic-link tokens
  are stored hashed, one-time codes encrypted.
- **Mail content needs full access.** Read-only access to a project shows each message's
  sender, recipients and delivery, not what it says: mail carries reset links and codes.
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
  sites. A link only exists if its email left the box through the relay: one that would
  wait in the box's dev inbox (which owners and admins can read) is never made, or is
  cancelled at once, because an emailed link counts as proof that you read that inbox.
- **Google and GitHub sign-in** only signs in people already on the box, matched the first
  time by an email the provider vouches for, then by the linked provider account. It
  never makes an account. See
  [Signing in with Google or GitHub](#signing-in-with-google-or-github).
- **New sign-in notices.** When someone with an email address signs in from a browser the
  box hasn't seen them use, it emails them (browser, time, address, how). Their first
  sign-in (the invite) and later sign-ins from the same browser are quiet. A random ID in
  an HttpOnly cookie (`tiffin_device`) is all the box keeps about a browser. The email's
  button, **Review sign-ins**, opens the page below.
- **Sign-ins and signing out.** **Settings › Sign-ins** lists where you're signed in, with
  **Sign out** on each and **Sign out everywhere else**. See
  [Seeing and ending sign-ins](#seeing-and-ending-sign-ins).
- **Console history stays in the tab.** What you type in the SQL and KV consoles (which can
  hold a password or an `AUTH`) is kept for the up arrow in that browser tab only, never in
  the browser's lasting storage, and is wiped when you sign out or your session ends.
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

A session that is signed out is refused on its very next request. API keys made while
signed in there keep working: revoke them on **Settings › API keys**. Other people's
sessions, even ones signed in with a link this session sent, stay.

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

## Sudo mode: confirm it is you

A few things outlive the dashboard session that does them, so a stolen session must not
be able to do them quietly:

- **Creating an API key** that lasts longer than a day, or has full access (and so any
  admin key). A read-only key for a day doesn't ask.
- **Adding a passkey.** A passkey signs in as you for good, and it would pass every later
  "confirm it's you", so a stolen session must not be able to add its own.
- **Changing an email address** (yours, or as an owner or admin, someone else's; only
  the owner changes the owner's). Emailed sign-in links go there, and they count as a
  strong sign-in.

Each needs a strong sign-in in the last 10 minutes: a passkey, Google, GitHub or a link you
asked to be emailed, or, for the owner, their own `tiffin login` (a link made with the
owner token for the owner). That token can already add passkeys and keys without asking,
so this grants nothing new, and it lets the owner of a box with no mail relay and no
Google or GitHub keys add a first passkey. A one-time link someone else made (an invite,
an admin's link, a link the owner token makes for someone else) is not one, and neither
is a link a session makes for itself, the owner's included. Otherwise the dashboard asks
you to confirm with one of your own passkeys (someone else's doesn't count), or to sign
in again (**Sign in again** goes to the login page and back); either gives you 10
minutes. If you have no passkey yet, you add your first one after signing in with
Google, GitHub or an emailed link (the owner: or a fresh `tiffin login`). Settings ›
Sign-ins shows the owner's terminal sign-ins as *tiffin login*.

The API answers `403 reauth_required` (with a hint) until then. The dashboard confirms with
`POST /v1/session/confirm/options`, then `navigator.credentials.get()`, then
`POST /v1/session/confirm` with `{"credential": ...}`; the session then repeats the call.
Keys and the owner token are not sessions: they never confirm, and the owner's CLI token
may add the owner's passkeys without it. Removing a passkey doesn't ask, but is emailed.

**API keys can't change email addresses.** A key isn't a session, so it can't confirm,
and an admin key that could repoint someone's address (the owner's included) could then
take over their emailed sign-in. `PUT /v1/people/{id}/email` with an API key is refused
(`403 forbidden`) when the address would change. People change addresses in the
dashboard; the owner token (`tiffin people email`) still changes anyone's.
An emailed sign-in link only counts when the mail left the box (see *Sign-in links by
email* above): one that would wait in the dev inbox is never made.

## Creating API keys in the dashboard

A key outlives the session that made it. Three things keep a stolen session from minting
one quietly:

- **Confirm it's you** ([sudo mode](#sudo-mode-confirm-it-is-you)), as above.
- **An email for every key.** The person who made it gets a *New API key* email: the
  key's name, its projects and access, when it expires, when, and the browser, address and
  country it came from, with a **Review API keys** button.
- **The audit log** records each key (`token.create`, with the person) and each
  confirmation (`session.confirm`).

Removing someone, or lowering their role, still revokes every key they made (and keys
those keys made).

Keys and the owner token are not sessions: they never confirm. A key made by another key
(an agent delegating) never outlives the key that made it, and revoking a key revokes the
keys it made.

For scripts: `POST /v1/tokens` with `"expiresInDays"`: 1, 30, 90 (the default when left
out) or 365, or 0 for never. In a session that hasn't confirmed, a key that needs it is
refused with `403 reauth_required`.

## Adding and removing passkeys

- **Confirm it's you** ([sudo mode](#sudo-mode-confirm-it-is-you)) before adding one, from a
  dashboard session: both `POST /v1/passkeys/register` and `POST /v1/passkeys` check it.
- **An email for every change.** The person gets a *New passkey* email when one is added
  (its name, when, and the browser, address and country it came from, with a **Review
  passkeys** button; "Wasn't you?" says to remove it and sign out everywhere else), and a
  *Passkey removed* email when one is removed.
- **The audit log** records `passkey.add` and `passkey.delete`, with the person and the
  passkey's name.

## Signing in with Google or GitHub

When an owner sets the box-wide Google or GitHub keys (Box settings › Sign-in providers),
the login page shows **Sign in with Google** or **Sign in with GitHub** to the box's
people: owners, admins and members. It uses the same OAuth app and the same callback URL
as the apps' sign-in (`https://<dashboard host>/api/auth/callback/<provider>`), so there
is nothing more to register. Remove the keys and the button goes.

- **Who gets in.** The first time, the box asks the provider for the account's email and
  signs in the active person on the box with that address (compared without case). For
  Google that is the ID token's `email`, only when `email_verified` is true and Google is
  authoritative for it: a Gmail address, or a Google Workspace account (`hd`). A Google
  account registered with any other address stays "verified" after that mailbox changes
  hands, so those don't match (sign in with an email link instead). For GitHub it is any
  verified address from `/user/emails` (the primary one is tried first); unverified
  addresses don't count. Nobody matches: *That Google account isn't on this box. Ask an
  owner to invite you.* Removed people never match. The box never makes an account, so
  invite someone (with their email) before they can sign in this way.
- **Linked accounts.** That first sign-in links the provider account (Google's `sub`,
  GitHub's user id) to the person. From then on that account signs them in, whatever
  address it shows, and no other account of that provider can, even one showing their
  verified address (`linked`): a provider's "verified" can outlive someone's hold on an
  address. To link a different account, remove the person and invite them again.
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
`unverified`, `linked`, `expired`, `denied`, `failed`, `busy`, `off`).

## Signing in with a passkey

Set up a device once in **Sign in with Touch ID / Face ID** (your menu). From then on,
choose **Sign in with Touch ID** on the login page: no username, no link. It is a passkey
kept on that device, and every person can set up their own. Adding one needs a recent
strong sign-in ([sudo mode](#sudo-mode-confirm-it-is-you)): confirm with a passkey you
already have, or, for your first, sign in with Google, GitHub or an emailed link first.
You are emailed about every passkey added or removed.

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
