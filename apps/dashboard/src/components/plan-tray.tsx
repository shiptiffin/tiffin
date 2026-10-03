import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { X } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { api, ApiError, type ManifestApp, type Op, type Plan, type Tier } from "@/api/client";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";
import { asTier, splitAddress, tierRank } from "@/lib/changes";
import { diffCounts, diffLines, hunks, stripNote } from "@/lib/diff";
import { useEnamel } from "@/lib/enamel";
import { int, mb, MINUS, signed, words } from "@/lib/format";
import { memoryModel } from "@/lib/memory";
import { useMe } from "@/lib/me";
import {
  applyEdits,
  closeTray,
  describe,
  discard,
  intentFor,
  openTray,
  serviceNames,
  undoWords,
  useAllStaged,
  useStaged,
  useTray,
  type StagedEdit,
} from "@/lib/staged";
import { EnamelSwatch } from "./enamel-swatch";
import { HoldToCommit } from "./hold-to-commit";
import { MorphLabel } from "./morph-label";
import { ProblemNote } from "./problem";
import { Qty } from "./qty";
import { RiskDots } from "./risk-dots";
import { SegMeter } from "./seg-meter";
import { toast } from "./toast";
import { Button } from "./ui/button";

const RESERVE_MB = 512; // kept free for spikes; the Box page says so too

/**
 * The staged-changes bar and the plan tray. Mount <StagedChanges /> once
 * (the Shell does, lazily, as soon as anything is staged). Levers call
 * stage(); the bar appears ("3 changes staged on shop · Review"); Review
 * raises the tray with the real plan from the API: ordered steps with risk,
 * room left in the box before → after, downtime, what undo does, and the
 * tiffin.config.ts diff. Apply sends the plan hash; the toast offers Undo.
 */
export function StagedChanges() {
  const all = useAllStaged();
  const tray = useTray();
  const projects = Object.keys(all);
  const first = projects[0];
  return (
    <>
      {first && !tray && <StagedBar project={first} others={projects.slice(1)} count={all[first].length} />}
      <D.Root open={!!tray} onOpenChange={(o) => !o && closeTray()}>
        <D.Portal>
          <D.Overlay className="tray-scrim fixed inset-0 z-40 bg-[var(--scrim)]" />
          {tray && <Tray project={tray} />}
        </D.Portal>
      </D.Root>
    </>
  );
}

function StagedBar({ project, others, count }: { project: string; others: string[]; count: number }) {
  const enamel = useEnamel(project);
  return (
    <div
      role="region"
      aria-label="Staged changes"
      className="fixed bottom-5 left-1/2 z-30 flex max-w-[calc(100vw-1.5rem)] -translate-x-1/2 animate-[rise_var(--dur-enter)_var(--ease-out)_both] items-center gap-3 rounded-[12px] border border-rule-2 bg-paper-raised py-1.5 pr-1.5 pl-3.5 text-[0.84375rem] shadow-raised lg:left-[calc(50%+116px)]"
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="size-2 shrink-0 rounded-full bg-brass" aria-hidden />
        <span className="truncate">
          <b className="font-[550]">{count === 1 ? "1 change" : `${int(count)} changes`}</b> staged on{" "}
          <span className="inline-flex items-center gap-1.5">
            <EnamelSwatch enamel={enamel} size={7} />
            {project}
          </span>
          {others.length > 0 && <span className="text-ink-3"> and {others.length === 1 ? others[0] : `${others.length} more`}</span>}
        </span>
      </span>
      <Button variant="ghost" size="sm" onClick={() => discard(project)}>
        Discard
      </Button>
      <Button variant="primary" size="md" onClick={() => openTray(project)} aria-keyshortcuts="Enter">
        Review
      </Button>
    </div>
  );
}

type Room = { before: number; after: number; delta: number; totalMB: number; usedMB: number; perMB: number; perApp?: string } | null;

