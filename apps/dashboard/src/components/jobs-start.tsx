// The Jobs area's first screen: how to define a job, in the SDK's own words
// (@shiptiffin/sdk/queue and /workflow), with the project's real app names.
// Three short recipes: a queue, a schedule, a workflow.
import { Collapsible, Tabs as T } from "radix-ui";
import { useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { CopyButton } from "@/components/copy";
import { sentence } from "@/components/problem";
import { Button } from "@/components/ui/button";
import emptyJobs from "@/assets/illustrations/mascot-night.webp";
import { cn } from "@/lib/cn";

export type StartKind = "queue" | "schedule" | "workflow";

type Recipe = { kind: StartKind; label: string; lede: string; steps: string[]; files: Array<{ name: string; code: string }> };

function recipes(app: string): Recipe[] {
  return [
    {
      kind: "queue",
      label: "Queue",
      lede: "Send work to the background. The box delivers each job to your app, retries it if it throws, and shows every run here.",
      steps: ["Declare the queue", "Handle its jobs", "Send one"],
      files: [
        {
          name: "tiffin.config.ts",
          code: `queues: {
  emails: { app: "${app}", concurrency: 4 },
},`,
        },
        {
          name: "app/queues/emails/route.ts",
          code: `import { defineHandler } from "@shiptiffin/sdk/queue";

// The box POSTs each job here, signed. A throw retries with backoff.
export const POST = defineHandler<{ to: string }>(async (job) => {
  await sendWelcome(job.payload.to);
});`,
        },
        {
          name: "anywhere on the server",
          code: `import { queue } from "@shiptiffin/sdk/queue";

await queue.send("emails", { to: "sam@example.com" }, { delay: "10m" });`,
        },
      ],
    },
    {
      kind: "schedule",
      label: "Schedule",
      lede: "Run something on a timetable. The box calls your route on every tick and keeps the result of each run.",
      steps: ["Declare the schedule", "Handle the tick"],
      files: [
        {
          name: "tiffin.config.ts",
          code: `crons: {
  digest: { schedule: "0 9 * * 1-5", app: "${app}" }, // weekdays at 09:00
},`,
        },
        {
          name: "app/cron/digest/route.ts",
          code: `import { defineHandler } from "@shiptiffin/sdk/queue";

// The box calls /cron/<name> on every tick.
export const POST = defineHandler(async () => {
  await sendDigest();
});`,
        },
      ],
    },
    {
      kind: "workflow",
      label: "Workflow",
      lede: "Steps that survive restarts and deploys: each finished step is saved, so a failure retries from where it broke.",
      steps: ["Define the steps", "Start a run"],
      files: [
        {
          name: "onboard.ts",
          code: `import { workflow } from "@shiptiffin/sdk/workflow";

export const onboard = workflow.define("onboard", async (ctx, input: { userId: string }) => {
  const user = await ctx.step("load user", () => db.users.get(input.userId));
  await ctx.sleep("wait a day", "1d");
  await ctx.step("send tips", () => sendTips(user));
});
// The box POSTs each turn to /_tiffin/workflows: serve workflow.handler() there.`,
        },
        {
          name: "anywhere on the server",
          code: `await onboard.start({ userId: "u_1" }, { id: "onboard-u_1" });`,
        },
      ],
    },
  ];
}

/** The app a job most likely lands on: a worker if there is one, else the first app. */
export function likelyApp(apps: Record<string, { role?: string }> | undefined): string {
  const names = Object.keys(apps ?? {});
  return names.find((n) => apps?.[n]?.role === "worker") ?? names[0] ?? "web";
}

function CodeFile({ name, code, n, step }: { name: string; code: string; n: number; step?: string }) {
  return (
    <li className="grid grid-cols-[1.5rem_minmax(0,1fr)] gap-x-3">
      <span aria-hidden className="grid size-6 place-items-center rounded-full border border-rule-2 text-xs text-ink-3 tnum">
        {n}
      </span>
      {step && <p className="self-center text-[0.8125rem] text-ink-2">{step}</p>}
      <div className={cn("min-w-0 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk", step && "col-start-2 mt-1.5")}>
        <div className="flex items-center justify-between border-b border-rule py-1 pr-1 pl-3.5">
          <span className="truncate font-mono text-[0.72rem] text-ink-3">{name}</span>
          <CopyButton value={code} label={`Copy ${name}`} />
        </div>
        <pre tabIndex={0} aria-label={name} className="overflow-x-auto px-3.5 py-2.5 font-mono text-[0.75rem] leading-5 text-ink">
          <code>{code}</code>
        </pre>
      </div>
    </li>
  );
}

/**
 * "Nothing has run yet", one line on what it's for, and the page's own
 * buttons (New schedule…) as the way in. The code to define one sits behind
 * "Show the code", open from the start only where there's no button, so the
 * code is the one way in. `focus` picks the first recipe (Schedules opens on
 * Schedule).
 */
export function JobsStart({ title, app, focus = "queue", actions, className }: { title: ReactNode; app: string; focus?: StartKind; actions?: ReactNode; className?: string }) {
  const all = recipes(app);
  const [kind, setKind] = useState<StartKind>(focus);
  const r = all.find((x) => x.kind === kind) ?? all[0];
  return (
    <section className={cn("max-w-[46rem]", className)} aria-label="How to define a job">
      <span className="art-plate block size-[88px]">
        <img src={emptyJobs} alt="" width={88} height={88} className="block size-full select-none" draggable={false} />
      </span>
      <h2 className="mt-4 text-[1.0625rem] font-[550] text-ink">{title}</h2>
      <p className="mt-1.5 max-w-[34rem] text-[0.875rem] leading-[1.375rem] text-ink-2">{r.lede}</p>
      {actions && <div className="mt-5 flex flex-wrap gap-2">{actions}</div>}
      <details className="group mt-6" open={!actions}>
        <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
          <span className="inline-block transition-transform group-open:rotate-90">›</span> Show the code
        </summary>
        <T.Root value={kind} onValueChange={(v) => setKind(v as StartKind)} className="mt-3 min-w-0">
          <T.List aria-label="Define a" className="mb-4 inline-flex gap-0.5 rounded-[8px] border border-rule-2 bg-paper-sunk p-0.5">
            {all.map((x) => (
              <T.Trigger
                key={x.kind}
                value={x.kind}
                className="h-7 rounded-[6px] px-3 text-[0.8125rem] text-ink-3 transition-colors hover:text-ink data-[state=active]:bg-paper-lift data-[state=active]:font-[550] data-[state=active]:text-ink data-[state=active]:shadow-[var(--top-light)]"
              >
                {x.label}
              </T.Trigger>
            ))}
          </T.List>
          {all.map((x) => (
            <T.Content key={x.kind} value={x.kind} className="outline-hidden">
              <ol className="grid gap-4">
                {x.files.map((f, i) => (
                  <CodeFile key={f.name} name={f.name} code={f.code} n={i + 1} step={x.steps[i]} />
                ))}
              </ol>
            </T.Content>
          ))}
        </T.Root>
      </details>
    </section>
  );
}

/**
 * The Jobs area can't read the box: when the job runner isn't up yet (its
 * Postgres is starting) say so plainly, with the box's words folded below
 * and a way to try again. Anything else is a ProblemNote-style line.
 */
export function JobsTrouble({ error, retry, className }: { error: unknown; retry?: () => void; className?: string }) {
  if (!error) return null;
  const p = error instanceof ApiError ? error.problem : undefined;
  const detail = p?.detail ?? (error instanceof Error ? error.message : "Something went wrong.");
  const down = error instanceof ApiError && error.status === 503 && /queue is not running/i.test(detail);
  return (
    <div role="alert" className={cn("rounded-[10px] border px-4 py-3.5", down ? "border-rule-2 bg-paper-sunk" : "border-danger-rule bg-danger-wash", className)}>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="min-w-0 max-w-[44rem]">
          <p className="text-[0.9375rem] font-[550] text-ink">{down ? "The job runner isn’t running on this box yet." : "Couldn’t load jobs."}</p>
          <p className="mt-1 text-[0.875rem] text-ink-2">
            {down
              ? "It starts with the box’s Postgres. Queues, schedules and runs show here as soon as it answers; nothing you sent is lost."
              : sentence(detail)}
          </p>
          {down && (
            <Collapsible.Root className="mt-2">
              <Collapsible.Trigger className="text-xs text-ink-3 underline decoration-rule-3 underline-offset-4 outline-hidden select-none hover:text-ink focus-visible:text-ink data-[state=open]:no-underline">
                What the box said
              </Collapsible.Trigger>
              <Collapsible.Content>
                <p className="mt-1.5 font-mono text-[0.72rem] leading-5 break-words text-ink-3">{detail}</p>
              </Collapsible.Content>
            </Collapsible.Root>
          )}
        </div>
        {retry && (
          <Button size="sm" onClick={retry}>
            Try again
          </Button>
        )}
      </div>
    </div>
  );
}
