import { useSyncExternalStore } from "react";
import { api, ApiError, type Manifest, type Plan } from "@/api/client";
import { q, queryClient } from "@/api/queries";
import { toast } from "@/components/toast";
import { asTier, tierRank } from "./changes";
import { partA, partName } from "./names";

/**
 * Changes happen when you click. A toggle, a stepper, Add or a limit calls
 *
 *   change("shop", { kind: "service", service: "postgres", from: "off", to: "on" })
 *
 * and this runs the same path an agent does, in one go: read the manifest,
 * apply the edit, POST /v1/plan, then POST /v1/apply with that plan's hash
 * and a written-for-you intent. History and Undo work as always; a quiet
 * toast says what happened ("Added a database to shop · Undo").
 *
 * Rapid clicks on one control (+, +, +) are batched: edits wait 500 ms for
 * the next click on the same project, then go as one change. While a change
 * is on its way, usePending(project) returns its edits so the control can
 * show the new value with a spinner ("Adding a database…").
 *
 * Only two kinds of plan stop to ask: one that can't be undone (it deletes
 * data) and one that reaches outside the box. Those open the confirm dialog
 * (components/plan-tray.tsx), which says exactly what's lost; Confirm applies
 * that same plan's hash. If a plan comes back riskier than the control
 * expected, it is the dialog that decides, never the click.
 */
export type StagedEdit =
  | { kind: "instances"; app: string; from: number; to: number }
  | { kind: "service"; service: string; from: "on" | "off"; to: "on" | "off" }
  /** A bucket's access (public/private), or "absent": adding a trashed bucket back restores it with its files. */
  | { kind: "bucket"; bucket: string; from: BucketAccess; to: BucketAccess }
  /**
   * Any other manifest edit (an app's memory, a new app, queue, schedule, env var, a limit): sets the value at
   * `path` (removes it when `to` is undefined). `what` and `undo` are the words the toast and History show.
   */
  | { kind: "set"; path: string[]; from?: unknown; to?: unknown; what: string; undo: string };

export type BucketAccess = "public" | "private" | "absent";

/** A plan that needs a person's yes before it applies. */
export type ConfirmRequest = { project: string; edits: StagedEdit[]; desired: Manifest; plan: Plan };

type Store = {
  queued: Record<string, StagedEdit[]>;
  inflight: Record<string, StagedEdit[]>;
  confirm: ConfirmRequest | null;
  /** inflight + queued per project, recomputed on every change (stable between them). */
  pending?: Record<string, StagedEdit[]>;
};

let state: Store = { queued: {}, inflight: {}, confirm: null, pending: {} };
const subs = new Set<() => void>();
const timers = new Map<string, ReturnType<typeof setTimeout>>();
const DEBOUNCE_MS = 500;
const none: StagedEdit[] = [];

function set(next: Store) {
  const pending: Record<string, StagedEdit[]> = {};
  for (const p of new Set([...Object.keys(next.inflight), ...Object.keys(next.queued)])) {
    const list = (next.queued[p] ?? []).reduce(merge, next.inflight[p] ?? []);
    pending[p] = list.length === 0 ? none : JSON.stringify(list) === JSON.stringify(state.pending?.[p]) ? state.pending![p] : list;
  }
  state = { ...next, pending };
  subs.forEach((f) => f());
}

export const editKey = (e: StagedEdit) =>
  e.kind === "instances"
    ? `instances:${e.app}`
    : e.kind === "bucket"
      ? `bucket:${e.bucket}`
      : e.kind === "set"
        ? `set:${e.path.join("/")}`
        : `service:${e.service}`;

const merge = (list: StagedEdit[], e: StagedEdit): StagedEdit[] => {
  const prev = list.find((x) => editKey(x) === editKey(e));
  const merged = { ...e };
  if (prev) merged.from = prev.from as never; // keep the live value as "from"
  const rest = list.filter((x) => editKey(x) !== editKey(e));
  return JSON.stringify(merged.from) === JSON.stringify(merged.to) ? rest : [...rest, merged];
};

/**
 * Makes a change now (after a short pause that batches rapid clicks on the same project).
 * `immediate` skips the pause, for one-off actions such as Add.
 */
export function change(project: string, e: StagedEdit, opts: { immediate?: boolean } = {}) {
  const queued = merge(state.queued[project] ?? [], e);
  const q2 = { ...state.queued };
  if (queued.length) q2[project] = queued;
  else delete q2[project];
  set({ ...state, queued: q2 });
  clearTimeout(timers.get(project));
  if (!queued.length) return;
  timers.set(
    project,
    setTimeout(() => void flush(project), opts.immediate ? 0 : DEBOUNCE_MS),
  );
}

/** Several edits as one change (Add an app that needs a database). */
export function changeMany(project: string, edits: StagedEdit[]) {
  for (const e of edits) change(project, e, { immediate: true });
}

const invalidate = async (project: string) => {
  await Promise.all(
    [["project", project], ["manifest", project], ["changes"], ["box-resources"], ["projects"], ["usage", project]].map((k) => queryClient.invalidateQueries({ queryKey: k })),
  );
};

