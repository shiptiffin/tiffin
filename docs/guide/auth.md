# Sign-in for your apps

```ts
services: { auth: { methods: ["email", "magic-link", "passkey", "google"], organizations: true } }
```

The box runs Better Auth for your apps at `/api/auth/*` on each app's own hosts. Users
and sessions live in the project's own Postgres (schema `auth`).

- **Methods:** email + password (with verification), magic links, one-time codes,
  passkeys, Google and GitHub (set `GOOGLE_CLIENT_ID`/`SECRET` etc. as secrets; until
  then the endpoint explains exactly what to set), TOTP two-factor.
- **Email verification:** `auth: { emailVerification: true | false }`. Left out it is
  automatic: new users confirm their address once the box has an SMTP relay (real mail
  goes out), and sign in at once while mail only reaches the dev inbox, so test sign-ups
  just work. `false` warns in every plan; the dashboard's Auth page has the switch.
- **Organizations:** every user gets a personal org; teams have roles owner, admin,
  member and viewer, email and link invites. Nobody can grant a role above their own.
- **API keys** act as their user, capped by a role.
- **Bots:** a proof-of-work check (ALTCHA) protects sign-up, sign-in and reset.

## In your app

```ts
import { getSession, requireRole, withOrg } from "tiffin-sdk/auth";
const session = await getSession(request);                 // null if signed out
const admin = await requireRole(request, "admin");         // throws 401/403 otherwise
const rows = await withOrg(sql, orgId, tx => tx`select * from projects`); // RLS by org
```

```tsx
import { SignIn, UserButton, OrgSwitcher } from "tiffin-sdk/react";
```

Mail (verification, links, invites) goes through the project's email service, so add
`email: {}` next to `auth` (the plan warns when it is missing). It lands in the dev inbox
until you set up a relay. Email + password sign-up needs a confirmed address: sign-up
answers `{"token": null}` and no session until the user opens the link in the mail.
Testing it yourself? The link is in `tiffin email messages list <project>` / `get`.

### Without the SDK

Everything above is plain HTTP, so any language works:

- **Who is signed in:** `GET $TIFFIN_AUTH_INTERNAL_URL/tiffin/session` with the request's
  `cookie` (or `x-api-key`) and `x-tiffin-host: <the app's host>`; it answers the session
  (`user`, `organization`) or 401.
- **Bot check:** sign-up, sign-in, magic links and resets need a proof of work. Fetch
  `GET /api/auth/altcha/challenge`, solve it with
  [altcha-lib](https://github.com/altcha-org/altcha-lib) (`solveChallenge`), and send
  `x-captcha-response: base64(JSON.stringify({challenge, solution}))` with the POST
  (for example `POST /api/auth/sign-up/email {email, password, name}`).

The dashboard lists users and organizations; you can ban users and revoke sessions.
