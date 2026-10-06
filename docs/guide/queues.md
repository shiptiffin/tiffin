# Queues, crons and workflows

Apps don't poll. Tiffin pushes each job to your app as a signed HTTP request and
retries until it succeeds.

```ts
import { queue } from "tiffin-sdk/queue";
await queue.send("emails", { to: "sam@example.com" }, { delay: "10m", key: "user:42" });
```

- **Retries** with backoff and jitter; respond `489` or set `Tiffin-Non-Retryable` to
  give up; failed jobs go to the dead-letter queue, from which you can replay them.
- **Limits:** concurrency per queue and per key, rate limits per key, FIFO groups.
- **Topics** fan out to every subscriber; **dedupe** within 24 hours.
- **`sendTx`:** enqueue inside your own database transaction (an outbox the box drains),
  so a job exists if and only if your write committed.
- **Long jobs** extend their lease with heartbeats, for up to 24 hours per attempt: an attempt
  still running then counts as failed and is retried.

### Without the SDK

- **Send:** `POST $TIFFIN_QUEUE_URL/v1/queue-internal/send` with
  `Authorization: Bearer $TIFFIN_QUEUE_KEY` and `{"name", "payload", "key"?, "delaySeconds"?, "dedupe"?}`.
- **Receive:** the box POSTs `{"id", "attemptId", "payload", ...}` with
  `Tiffin-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<raw body>">`, keyed with
  `TIFFIN_QUEUE_SIGNING_SECRET`. Check it (and that `t` is within 5 minutes), then answer
  2xx to finish, 489 to give up, anything else to retry. Crons arrive the same way.

## Declare queues and topics

A queue works as soon as an app sends to it, but declaring it in `tiffin.config.ts`
pins its target, limits and retry policy in your repo, where `tiffin plan` shows changes:

```ts
export default defineConfig({
  project: "shop",
  apps: { web: { framework: "next" }, worker: { role: "worker" } },
  queues: {
    // Jobs are POSTed to /queues/emails on the worker (the default path).
    emails: { app: "worker", concurrency: 4, maxAttempts: 5 },
    // Per-key limits: 2 at a time and 30 a minute for each `key` you send with.
    resize: { app: "worker", path: "/jobs/resize", keyConcurrency: 2, rateLimit: 30, leaseSeconds: 600 },
  },
  topics: {
    // Sending to "order.created" delivers one job to each subscribed queue's app and path.
    "order.created": { subscribers: ["emails", "resize"] },
  },
});
```

| Queue field | Default | Meaning |
|---|---|---|
| `app` | required | App that receives the jobs (a worker is fine) |
| `path` | `/queues/<name>` | Route the job is POSTed to |
| `concurrency` | 0 (no limit), max 1000 | Jobs of this queue running at once |
| `keyConcurrency` | 0 (no limit), max 1000 | Jobs running at once per `key` |
| `rateLimit` | 0 (no limit), max 10000 | Jobs started per period, per `key` |
| `ratePeriodSeconds` | 60 when `rateLimit` is set | The rate window, 1-86400 |
| `maxAttempts` | 10 (1-100) | Tries before a job goes to the dead-letter queue |
| `leaseSeconds` | 60 (5-3600) | How long an attempt may run without a response or heartbeat |

Queue names are slugs (lowercase letters, digits, dashes, at most 40). Topic names may also
contain dots (`order.created`); a name cannot be both a queue and a topic. A topic lists
`subscribers`: queues in the same file. Messages to a topic are retried with the topic's
own default retry settings, not the subscriber queue's limits.

The config is the source of truth for a declared queue: the next `tiffin apply` (and a box
restart) resets what `tiffin queue configure` changed, except pause. A paused queue stays
paused. Removing a queue from the config deletes it **and any jobs still waiting or dead in
it** (`tiffin plan` flags this as irreversible); removing a topic only unsubscribes its
queues.

## Crons

```ts
crons: {
  nightly: { schedule: "0 3 * * *", app: "worker", path: "/cron/nightly" },
  // 9:00 on weekdays in New York, summer and winter.
  morning: { schedule: "0 9 * * mon-fri", app: "worker", timezone: "America/New_York" },
}
```