function clearInflight(project: string) {
  const inflight = { ...state.inflight };
  delete inflight[project];
  set({ ...state, inflight });
}

async function flush(project: string, retried = false) {
  const edits = state.queued[project] ?? [];
  if (!edits.length) return;
  const queued = { ...state.queued };
  delete queued[project];
  set({ ...state, queued, inflight: { ...state.inflight, [project]: [...(state.inflight[project] ?? []), ...edits] } });
  try {
    const man = await queryClient.fetchQuery({ ...q.manifest(project), staleTime: 0 });
    const desired = applyEdits(man.manifest, edits);
    const plan = await api.plan(desired);
    if (!plan.ops?.length) {
      clearInflight(project);
      return;
    }
    if (tierRank[asTier(plan.risk)] > tierRank.reversible) {
      // It deletes data or reaches outside the box: the dialog asks first. The control keeps showing the edit until then.
      set({ ...state, confirm: { project, edits, desired, plan } });
      return;
    }
    await applyPlan(project, edits, desired, plan);
  } catch (err) {
    if (!retried && err instanceof ApiError && err.status === 428) {
      // Someone (or an agent) changed the project in between: plan again once.
      set({ ...state, queued: { ...state.queued, [project]: [...edits, ...(state.queued[project] ?? [])] } });
      clearInflight(project);
      return flush(project, true);
    }
    clearInflight(project);
    toast({ title: `Couldn’t ${lower(intentFor(edits, project))}.`, detail: err instanceof ApiError ? (err.problem.detail ?? err.message) : String(err), tone: "danger" });
  }
}

/** Applies a plan the person has seen (or one that didn't need asking), then says so with Undo. */
export async function applyPlan(project: string, edits: StagedEdit[], desired: Manifest, plan: Plan) {
  try {
    const r = await api.apply(desired, plan.hash, intentFor(edits, project));
    await invalidate(project);
    const id = r.change?.id;
    toast({
      title: r.applied ? doneWords(edits, project) : "Nothing to change: it already looks like that.",
      action: id && r.change && asTier(r.change.plan.risk) !== "irreversible" ? { label: "Undo", run: () => undoChange(id) } : undefined,
    });
  } finally {
    clearInflight(project);
    if (state.confirm?.plan.hash === plan.hash) set({ ...state, confirm: null });
  }
}

/** The person said no in the confirm dialog: nothing happens, the control goes back. */
export function cancelConfirm() {
  const c = state.confirm;
  set({ ...state, confirm: null });
  if (c) clearInflight(c.project);
}

let go: ((to: string) => void) | undefined;
/** The Shell hands over navigation, so an undo that needs a look can open its change. */
export function setNavigator(f: (to: string) => void) {
  go = f;
}

/** Undo from a toast or History: plan the undo; apply it at once when it can be undone again, otherwise open the change. */
export async function undoChange(id: string) {
  try {
    await api.undo(id);
  } catch (e) {
    const plan = e instanceof ApiError && e.status === 428 ? (e.problem.plan as Plan | undefined) : undefined;
    if (!plan) {
      toast({ title: "Couldn’t undo that.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" });
      return;
    }
    if (tierRank[asTier(plan.risk)] > tierRank.outbound) {
      if (go) go(`/changes/${encodeURIComponent(id)}`);
      return;
    }
    try {
      await api.undo(id, plan.hash);
      toast({ title: "Undone. Everything is back as it was." });
    } catch (e2) {
      toast({ title: "Couldn’t undo that.", detail: e2 instanceof ApiError ? (e2.problem.detail ?? e2.message) : String(e2), tone: "danger" });
    }
  }
  for (const k of [["changes"], ["box-resources"], ["projects"]]) void queryClient.invalidateQueries({ queryKey: k });
  void queryClient.invalidateQueries({ predicate: (qq) => ["project", "manifest", "usage", "secrets"].includes(String(qq.queryKey[0])) });
}

const subscribe = (f: () => void) => {
  subs.add(f);
  return () => subs.delete(f);
};

/** Edits on their way for a project (queued or applying): show them as the new value, with a spinner. */
export function usePending(project: string | undefined): StagedEdit[] {
  return useSyncExternalStore(subscribe, () => (project ? (state.pending?.[project] ?? none) : none));
}

/** The plan waiting for a person's yes, if any. */
export function useConfirmRequest(): ConfirmRequest | null {
  return useSyncExternalStore(subscribe, () => state.confirm);
}

/** The pending edit on one control, if any. */
export function pendingFor(edits: StagedEdit[], key: string) {
  return edits.find((e) => editKey(e) === key);
}

