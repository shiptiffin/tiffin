import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { mod, mod2, mod3, mq } from "@/api/modules";
import { bytes, count, int, plainWords } from "@/lib/format";
import { liveSince, relative } from "@/lib/time";
import type { PilotState } from "./pilot";

// One status sentence per part of the box, from real data only. While the
// data loads the sentence is empty (rows keep their height); if a module
// can't answer, the row says so plainly instead of inventing a number.

const quiet = { retry: false, refetchOnWindowFocus: false, staleTime: 15_000 } as const;

export type AppStatus = { sentence: ReactNode; pilot?: PilotState; fault?: boolean };

/** "41 requests in the last hour. Went live 13 min ago." / "Building from upload, 41 s so far." */
export function useAppStatus(project: string, app: string, role: string | undefined, framework: string | undefined): AppStatus {
  const deploys = useQuery({ queryKey: ["deploys", project, app, ""], queryFn: () => mod3.deploys(project, app), ...quiet, refetchInterval: 10_000 });
  const metrics = useQuery({ queryKey: ["observe-apps", project], queryFn: () => mod.apps(project, "1h"), ...quiet, refetchInterval: 30_000 });
  const list = deploys.data ?? [];
  const prod = list.filter((d) => !d.preview);
  const latest = prod[0];
  const live = prod.find((d) => d.status === "live");
  const m = (metrics.data ?? []).find((x) => x.app === app);
  if (deploys.isError) return { sentence: <span className="text-ink-3">Deploys can’t be read here.</span> };
  if (!deploys.data) return { sentence: null };
  if (latest && ["queued", "building", "starting"].includes(latest.status)) {
    return {
      pilot: "busy",
      sentence: (
        <>
          <span className="font-[550] text-brass-ink">{latest.status === "starting" ? "Starting" : "Building"}</span> a new version, started{" "}
          {relative(latest.createdAt)}.
        </>
      ),
    };
  }
  if (latest?.status === "failed") {
    const why = (latest.error ?? "")
      .split("\n")[0]
      .replace(/^error:\s*/i, "")
      .replace(/\btf\.[\w.-]+\s*/g, "")
      .split(/\.\s/)[0]
      .replace(/\.$/, "");
    return {
      pilot: "fault",
      fault: !live,
      sentence: (
        <>
          <span className="text-danger">The last deploy failed{why ? `: ${why}` : ""}.</span>
          {live ? ` The previous version is still serving.` : ""}
        </>
      ),
    };
  }
  if (!live) return { sentence: <span className="text-ink-3">Not deployed yet. `tiffin deploy` ships it.</span> };
  const went = `Went live ${relative(liveSince(live))}.`;
  if (framework === "static") return { sentence: `Served by the edge. ${went}` };
  if (role === "worker") return { sentence: `Runs jobs in the background. ${went}` };
  if (m && m.requests > 0) {
    const perMin = m.rps * 60;
    const traffic = perMin >= 1 ? `${int(perMin)} ${Math.round(perMin) === 1 ? "request" : "requests"} a minute.` : `${count(m.requests, "request")} in the last hour.`;
    const errs = m.errors > 0 ? <span className="text-danger"> {count(m.errors, "error")}.</span> : null;
    return {
      sentence: (
        <>
          {traffic}
          {errs} {went}
        </>
      ),
    };
  }
  return { sentence: `No requests in the last hour. ${went}` };
}

export function usePostgresStatus(project: string): ReactNode {
  const pg = useQuery({ queryKey: ["pg", project], queryFn: () => mod.pg(project), ...quiet });
  const backups = useQuery({ ...mq.backups, ...quiet, refetchInterval: 60_000 });
  if (pg.isError) return <span className="text-ink-3">Postgres isn’t answering.</span>;
  if (!pg.data) return null;
  const last = backups.data?.lastOkAt;
  return (
    <>
      {bytes(pg.data.sizeBytes)} on disk, {count(pg.data.connections, "connection")}.{" "}
      {last ? `Backed up ${relative(last)}.` : <span className="text-warn-ink">Not backed up yet.</span>}
    </>
  );
}

