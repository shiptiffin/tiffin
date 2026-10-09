<p align="center">
  <a href="https://shiptiffin.com">
    <img src="https://shiptiffin.com/readme/sdk-banner.png" width="880" alt="@shiptiffin/sdk and its import paths: /kv, /storage, /queue, /workflow, /auth, /email, /analytics, /client and /next. Beside them, an open steel tiffin whose tiers hold a database, files, an envelope, a key, a clock and a chart.">
  </a>
</p>

<p align="center">
  <b>The SDK for apps on a ShipTiffin box.</b><br>
  KV, files, background jobs, workflows, auth, email and analytics from one package, with no keys or URLs to set up.
</p>

<p align="center">
  <a href="https://shiptiffin.com">Website</a> ·
  <a href="https://github.com/shiptiffin/tiffin/tree/main/docs/guide">Guides</a> ·
  <a href="https://github.com/shiptiffin/tiffin/tree/main/packages/sdk">Source</a> ·
  <a href="#licence">Apache-2.0</a>
</p>

[ShipTiffin](https://shiptiffin.com) runs your whole app on one server: the apps, a Postgres database, Valkey,
file storage, email, auth, queues, workflows and analytics. The box gives each app the
environment variables for its project's services, and this package reads them. `kv()` is
already connected to the project's own keyspace, `send()` already has an SMTP account, and
`getSession()` already knows where the auth engine is.

- ESM with TypeScript types. Each import path is its own module, so you load only what you use.
- Two runtime dependencies: `nodemailer` (email) and `altcha-lib` (the browser bot check).
  Next.js, React, Better Auth and React Email are optional peers.
- Runs on Bun and on Node.js 20 or later. `/client` and `/vitals` run in the browser.

## Install

```sh
bun add @shiptiffin/sdk
```

npm, pnpm and Yarn work too. If the build can't reach a registry, `tiffin sdk add` copies the
SDK bundled with your `tiffin` CLI into `vendor/` and points `package.json` at it.

## A first app

Describe the project, then use the services from a route. This one counts likes for
signed-in users:

```ts
// tiffin.config.ts
import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "hello",
  apps: { web: { framework: "next" } },
  services: { auth: {} }, // Postgres, KV, files, email and analytics are always there
});
```

```ts
// app/api/like/route.ts
import { getSession } from "@shiptiffin/sdk/auth";
import { kv } from "@shiptiffin/sdk/kv";
import { track } from "@shiptiffin/sdk/analytics";

export async function POST(request: Request) {
  const session = await getSession(request);
  if (!session) return Response.json({ error: "Sign in first." }, { status: 401 });

  const likes = await kv().incr("likes");
  await track("Like", { total: likes }, { request });
  return Response.json({ likes });
}
```

```sh
tiffin plan                     # every step, its risk, and a hash
tiffin apply --confirm <hash>   # creates the project
tiffin deploy                   # builds the app and serves it at https://hello.<your box>
```

The route has no connection string, API key or client setup. On the box, every value it
needs is already in the environment.

## What's in it

| Import | What it does |
| --- | --- |
| [`@shiptiffin/sdk`](#config) | `defineConfig()` and the types for `tiffin.config.ts` |
| [`/kv`](#kv) | The project's Valkey: JSON values, hashes, lists, sets, sorted sets, pipelines, rate limits and a cache helper |
| [`/storage`](#storage) | Buckets from server code: uploads, signed and public links, image resizing, browser upload tickets |
| [`/queue`](#queues) | Background jobs the box pushes to your app, with retries, delays, dedupe and per-key limits |
| [`/workflow`](#workflows) | Durable workflows: steps, sleeps, events and approvals that survive restarts and deploys |
| [`/verify`](#verify) | Checks that a cron or queue call came from your box |
| [`/auth`](#sign-in) | Sessions, roles and organization-scoped SQL on the server, for any framework |
| [`/next/auth`](#sign-in-with-nextjs) | Sign-in for the Next.js App Router: `proxy.ts`, `verifySession()`, Server Actions |
| [`/email`](#email) | Sends mail, React Email components included |
| [`/analytics`](#analytics) | Server-side events for the box's cookieless analytics |
| [`/vitals`, `/next/vitals`](#web-vitals) | Web Vitals from real visitors |
| [`/client`](#in-the-browser) | Browser helpers: `uploadFile`, `subscribeRun`, the bot check |
| [`/next/*`](#nextjs-cache-and-images) | Next.js cache handlers backed by Valkey, and an image loader |

## Config

`tiffin.config.ts` describes a project: its apps, services, queues and crons. `tiffin plan`
reads it and shows what would change.

```ts
import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "shop",
  apps: {
    web: { framework: "next", instances: 2 },
    worker: { role: "worker", command: "bun run worker.ts" },
  },
  services: {
    auth: { methods: ["email", "passkey", "google"] },
    storage: { buckets: { media: { public: true, allowedTypes: ["image/*"] } } },
  },
  queues: { emails: { app: "worker", concurrency: 4 } },
  crons: { digest: { app: "worker", schedule: "0 9 * * 1-5", timezone: "Europe/London" } },
});
```

`defineConfig` returns its argument unchanged; it exists for types and completion. The CLI
runs the file in a sandbox with no network and an empty `process.env`, so keep it plain data.

## KV

```ts
import { kv } from "@shiptiffin/sdk/kv";

const store = kv();

await store.set("user:1", { name: "Ada" }, { ex: 3600 }); // objects are stored as JSON
const user = await store.get<{ name: string }>("user:1");

await store.zadd("scores", { score: 42, member: "ada" });
const top = await store.zrange("scores", 0, 9, { rev: true, withScores: true });

const limit = await store.rateLimit(`login:${ip}`, { limit: 5, window: "1 m" });
if (!limit.allowed) {
  return new Response("Slow down", { status: 429, headers: { "retry-after": String(limit.retryAfter) } });
}

const posts = await store.cached("posts:latest", 60, () => loadLatestPosts());
```

- Method names and options follow `@upstash/redis`, so moving from it or from `@vercel/kv` is
  mostly a change of import. Two differences: `set` returns a boolean, and
  `zrange(..., { withScores: true })` returns `{ member, score }[]`.
- Every key gets the project's prefix, and keys that come back lose it. `scan()` and `keys()`
  only ever see the project's own keys.
- `rateLimit` is a sliding window in one Lua script, shared by every instance of the app.
  `cached` lets one caller refresh an expired value while the others get the old one.
- One connection per process: Bun's built-in Redis client on Bun, the SDK's own on Node.
  `store.pipeline()` and `store.multi()` batch commands into one round trip.

## Storage

```ts
import { upload, signedUrl, publicUrl } from "@shiptiffin/sdk/storage";

await upload("media", "avatars/42.png", file);

// A time-limited link to a private file, resized to 256 px wide WebP
const avatar = signedUrl("media", "avatars/42.png", { width: 256 });

// A file in a public bucket
const hero = publicUrl("assets", "hero.jpg", { width: 1200 });
```

Browser uploads skip your app: the server hands out a ticket, and the file goes straight to
the box's storage, in parallel parts for large files, with progress and resume.

```ts
// app/api/upload/route.ts
import { getSession } from "@shiptiffin/sdk/auth";
import { uploadRoute } from "@shiptiffin/sdk/storage";

export const POST = uploadRoute({
  bucket: "media",
  maxSize: 50 << 20, // 50 MiB
  allowedTypes: ["image/*", "video/mp4"],
  authorize: async (_file, req) => (await getSession(req)) !== null,
});
```

```ts
// in the browser
import { uploadFile } from "@shiptiffin/sdk/client";

const { key } = await uploadFile(file, "/api/upload", { onProgress: (p) => setPercent(p.percent) });
```

`Bun.s3` and the AWS SDKs also work with no setup: the box gives every app `S3_*` and `AWS_*`
variables. `onUploadCompleted()` runs code when an upload lands.

## Queues

```ts
// anywhere on the server
import { queue } from "@shiptiffin/sdk/queue";

await queue.send("emails", { to: user.email }, { delay: "10m", dedupe: `welcome-${user.id}` });
```

```ts
// app/queues/emails/route.ts: the box POSTs each job to /queues/<name>, signed
import { defineHandler, NonRetryableError } from "@shiptiffin/sdk/queue";

export const POST = defineHandler<{ to: string }>(async (job) => {
  // NonRetryableError sends the job straight to the dead-letter queue
  if (!job.payload.to.includes("@")) throw new NonRetryableError("Not an email address.");
  await sendWelcome(job.payload.to); // any other throw retries with backoff
});
```

`defineHandler` checks the signature. Crons arrive the same way, at `/cron/<name>`. Send to a
topic to fan out to every subscribed queue, or use `queue.sendTx(db, ...)` to enqueue inside your
own Postgres transaction, so the job exists only if the write commits.

## Workflows

```ts
// workflows/onboard.ts
import { workflow } from "@shiptiffin/sdk/workflow";

export const onboard = workflow.define("onboard", async (ctx, input: { userId: string }) => {
  const user = await ctx.step("load user", () => findUser(input.userId));
  await ctx.step("send welcome", () => sendWelcome(user.email));
  await ctx.sleep("wait a day", "1d");

  const paid = await ctx.waitForEvent("payment", { event: `paid-${user.id}`, timeout: "7d" });
  if (paid.timedOut) return { status: "unpaid" };

  const decision = await ctx.approval("ship", { title: `Ship the order for ${user.email}?` });
  return { status: decision.approved ? "shipped" : "held" };
});
```

```ts
// app/%5Ftiffin/workflows/route.ts serves /_tiffin/workflows, where the box sends each turn
// (Next.js treats folders that start with _ as private; %5F is the way around that)
import { workflow } from "@shiptiffin/sdk/workflow";
import "@/workflows/onboard";

export const POST = workflow.handler();
```

```ts
await onboard.start({ userId: "u_1" }, { id: "onboard-u_1" }); // one run per id
await workflow.emit("paid-u_1", { amount: 1200 });               // wakes the waiting run
```

Each step's result is saved when it finishes, so a crash or a deploy resumes the run where it
stopped. Put side effects, `Date.now()` and randomness inside `ctx.step`. A run can sleep for
months; the dashboard shows it as a timeline.

## Verify

Crons and queues can also call an address outside the box. Check that the call is real:

```ts
import { verifyRequest } from "@shiptiffin/sdk/verify";

export async function POST(req: Request) {
  const call = await verifyRequest(req, process.env.TIFFIN_QUEUE_SIGNING_SECRET!);
  if (!call) return new Response("Bad signature", { status: 401 });
  await handle(call.payload); // call.id stays the same on every retry
  return new Response(null, { status: 204 });
}
```

## Sign-in

Turn on `services.auth` and the box runs [Better Auth](https://www.better-auth.com) for your
users: email and password, magic links, one-time codes, passkeys, Google, GitHub and more,
organizations with roles, and API keys. On the server, for any framework:

```ts
import { AuthError, requireRole, withOrg } from "@shiptiffin/sdk/auth";

export async function GET(request: Request) {
  try {
    const { organization } = await requireRole(request, "member");
    // Postgres row-level security shows only this organization's rows
    const notes = await withOrg(sql, organization.id, (tx) => tx`select * from notes`);
    return Response.json(notes);
  } catch (e) {
    if (e instanceof AuthError) return e.toResponse(); // 401 or 403
    throw e;
  }
}
```

Most requests are checked locally against a session cookie the engine signs, without a network
call, so a revoked session can take up to 60 seconds to reach your code. Pass
`{ fresh: true }` for sensitive actions.

The forms are yours, built on Better Auth's client. When the app has the proof-of-work bot
check on, sign-up and sign-in need a solved one; `prepareCaptcha()` solves it in the background
and gives no headers when the check is off:

```ts
import { createAuthClient } from "better-auth/client";
import { prepareCaptcha } from "@shiptiffin/sdk/client";

const authClient = createAuthClient({ basePath: "/api/auth" });
const captcha = prepareCaptcha();

await authClient.signUp.email({ name, email, password }, { headers: await captcha() });
```

## Sign-in with Next.js

`/next/auth` follows the Next.js authentication guide: a quick cookie check in `proxy.ts`, the
real check next to the data, and Server Actions for the forms. There is no auth route to write.

```ts
// proxy.ts: looks for the session cookie only, no network
import { authProxy } from "@shiptiffin/sdk/next/auth";

export const proxy = authProxy({ protect: ["/dashboard/:path*"] });
```

```tsx
// app/dashboard/page.tsx
import { verifySession } from "@shiptiffin/sdk/next/auth";

export default async function Dashboard() {
  const { user } = await verifySession(); // signed out: redirects to /sign-in?next=/dashboard
  return <h1>Hello, {user.name}</h1>;
}
```

```ts
// app/actions.ts
"use server";
import { signIn, signOut } from "@shiptiffin/sdk/next/auth";

export async function signInAction(_: unknown, form: FormData) {
  return signIn(form, { redirectTo: "/dashboard" }); // { ok: false, code, message } on failure
}

export async function signOutAction() {
  await signOut({ redirectTo: "/" });
}
```

`requireRole("admin")` answers 403 when the role is too low. `attachCaptcha(form)` from
`/client` adds the bot check to a plain form.

## Email

```tsx
import { send } from "@shiptiffin/sdk/email";
import { WelcomeEmail } from "./emails/welcome";

await send({ to: user.email, subject: "Welcome", react: <WelcomeEmail name={user.name} /> });
await send({ to: "ops@example.com", subject: "Nightly report", text: "All good." });
```

Until the box owner sets up an SMTP relay, and always on preview deploys, mail goes to the
box's dev inbox instead of real people. `react` needs `@react-email/render` in your app.

## Analytics

```ts
import { track } from "@shiptiffin/sdk/analytics";

await track("Signup", { plan: "pro" }, { request }); // joins the visitor's session, no cookies
await track("Invoice paid", { amount: 49 });          // from a job: an event with no visitor
```

Pageviews need no code: the box counts them at its edge. `track()` never throws, and it returns
`false` when analytics isn't available, as in local development.

## Web Vitals

```tsx
// app/layout.tsx
import { WebVitals } from "@shiptiffin/sdk/next/vitals";

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <WebVitals />
        {children}
      </body>
    </html>
  );
}
```

It reports LCP, INP, CLS, FCP and TTFB per route (`/products/[id]`, not `/products/42`), in one
beacon when the page is hidden. Outside Next.js, call `reportWebVitals()` from
`@shiptiffin/sdk/vitals` in browser code.

## In the browser

`/client` has no framework dependency:

- `uploadFile(file, ticketOrRoute)` uploads [straight to storage](#storage).
- `subscribeRun(id, token, onChange)` streams a job's or workflow run's progress.
- `prepareCaptcha()` and `attachCaptcha(form)` handle [the bot check](#sign-in).
- `authConfig()` says which sign-in methods the app has on; `errorText(e)` turns an auth error
  into a sentence.

Start work from a Server Action, then show its progress live:

```ts
// app/actions.ts
"use server";
import { workflow } from "@shiptiffin/sdk/workflow";

export async function buildReport(month: string) {
  return workflow.startWithToken("report", { month }); // { id, token }
}
```

```tsx
// app/report-status.tsx
"use client";
import { useEffect, useState } from "react";
import { subscribeRun, type LiveRun } from "@shiptiffin/sdk/client";

export function ReportStatus({ id, token }: { id: string; token: string }) {
  const [run, setRun] = useState<LiveRun<{ pct: number }, string> | null>(null);
  useEffect(() => subscribeRun(id, token, setRun), [id, token]);

  if (run?.error) return <p>Failed: {run.error}</p>;
  if (run?.done) return <a href={run.output ?? "#"}>Download</a>;
  return <progress value={run?.progress?.pct ?? 0} max={100} />;
}
```

## Next.js cache and images

On a Tiffin box there's nothing to set up. Deploys of Next.js 16.2 and later use the Valkey
cache handlers in this package unless `next.config` sets its own, so with two or more
instances, `revalidateTag` and `revalidatePath` reach all of them. To wire them up yourself
(a prebuilt image, say):

```js
// next.config.mjs
import { fileURLToPath } from "node:url";
const here = (p) => fileURLToPath(new URL(p, import.meta.url));

export default {
  // ISR, route handlers, fetch and unstable_cache
  cacheHandler: here("./cache-handler.mjs"),
  // "use cache" (with cacheComponents)
  cacheHandlers: {
    default: here("./use-cache-handler.mjs"),
    remote: here("./use-cache-handler.mjs"),
  },
};

// cache-handler.mjs
export { default } from "@shiptiffin/sdk/next/cache-handler";

// use-cache-handler.mjs
export { default } from "@shiptiffin/sdk/next/use-cache";
```

The image loader has the box resize images stored in buckets, so the app doesn't run `sharp`:

```ts
// image-loader.ts
export { default } from "@shiptiffin/sdk/next/image-loader";

// next.config.ts
images: { loader: "custom", loaderFile: "./image-loader.ts" },
```

## Works with

- **Next.js 16** with the App Router comes first: `/next/auth`, the cache handlers, the image
  loader and `<WebVitals />`. The peer range is `next >= 15`.
- **Any framework with Web `Request` and `Response`**: Hono, `Bun.serve`, TanStack Start,
  SvelteKit, Astro, React Router. Handlers such as `defineHandler`, `uploadRoute` and
  `workflow.handler()` are plain `(Request) => Promise<Response>` functions.
- **Bun and Node.js 20+** on the server, and any modern browser for `/client` and `/vitals`.
- **Outside the box** (tests, scripts, your laptop), the helpers read the same environment
  variables. When one is missing, the error names it and says where it comes from. Most helpers
  also take options (`{ url }`, `{ smtpUrl }`, `{ env }`) to point them elsewhere.

## Docs

The guides cover each service in depth: [quickstart](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/quickstart.md),
[Postgres and KV](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/data.md),
[storage](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/storage.md),
[queues and workflows](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/queues.md),
[sign-in](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/auth.md),
[email](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/email.md),
[analytics](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/analytics.md) and
[what works and what doesn't](https://github.com/shiptiffin/tiffin/blob/main/docs/guide/limits.md).
Every export also has doc comments, so your editor and your coding agent see them.

## Licence

Apache-2.0. The Tiffin platform is licensed AGPL-3.0-only, but this SDK is not: importing it
puts no conditions on your app. Your code stays yours, under whatever licence you choose.
