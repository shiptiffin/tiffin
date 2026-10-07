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
- **Dashboard sign-in** is a one-time link (`tiffin login`, an invite, or one emailed on
  request) or a passkey.
  Either gives a 12-hour session with exactly that person's role, in an HttpOnly,
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
- **New sign-in notices.** When someone with an email address signs in from a browser the
  box hasn't seen them use, it emails them (browser, time, address, how). Their first
  sign-in (the invite) and later sign-ins from the same browser are quiet. A random ID in
  an HttpOnly cookie (`tiffin_device`) is all the box keeps about a browser.
- **Every change and security event is logged** (changes with the key or person that
  made them, keys created and revoked, sign-ins, secrets).
- **Known gaps:** a person or agent with shell access to your Mac can read your local
  owner token in `~/.tiffin`. Backups stay on the box unless you set an off-box destination
  (`tiffin backups offsite set`); keep its passphrase off the box.

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
