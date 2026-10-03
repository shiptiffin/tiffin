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
- **Long jobs** extend their lease with heartbeats; nothing has a time limit.

## Crons

```ts
crons: { nightly: { schedule: "0 3 * * *", app: "worker", path: "/cron/nightly" } }
```

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
