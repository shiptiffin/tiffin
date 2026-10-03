import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Eye, KeyRound, Search, X } from "lucide-react";
import { useEffect, useState } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type KVValue } from "@/api/modules";
import { Meter } from "@/components/chart";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { Page, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { bytes, num } from "@/lib/format";
import { DataHeader } from "./data";

// Types are labels, not states: one neutral tone (colour means risk or status here).
const typeTone: Record<string, string> = { string: "bg-hover text-ink-2" };

function ttl(ms: number) {
  if (ms < 0) return "no expiry";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s left`;
  if (s < 3600) return `${Math.round(s / 60)} min left`;
  if (s < 86400) return `${Math.round(s / 3600)} h left`;
  return `${Math.round(s / 86400)} d left`;
}

export function KvPage({ project, match, k }: { project: string; match?: string; k?: string }) {
  useTitle(`${project} · Key-value`);
  const navigate = useNavigate();
  const stats = useQuery(mq.kvStats(project));
  const [glob, setGlob] = useState(match ?? "");
  const prefix = stats.data?.prefix ?? "";
  const keys = useInfiniteQuery({
    queryKey: ["kv-keys", project, match ?? ""],
    queryFn: ({ pageParam }) => mod.kvKeys(project, match || undefined, pageParam),
    initialPageParam: "0",
    getNextPageParam: (last) => (last.cursor && last.cursor !== "0" ? last.cursor : undefined),
  });
  const go = (o: { match?: string; key?: string }) =>
    navigate({ to: "/projects/$project/data/kv", params: { project }, search: { match: o.match || undefined, key: o.key } });
  useEffect(() => {
    const t = setTimeout(() => glob !== (match ?? "") && go({ match: glob }), 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [glob]);

  if (stats.isError && notOnBox(stats.error)) return <NotOnBox what="Key-value stores" />;
  const s = stats.data;
  const all = (keys.data?.pages ?? []).flatMap((p) => p.keys ?? []).sort((a, b) => a.key.localeCompare(b.key));
  const cap = (s?.maxMemoryMB ?? 0) * 1024 * 1024;

  return (
    <Page full>
      <DataHeader
        project={project}
        title="Key-value"
        lede={
          s ? (
            <>
              Valkey {s.server.version} · every key of <code className="font-mono text-ink">{project}</code> lives under{" "}
              <code className="font-mono text-ink">{s.prefix}</code>, and apps don't see the prefix.
            </>
          ) : undefined
        }
      />
      {stats.isError && <ProblemNote className="mt-8" error={stats.error} />}
      {s && (
        <dl className="mt-8 grid grid-cols-2 max-sm:[&>*:last-child:nth-child(odd)]:col-span-2 gap-px overflow-hidden rounded-xl border border-rule bg-rule sm:grid-cols-4">
          <div className="bg-raised px-4 py-4">
            <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">Keys</dt>
            <dd className="display mt-1.5 text-2xl text-ink tnum">{num(s.keys)}</dd>
            {s.approximate && <dd className="text-xs text-ink-3">sampled</dd>}
          </div>
          <div className="bg-raised px-4 py-4">
            <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">Memory</dt>
            <dd className="display mt-1.5 text-2xl text-ink tnum">{bytes(s.memoryBytes)}</dd>
            {cap > 0 && (
              <dd className="mt-2">
                <Meter ratio={s.memoryBytes / cap} label="Memory used of the project's cap" />
                <span className="mt-1 block text-xs text-ink-3">
                  of {s.maxMemoryMB} MB{s.overCap ? " · over the cap: oldest keys with an expiry go first" : ""}
                </span>
              </dd>
            )}
          </div>
          <div className="bg-raised px-4 py-4">
            <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">When full</dt>
            <dd className="mt-2 font-mono text-sm text-ink">{s.server.policy}</dd>
            <dd className="mt-0.5 text-xs text-ink-3">{s.server.aofEnabled ? "written to disk as it changes" : "snapshots only"}</dd>
          </div>
          <div className="bg-raised px-4 py-4">
            <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">Server</dt>
            <dd className="mt-2 text-sm text-ink-2">
              {num(s.server.totalKeys)} keys in all · {s.server.clients} {s.server.clients === 1 ? "client" : "clients"}
            </dd>
            <dd className="mt-0.5 text-xs text-ink-3">{bytes(s.server.usedBytes)} used box-wide</dd>
          </div>
        </dl>
      )}

      <div className="mt-6 grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
        <section className="min-w-0 overflow-hidden rounded-xl border border-rule bg-raised/60" aria-label="Keys">
          <div className="flex items-center gap-2 border-b border-rule px-3 py-2.5">
            <Search className="size-4 text-ink-4" />
            <input
              value={glob}
              onChange={(e) => setGlob(e.target.value)}
              placeholder="Filter with a glob, e.g. session:*"
              aria-label="Filter keys"
              className="h-7 min-w-0 flex-1 bg-transparent font-mono text-sm text-ink outline-none placeholder:font-sans placeholder:text-ink-4"
            />
            {glob && (
              <button
                onClick={() => setGlob("")}
                aria-label="Clear filter"
                className="grid size-6 place-items-center rounded text-ink-3 hover:bg-hover"
              >
                <X className="size-3.5" />
              </button>
            )}
          </div>
          <ul className="max-h-[60vh] divide-y divide-rule/60 overflow-y-auto">
            {keys.isPending && <Skeleton className="m-3 h-32" />}
            {all.map((x) => {
              const short = x.key.startsWith(prefix) ? x.key.slice(prefix.length) : x.key;
              return (
                <li key={x.key}>
                  <button
                    onClick={() => go({ match, key: k === short ? undefined : short })}
                    className={cn("flex w-full items-center gap-3 px-4 py-2 text-left hover:bg-hover/60", k === short && "bg-hover")}
                    aria-pressed={k === short}
                  >
                    <span
                      className={cn("w-12 shrink-0 rounded px-1.5 py-px text-center font-mono text-[0.6875rem]", typeTone[x.type] ?? typeTone.string)}
                    >
                      {x.type}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-[0.8125rem] text-ink">{short}</span>
                    <span className={cn("shrink-0 text-xs tnum", x.ttlMs >= 0 ? "text-ink-3" : "text-ink-4")}>{ttl(x.ttlMs)}</span>
                  </button>
                </li>
              );
            })}
          </ul>
          {keys.isSuccess && all.length === 0 && (
            <p className="px-4 py-10 text-center text-base text-ink-3">{match ? "No keys match." : "No keys yet."}</p>
          )}
          {keys.hasNextPage && (
            <div className="border-t border-rule p-2 text-center">
              <Button size="sm" variant="ghost" onClick={() => keys.fetchNextPage()} disabled={keys.isFetchingNextPage}>
                Load more
              </Button>
            </div>
          )}
        </section>
        <section className="min-w-0" aria-label="Value">
          {k ? (
            <KeyView project={project} k={k} />
          ) : (
            <div className="grid h-full min-h-48 place-items-center rounded-xl border border-dashed border-rule-strong p-8 text-center">
              <div>
                <KeyRound className="mx-auto size-5 text-ink-4" />
                <p className="mt-3 text-base text-ink-2">Pick a key to see its value.</p>
              </div>
            </div>
          )}
          <Connection project={project} />
        </section>
      </div>
    </Page>
  );
}

function KeyView({ project, k }: { project: string; k: string }) {
  const v = useQuery({ queryKey: ["kv-key", project, k], queryFn: () => mod.kvKey(project, k) });
  if (v.isPending) return <Skeleton className="h-48" />;
  if (v.isError) return <ProblemNote error={v.error} />;
  const d = v.data;
  return (
    <div className="animate-pop">
      <div className="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 className="font-mono text-base text-ink">{k}</h2>
        <span className="text-sm text-ink-3">
          {d.type} · {num(d.length)} {d.type === "string" ? "bytes" : "items"} · {bytes(d.memoryBytes)} in memory · {ttl(d.ttlMs)}
        </span>
      </div>
      <Untrusted label="Written by your apps. Shown as plain text.">
        <Value d={d} />
        {d.truncated && <p className="border-t border-rule px-4 py-1.5 text-xs text-ink-3">Long value: only the start is shown.</p>}
      </Untrusted>
    </div>
  );
}

function Value({ d }: { d: KVValue }) {
  const v = d.value;
  if (d.type === "string") {
    let text = String(v ?? "");
    try {
      text = JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      /* not JSON: show as is */
    }
    return <pre className="max-h-[50vh] overflow-auto px-4 py-3 font-mono text-[0.78rem] leading-5 whitespace-pre-wrap text-ink-2">{text}</pre>;
  }
  const rows: Array<[string, string]> =
    d.type === "hash" && v && typeof v === "object" && !Array.isArray(v)
      ? Object.entries(v as Record<string, unknown>).map(([a, b]) => [a, String(b)])
      : Array.isArray(v)
        ? v.map((x, i) => (Array.isArray(x) ? [String(x[0]), String(x[1])] : [String(i), typeof x === "string" ? x : JSON.stringify(x)]))
        : [["value", JSON.stringify(v)]];
  const head = d.type === "hash" ? ["field", "value"] : d.type === "zset" ? ["member", "score"] : ["#", "value"];
  return (
    <table className="w-full font-mono text-[0.78rem]">
      <thead>
        <tr className="text-left text-ink-4">
          <th className="border-b border-rule px-4 py-1.5 font-normal">{head[0]}</th>
          <th className="border-b border-rule px-4 py-1.5 font-normal">{head[1]}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map(([a, b], i) => (
          <tr key={i}>
            <td className="w-1/3 border-b border-rule/60 px-4 py-1.5 text-ink">{a}</td>
            <td className={cn("border-b border-rule/60 px-4 py-1.5 break-all text-ink-2", d.type === "zset" && "tnum")}>{b}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Connection({ project }: { project: string }) {
  const [url, setUrl] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  return (
    <div className="mt-6 rounded-xl border border-rule bg-raised/60 p-4">
      <p className="text-sm text-ink-2">
        Apps get <code className="font-mono text-ink">REDIS_URL</code> already. For valkey-cli or redis-cli on the box:
      </p>
      {!url && (
        <Button
          size="sm"
          className="mt-3"
          onClick={async () => {
            setErr(null);
            try {
              setUrl((await mod.kvConnection(project)).redisUrl);
            } catch (e) {
              setErr(e);
            }
          }}
        >
          <Eye />
          Show the connection URL
        </Button>
      )}
      {err ? <ProblemNote className="mt-3" error={err} /> : null}
      {url && <Command className="mt-3" cmd={`valkey-cli -u "${url}"`} />}
    </div>
  );
}