function Tray({ project }: { project: string }) {
  const edits = useStaged(project);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can, name: me } = useMe();
  const [intent, setIntent] = useState("");
  const [armed, setArmed] = useState("");
  const [stale, setStale] = useState(false);

  const manifest = useQuery({ ...q.manifest(project), refetchOnWindowFocus: false });
  const desired = useMemo(() => (manifest.data ? applyEdits(manifest.data.manifest, edits) : undefined), [manifest.data, edits]);
  const key = JSON.stringify(desired ?? null);
  const plan = useQuery({
    queryKey: ["plan", project, key],
    queryFn: () => api.plan(desired!),
    enabled: !!desired,
    retry: false,
    staleTime: 0,
    refetchOnWindowFocus: false,
  });
  const rendered = useQuery({
    queryKey: ["render", key],
    queryFn: () => api.renderConfig(desired!),
    enabled: !!desired,
    retry: false,
    staleTime: Infinity,
  });
  const res = useQuery(q.resources);

  const p = plan.data;
  const ops = p?.ops ?? [];
  const tier = asTier(p?.risk);
  const irreversible = tier === "irreversible";
  const empty = !!p && ops.length === 0;
  const allowed = !irreversible || can("apply:irreversible");
  const isArmed = !irreversible || armed.trim() === project;
  const n = edits.length;
  const changeWord = n === 1 ? "1 change" : `${words(n)} changes`;

  const apps = useMemo(() => (manifest.data?.manifest.apps ?? {}) as Record<string, ManifestApp>, [manifest.data]);
  const room: Room = useMemo(() => {
    const r = res.data;
    if (!r) return null;
    const m = memoryModel(r);
    const avail = m.freeMB;
    let delta = 0;
    let per = 512;
    let perApp: string | undefined;
    for (const e of edits) {
      if (e.kind !== "instances") continue;
      const memMB = apps[e.app]?.memoryMB ?? 512;
      delta += (e.to - e.from) * memMB;
      per = memMB;
      perApp = e.app;
    }
    return { before: avail, after: avail - delta, delta, totalMB: m.totalMB, usedMB: m.usedMB, perMB: per, perApp };
  }, [res.data, edits, apps]);

  const apply = useMutation({
    mutationFn: () => api.apply(desired!, p!.hash, intent.trim() || intentFor(edits, project)),
    onSuccess: (r) => {
      const done = [...edits];
      discard(project);
      closeTray();
      for (const k of [["project", project], ["changes"], ["manifest", project], ["box-resources"], ["projects"]]) qc.invalidateQueries({ queryKey: k });
      const id = r.change?.id;
      toast({
        title: r.applied ? <AppliedWords edits={done} project={project} /> : "Nothing to change: the project already looks like that.",
        detail: r.applied ? "Signed into the Ledger." : undefined,
        action: id && r.change && asTier(r.change.plan.risk) !== "irreversible" ? { label: "Undo", run: () => undoChange(id, qc, navigate) } : undefined,
      });
    },
    onError: (e) => {
      // Someone (or an agent) changed the project since this plan: plan again.
      if (e instanceof ApiError && e.status === 428) {
        setStale(true);
        void manifest.refetch();
      }
    },
  });

  // ↵ applies (reversible and outbound plans), unless you're typing.
  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== "Enter" || ev.metaKey || ev.ctrlKey || ev.repeat) return;
      const t = ev.target;
      if (t instanceof Element && t.closest("input, textarea, button, a, [role=slider]")) return;
      if (p && !empty && !irreversible && allowed && !apply.isPending) {
        ev.preventDefault();
        apply.mutate();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [p, empty, irreversible, allowed, apply]);

  const loading = manifest.isPending || (!!desired && plan.isPending);
  const error = manifest.error ?? plan.error;
  const verb = `Apply ${changeWord} to ${project}`;
  const label = loading ? `Review ${changeWord}` : verb;

  return (
    <D.Content
      aria-describedby={undefined}
      onOpenAutoFocus={(e) => e.preventDefault()}
      className={cn(
        "tray fixed inset-x-0 bottom-0 z-50 mx-auto flex max-h-[92dvh] w-full max-w-[1080px] flex-col overflow-hidden rounded-t-[16px] border bg-paper-raised shadow-overlay outline-none",
        "sm:bottom-5 sm:w-[calc(100%-2.5rem)] sm:rounded-[16px] lg:left-[232px] lg:w-[calc(100%-232px-5rem)]",
        irreversible && isArmed && armed ? "border-danger" : irreversible ? "border-danger-rule" : "border-rule-2",
      )}
    >
      <div className="mx-auto mt-2 h-1 w-9 shrink-0 rounded-full bg-rule-2" aria-hidden />
      <header className="flex shrink-0 items-start gap-3 border-b border-rule px-5 pt-2 pb-4 sm:px-7">
        <div className="min-w-0 flex-1">
          <D.Title className="text-[1.375rem] leading-7 font-[500] tracking-[-0.02em] text-ink">
            {words(n, true)} {n === 1 ? "change" : "changes"} staged on {project}
          </D.Title>
          <p className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[0.78125rem] text-ink-3">
            {p ? <RiskDots tier={tier} label={`${riskWord(tier)} overall`} /> : <span>Planning…</span>}
            {p && (
              <>
                <span aria-hidden>·</span>
                <span>
                  plan <span className="ident text-[0.75rem] text-ink-2">{p.hash.slice(0, 4)}&#8239;{p.hash.slice(4, 8)}</span>
                </span>
              </>
            )}
            {me && (
              <>
                <span aria-hidden>·</span>
                <span>staged by {me}</span>
              </>
            )}
            <span aria-hidden className="max-sm:hidden">
              ·
            </span>
            <span className="max-sm:hidden">
              {irreversible ? (
                "arm it to apply,"
              ) : (
                <>
                  <span className="kbd">↵</span> applies,
                </>
              )}{" "}
              <span className="kbd">esc</span> keeps it staged
            </span>
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => discard(project)}>
          Discard all
        </Button>
        <D.Close asChild>
          <Button variant="ghost" size="icon-sm" aria-label="Close and keep staged">
            <X />
          </Button>
        </D.Close>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {stale && (
          <p role="status" className="mx-5 mt-4 rounded-[8px] bg-brass-wash px-3.5 py-2.5 text-sm text-ink sm:mx-7">
            The project changed since you opened this, so here is a fresh plan. Read it again before you apply.
          </p>
        )}
        {error ? (
          <div className="p-5 sm:p-7">
            <ProblemNote error={error} title="This change can’t be planned." />
          </div>
        ) : loading || !p ? (
          <TraySkeleton />
        ) : empty ? (
          <p className="px-5 py-8 text-md text-ink-2 sm:px-7">Nothing would change: {project} already looks like that. Discard the staged edits.</p>
        ) : (
          <>
            <div className="grid md:grid-cols-[minmax(0,1fr)_320px]">
              <section className="px-5 pb-6 sm:px-7 md:border-r md:border-rule" aria-label="Steps">
                <h3 className="label pt-5 pb-1">What will happen, in order</h3>
                <ol>
                  {ops.map((op, i) => (
                    <Step key={op.address + i} n={i + 1} op={op} project={project} apps={apps} />
                  ))}
                </ol>
              </section>
              <aside className="px-5 pb-6 sm:px-7 md:px-6" aria-label="Impact">
                <h3 className="label pt-5 pb-1">Room left in the box</h3>
                <RoomBlock room={room} unavailable={!!res.error} />
                <Impact edits={edits} ops={ops} project={project} apps={apps} />
              </aside>
            </div>
            <ConfigDiff project={project} before={manifest.data?.config} after={rendered.data?.config} error={rendered.error} />
          </>
        )}
      </div>

      <footer className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-t border-rule bg-paper-raised px-5 py-3.5 sm:px-7">
        {apply.isError && !(apply.error instanceof ApiError && apply.error.status === 428) && (
          <ProblemNote error={apply.error} className="mb-1 w-full" />
        )}
        <label className="flex min-w-[12rem] flex-1 items-center gap-2 text-sm text-ink-3">
          <span className="shrink-0">Why</span>
          <input
            value={intent}
            onChange={(e) => setIntent(e.target.value)}
            placeholder={intentFor(edits, project)}
            maxLength={500}
            className="h-8 min-w-0 flex-1 rounded-[7px] border border-rule-2 bg-paper px-2.5 text-[0.84375rem] text-ink placeholder:text-ink-4 focus-visible:border-brass focus-visible:outline-none"
            aria-label="Why you are making this change (shown in the Ledger)"
          />
        </label>
        {irreversible && p && !empty && (
          <>
            {!allowed ? (
              <p className="text-sm text-danger">Your role can’t apply irreversible changes. Ask the owner or an admin.</p>
            ) : (
              <label className="flex items-center gap-2 text-sm text-ink-2">
                Type <code className="ident rounded-[4px] bg-danger-wash px-1 text-danger">{project}</code> to arm
                <input
                  value={armed}
                  onChange={(e) => setArmed(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={`Type ${project} to arm this irreversible change`}
                  className="ident h-8 w-28 rounded-[7px] border border-danger-rule bg-paper px-2 text-ink focus-visible:border-danger focus-visible:outline-none"
                />
              </label>
            )}
          </>
        )}
        {irreversible && isArmed && allowed && p && !empty ? (
          <HoldToCommit label={`Hold to ${verb.charAt(0).toLowerCase()}${verb.slice(1)}`} doneLabel="Applying…" onCommit={() => apply.mutate()} />
        ) : (
          <Button
            variant="primary"
            size="lg"
            disabled={loading || !p || empty || !allowed || !isArmed || apply.isPending || !!error}
            onClick={() => apply.mutate()}
          >
            <MorphLabel text={apply.isPending ? "Applying…" : label} />
            {!irreversible && (
              <span className="kbd border-current text-current opacity-55" aria-hidden>
                ↵
              </span>
            )}
          </Button>
        )}
      </footer>
    </D.Content>
  );
}

