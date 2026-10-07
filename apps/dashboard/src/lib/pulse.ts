import { queryOptions, useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { mod3, type Deploy } from "@/api/modules";
import { q } from "@/api/queries";
import { toast } from "@/components/toast";
import { liveSince, relative, sinceWhen } from "./time";

/**
 * One plain status line per project and per app, from real data only:
 *   "Live · updated 2 hours ago"   "Last deploy failed · See why"   "Building…"
 *   "Asleep since 14:05 · wakes on the next visit" (with Wake)
 * Shared by the home cards and the project header, through the same query
 * keys the deeper pages use (so moving between them doesn't refetch).
 * When a check fails (500, 403, offline) the line says so, with Retry: missing
 * evidence is never shown as healthy, even if an older answer said it was.
 */

const quiet = { retry: false, refetchOnWindowFocus: false, staleTime: 15_000 } as const;
const inFlight = (list?: Deploy[]) => !!list?.some((d) => ["queued", "building", "starting"].includes(d.status));

/**
 * An app's deploy history: the one query (and key) every view of it shares.
 * Polled every 5s while a deploy is in flight, every 30s otherwise (this
 * dashboard's own deploy actions refresh it at once); `active` false (a home
 * card scrolled out of view) stops the polling, not the first read.
 */
export const deploysQuery = (project: string, app: string, active = true) =>
  queryOptions({
    queryKey: ["deploys", project, app, ""],
    queryFn: () => mod3.deploys(project, app),
    ...quiet,
    refetchInterval: (query) => (!active ? false : inFlight(query.state.data) ? 5_000 : 30_000),
  });
export const runtimeQuery = (project: string, app: string, active = true) =>
  queryOptions({
    queryKey: ["runtime", project, app],
    queryFn: () => mod3.runtime(project, app),
    ...quiet,
    staleTime: 60_000,
    refetchInterval: active ? 60_000 : false, // apps of a project that lets them sleep fall asleep on their own
  });

export type Tone = "ok" | "busy" | "bad" | "quiet" | "unknown";
export type AppPulse = { tone: Tone; words: string; since?: string; failedWhy?: string; servingOld?: boolean };

/** Why a deploy failed, in its first clause. */
export function failWhy(d?: Deploy): string {
  return (d?.error ?? "")
    .split("\n")[0]
    .replace(/^error:\s*/i, "")
    .replace(/\btf\.[\w.-]+\s*/g, "")
    .split(/\.\s/)[0]
    .replace(/\.$/, "");
}

/** An app's state from its production deploys. */
export function appPulse(list: Deploy[] | undefined, role?: string): AppPulse | undefined {
  if (!list) return undefined;
  const prod = list.filter((d) => !d.preview);
  const latest = prod[0];
  const live = prod.find((d) => d.status === "live");
  if (latest && ["queued", "building", "starting"].includes(latest.status)) return { tone: "busy", words: latest.status === "starting" ? "Starting" : "Building" };
  if (latest?.status === "failed") return { tone: "bad", words: "Last deploy failed", failedWhy: failWhy(latest), servingOld: !!live };
  if (!live) return { tone: "quiet", words: "Not live yet" };
  return { tone: "ok", words: role === "worker" ? "Working" : "Live", since: liveSince(live) };
}

export type ProjectPulse = {
  tone: Tone;
  /** The plain line: "Live · updated 2 hours ago", "Building…", "Last deploy failed". */
  words: string;
  /** Where "see why" goes, when something is wrong. */
  why?: { app: string };
  /** The project's public address, if it has one live. */
  url?: string;
  apps: string[];
  services: string[];
  loading: boolean;
  /** Asks again, when a check failed (tone "unknown"). */
  retry?: () => void;
  /** Starts its sleeping apps, when every app it has live is asleep. */
  wake?: () => void;
  waking: boolean;
};

/**
 * A project's one-line state and live link. `active` false (a home card out
 * of view) keeps what's cached but stops polling until it's back in view.
 */
export function useProjectPulse(project: string, active = true): ProjectPulse {
  // The project's own pages poll its state every 5s; a status line needs less.
  const p = useQuery({ ...q.project(project), refetchInterval: active ? 15_000 : false });
  const res = p.data?.resources ?? [];
  const apps = res.filter((r) => r.address.startsWith("app/")).map((r) => ({ name: r.address.slice(4), spec: r.spec as { role?: string; framework?: string } }));
  const services = res.filter((r) => r.address.startsWith("service/")).map((r) => r.address.slice(8));
  const deploys = useQueries({ queries: apps.map((a) => deploysQuery(project, a.name, active)) });
  const rts = useQueries({ queries: apps.map((a) => runtimeQuery(project, a.name, active)) });
  const qc = useQueryClient();
  const wake = useMutation({
    mutationFn: () => mod3.wake(project),
    onSuccess: (r) => {
      const failed = r.find((x) => x.error);
      if (failed) toast({ title: `${failed.app} couldn’t start.`, detail: failed.error });
    },
    onError: (e) => toast({ title: `${project} couldn’t wake.`, detail: e instanceof Error ? e.message : undefined }),
    onSettled: () => Promise.all(apps.map((a) => qc.invalidateQueries({ queryKey: ["runtime", project, a.name] }))),
  });
  // The app at the project's own name first (shop.<domain> over shop-docs), then
  // one at web, www or app, else the first that's live.
  const urls = rts
    .filter((_, i) => apps[i].spec?.role !== "worker")
    .map((r) => r.data?.production?.url)
    .filter((u): u is string => !!u);
  const url = urls.find((u) => u.includes(`//${project}.`)) ?? urls.find((u) => /\/\/(web|www|app)\./.test(u)) ?? urls[0];

  // An app whose last deploy failed with none live reads failed too: its deploys say that more exactly, below.
  const failedRes = Object.values(p.data?.status ?? {}).filter((s) => s.state === "failed" && s.release !== "failed");
  const pulses = apps.map((a, i) => ({ app: a.name, pulse: appPulse(deploys[i]?.data, a.spec?.role) }));
  const loading = p.isPending || deploys.some((d) => d.isPending);
  const failedChecks = [p, ...deploys].filter((x) => x.isError);

  // Asleep: every app it has live sleeps (a project that lets its apps sleep, unused for a while).
  const live = rts.map((r, i) => ({ env: r.data?.production, web: apps[i].spec?.role !== "worker" })).filter((x) => x.env?.live);
  const asleep =
    live.length > 0 && live.every((x) => x.env?.sleeping)
      ? { since: live.map((x) => x.env?.sleepingSince ?? "").sort().pop() || undefined, web: live.some((x) => x.web) }
      : undefined;

  let tone: Tone = "ok";
  let canWake = false;
  let words: string;
  let why: { app: string } | undefined;
  const bad = pulses.find((x) => x.pulse?.tone === "bad");
  const busy = pulses.find((x) => x.pulse?.tone === "busy");
  if (failedRes.length > 0) {
    const name = failedRes[0].address.split("/")[1] ?? failedRes[0].address;
    tone = "bad";
    words = `${name} is down`;
    if (failedRes[0].address.startsWith("app/")) why = { app: name };
  } else if (bad) {
    tone = "bad";
    words = apps.length > 1 ? `${bad.app}’s last deploy failed` : "Last deploy failed";
    why = { app: bad.app };
  } else if (failedChecks.length > 0) {
    // An older answer may still be cached; say what it was, but not as health.
    tone = "unknown";
    words = "Couldn’t check its status";
    const seen = Math.min(...failedChecks.map((x) => x.dataUpdatedAt));
    if (apps.length > 0 && failedChecks.every((x) => x.data) && pulses.every((x) => x.pulse?.tone === "ok") && seen > 0) words += ` · last seen live ${relative(new Date(seen).toISOString())}`;
  } else if (busy) {
    tone = "busy";
    words = `${busy.pulse?.words}…`;
  } else if (apps.length === 0 && services.length === 0) {
    tone = "quiet";
    words = "Empty so far";
  } else if (apps.length > 0 && pulses.every((x) => x.pulse?.tone === "quiet")) {
    tone = "quiet";
    words = "Not live yet";
  } else if (apps.length === 0) {
    words = "Ready, no app yet";
  } else if (wake.isPending) {
    tone = "busy";
    words = "Waking up…";
  } else if (asleep) {
    tone = "quiet";
    canWake = true;
    words = asleep.since ? `Asleep since ${sinceWhen(asleep.since)}` : "Asleep";
    words += asleep.web ? " · wakes on the next visit" : " · wakes on its next job";
  } else {
    words = "Live";
    const since = pulses.map((x) => x.pulse?.since).filter(Boolean).sort().pop();
    if (since) words += ` · updated ${relative(since)}`;
  }
  const retry = tone === "unknown" ? () => failedChecks.forEach((x) => void x.refetch()) : undefined;
  return { tone, words, why, url, apps: apps.map((a) => a.name), services, loading, retry, wake: canWake ? () => wake.mutate() : undefined, waking: wake.isPending };
}

export const toneClass: Record<Tone, string> = {
  ok: "bg-ok",
  busy: "bg-brass",
  bad: "bg-danger",
  quiet: "bg-ink-4",
  unknown: "bg-warn",
};
