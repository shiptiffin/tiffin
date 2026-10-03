# Security model

Plainly, so you can decide what to trust it with.

- **The box is the boundary.** Tiffin runs as root on its own machine and manages
  system services. Apps run in containers. Do not share a box with people you don't trust.
- **HTTPS everywhere.** Locally the box has its own certificate authority, created on
  the box and never shared. `tiffin trust` adds it to your Mac's keychain.
- **Only HTTPS leaves a local box,** and only to `127.0.0.1:8443` on your Mac. Postgres,
  Valkey and the rest are not reachable from outside the VM.
- **Tokens** are random 200-bit secrets; only their SHA-256 is stored. Agent tokens
  expire, can't mint more power than they hold, and are revoked with everything they minted.
- **Secrets** (env vars) are encrypted with the box's own age key and are never shown
  after you set them.
- **Dashboard sign-in** is a one-time link (`tiffin login`, or an invite) or a passkey.
  Either gives a 12-hour session with exactly that person's role, in an HttpOnly,
  Secure, SameSite=Strict cookie. Passkey sign-in needs user verification (Face ID,
  fingerprint or PIN), uses a single-use challenge that expires after 2 minutes, refuses
  people who were removed and passkeys whose signature counter goes backwards (a sign of
  a copied key), is limited to 10 attempts a minute per address, and is in the audit log.
- **Approvals** for risky plans need a person's passkey, are bound to the exact plan
  hash, single-use and expire after a day.
- **Every change and security event is logged** (changes, tokens, approvals, secrets).
- **Known gaps:** a person or agent with shell access to your Mac can read your local
  owner token in `~/.tiffin`. Backups stay on the box until off-site storage arrives.

## Signing in with a passkey

Add a passkey once in **Settings › Passkeys** (sign in with a link first). From then
on, choose **Sign in with a passkey** on the login page and pick it: no username, no
link. Every person can add passkeys for themselves; owners and admins also use them to
approve plans.

Passkeys added before passkey sign-in existed were not required to be *discoverable*
(stored on the device so the browser can offer them without a username). Most phone and
laptop passkeys are anyway; if yours isn't offered at the login page, remove it and add
it again. It keeps approving plans either way.

For the dashboard: `POST /v1/session/passkey/options` returns options for
`navigator.credentials.get()` (byte fields base64url, no `allowCredentials`); send
the result to `POST /v1/session/passkey` as `{"credential": ...}`. It sets the session
cookie and returns `{person, name, role, expiresAt}`. Failures are `401 unauthenticated`
with a `hint` (expired or replayed prompt, unknown passkey, removed person) or
`429 rate_limited` with `Retry-After`.