const riskWord = (t: Tier) => (t === "irreversible" ? "Irreversible" : t === "outbound" ? "Outbound" : t === "reversible" ? "Reversible" : "Read only");

function AppliedWords({ edits, project }: { edits: StagedEdit[]; project: string }) {
  if (edits.length !== 1) return <>Applied {words(edits.length)} changes to {project}.</>;
  const e = edits[0];
  if (e.kind === "instances") return <>Scaled {e.app} to {e.to} {e.to === 1 ? "instance" : "instances"} in {project}.</>;
  return <>{describe(e, project).replace(/^Remove/, "Removed").replace(/^Add/, "Added")}.</>;
}

/** Undo from a toast: plan the undo, and apply it at once when it's reversible; otherwise open the change to review it. */
export async function undoChange(id: string, qc: ReturnType<typeof useQueryClient>, navigate: ReturnType<typeof useNavigate>) {
  try {
    await api.undo(id);
  } catch (e) {
    const plan = e instanceof ApiError && e.status === 428 ? (e.problem.plan as Plan | undefined) : undefined;
    if (!plan) {
      toast({ title: "Couldn’t undo that.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" });
      return;
    }
    if (tierRank[asTier(plan.risk)] > tierRank.outbound) {
      void navigate({ to: "/changes/$id", params: { id } });
      return;
    }
    try {
      await api.undo(id, plan.hash);
      toast({ title: "Undone. Everything is back as it was.", detail: "The undo is in the Ledger too." });
    } catch (e2) {
      toast({ title: "Couldn’t undo that.", detail: e2 instanceof ApiError ? (e2.problem.detail ?? e2.message) : String(e2), tone: "danger" });
    }
  }
  for (const k of [["changes"], ["box-resources"], ["projects"]]) void qc.invalidateQueries({ queryKey: k });
  void qc.invalidateQueries({ predicate: (qq) => ["project", "manifest"].includes(String(qq.queryKey[0])) });
}

