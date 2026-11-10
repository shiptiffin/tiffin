# Sign-in for your apps

```ts
services: { auth: { methods: ["email", "magic-link", "passkey", "google"], organizations: true } }
```

The box runs Better Auth for your apps at `/api/auth/*` on each app's own hosts. Users
and sessions live in the project's own Postgres (schema `tiffin_auth`; `auth` and every
name without the `tiffin` prefix stay free for your app). That path is reserved:
requests under `/api/auth/` never reach your app, so don't put routes there (plans warn
about app routes under it).

- **Methods:** email + password (with verification), magic links, one-time codes,
  passkeys, two-step sign-in (an authenticator app or a code by email), and sign-in with Google, GitHub, Apple, Microsoft, Discord,
  Facebook, X, LinkedIn, GitLab, Slack, Twitch or any OpenID Connect provider (see
  [Sign-in providers](#sign-in-providers): keys set once for the whole box, or per project).
- **Email needs a mail service.** Email + password sign-up, magic links, one-time codes,
  password resets and verification mail go through the project's email service, which
  sends with the box's relay: your own mail provider (Resend, Postmark, SES...), set in
  **Settings › Email**. There is no shared sending. Until a relay is connected, production
  refuses those with `EMAIL_NOT_SET_UP` ("This app can't send email yet: connect a mail
  service in Settings › Email."), so nobody signs up with an address they don't own;
  passkeys and sign-in providers keep working. Previews and local boxes keep the dev
  inbox, so testing just works. Plans warn, the Auth page shows a banner, and
  `authConfig()` reports `emailReady: false` so the app can hide those forms.
- **The emails** carry the app's name (`APP_NAME`, else the project's) and its icon
  (Settings › General). The button is the dashboard's brass unless you set your own:
  `auth: { emailAccent: "#2f6b4f" }`; its text turns white or near-black, whichever reads
  better. The project's sidebar colour is only for telling projects apart and never goes
  into email. One-time codes are in the subject.
- **Changing an account's email:** the current address approves the move, the new one
  confirms it, and the old one is then told the account moved (`authClient.changeEmail`).
- **Email verification:** `auth: { emailVerification: true | false }`. Left out it is
  automatic: on once the box has a relay. `false` warns in every plan; the dashboard's
  Auth page has the switch.
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

Away from the box (`bun dev` on your laptop, no `TIFFIN_AUTH_URL`), `getSession` answers
`null` and logs one warning, so the app runs signed out instead of failing on a leftover
cookie.

### Sign-in forms in the browser

The forms are yours. Better Auth's own client talks to the box's `/api/auth` on the
app's host. `createAuthClient` from `@shiptiffin/sdk/client/auth` sets it up: `basePath`
`/api/auth` and the client plugins for what the box serves (magic links, email codes,
organizations, two-step sign-in). It is its own entry point, so apps that don't sign anyone
in never load Better Auth. The bot check comes from `@shiptiffin/sdk/client`:
`prepareCaptcha()` starts solving at once and hands over headers for one request at a time
(`{}` when the app has the check off).

**Install the `better-auth` the box runs** (`bun add better-auth@<version>`): the client and
the box's engine speak one release's protocol. `tiffin auth show <project>` prints it
(`betterAuthVersion`), and so does `authConfig()` in the browser. When the box updates
Better Auth, update the app's too.

```tsx
// lib/auth-client.ts
import { createAuthClient } from "better-auth/react"; // React hooks (useSession)
import { tiffinClientPlugins } from "@shiptiffin/sdk/client/auth";
export const authClient = createAuthClient({ basePath: "/api/auth", plugins: [...tiffinClientPlugins()] });
// Without React: import { createAuthClient } from "@shiptiffin/sdk/client/auth"; createAuthClient()

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
import { createAuthClient } from "@shiptiffin/sdk/client/auth";
import { prepareCaptcha } from "@shiptiffin/sdk/client";
const authClient = createAuthClient();
const captcha = prepareCaptcha();
await authClient.signIn.email({ email, password }, { headers: await captcha() });
const { data } = await authClient.getSession(); // data?.user.email
await authClient.signOut();
```

`createAuthClient` already has `magicLinkClient()`, `emailOTPClient()`,
`organizationClient()` and `twoFactorClient()`. Passkeys live in their own package: add
`passkeyClient()` from `@better-auth/passkey/client` (same version as `better-auth`) with
`createAuthClient({ plugins: [passkeyClient()] })` for `authClient.signIn.passkey()` and
`authClient.passkey.addPasskey()`. `authConfig()` from
`@shiptiffin/sdk/client` says which methods the app has on, to show only those buttons;
its `providers` list is the sign-in buttons, in order, with their names:

```tsx
const { providers } = await authConfig();
providers.filter((p) => p.configured).map((p) => (
  <button key={p.id} onClick={() => authClient.signIn.social({ provider: p.id, callbackURL: "/dashboard" })}>
    Continue with {p.name}
  </button>
));
```

Mail (verification, links, invites) goes through the project's email, which every
project has. Until the box has a relay,
production refuses email sign-up and links (`EMAIL_NOT_SET_UP`, see above); on previews and
local boxes it lands in the dev inbox. Email + password sign-up needs a confirmed address:
sign-up answers `{"token": null}` and no session until the user opens the link in the mail.
Testing on a preview? The link is in `tiffin email messages list <project>` / `get`.
If the mail service refuses an address for good (it bounced before, say), the request
still answers as usual, so nobody can probe which addresses are refused; the box's log
records it.

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

### Astro

`@shiptiffin/sdk/astro` does for Astro what the Next.js helpers do, for pages rendered on
demand (the Node adapter, `framework: "astro"`). It needs nothing from `astro` at runtime.
A middleware reads the session once per request into `Astro.locals` and keeps signed-out
visitors out of the paths you protect (the same `:path*` patterns as `authProxy`):

```ts
// src/middleware.ts
import { authMiddleware } from "@shiptiffin/sdk/astro";
export const onRequest = authMiddleware({ protect: ["/app/:path*", "/api/private/:path*"], signIn: "/sign-in/" });
// with your own middleware too: sequence(authMiddleware({ ... }), yours) from "astro:middleware"

// src/env.d.ts
declare namespace App {
  interface Locals extends import("@shiptiffin/sdk/astro").AuthLocals {} // user, organization, session
}
```

Signed out on a protected page: a redirect to `/sign-in/?next=/app/...`; under `/api/`: 401.
Prerendered pages are left alone (they have no request; the box serves them as files). A
refreshed session cookie from the engine is passed on to the browser, and if the engine
can't be reached the request counts as signed out, never signed in.

In endpoints and pages, `requireSession` and `requireRole` return the session or the
`Response` to send instead:

```ts
// src/pages/api/notes.ts
import type { APIRoute } from "astro";
import { requireRole } from "@shiptiffin/sdk/astro";
export const GET: APIRoute = async (context) => {
  const s = await requireRole(context, "member", { fresh: true }); // fresh: ask the engine now
  if (s instanceof Response) return s; // 401 signed out, 403 role too low or no organization
  return Response.json(await notesFor(s.organization.id));
};
```

```astro
---
// src/pages/account.astro
import { requireSession } from "@shiptiffin/sdk/astro";
const s = await requireSession(Astro); // signed out: to /sign-in?next=/account
if (s instanceof Response) return s;
---
<h1>Hello, {s.user.name}</h1>
```

They use what the middleware read, so the engine is asked once per request at most.
`getSession(context)` answers the session or `null`. Sign-in forms use the browser client
above. Pages that read the session are per person: send `cache-control: private, no-store`.

### Users from server code

Server code of the project's own apps can find an account by email, or make one:

```ts
import { createUser, getUserByEmail } from "@shiptiffin/sdk/auth";
const user = await getUserByEmail("ana@example.com");          // the account, or null
const { user: u, created } = await createUser({ email, name }); // the account; created: false when it was there
```

Both lowercase the address. `createUser` is idempotent: for an address that has an account it
returns that account unchanged (`created: false`), and two calls at once make one account.
It makes the account and nothing else:

- **Nobody is signed in.** No session, no cookie, no token comes back.
- **No email is sent**, and the address stays **unconfirmed** unless you pass
  `emailVerified: true`. Pass that only when you know the person controls the address; an
  email typed into a checkout form is not proof. Unconfirmed is the safe default: the first
  magic link or one-time code to that address confirms it and signs the person in (Better
  Auth then drops anything linked to the account before that proof). Sign-in providers
  (Google...) join an account only once its address is confirmed, so until then the person
  signs in with a link or a code.
- A new account gets its personal organization, as one made by sign-up does. It isn't counted
  as a sign-up in analytics (there is no visit behind it).

**Sell access before sign-up.** A payment webhook can give the buyer an account before
they have ever signed in, so the purchase waits for them:

```ts
// src/pages/api/stripe/webhook.ts (or app/api/stripe/webhook/route.ts)
import { createUser } from "@shiptiffin/sdk/auth";

export async function POST(request: Request) {
  const event = stripe.webhooks.constructEvent(await request.text(), request.headers.get("stripe-signature")!, secret);
  if (event.type === "checkout.session.completed") {
    const email = event.data.object.customer_details!.email!;
    const { user } = await createUser({ email, name: event.data.object.customer_details!.name ?? undefined });
    await sql`insert into access (user_id, product) values (${user.id}, 'pack') on conflict do nothing`;
  }
  return new Response("ok");
}
```

The thank-you page then asks for the email and sends a magic link
(`authClient.signIn.magicLink({ email, callbackURL: "/app/" })`); opening it signs the buyer
in to the account that already holds the purchase. Key access to the account's `id`, not
the email: the address on an account can change.

**Who can call them.** Only the project's own apps, on the box. The box gives every app of
an auth-enabled project `TIFFIN_AUTH_KEY`, a key for that project (an HMAC of its name
under the project's auth secret, which only the box holds). The calls go to the engine's
internal address (`TIFFIN_AUTH_INTERNAL_URL`) with that key and `TIFFIN_AUTH_HOST`:

- Another project's app on the box has another key, so it can't read or make this
  project's users, whatever host it names.
- Browsers can't reach them: under `/api/auth/` on an app's host they answer 404, key or
  not, and Better Auth's router doesn't have them at all.
- Away from the box (no `TIFFIN_AUTH_KEY`) they throw an error that says so. Keep the key
  out of browser code and logs, as you would `DATABASE_URL`; previews get it too, like
  every other secret of the project.

### Without the SDK

Everything above is plain HTTP, so any language works:

- **Who is signed in:** `GET $TIFFIN_AUTH_INTERNAL_URL/tiffin/session` with the request's
  `cookie` (or `x-api-key`), `x-tiffin-host: <the request's host>` and
  `x-tiffin-auth-host: $TIFFIN_AUTH_HOST`; it answers the session (`user`, `organization`)
  or 401. Always send `x-tiffin-auth-host`: other apps on the box can reach yours directly
  with any `Host`, and the engine answers for that header's project whatever host the
  request names.
- **Users by email:** `POST $TIFFIN_AUTH_INTERNAL_URL/tiffin/server/find-user {"email"}`
  answers `{"user": {...} | null}`; `POST .../tiffin/server/create-user {"email", "name"?,
  "emailVerified"?}` answers `{"user": {...}, "created": true | false}`. Send
  `x-tiffin-auth-host: $TIFFIN_AUTH_HOST` and `x-tiffin-auth-key: $TIFFIN_AUTH_KEY`; without
  the right key they answer 401. A user is `id`, `email`, `name`, `image`, `emailVerified`,
  `banned`, `createdAt`, `updatedAt`.
- **Bot check:** sign-up, sign-in, magic links and resets need a proof of work. Fetch
  `GET /api/auth/altcha/challenge`, solve it with
  [altcha-lib](https://github.com/altcha-org/altcha-lib) (`solveChallenge`), and send
  `x-captcha-response: base64(JSON.stringify({challenge, solution}))` with the POST
  (for example `POST /api/auth/sign-up/email {email, password, name}`).

The dashboard lists users and organizations; you can ban users and revoke sessions.

## Sign-in providers

Google is tested end to end; the others are wired up but not tested yet (see
[What works and what doesn't](https://shiptiffin.com/docs/limits.md#sign-in)).

| Method | Provider | Project secrets (when the project brings its own keys) |
| --- | --- | --- |
| `google` | Google | `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` |
| `github` | GitHub | `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` |
| `apple` | Apple | `APPLE_CLIENT_ID` (the Services ID) and `APPLE_TEAM_ID`, `APPLE_KEY_ID`, `APPLE_PRIVATE_KEY` (the .p8 file); or a ready-made `APPLE_CLIENT_SECRET` JWT |
| `microsoft` | Microsoft (Entra ID) | `MICROSOFT_CLIENT_ID`, `MICROSOFT_CLIENT_SECRET`, optional `MICROSOFT_TENANT_ID` (default `common`) |
| `discord` | Discord | `DISCORD_CLIENT_ID`, `DISCORD_CLIENT_SECRET` |
| `facebook` | Facebook | `FACEBOOK_CLIENT_ID` (App ID), `FACEBOOK_CLIENT_SECRET` |
| `twitter` | X | `TWITTER_CLIENT_ID`, `TWITTER_CLIENT_SECRET` (OAuth 2.0) |
| `linkedin` | LinkedIn | `LINKEDIN_CLIENT_ID`, `LINKEDIN_CLIENT_SECRET` |
| `gitlab` | GitLab | `GITLAB_CLIENT_ID`, `GITLAB_CLIENT_SECRET`, optional `GITLAB_ISSUER` (self-managed) |
| `slack` | Slack | `SLACK_CLIENT_ID`, `SLACK_CLIENT_SECRET` |
| `twitch` | Twitch | `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET` |
| `oidc` | Any OpenID Connect provider: Okta, Auth0, Keycloak, Entra, company SSO | `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, optional `OIDC_NAME` (the button's name) |

Each needs an OAuth app made in the provider's console. People see that app's name on the
provider's sign-in screen, so where the keys come from matters:

- **This app's own keys (for a product).** On the project's **Auth → Sign-in settings**,
  choose "This app's own keys" for the provider. The page walks through the provider's
  console, shows the one redirect URI to register, takes the client ID and secret (saved
  as the project's own encrypted secrets, a change in History you can undo, or
  `tiffin auth keys set`) and has a **Test sign-in** link. People see your app's name,
  never the box's. You can also set the secrets in the table yourself (Environment
  Variables); they win over the box's keys.
- **The box's keys (a shortcut for side projects).** Set a provider's keys once in
  **Settings → Sign-in providers** (`tiffin auth providers set`; `tiffin auth providers list`
  shows them). Any project that turns the method on uses them with nothing else to do;
  people see the box's app name. On a hosted box these are keys in *your* provider
  accounts: the box never comes with keys of its own.

Accounts and sessions are always per project, whichever keys a provider uses: signing in to
one project never signs anyone in to another, and moving a project from the box's keys to
its own keeps its users. (Some providers, Apple for one, give a person a different ID under
each developer account; then the next sign-in is matched to the account by verified email,
as below.)

**One redirect URI each.** Both kinds need one redirect URI per provider, whatever the
hosts:

- The box's keys: `https://dashboard.<box domain>/api/auth/callback/<provider>`, for every
  project on the box.
- An app's own keys: `https://<the app's sign-in host>/api/auth/callback/<provider>`. The
  sign-in host is the app's first custom domain, or its first box address while it has
  none. Sign-ins on the app's other hosts (its box subdomain, `www`, previews) go out with
  that redirect URI and come back through it, so adding a domain or a preview never means
  another trip to the provider's console. When the sign-in host changes (the app gets its
  first custom domain, say), the redirect URI changes: the Auth page says so, shows the new
  one, and asks you to confirm once you've updated it with the provider.

A method with neither shows its button as not set up: signing in answers
`SOCIAL_NOT_CONFIGURED` with what to set, and `authConfig()` reports `configured: false`.
The project's Auth page shows, for each provider, whether it uses box-wide keys, the
project's own, or needs keys.

**How the one redirect URI works.** The box runs Better Auth's OAuth proxy plugin. A
sign-in that starts on a preview (`pr-3--shop.<box domain>`) goes to the provider with the one redirect
URI (the dashboard's for the box's keys, the app's sign-in host for its own); the provider
sends the browser back there; the box exchanges the code, encrypts the profile (valid for
60 seconds) and redirects to the host the sign-in started on, which makes the account and
session in the project's own database. What keeps it safe:

- The proxy key lives only in the auth engine's config (readable by the engine alone),
  never in an app's environment. Box-wide client secrets are stored encrypted with the
  box key and never returned by the API.
- A sign-in can only come back to the hosts of the project it started on (its own app
  origins, no wildcards). Through the dashboard, only for a project that uses the box's
  keys for that provider. Anything else is refused.
- The profile is accepted only in the browser that started the sign-in (its signed state
  cookie must match), so a stolen or forwarded link can't sign anyone else in or link
  their account.
- On the dashboard host only `/api/auth/callback/<provider>` and a plain-text error page
  exist; the dashboard's own cookies never reach the auth engine.

**Apple.** Apple's client secret is a JWT signed with your .p8 key and valid for at most
six months. Give the box the Team ID, Key ID, Services ID and the .p8 file; it signs the
secret itself and makes a new one a month before it expires. Apple posts its callback
(`form_post`); that works through the one callback URL too. People can hide their
address behind `@privaterelay.appleid.com`: to email them, register your sending domain
in Apple's "Sign in with Apple for Email Communication". Apple sends the email address
only the first time someone signs in.

**Accounts with the same email.** A person who signs in with a provider using an address
that already has a confirmed account here joins that account only when the provider
reports the address as verified. For Google that means a Gmail address or a Google
Workspace account: Google keeps any other address "verified" after the mailbox changes
hands. Otherwise the sign-in is refused (`account_not_linked`) and the person signs in the
way they did before. Someone who signed in with a provider before is recognised by that
provider account, whatever address it shows now.

**Provider tokens.** The access, refresh and ID tokens a provider returns at sign-in are
kept with the person's account (`tiffin_auth.account`), encrypted (XChaCha20-Poly1305)
with the project's auth key. That key lives in the auth engine's config, never in the
app's database or environment, so reading the table gives ciphertext. To call the
provider's API for the signed-in person, ask the engine: Better Auth's client
`authClient.getAccessToken({ accountId })` (the account's `id` from `listAccounts()`)
returns a valid access token, refreshing it first when it has expired, and
`refreshToken({ accountId })` forces a refresh. Both answer only to that account's own user.

## Previews

Sign-in works on previews (`<preview>--<name>.<domain>`) as on production: the box
serves `/api/auth/*` on each preview's host from the moment it deploys until it is
deleted, and the preview's `TIFFIN_AUTH_URL` and `TIFFIN_AUTH_HOST` name that host.

- **Same users.** A preview signs in against the project's own accounts, so testers use
  their real account and anyone who signs up on a preview is a user of the app.
- **Own cookies.** Session cookies are host-only `__Host-tiffin.*` cookies (Secure,
  `Path=/`, no `Domain`): a preview never sees production's cookies, signing in on a
  preview doesn't sign you in on production, and no other app on the box can plant a
  session in your app (browsers only take a `__Host-` cookie from its own host).
- **Own passkeys.** A preview's host is its own passkey rpID; passkeys made on
  production don't show up there (sign in another way, or add one on the preview).
- **Emails** (verification, magic links, invites) sent from a preview link back to it,
  and go out like production's, since they reach real users.
- **Sign-in providers** work on previews with no setup, with the box's keys or the app's
  own: a preview's sign-in comes back through the one redirect URI.

Code in a preview runs with the project's auth like production's, so treat a preview of
someone else's branch as you would deploying it.
