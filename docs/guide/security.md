# Security model

Plainly, so you can decide what to trust it with.

- **The box is the boundary.** Tiffin runs as root on its own machine and manages
  system services. Apps run in containers. Do not share a box with people you don't trust.
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
- **Dashboard sign-in** is a one-time link (`tiffin login`, or an invite) or a passkey.
  Either gives a 12-hour session with exactly that person's role, in an HttpOnly,
  Secure, SameSite=Strict cookie. Passkey sign-in needs user verification (Face ID,
  fingerprint or PIN), uses a single-use challenge that expires after 2 minutes, refuses
  people who were removed and passkeys whose signature counter goes backwards (a sign of
  a copied key), is limited to 10 attempts a minute per address, and is in the audit log.
- **Every change and security event is logged** (changes with the key or person that
  made them, keys created and revoked, sign-ins, secrets).
- **Known gaps:** a person or agent with shell access to your Mac can read your local
  owner token in `~/.tiffin`. Backups stay on the box until off-site storage arrives.

## Signing in with Touch ID / Face ID

Set up a device once in **Sign in with Touch ID / Face ID** (your menu; sign in with a
link first). From then on, choose **Sign in with Touch ID** on the login page: no
username, no link. It is a passkey kept on that device, and every person can set up
their own.

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