function opTitle(op: Op, project: string, apps: Record<string, ManifestApp>): { title: string; detail?: string; facts: ReactNode[] } {
  const { kind, name } = splitAddress(op.address);
  const before = (op.before ?? {}) as { instances?: number; memoryMB?: number; role?: string };
  const after = (op.after ?? {}) as { instances?: number; memoryMB?: number; role?: string };
  if (kind === "app" && op.action === "update" && op.fields?.length === 1 && op.fields[0] === "instances") {
    const from = before.instances ?? 1;
    const to = after.instances ?? 1;
    const per = after.memoryMB ?? apps[name]?.memoryMB ?? 512;
    const up = to > from;
    const worker = after.role === "worker";
    const more = to - from;
    return {
      title: `Scale ${name} from ${from} to ${to} ${to === 1 ? "instance" : "instances"}`,
      detail: worker
        ? up
          ? `Starts ${words(more)} more ${more === 1 ? "instance" : "instances"} of ${name}; ${more === 1 ? "it takes" : "they take"} jobs from the same queues. Jobs already running finish where they are.`
          : `Stops ${words(from - to)} ${from - to === 1 ? "instance" : "instances"} of ${name} once ${from - to === 1 ? "its jobs finish" : "their jobs finish"}.`
        : up
        ? `Starts ${words(to - from)} more ${to - from === 1 ? "instance" : "instances"} of ${name}, waits for ${to - from === 1 ? "its" : "their"} health check, then adds ${to - from === 1 ? "it" : "them"} to the edge. The running ${from === 1 ? "instance keeps" : "instances keep"} serving.`
        : `Stops ${words(from - to)} ${from - to === 1 ? "instance" : "instances"} of ${name} once ${from - to === 1 ? "it finishes its" : "they finish their"} requests.`,
      facts: [
        <span key="m">
          Memory <b className="font-[550] text-ink-2">{signed((to - from) * per)}&#8239;MB</b> at most
        </span>,
        <span key="d">Downtime none</span>,
      ],
    };
  }
  const service = kind === "service" ? (serviceNames[name] ?? name) : undefined;
  const reason = op.reason ? op.reason.charAt(0).toUpperCase() + op.reason.slice(1) + "." : undefined;
  if (service && op.action === "delete") return { title: `Remove ${service} from ${project}`, detail: reason, facts: [] };
  if (service && op.action === "create") return { title: `Add ${service} to ${project}`, detail: reason, facts: [] };
  const verb = op.action === "create" ? "Add" : op.action === "delete" ? "Remove" : "Change";
  return { title: `${verb} ${name ? `${kind} ${name}` : kind}`, detail: reason, facts: op.fields?.length ? [<span key="f">Changes {op.fields.join(", ")}</span>] : [] };
}