/** The manifest with the edits applied. Pure: the input is not changed. */
export function applyEdits(m: Manifest, edits: StagedEdit[]): Manifest {
  const next = structuredClone(m) as Manifest & { apps?: Record<string, { instances?: number }>; services?: Record<string, unknown> };
  for (const e of edits) {
    if (e.kind === "instances") {
      const app = next.apps?.[e.app];
      if (app) app.instances = e.to;
    } else if (e.kind === "service") {
      const services = (next.services ?? {}) as Record<string, unknown>;
      if (e.to === "off") delete services[e.service];
      else if (!(e.service in services)) services[e.service] = {};
      next.services = services as Manifest["services"];
    } else if (e.kind === "bucket") {
      const services = (next.services ?? {}) as Record<string, { buckets?: Record<string, { public: boolean }> } | undefined>;
      const storage = (services.storage ??= {});
      const buckets = (storage.buckets ??= {});
      if (e.to === "absent") delete buckets[e.bucket];
      else buckets[e.bucket] = { ...buckets[e.bucket], public: e.to === "public" };
      next.services = services as Manifest["services"];
    } else if (e.kind === "set") {
      let at = next as unknown as Record<string, unknown>;
      for (const k of e.path.slice(0, -1)) {
        if (typeof at[k] !== "object" || at[k] === null) at[k] = {};
        at = at[k] as Record<string, unknown>;
      }
      const last = e.path[e.path.length - 1];
      if (e.to === undefined) delete at[last];
      else at[last] = structuredClone(e.to);
    }
  }
  return next;
}

/** The services by their names in the interface (lib/names.ts): "Database", "Cache", "Files"… */
export const serviceNames: Record<string, string> = Object.fromEntries(["postgres", "valkey", "storage", "email", "auth", "analytics", "queue"].map((k) => [k, partName(k)]));

/** The services as a person says them in a sentence: "Add a database to shop". */
export const serviceWords: Record<string, string> = Object.fromEntries(["postgres", "valkey", "storage", "email", "auth", "analytics", "queue"].map((k) => [k, partA(k)]));

/** "Scale web to 3 copies", "Remove the database from shop". */
export function describe(e: StagedEdit, project: string): string {
  if (e.kind === "instances") return `Run ${e.app} on ${e.to} ${e.to === 1 ? "copy" : "copies"} instead of ${e.from}`;
  if (e.kind === "set") return e.what;
  if (e.kind === "bucket") {
    if (e.to === "absent") return `Remove the ${e.bucket} bucket from ${project}`;
    if (e.from === "absent") return `Restore the ${e.bucket} bucket from the trash`;
    return `Make the ${e.bucket} bucket ${e.to}`;
  }
  const name = serviceWords[e.service] ?? e.service;
  return e.to === "off" ? `Remove ${name.replace(/^a /, "the ")} from ${project}` : `Add ${name} to ${project}`;
}

/** What undo would do for this edit, in words. */
export function undoWords(e: StagedEdit): string {
  if (e.kind === "instances") return `${e.app} goes back to ${e.from} ${e.from === 1 ? "copy" : "copies"}`;
  if (e.kind === "set") return e.undo;
  if (e.kind === "bucket") return e.from === "absent" ? `${e.bucket} goes back to the trash` : `${e.bucket} is ${e.from} again`;
  const name = serviceWords[e.service] ?? e.service;
  return e.to === "off" ? `${name.replace(/^a /, "the ")} comes back with its settings` : `${name.replace(/^a /, "the ")} is removed again`;
}

/** "Adding a database…": what a control says while its change is on the way. */
export function progressWords(e: StagedEdit, project: string): string {
  const d = describe(e, project);
  return `${ing(d)}…`;
}

/** One intent sentence for the change, from its edits. */
export function intentFor(edits: StagedEdit[], project: string): string {
  if (edits.length === 1) return describe(edits[0], project);
  const parts = edits.map((e) => describe(e, project));
  return `${parts.slice(0, -1).join(", ")} and ${lower(parts[parts.length - 1])}`;
}

const lower = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);

const PAST: Record<string, string> = {
  Add: "Added",
  Remove: "Removed",
  Run: "Running",
  Scale: "Scaled",
  Make: "Made",
  Restore: "Restored",
  Set: "Set",
  Give: "Gave",
  Let: "Let",
  Limit: "Limited",
  Change: "Changed",
  Declare: "Declared",
};
const ING: Record<string, string> = {
  Add: "Adding",
  Remove: "Removing",
  Run: "Moving",
  Scale: "Scaling",
  Make: "Making",
  Restore: "Restoring",
  Set: "Setting",
  Give: "Giving",
  Let: "Letting",
  Limit: "Limiting",
  Change: "Changing",
  Declare: "Declaring",
};
const swapVerb = (s: string, map: Record<string, string>) => s.replace(/^(\w+)/, (v) => map[v] ?? v);
const ing = (s: string) => swapVerb(s, ING);

/** "Added a database to shop." */
export function doneWords(edits: StagedEdit[], project: string): string {
  if (edits.length === 1 && edits[0].kind === "instances") return `${edits[0].app} runs on ${edits[0].to} ${edits[0].to === 1 ? "copy" : "copies"} now.`;
  return `${swapVerb(intentFor(edits, project), PAST)}.`;
}
