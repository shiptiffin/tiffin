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
export const onboarding = workflow.define("onboarding", async (ctx, user) => {
  await ctx.step("welcome", () => sendWelcome(user));
  await ctx.sleep("3 days");
  const ok = await ctx.waitForEvent("approved", { timeout: "7 days" }); // or a human approval
  await ctx.all([ctx.step("a", a), ctx.step("b", b)]);
});
```

Runs survive app restarts, redeploys (a run finishes on the release it started on) and
box restarts. The dashboard shows each run as a timeline.