function Step({ n, op, project, apps }: { n: number; op: Op; project: string; apps: Record<string, ManifestApp> }) {
  const t = opTitle(op, project, apps);
  const tier = asTier(op.risk);
  return (
    <li className="grid grid-cols-[26px_minmax(0,1fr)_auto] gap-x-3 gap-y-1 border-t border-rule py-3.5 first:border-t-0">
      <span className="ident pt-0.5 text-[0.6875rem] leading-[1.375rem] text-ink-3">{String(n).padStart(2, "0")}</span>
      <h4 className="text-[0.9375rem] leading-[1.375rem] font-[550] tracking-[-0.01em] text-ink">{t.title}</h4>
      <RiskDots tier={tier} label={tier === "irreversible" ? "Irreversible" : tier === "outbound" ? "Outbound" : "Low"} className="self-center" />
      {t.detail && <p className="col-start-2 col-end-4 max-w-[60ch] text-sm text-ink-2">{t.detail}</p>}
      {t.facts.length > 0 && <div className="col-start-2 col-end-4 mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">{t.facts}</div>}
    </li>
  );
}

function RoomBlock({ room, unavailable }: { room: Room; unavailable: boolean }) {
  if (!room) {
    return <p className="pt-1 pb-4 text-sm text-ink-3">{unavailable ? "Room left is measured on a running box." : "Measuring…"}</p>;
  }
  const after = Math.max(0, room.after);
  const fits = Math.max(0, Math.floor((after - RESERVE_MB) / room.perMB));
  return (
    <div className="pb-4">
      <div className="mt-1 mb-2.5 flex items-baseline gap-2.5">
        {room.delta !== 0 && <span className="text-[0.9375rem] text-ink-3 tnum line-through decoration-ink-4">{mb(room.before * 1048576)}</span>}
        <Qty value={mb(after * 1048576)} unit="MB" className="reading text-[1.875rem] leading-8" />
        {room.delta !== 0 && (
          <span className="text-sm text-brass-ink tnum">
            {room.delta > 0 ? MINUS : "+"}
            {int(Math.abs(room.delta))}&#8239;MB
          </span>
        )}
      </div>
      <SegMeter
        label="Memory in use after this change"
        value={room.usedMB}
        add={room.delta}
        max={room.totalMB}
        segments={40}
        valueText={`${int(room.usedMB + room.delta)} of ${int(room.totalMB)} MB`}
      />
      <p className="mt-2.5 text-sm text-ink-2">
        {room.perApp
          ? `Still room for about ${words(fits)} more ${fits === 1 ? "instance" : "instances"} the size of ${room.perApp}, keeping ${int(RESERVE_MB)} MB spare.`
          : `Memory doesn’t change.`}
      </p>
    </div>
  );
}