| Cron field | Default | Meaning |
|---|---|---|
| `schedule` | required | 5 cron fields (`minute hour day month weekday`) or `@hourly`, `@daily`, `@weekly`, `@monthly` |
| `app` | required | App that receives the call (a worker is fine) |
| `path` | `/cron/<name>` | Route the call is POSTed to |
| `timezone` | UTC | IANA time zone the schedule is read in |
| `overlap` | `false` | Run a tick even while the previous run is still going |

When clocks change, a time that happens twice runs once and a time that is skipped runs at
the change. A tick whose previous run is still queued or running is skipped, not stacked;
`tiffin queue crons list` shows the time zone, the latest run and `lastSkippedAt`. Set
`overlap: true` to run every tick regardless.

**Crons in vercel.json.** An app's `vercel.json` crons run too, with no change to the app:

```json
{ "crons": [{ "path": "/api/cron/digest", "schedule": "0 5 * * *" }] }
```

Each is called the way Vercel calls it: a `GET` to the path, with `user-agent: vercel-cron/1.0`
and, when the app has a `CRON_SECRET` (env or `tiffin secrets set`), `Authorization: Bearer
<CRON_SECRET>`. The schedule is read in UTC; ticks, retries and skipping work as above. A
cron is named after its path (`api-cron-digest`) and comes and goes with the app's live
production deploy: a deploy or rollback replaces the app's set, previews run none, and
deleting the app removes them. One declared in `tiffin.config.ts` wins over one with the
same name, or the same app and path. `tiffin queue crons list` shows where each comes from
(`origin`: `tiffin.config.ts` or `vercel.json`) and how it is called (`method`).

## Workflows

Durable, checkpointed code in your app:

```ts
import { workflow } from "tiffin-sdk/workflow";
export const onboard = workflow.define("onboard", async (ctx, input: { userId: string }) => {
  const user = await ctx.step("load user", () => db.users.get(input.userId));
  await ctx.step("send welcome", () => sendWelcome(user));
  await ctx.sleep("wait a day", "1d");
  const paid = await ctx.waitForEvent("payment", { event: `paid-${user.id}`, timeout: "7d" });
});
await workflow.start("onboard", { userId: "42" });
await workflow.emit(`paid-42`, { amount: 900 });
```

Everything with side effects (I/O, `Date.now()`, randomness) goes inside `ctx.step`.

Runs survive app restarts, redeploys (a run finishes on the release it started on) and
box restarts. The dashboard shows each run as a timeline.

### Already using Vercel Workflow?

A Next.js app built on the Workflow DevKit (`workflow` in package.json, `withWorkflow` in
next.config, `"use workflow"` / `"use step"`, `sleep("3d")`, `FatalError`,
`RetryableError`) deploys unchanged. In production the box runs it on the DevKit's
Postgres world (`@workflow/world-postgres`, the release that matches your `workflow` major,
or your own if package.json has it) on the project's database, so the project needs
`services: { postgres: {} }`; without it the deploy fails and says so. At server start
the box brings the world's tables (schemas `workflow`, `workflow_drizzle`,
`graphile_worker`) up to date and starts its worker in every instance; all running
releases share one queue, so a sleep or a retry that comes due during a deploy runs on
whichever release is up, and on the new one once the old has stopped. Runs survive
redeploys and box restarts. Your app's own `instrumentation.ts` still runs.

- **Previews** use the DevKit's local world: their runs stay inside the instance, apart
  from production's, and do not survive a redeploy.
- **Queue routes** (`/.well-known/workflow/v1/flow` and `/step`) answer only the world
  inside the box; webhook routes stay public.
- **Seeing runs:** `npx workflow inspect runs --backend @workflow/world-postgres` (or
  `npx workflow web`) with `WORKFLOW_POSTGRES_URL` set to the project's `DATABASE_URL`
  (`tiffin db connection <project>`; from your machine, through an SSH tunnel).
- **Your own world:** set `WORKFLOW_TARGET_WORLD` and the box leaves the DevKit alone.

Tiffin's own workflows (above) remain the native option: runs finish on the release they
started on, and the dashboard shows each one.