export function useValkeyStatus(project: string): ReactNode {
  const kv = useQuery({ queryKey: ["kv-stats", project], queryFn: () => mod.kvStats(project), ...quiet });
  if (kv.isError) return <span className="text-ink-3">Valkey isn’t answering.</span>;
  if (!kv.data) return null;
  const k = kv.data;
  return (
    <>
      {count(k.keys, "key")} in {bytes(k.memoryBytes)}
      {k.maxMemoryMB ? `, capped at ${int(k.maxMemoryMB)}\u202FMB` : ""}.{k.overCap && <span className="text-danger"> Over its cap.</span>}
    </>
  );
}

export function useStorageStatus(project: string): { sentence: ReactNode; buckets?: number } {
  const st = useQuery({ ...mq.storage(project), ...quiet });
  if (st.isError) return { sentence: <span className="text-ink-3">Storage isn’t answering.</span> };
  if (!st.data) return { sentence: null };
  const b = st.data.buckets ?? [];
  const files = b.reduce((s, x) => s + (x.objects ?? 0), 0);
  return { sentence: `${bytes(st.data.usedBytes)} in ${count(files, "file")}.`, buckets: b.length };
}

export function useEmailStatus(project: string): { sentence: ReactNode; mode?: string } {
  const status = useQuery({ ...mq.emailStatus, ...quiet });
  const msgs = useQuery({ queryKey: ["messages", project, ""], queryFn: () => mod.messages(project, undefined, true), ...quiet });
  if (status.isError) return { sentence: <span className="text-ink-3">Email isn’t answering.</span> };
  if (!status.data || !msgs.data) return { sentence: null };
  const n = msgs.data.length;
  const caught = n >= 200 ? "200+" : int(n);
  if (status.data.mode === "inbox") return { sentence: `Catching mail locally. ${caught} caught, none sent.`, mode: "Dev inbox" };
  const failed = status.data.failedLastDay;
  return {
    sentence: (
      <>
        Sending through the relay. {count(n, "message")} recently.{failed > 0 && <span className="text-danger"> {int(failed)} failed today.</span>}
      </>
    ),
    mode: "Relay",
  };
}

export function useAuthStatus(project: string): ReactNode {
  const a = useQuery({ queryKey: ["auth", project], queryFn: () => mod3.auth(project), ...quiet });
  if (a.isError) return <span className="text-ink-3">Sign-in isn’t answering.</span>;
  if (!a.data) return null;
  const s = a.data.stats;
  if (s.users === 0) return "No one has signed up yet.";
  return `${count(s.users, "person", "people")}. ${s.signups7d > 0 ? `${int(s.signups7d)} signed up this week.` : "No new sign-ups this week."}`;
}

export function useAnalyticsStatus(project: string): ReactNode {
  const a = useQuery({ queryKey: ["analytics", project, "24h", "box"], queryFn: () => mod2.analytics(project, "24h"), ...quiet });
  if (a.isError) return <span className="text-ink-3">Analytics isn’t answering.</span>;
  if (!a.data) return null;
  const t = a.data.totals;
  if (t.pageviews === 0) return "No visits in the last 24 hours.";
  return `${count(t.pageviews, "page view")} from ${count(t.visitors, "visitor")} in the last 24 hours.`;
}

/** A box check's detail in plain words, trimmed of version noise. */
export function checkWords(detail: string | undefined): string {
  if (!detail) return "";
  const d = plainWords(detail)
    .replace(/\s*\([^)]*\)/g, "")
    .replace(/ on 127\.0\.0\.1:\d+/g, "")
    .replace(/\s+/g, " ")
    .trim();
  return d.charAt(0).toUpperCase() + d.slice(1) + (/[.!?]$/.test(d) ? "" : ".");
}