function Impact({ edits, ops, project, apps }: { edits: StagedEdit[]; ops: Op[]; project: string; apps: Record<string, ManifestApp> }) {
  const onlyScale = edits.every((e) => e.kind === "instances");
  const lost = ops.filter((o) => asTier(o.risk) === "irreversible");
  const scaled = edits.filter((e): e is Extract<StagedEdit, { kind: "instances" }> => e.kind === "instances");
  return (
    <div className="text-sm text-ink-2 [&>p]:border-t [&>p]:border-rule [&>p]:py-3.5">
      {onlyScale && (
        <p>
          <b className="font-[550] text-ink">Downtime: none.</b>{" "}
          {scaled.map((e) => `${e.app} keeps ${apps[e.app]?.role === "worker" ? "working" : "serving"} from ${e.from} ${e.from === 1 ? "instance" : "instances"} while the change rolls out.`).join(" ")}
        </p>
      )}
      <p>
        <b className="font-[550] text-ink">If you undo</b>, {edits.map((e) => undoWords(e)).join("; ")}.
      </p>
      {lost.length > 0 && (
        <p className="!border-t-danger-rule text-danger">
          <b className="font-[550]">What undo can’t restore:</b> {lost.map((o) => o.reason).join("; ")}. Undo brings the settings back to {project}, not the data.
        </p>
      )}
      <p>You’ll get a signed entry in the Ledger{lost.length === 0 ? " with an Undo button" : ""}.</p>
    </div>
  );
}

function ConfigDiff({ project, before, after, error }: { project: string; before?: string; after?: string; error: unknown }) {
  const lines = useMemo(() => (before !== undefined && after !== undefined ? diffLines(stripNote(before), after) : null), [before, after]);
  const c = lines ? diffCounts(lines) : null;
  return (
    <section className="border-t border-rule px-5 pt-4 pb-6 sm:px-7" aria-label="tiffin.config.ts">
      <h3 className="label pb-2.5">tiffin.config.ts</h3>
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

function TraySkeleton() {
  return (
    <div className="grid gap-3 px-5 py-6 sm:px-7" aria-label="Planning" role="status">
      <div className="h-3 w-40 rounded bg-paper-sunk" />
      <div className="h-5 w-80 max-w-full rounded bg-paper-sunk" />
      <div className="h-4 w-[28rem] max-w-full rounded bg-paper-sunk opacity-70" />
      <div className="mt-4 h-28 rounded-[10px] bg-paper-sunk opacity-60" />
    </div>
  );
}
