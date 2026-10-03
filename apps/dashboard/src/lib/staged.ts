import { useSyncExternalStore } from "react";
import type { Manifest } from "@/api/client";

/**
 * Staged changes: what levers have been moved but not applied. Nothing on
 * the dashboard applies a lever directly; it stages here, per project, and
 * the plan tray turns the staged edits into one plan (GET manifest → apply
 * edits → POST /v1/plan → POST /v1/apply with the hash).
 *
 * Kept in sessionStorage, so a reload keeps your staged changes ("esc keeps
 * it staged") but a new tab starts clean.
 *
 *   stage("shop", { kind: "instances", app: "web", from: 2, to: 3 })
 *   const edits = useStaged("shop")
 *   const next = applyEdits(manifest, edits)
 *
 * To add a new kind of lever: add a variant to StagedEdit, teach applyEdits,
 * describe and undoWords about it. Everything else (tray, counts, Review
 * button) follows.
 */
export type StagedEdit =
  | { kind: "instances"; app: string; from: number; to: number }
  | { kind: "service"; service: string; from: "on" | "off"; to: "on" | "off" };

type Store = { edits: Record<string, StagedEdit[]>; tray: string | null };

const KEY = "tiffin.staged";
let state: Store = load();
const subs = new Set<() => void>();

function load(): Store {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (raw) return { edits: JSON.parse(raw) as Record<string, StagedEdit[]>, tray: null };
  } catch {
    /* storage blocked or garbled: start clean */
  }
  return { edits: {}, tray: null };
}

function set(next: Store) {
  state = next;
  try {
    sessionStorage.setItem(KEY, JSON.stringify(state.edits));
  } catch {
    /* storage blocked: staged edits live for this page only */
  }
  subs.forEach((f) => f());
}

export const editKey = (e: StagedEdit) => (e.kind === "instances" ? `instances:${e.app}` : `service:${e.service}`);

/** Stages an edit, replacing an earlier one on the same lever. Moving a lever back to where it is unstages it. */
export function stage(project: string, e: StagedEdit) {
  const list = (state.edits[project] ?? []).filter((x) => editKey(x) !== editKey(e));
  const merged = { ...e };
  const prev = (state.edits[project] ?? []).find((x) => editKey(x) === editKey(e));
  if (prev) merged.from = prev.from as never; // keep the live value as "from"
  const next = merged.from === merged.to ? list : [...list, merged];
  const edits = { ...state.edits };
  if (next.length) edits[project] = next;
  else delete edits[project];
  set({ ...state, edits });
}

export function unstage(project: string, key: string) {
  const list = (state.edits[project] ?? []).filter((x) => editKey(x) !== key);
  const edits = { ...state.edits };
  if (list.length) edits[project] = list;
  else delete edits[project];
  set({ ...state, edits });
}

export function discard(project: string) {
  const edits = { ...state.edits };
  delete edits[project];
  set({ edits, tray: state.tray === project ? null : state.tray });
}

export function openTray(project: string) {
  set({ ...state, tray: project });
}

export function closeTray() {
  set({ ...state, tray: null });
}

const subscribe = (f: () => void) => {
  subs.add(f);
  return () => subs.delete(f);
};

const none: StagedEdit[] = [];
export function useStaged(project: string | undefined): StagedEdit[] {
  return useSyncExternalStore(subscribe, () => (project ? (state.edits[project] ?? none) : none));
}

export function useAllStaged(): Record<string, StagedEdit[]> {
  return useSyncExternalStore(subscribe, () => state.edits);
}

export function useTray(): string | null {
  return useSyncExternalStore(subscribe, () => state.tray);
}

/** The staged edit on one lever, if any. */
export function stagedFor(edits: StagedEdit[], key: string) {
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
    }
  }
  return next;
}

export const serviceNames: Record<string, string> = {
  postgres: "Postgres",
  valkey: "Valkey",
  storage: "Storage",
  email: "Email",
  auth: "Sign-in",
  analytics: "Analytics",
  queue: "Queues",
};

/** "Scale web from 2 to 3 instances", "Remove Analytics from shop". */
export function describe(e: StagedEdit, project: string): string {
  if (e.kind === "instances") return `Scale ${e.app} from ${e.from} to ${e.to} ${e.to === 1 ? "instance" : "instances"}`;
  const name = serviceNames[e.service] ?? e.service;
  return e.to === "off" ? `Remove ${name} from ${project}` : `Add ${name} to ${project}`;
}

/** What undo would do for this edit, in words. */
export function undoWords(e: StagedEdit): string {
  if (e.kind === "instances") return `${e.app} goes back to ${e.from} ${e.from === 1 ? "instance" : "instances"}`;
  const name = serviceNames[e.service] ?? e.service;
  return e.to === "off" ? `${name} comes back with its settings` : `${name} is removed again`;
}

/** One intent sentence for the change, from its edits. */
export function intentFor(edits: StagedEdit[], project: string): string {
  if (edits.length === 1) return describe(edits[0], project);
  const parts = edits.map((e) => describe(e, project));
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1].charAt(0).toLowerCase()}${parts[parts.length - 1].slice(1)}`;
}
