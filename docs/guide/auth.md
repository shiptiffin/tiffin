# Sign-in for your apps

```ts
services: { auth: { methods: ["email", "magic-link", "passkey", "google"], organizations: true } }
```

The box runs Better Auth for your apps at `/api/auth/*` on each app's own hosts. Users
and sessions live in the project's own Postgres (schema `auth`). That path is reserved:
requests under `/api/auth/` never reach your app, so don't put routes there (plans warn
about app routes under it).

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
- **Passkeys** work on every host of the app (box subdomain and custom domains). A passkey
  belongs to the host it was made on, or to a parent host the app also serves (one made on
  `example.com` works on `www.example.com`).
- **Bots:** a proof-of-work check (ALTCHA) protects sign-up, sign-in and reset.

## In your app

```ts
import { getSession, requireRole, withOrg } from "@shiptiffin/sdk/auth";
const session = await getSession(request);                 // null if signed out
const admin = await requireRole(request, "admin");         // throws 401/403 otherwise
const rows = await withOrg(sql, orgId, tx => tx`select * from projects`); // RLS by org
```

`getSession` doesn't ask the engine on every request. Each sign-in also sets a short-lived
cookie the engine signs with the project's key (the user, the active organization and
your role in it); the SDK checks the signature itself, with the public key, and asks the
engine only when that cookie is missing or expired, remembering the answer for 5 seconds.
So a signed-out session, a ban or a lowered role can keep working in your server code for
up to **60 seconds** (the engine's own endpoints see it at once). For a sensitive action,
check now: `getSession(request, { fresh: true })`.

### Sign-in forms in the browser

The forms are yours. Better Auth's own client talks to the box's `/api/auth` on the
app's host (`bun add better-auth`; the box runs 1.7). The bot check comes from
`@shiptiffin/sdk/client`: `prepareCaptcha()` starts solving at once and hands over headers
for one request at a time (`{}` when the app has the check off).

```tsx
// lib/auth-client.ts
import { createAuthClient } from "better-auth/react"; // or "better-auth/client" without React
export const authClient = createAuthClient({ basePath: "/api/auth" });

// app/sign-up/page.tsx
"use client";
import { useMemo, useState } from "react";
import { errorText, prepareCaptcha } from "@shiptiffin/sdk/client";
import { authClient } from "@/lib/auth-client";

export default function SignUp() {
  const captcha = useMemo(() => prepareCaptcha(), []);
  const [error, setError] = useState("");
  async function submit(form: FormData) {
    const email = String(form.get("email")), password = String(form.get("password"));
    const { error } = await authClient.signUp.email({ name: email.split("@")[0]!, email, password }, { headers: await captcha() });
    if (error) setError(errorText(error));
    else location.href = "/dashboard";
  }
  return (
    <form action={submit}>
      <input name="email" type="email" required />
      <input name="password" type="password" minLength={8} required />
      {error && <p>{error}</p>}
      <button>Create account</button>
    </form>
  );
}
```

Sign in the same way with `authClient.signIn.email({ email, password }, { headers: await captcha() })`
(magic links and email codes take the headers too); `authClient.signOut()` signs out, and
`authClient.useSession()` (React) or `await authClient.getSession()` (plain) says who is
signed in. Without React:

```ts
import { createAuthClient } from "better-auth/client";
import { prepareCaptcha } from "@shiptiffin/sdk/client";
const authClient = createAuthClient({ basePath: "/api/auth" });
const captcha = prepareCaptcha();
await authClient.signIn.email({ email, password }, { headers: await captcha() });
const { data } = await authClient.getSession(); // data?.user.email
await authClient.signOut();
```

Add Better Auth's client plugins for the rest of what the box serves:
`organizationClient()`, `magicLinkClient()`, `emailOTPClient()`, `twoFactorClient()` from
`better-auth/client/plugins`, `passkeyClient()` from `@better-auth/passkey/client`
(`authClient.signIn.passkey()`, `authClient.passkey.addPasskey()`). `authConfig()` from
`@shiptiffin/sdk/client` says which methods the app has on, to show only those buttons.

Mail (verification, links, invites) goes through the project's email service, so add
`email: {}` next to `auth` (the plan warns when it is missing). It lands in the dev inbox
until you set up a relay. Email + password sign-up needs a confirmed address: sign-up
answers `{"token": null}` and no session until the user opens the link in the mail.
Testing it yourself? The link is in `tiffin email messages list <project>` / `get`.

### Next.js

`@shiptiffin/sdk/next/auth` follows the Next.js authentication guide: an optimistic check in
`proxy.ts`, the real check next to the data, and Server Actions that sign in. The box
sets everything it needs; there is no auth route or config to write.

```ts
// proxy.ts: only looks for the session cookie (no network)
import { authProxy } from "@shiptiffin/sdk/next/auth";
export const proxy = authProxy({ protect: ["/dashboard/:path*"], signIn: "/sign-in" });
```

Signed out on a protected page: a redirect to `/sign-in?next=/dashboard/...`; under
`/api/`: 401. Then check for real where the data is read, in a data access layer:

```ts
// app/lib/dal.ts
import "server-only";
export { getSession, verifySession, requireRole, currentUser } from "@shiptiffin/sdk/next/auth";

// app/dashboard/page.tsx
const { user, organization } = await verifySession(); // signed out: to /sign-in?next=...
// app/settings/page.tsx
const { organization } = await requireRole("admin");  // role too low: 403
```

They work in Server Components, Server Actions and Route Handlers, run once per request,
and return plain data (`user`, `organization` with your `role`; no tokens), safe to pass
to Client Components. `requireRole`, and `verifySession({ signIn: false })` for a 401,
use `forbidden()` and `unauthorized()`; the box turns on `experimental.authInterrupts` they
need unless your next.config sets it.

Server Actions call the engine for the browser and set its cookies:

```ts
// app/actions.ts
"use server";
import { requireRole, signIn, signOut } from "@shiptiffin/sdk/next/auth";

export async function signInAction(_: unknown, form: FormData) {
  // email, password, captcha and next from the form; { ok: false, code, message } on failure
  return signIn(form, { redirectTo: "/dashboard" });
}
export async function signOutAction() {
  await signOut({ redirectTo: "/" });
}
export async function deleteProject(id: string) {
  const { organization } = await requireRole("admin", { fresh: true }); // now, not up to 60 s old
  await withOrg(sql, organization.id, (tx) => tx`delete from projects where id = ${id}`);
}
```

```tsx
// app/sign-in/form.tsx
"use client";
import { useActionState, useEffect, useRef } from "react";
import { attachCaptcha } from "@shiptiffin/sdk/client";
import { signInAction } from "../actions";

export function SignInForm({ next = "" }: { next?: string }) {
  const [state, action, pending] = useActionState(signInAction, null);
  const form = useRef<HTMLFormElement>(null);
  useEffect(() => attachCaptcha(form.current!), []); // fills a hidden "captcha" field
  return (
    <form ref={form} action={action}>
      <input name="email" type="email" />
      <input name="password" type="password" />
      <input name="next" type="hidden" value={next} />
      {state?.ok === false && <p>{state.message}</p>}
      <button disabled={pending}>Sign in</button>
    </form>
  );
}
```

`signUp` works the same (`name`, `email`, `password`); it answers `signedIn: false` when
the user must confirm their email first. `attachCaptcha` solves the bot check in the
browser and holds a submit until it is ready. Or skip the actions and use Better Auth's
client as above.

With Cache Components: a `"use cache"` function can't read cookies, so never call
`getSession` inside one. Check the session outside and pass in what the cached work
needs (`getProjects(user.id)`, with a `cacheTag` per user), or use `"use cache: private"`
for per-user results that must not be shared. A page that reads the session renders per
request: keep that part inside `<Suspense>`.

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

## Previews

Sign-in works on previews (`<preview>--<name>.<domain>`) as on production: the box
serves `/api/auth/*` on each preview's host from the moment it deploys until it is
deleted, and the preview's `TIFFIN_AUTH_URL` and `TIFFIN_AUTH_HOST` name that host.

- **Same users.** A preview signs in against the project's own accounts, so testers use
  their real account and anyone who signs up on a preview is a user of the app.
- **Own cookies.** Session cookies are host-only (no `Domain`): a preview never sees
  production's cookies, and signing in on a preview doesn't sign you in on production.
- **Own passkeys.** A preview's host is its own passkey rpID; passkeys made on
  production don't show up there (sign in another way, or add one on the preview).
- **Emails** (verification, magic links, invites) sent from a preview link back to it,
  and go out like production's, since they reach real users.

Code in a preview runs with the project's auth like production's, so treat a preview of
someone else's branch as you would deploying it.
