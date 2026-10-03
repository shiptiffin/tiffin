# Sign-in for your apps

```ts
services: { auth: { methods: ["email", "magic-link", "passkey", "google"], organizations: true } }
```

The box runs Better Auth for your apps at `/api/auth/*` on each app's own hosts. Users
and sessions live in the project's own Postgres (schema `auth`).

- **Methods:** email + password (with verification), magic links, one-time codes,
  passkeys, Google and GitHub (set `GOOGLE_CLIENT_ID`/`SECRET` etc. as secrets; until
  then the endpoint explains exactly what to set), TOTP two-factor.
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

Mail (verification, links, invites) goes through the project's email service, so it
lands in the dev inbox until you set up a relay.

The dashboard lists users and organizations; you can ban users and revoke sessions.
