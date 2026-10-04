import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { useMemo, useState, type ReactNode } from "react";
import { api, ApiError, type ManifestApp, type Op } from "@/api/client";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";
import { asTier, splitAddress } from "@/lib/changes";
import { diffCounts, diffLines, hunks, stripNote } from "@/lib/diff";
import { count, cronWords, int, MINUS, signed, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { applyPlan, cancelConfirm, serviceNames, serviceWords, useConfirmRequest, type ConfirmRequest, type StagedEdit } from "@/lib/staged";
import { lossEmpty, lossParts } from "./loss";
import { ProblemNote } from "./problem";
import { Button } from "./ui/button";
import { ProjectIcon } from "@/components/project-icon";

/**
 * The confirm dialog. Most changes happen when you click (lib/staged.ts);
 * this opens only when a plan can't be undone (it deletes data) or reaches
 * outside the box. It says what will happen in a line or two, exactly what
 * is lost ("18,204 rows will be gone for good"), and asks for the project's
 * name when real data goes. Confirm applies that same plan's hash. The
 * steps, the tiffin.config.ts diff and the plan id sit behind Details.
 *
 * Mount <ChangeConfirm /> once (the Shell does, lazily, when a plan asks).
 */
export function ChangeConfirm() {
  const req = useConfirmRequest();
  return (
    <D.Root open={!!req} onOpenChange={(o) => !o && cancelConfirm()}>
      <D.Portal>
        <D.Overlay className="tray-scrim fixed inset-0 z-40 bg-[var(--scrim)]" />
        {req && <Sheet key={req.plan.hash} req={req} />}
      </D.Portal>
    </D.Root>
  );
}

const cap = (x?: string) => (x ? x.charAt(0).toUpperCase() + x.slice(1).replace(/\.$/, "") + "." : "");

function Sheet({ req }: { req: ConfirmRequest }) {
  const { project, edits, desired, plan } = req;
  const { can } = useMe();
  const [typed, setTyped] = useState("");
  const [details, setDetails] = useState(false);
  const manifest = useQuery({ ...q.manifest(project), refetchOnWindowFocus: false });
  const apps = useMemo(() => (manifest.data?.manifest.apps ?? {}) as Record<string, ManifestApp>, [manifest.data]);
  const ops = plan.ops ?? [];
  const tier = asTier(plan.risk);
  const irreversible = tier === "irreversible";
  const lost = ops.filter((o) => asTier(o.risk) === "irreversible");
  const out = ops.filter((o) => asTier(o.risk) === "outbound");
  const main = lost[0] ?? out[0] ?? ops[0];
  const title = main ? opTitle(main, project, apps, edits).title : "Make this change";
  const others = ops.filter((o) => o !== main).slice(0, 2);
  const realLoss = lost.some((o) => o.loss && !lossEmpty(o.loss));
  const needsName = irreversible && (realLoss || lost.some((o) => !o.loss));
  const armed = !needsName || typed.trim() === project;
  const allowed = !irreversible || can("apply:irreversible");

  const apply = useMutation({ mutationFn: () => applyPlan(project, edits, desired, plan) });
  const stale = apply.error instanceof ApiError && apply.error.status === 428;

  return (
    <D.Content
      aria-describedby={undefined}
      className={cn(
        "tray fixed inset-x-0 bottom-0 z-50 mx-auto flex max-h-[92dvh] w-full max-w-[34rem] flex-col overflow-hidden rounded-t-[16px] border bg-paper-raised shadow-overlay outline-none",
        "sm:top-[max(1rem,14vh)] sm:bottom-auto sm:w-[calc(100%-2rem)] sm:rounded-[14px]",
        irreversible ? "border-danger-rule" : "border-rule-2",
      )}
    >
      <div className="min-h-0 flex-1 overflow-y-auto px-5 pt-5 pb-4 sm:px-6 sm:pt-6">
        <p className="mb-2 flex items-center gap-1.5 text-[0.8125rem] text-ink-3">
          <ProjectIcon project={project} size={14} />
          {project}
        </p>
        <D.Title className="text-[1.25rem] leading-7 font-[550] tracking-[-0.015em] text-ink">{title.replace(/\.$/, "")}?</D.Title>

        {lost.length > 0 ? (
          <div className="mt-3 rounded-[10px] bg-danger-wash px-3.5 py-3 text-[0.9375rem] leading-[1.375rem] text-ink">
            {lost.map((o, i) => (
              <p key={o.address + i}>
                {o.loss && !lossEmpty(o.loss) ? (
                  <>
                    <b className="font-[550]">{lossParts(o.loss).join(" · ")}</b> will be gone for good.
                  </>
                ) : o.loss ? (
                  "It’s empty, so nothing is lost, but it can’t be undone."
                ) : (
                  <>{cap(o.reason) || "This can’t be undone."}</>
                )}
              </p>
            ))}
            <p className="mt-1 text-sm text-ink-2">This can’t be undone.</p>
          </div>
        ) : out.length > 0 ? (
          <div className="mt-3 rounded-[10px] bg-warn-wash px-3.5 py-3 text-[0.9375rem] leading-[1.375rem] text-ink">
            {out.map((o, i) => (
              <p key={o.address + i}>{cap(o.reason) || "It reaches outside the box."}</p>
            ))}
            <p className="mt-1 text-sm text-ink-2">You can undo it later, but anyone who saw it will have seen it.</p>
          </div>
        ) : (
          <p className="mt-3 text-[0.9375rem] text-ink-2">You can undo this.</p>
        )}

        {others.length > 0 && (
          <ul className="mt-3 flex flex-col gap-1 text-[0.875rem] text-ink-2">
            {others.map((o, i) => (
              <li key={o.address + i} className="flex gap-2">
                <span aria-hidden className="text-ink-4">
                  ·
                </span>
                {opTitle(o, project, apps, edits).title}
              </li>
            ))}
            {ops.length > 3 && <li className="pl-4 text-ink-3">and {words(ops.length - 3)} more (see Details)</li>}
          </ul>
        )}

        {needsName && allowed && (
          <label className="mt-4 block text-sm text-ink-2">
            Type <b className="ident font-[550] text-ink">{project}</b> to confirm
            <input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              autoFocus
              aria-label={`Type ${project} to confirm`}
              className="ident mt-1.5 h-9 w-full rounded-[8px] border border-rule-2 bg-paper px-2.5 text-ink focus-visible:border-danger focus-visible:outline-none"
            />
          </label>
        )}
        {!allowed && <p className="mt-4 text-sm text-danger">Your role can’t make changes that delete data. Ask the owner or an admin.</p>}
        {stale && <p className="mt-4 text-sm text-ink">The project changed while this was open. Close this and try again.</p>}
        {apply.isError && !stale && <ProblemNote className="mt-4" error={apply.error} />}

        <button
          type="button"
          onClick={() => setDetails((d) => !d)}
          aria-expanded={details}
          className="mt-5 inline-flex items-center gap-1 text-[0.8125rem] font-[550] text-ink-3 hover:text-ink"
        >
          Details
          <ChevronDown className={cn("size-3.5 transition-transform duration-[var(--dur-state)]", details && "rotate-180")} />
        </button>
        {details && (
          <div className="mt-2">
            <ol className="border-t border-rule">
              {ops.map((op, i) => (
                <Step key={op.address + i} n={i + 1} op={op} project={project} apps={apps} edits={edits} />
              ))}
            </ol>
            <ConfigDiff project={project} before={manifest.data?.config} desired={desired} />
            <p className="mt-2 text-xs text-ink-3">
              Plan <span className="ident">{plan.hash.slice(0, 12)}</span>. The same change as editing tiffin.config.ts and running <span className="ident">tiffin apply</span>.
            </p>
          </div>
        )}
      </div>

      <footer className="flex shrink-0 items-center justify-end gap-2 border-t border-rule px-5 py-3.5 sm:px-6">
        <D.Close asChild>
          <Button variant="ghost" size="lg">
            Cancel
          </Button>
        </D.Close>
        <Button
          variant={irreversible ? "danger" : "primary"}
          size="lg"
          disabled={!armed || !allowed || apply.isPending || stale}
          onClick={() => apply.mutate()}
        >
          {apply.isPending ? "Working…" : irreversible ? "Delete for good" : "Confirm"}
        </Button>
      </footer>
    </D.Content>
  );
}

type AppLike = { instances?: number; memoryMB?: number; role?: string; framework?: string };
type QueueLike = { app?: string; concurrency?: number; keyConcurrency?: number; rateLimit?: number; ratePeriodSeconds?: number; maxAttempts?: number; leaseSeconds?: number };
type CronLike = { schedule?: string; app?: string; path?: string };
type BucketLike = { public?: boolean };

const fieldWords: Record<string, string> = {
  memoryMB: "memory",
  maxMemoryMB: "memory cap",
  instances: "instances",
  retentionDays: "how long events are kept",
  healthcheck: "health check",
  routes: "addresses",
  framework: "framework",
  path: "source folder",
  extensions: "extensions",
  methods: "sign-in methods",
  organizations: "organizations",
  from: "sender",
  concurrency: "how many run at once",
  keyConcurrency: "how many run at once per key",
  maxAttempts: "retries",
  leaseSeconds: "how long a job may run",
  rateLimit: "rate limit",
  schedule: "schedule",
  public: "access",
};
const fieldsWords = (f: string[]) => f.map((x) => fieldWords[x] ?? x.replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase()).join(", ");
const runAtOnce = (n?: number) => (!n ? "no limit on how many run at once" : `${words(n)} at once`);
const mbw = (n: number) => (n >= 1024 && n % 1024 === 0 ? `${n / 1024}\u202FGB` : `${int(n)}\u202FMB`);

/** A plan step in words a person would say. Falls back to the staged edit's own words, then to the server's reason. */
function opTitle(op: Op, project: string, apps: Record<string, ManifestApp>, edits: StagedEdit[]): { title: string; detail?: string; facts: ReactNode[] } {
  const { kind, name } = splitAddress(op.address);
  const reason = op.reason ? op.reason.charAt(0).toUpperCase() + op.reason.slice(1) + "." : undefined;
  const fields = op.fields ?? [];
  const staged = edits.find((e) => e.kind === "set" && `${e.path[0] === "apps" ? "app" : e.path[0] === "queues" ? "queue" : e.path[0] === "crons" ? "cron" : e.path[0]}/${e.path[1]}` === op.address);
  const fallback = staged?.kind === "set" ? staged.what : undefined;

  if (kind === "app") {
    const b = (op.before ?? {}) as AppLike;
    const a = (op.after ?? {}) as AppLike;
    if (op.action === "create") {
      const per = a.memoryMB ?? 512;
      const n = a.instances ?? 1;
      return {
        title: fallback ?? `Add the ${name} app to ${project}`,
        detail: `It is built on the box; its page offers the first deploy. Until then it uses no memory.`,
        facts: [
          <span key="m">
            Memory up to <b className="font-[550] text-ink-2">{mbw(per * n)}</b> once it runs
          </span>,
        ],
      };
    }
    if (op.action === "delete") return { title: `Remove the ${name} app from ${project}`, detail: reason, facts: [] };
    const fromN = b.instances ?? 1;
    const toN = a.instances ?? 1;
    const fromM = b.memoryMB ?? 512;
    const toM = a.memoryMB ?? apps[name]?.memoryMB ?? 512;
    const delta = toN * toM - fromN * fromM;
    const memFact = (
      <span key="m">
        Memory <b className="font-[550] text-ink-2">{signed(delta)}&#8239;MB</b> at most
      </span>
    );
    const onlyMem = fields.length === 1 && fields[0] === "memoryMB";
    const onlyInst = fields.length === 1 && fields[0] === "instances";
    const both = fields.length === 2 && fields.includes("memoryMB") && fields.includes("instances");
    if (onlyMem) {
      return {
        title: `Give ${name} ${mbw(toM)} of memory, ${toM > fromM ? "up" : "down"} from ${mbw(fromM)}`,
        detail: `Per instance. ${name} restarts one instance at a time with the new limit${fromN > 1 ? ", so the others keep serving" : "; it is away for a moment while it restarts"}.`,
        facts: [memFact, <span key="d">{fromN > 1 ? "Downtime none" : "Downtime a few seconds"}</span>],
      };
    }
    if (onlyInst || both) {
      const up = toN > fromN;
      const worker = a.role === "worker";
      const more = toN - fromN;
      const title = both
        ? `Run ${name} as ${count(toN, "instance")} of ${mbw(toM)} (now ${fromN} × ${mbw(fromM)})`
        : `Scale ${name} from ${fromN} to ${count(toN, "instance")}`;
      return {
        title,
        detail: worker
          ? up
            ? `Starts ${words(more)} more ${more === 1 ? "instance" : "instances"} of ${name}; ${more === 1 ? "it takes" : "they take"} jobs from the same queues. Jobs already running finish where they are.`
            : `Stops ${words(-more)} ${-more === 1 ? "instance" : "instances"} of ${name} once ${-more === 1 ? "its jobs finish" : "their jobs finish"}.`
          : up
            ? `Starts ${words(more)} more ${more === 1 ? "instance" : "instances"} of ${name}, waits for ${more === 1 ? "its" : "their"} health check, then adds ${more === 1 ? "it" : "them"} to the edge. The running ${fromN === 1 ? "instance keeps" : "instances keep"} serving.`
            : more < 0
              ? `Stops ${words(-more)} ${-more === 1 ? "instance" : "instances"} of ${name} once ${-more === 1 ? "it finishes its" : "they finish their"} requests.`
              : `Restarts ${name}'s instances one at a time with the new memory limit.`,
        facts: [memFact, <span key="d">Downtime none</span>],
      };
    }
    return { title: fallback ?? `Change ${name}’s ${fieldsWords(fields)}`, detail: reason, facts: [] };
  }

  if (kind === "queue") {
    const a = (op.after ?? {}) as QueueLike;
    const b = (op.before ?? {}) as QueueLike;
    if (op.action === "create") {
      const bits = [runAtOnce(a.concurrency), a.maxAttempts ? `up to ${count(a.maxAttempts, "try", "tries")}` : null, a.rateLimit ? `${int(a.rateLimit)} per ${a.ratePeriodSeconds ?? 1}\u202Fs per key` : null].filter(Boolean);
      return {
        title: `Declare the ${name} queue: ${bits.join(", ")}`,
        detail: a.app ? `Jobs go to ${a.app}. Declared in tiffin.config.ts, its settings survive a redeploy.` : reason,
        facts: [],
      };
    }
    if (op.action === "delete") return { title: `Remove the ${name} queue`, detail: reason, facts: [] };
    if (fields.length === 1 && fields[0] === "concurrency") return { title: `Let ${name} run ${runAtOnce(a.concurrency)} (now ${runAtOnce(b.concurrency).replace(" at once", "")})`, detail: reason, facts: [] };
    return { title: fallback ?? `Change the ${name} queue’s ${fieldsWords(fields)}`, detail: reason, facts: [] };
  }

  if (kind === "cron") {
    const a = (op.after ?? {}) as CronLike;
    if (op.action === "create") return { title: `Add the ${name} schedule: ${cronWords(a.schedule ?? "")}${a.app ? ` on ${a.app}` : ""}`, detail: reason, facts: [] };
    if (op.action === "delete") return { title: `Remove the ${name} schedule`, detail: reason, facts: [] };
    return { title: fallback ?? `Change the ${name} schedule${a.schedule ? ` to ${cronWords(a.schedule)}` : ""}`, detail: reason, facts: [] };
  }

  if (kind === "bucket") {
    const a = (op.after ?? {}) as BucketLike;
    const restoring = edits.some((e) => e.kind === "bucket" && e.bucket === name && e.from === "absent");
    if (op.action === "create" && restoring)
      return { title: `Restore the ${name} bucket from the trash`, detail: `It comes back ${a.public ? "public" : "private"}, with its files. Undo puts it back in the trash.`, facts: [] };
    if (op.action === "create") return { title: `Add the ${name} bucket (${a.public ? "public" : "private"})`, detail: reason, facts: [] };
    if (op.action === "delete") return { title: `Remove the ${name} bucket`, detail: reason, facts: [] };
    return { title: `Make the ${name} bucket ${a.public ? "public" : "private"}`, detail: reason, facts: [] };
  }

  if (kind === "env") {
    const v = typeof op.after === "string" ? op.after : JSON.stringify(op.after);
    if (op.action === "create") return { title: `Set ${name} to “${v}”`, detail: "Apps restart with it.", facts: [] };
    if (op.action === "delete") return { title: `Remove ${name}`, detail: "Apps restart without it.", facts: [] };
    return { title: `Change ${name} to “${v}”`, detail: "Apps restart with it.", facts: [] };
  }

  if (kind === "topic") {
    if (op.action === "create") return { title: `Add the ${name} topic`, detail: reason, facts: [] };
    if (op.action === "delete") return { title: `Remove the ${name} topic`, detail: reason, facts: [] };
    return { title: `Change the ${name} topic’s ${fieldsWords(fields)}`, detail: reason, facts: [] };
  }

  if (kind === "service") {
    const service = serviceWords[name]?.replace(/^a /, "the ") ?? name;
    if (op.action === "delete") return { title: `Remove ${service} from ${project}`, detail: reason, facts: [] };
    if (op.action === "create") return { title: `Add ${serviceWords[name] ?? name} to ${project}`, detail: reason, facts: [] };
    return { title: fallback ?? `Change ${serviceNames[name] ?? name}’s ${fieldsWords(fields)}`, detail: reason, facts: [] };
  }

  if (kind === "project") return { title: op.action === "create" ? `Start the ${project} project` : op.action === "delete" ? `Remove the ${project} project` : `Change ${project}`, detail: reason, facts: [] };
  return { title: fallback ?? reason ?? op.address, facts: [] };
}

function Step({ n, op, project, apps, edits }: { n: number; op: Op; project: string; apps: Record<string, ManifestApp>; edits: StagedEdit[] }) {
  const t = opTitle(op, project, apps, edits);
  const tier = asTier(op.risk);
  return (
    <li className="grid grid-cols-[22px_minmax(0,1fr)_auto] gap-x-2.5 gap-y-1 border-b border-rule py-3">
      <span className="ident pt-0.5 text-[0.6875rem] leading-[1.375rem] text-ink-3">{String(n).padStart(2, "0")}</span>
      <h4 className="text-[0.875rem] leading-5 font-[550] text-ink">{t.title}</h4>
      <span className={cn("self-center text-xs", tier === "irreversible" ? "text-danger" : tier === "outbound" ? "text-warn-ink" : "text-ink-3")}>
        {tier === "irreversible" ? "can’t be undone" : tier === "outbound" ? "reaches out" : "can be undone"}
      </span>
      {t.detail && tier !== "irreversible" && <p className="col-start-2 col-end-4 max-w-[60ch] text-sm text-ink-2">{t.detail}</p>}
      {t.facts.length > 0 && <div className="col-start-2 col-end-4 mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">{t.facts}</div>}
    </li>
  );
}

function ConfigDiff({ project, before, desired }: { project: string; before?: string; desired: ConfirmRequest["desired"] }) {
  const rendered = useQuery({ queryKey: ["render", JSON.stringify(desired)], queryFn: () => api.renderConfig(desired), retry: false, staleTime: Infinity });
  const after = rendered.data?.config;
  const error = rendered.error;
  const lines = useMemo(() => (before !== undefined && after !== undefined ? diffLines(stripNote(before), after) : null), [before, after]);
  const c = lines ? diffCounts(lines) : null;
  return (
    <section className="mt-3" aria-label="tiffin.config.ts">
            <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-rule px-3.5 py-2 text-[0.78125rem] text-ink-3">
          <span className="ident text-ink">{project}/tiffin.config.ts</span>
          {c && (
            <span className="counts">
              <span data-n="">+{c.add}</span> <span data-n="">{MINUS}{c.del}</span>
            </span>
          )}
          <span className="ml-auto max-sm:hidden">
            The same change as editing this file and running <span className="ident text-ink-2">tiffin apply</span>.
          </span>
        </div>
        {error ? (
          <p className="px-3.5 py-3 text-sm text-ink-3">The config file can’t be shown for this edit.</p>
        ) : !lines ? (
          <div className="h-32 animate-pulse bg-paper-sunk" />
        ) : (
          <pre className="diff overflow-x-auto py-2.5">
            {hunks(lines).map((r, i) =>
              r.k === "fold" ? (
                <span key={i} className="ln" data-k="fold">
                  <b>…</b>
                  <s> </s>
                  {r.count} unchanged {r.count === 1 ? "line" : "lines"}
                </span>
              ) : (
                <span key={i} className="ln" data-k={r.k}>
                  <b>{r.k === "del" ? r.a : r.b}</b>
                  <s>{r.k === "add" ? "+" : r.k === "del" ? MINUS : " "}</s>
                  {r.text || " "}
                </span>
              ),
            )}
          </pre>
        )}
      </div>
    </section>
  );
}

