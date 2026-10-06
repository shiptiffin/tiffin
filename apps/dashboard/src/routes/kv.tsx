import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Eye, Search, X } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type KVValue } from "@/api/modules";
import { Command } from "@/components/copy";
import { Reading, Readings, Section, TypeWord } from "@/components/data-parts";
import { useTitle } from "@/components/favicon";
import { Page, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { SegMeter } from "@/components/seg-meter";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { bytes, bytesParts, count, dec, duration, int, num } from "@/lib/format";
import { relative } from "@/lib/time";
import { DataHeader } from "./data";
import { ConnectButton } from "@/components/connect";

/** "expires in 4 min", or nothing when the key lives until it's deleted. */
function ttl(ms: number): string | null {
  if (ms < 0) return null;
  return `expires in ${duration(Math.max(1, Math.round(ms / 1000)))}`;
}

/** What Valkey does when the project's memory cap is reached, in words. */
const policyWords: Record<string, string> = {
  "volatile-lru": "Drops the least recently used keys that have an expiry",
  "allkeys-lru": "Drops the least recently used keys",
  "volatile-lfu": "Drops the least often used keys that have an expiry",
  "allkeys-lfu": "Drops the least often used keys",
  "volatile-ttl": "Drops the keys closest to expiring",
  "volatile-random": "Drops random keys that have an expiry",
  "allkeys-random": "Drops random keys",
  noeviction: "Refuses new writes",
};

const lengthWord: Record<string, [string, string]> = {
  string: ["byte", "bytes"],
  hash: ["field", "fields"],
  list: ["item", "items"],
  set: ["member", "members"],
  zset: ["member", "members"],
  stream: ["entry", "entries"],
};

const byName = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true });

export function KvPage({ project, match, k }: { project: string; match?: string; k?: string }) {
  useTitle(`${project} · KV`);
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
  const all = (keys.data?.pages ?? []).flatMap((p) => p.keys ?? []).sort((a, b) => byName(a.key, b.key));
  const cap = (s?.maxMemoryMB ?? 0) * 1024 * 1024;
  const mem = bytesParts(s?.memoryBytes ?? 0);
  const share = cap > 0 && s ? s.memoryBytes / cap : 0;

  return (
    <Page full>
      <DataHeader
        project={project}
        title="KV"
        tabs={false}
        actions={<ConnectButton part="kv" project={project} />}
        lede={
          s ? (
            <>
              Valkey {s.server.version}. Every key of {project} lives under <code className="ident text-ink">{s.prefix}</code>; apps don't see the
              prefix.
            </>
          ) : undefined
        }
      />
      {stats.isError && <ProblemNote className="mt-8" error={stats.error} />}
      {s && (
        <Readings className="grid-cols-2 lg:grid-cols-[1.4fr_0.8fr_1.2fr_1fr]">
          <Reading
            className="col-span-2 lg:col-span-1"
            label="Memory"
            value={mem.value}
            unit={cap > 0 ? `${mem.unit} of ${int(s.maxMemoryMB)} MB` : mem.unit}
          >
            {cap > 0 && (
              <SegMeter
                className="mt-2.5"
                label="Memory used of the project's cap"
                value={s.memoryBytes}
                max={cap}
                warnAt={0.8}
                fullAt={0.95}
                scale={["0", `${int(s.maxMemoryMB / 2)} MB`, `${int(s.maxMemoryMB)} MB`]}
                valueText={`${bytes(s.memoryBytes)} of ${int(s.maxMemoryMB)} MB`}
              />
            )}
            {s.overCap ? (
              <p className="mt-2 text-xs text-warn-ink">Over the cap: keys with an expiry are being dropped.</p>
            ) : (
              cap > 0 && (
                <p className="mt-2 text-xs text-ink-3">
                  {share < 0.001 ? "Under 0.1 %" : `${dec(share * 100, 1)} %`} of the cap, set in tiffin.config.ts.
                </p>
              )
            )}
          </Reading>
          <Reading label="Keys" value={num(s.keys)} sub={s.approximate ? "sampled, so about" : undefined} />
          <Reading
            label="When full"
            value={
              <span className="block pt-1 text-[0.9375rem] leading-[1.375rem] tracking-normal">
                {policyWords[s.server.policy] ?? "Follows its eviction policy"}
              </span>
            }
            sub={<code className="ident">{s.server.policy}</code>}
          />
          <Reading
            label="On disk"
            value={
              <span className="block pt-1 text-[0.9375rem] leading-[1.375rem] tracking-normal">
                {s.server.aofEnabled ? "Every write, as it happens" : "Snapshots only"}
              </span>
            }
            sub={s.server.lastSaveAt ? `last snapshot ${relative(s.server.lastSaveAt)}` : undefined}
          />
        </Readings>
      )}

      <div className="mt-8 grid gap-x-10 gap-y-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
        <Section id="keys" label="Keys" aside={keys.isSuccess ? `${count(all.length, "key")}${keys.hasNextPage ? " so far" : ""}` : undefined}>
          <label className="flex h-9 items-center gap-2.5 border-t border-rule">
            <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
            <input
              value={glob}
              onChange={(e) => setGlob(e.target.value)}
              placeholder="Filter with a pattern, like cart:*"
              aria-label="Filter keys"
              className="h-8 min-w-0 flex-1 bg-transparent font-mono text-sm text-ink outline-none placeholder:font-sans placeholder:text-ink-4"
            />
            {glob && (
              <button
                onClick={() => setGlob("")}
                aria-label="Clear filter"
                className="grid size-6 place-items-center rounded-[5px] text-ink-3 hover:bg-paper-sunk"
              >
                <X className="size-3.5" />
              </button>
            )}
          </label>
          <ul className="max-h-[60vh] divide-y divide-rule overflow-y-auto border-y border-rule">
            {keys.isPending && (
              <li className="py-3">
                <Skeleton className="h-32" />
              </li>
            )}
            {all.map((x) => {
              const short = x.key.startsWith(prefix) ? x.key.slice(prefix.length) : x.key;
              const active = k === short;
              const t = ttl(x.ttlMs);
              return (
                <li key={x.key} className="relative">
                  {active && <span aria-hidden className="absolute inset-y-1.5 -left-3 w-[2px] rounded-full bg-brass max-sm:hidden" />}
                  <button
                    onClick={() => go({ match, key: active ? undefined : short })}
                    className={cn(
                      "grid w-full grid-cols-[minmax(0,1fr)_auto_3.25rem] items-baseline gap-x-4 py-2 text-left transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk sm:-mx-2 sm:w-[calc(100%+1rem)] sm:px-2",
                      active && "bg-paper-sunk",
                    )}
                    aria-pressed={active}
                  >
                    <span className="min-w-0 truncate font-mono text-[0.8125rem] text-ink">{short}</span>
                    <span className="text-xs text-ink-3 tnum">{t ?? ""}</span>
                    <TypeWord className="text-right">{x.type}</TypeWord>
                  </button>
                </li>
              );
            })}
          </ul>
          {keys.isSuccess && all.length === 0 && (
            <p className="border-b border-rule py-8 text-center text-base text-ink-3">{match ? "No keys match that pattern." : "No keys yet."}</p>
          )}
          {keys.hasNextPage && (
            <div className="pt-2 text-center">
              <Button size="sm" variant="ghost" onClick={() => keys.fetchNextPage()} disabled={keys.isFetchingNextPage}>
                Load more
              </Button>
            </div>
          )}
        </Section>
        <div className="min-w-0">
          {k ? (
            <KeyView project={project} k={k} />
          ) : (
            <div className="hidden h-full min-h-48 place-items-center border-y border-dashed border-rule-3 p-8 text-center lg:grid">
              <p className="text-base text-ink-2">Pick a key to see its value.</p>
            </div>
          )}
        </div>
      </div>
      <Connection project={project} />
    </Page>
  );
}

function KeyView({ project, k }: { project: string; k: string }) {
  const v = useQuery({ queryKey: ["kv-key", project, k], queryFn: () => mod.kvKey(project, k) });
  if (v.isPending) return <Skeleton className="h-48" />;
  if (v.isError) return <ProblemNote error={v.error} />;
  const d = v.data;
  const [one, many] = lengthWord[d.type] ?? ["item", "items"];
  return (
    <section className="animate-fade" aria-labelledby="kv-key">
      <div className="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 id="kv-key" className="font-mono text-[0.9375rem] text-ink">
          {k}
        </h2>
        <span className="text-sm text-ink-3 tnum">
          {d.type}, {count(d.length, one, many)}, {bytes(d.memoryBytes)} in memory{ttl(d.ttlMs) ? `, ${ttl(d.ttlMs)}` : ", no expiry"}
        </span>
      </div>
      <Untrusted label="Written by your apps. Shown as plain text.">
        <Value d={d} />
        {d.truncated && <p className="border-t border-rule px-3.5 py-1.5 text-xs text-ink-3">Long value: only the start is shown.</p>}
      </Untrusted>
    </section>
  );
}

/** A 10-digit number that reads as a Unix time this century gets a quiet date beside it. */
function unixHint(s: string): string | null {
  if (!/^\d{10}$/.test(s)) return null;
  const t = Number(s) * 1000;
  if (t < Date.UTC(2001, 0) || t > Date.UTC(2100, 0)) return null;
  return new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(new Date(t));
}

function pretty(text: string): string {
  try {
    const j = JSON.parse(text);
    return typeof j === "object" && j !== null ? JSON.stringify(j, null, 2) : text;
  } catch {
    return text;
  }
}

function Value({ d }: { d: KVValue }) {
  const v = d.value;
  if (d.type === "string") {
    return (
      <pre className="max-h-[50vh] overflow-auto px-3.5 py-3 font-mono text-[0.78125rem] leading-5 whitespace-pre-wrap text-ink">
        {pretty(String(v ?? ""))}
      </pre>
    );
  }
  let head: [string, string, string?] = ["#", "value"];
  let rows: Array<[ReactNode, string]>;
  if (d.type === "hash" && v && typeof v === "object" && !Array.isArray(v)) {
    head = ["field", "value"];
    rows = Object.entries(v as Record<string, unknown>)
      .sort(([a], [b]) => byName(a, b))
      .map(([a, b]) => [a, typeof b === "string" ? b : JSON.stringify(b)]);
  } else if (d.type === "zset" && Array.isArray(v)) {
    // Sorted sets read top-down, highest score first, the way a leaderboard does.
    head = ["rank", "member", "score"];
    const sorted = [...(v as Array<[unknown, unknown]>)].sort((x, y) => Number(y[1]) - Number(x[1]));
    return (
      <Grid head={head}>
        {sorted.map(([m, sc], i) => (
          <tr key={i}>
            <td className="w-14 border-b border-rule px-3.5 py-1.5 text-ink-3 tnum">{int(i + 1)}</td>
            <td className="border-b border-rule px-3.5 py-1.5 break-all text-ink">{String(m)}</td>
            <td className="border-b border-rule px-3.5 py-1.5 text-right text-ink tnum">{String(sc)}</td>
          </tr>
        ))}
      </Grid>
    );
  } else if (Array.isArray(v)) {
    head = [d.type === "set" ? "" : "#", d.type === "set" ? "member" : "value"];
    const items = v.map((x) => (typeof x === "string" ? x : JSON.stringify(x)));
    if (d.type === "set") items.sort(byName);
    rows = items.map((x, i) => [d.type === "set" ? "" : int(i), x]);
  } else {
    rows = [["value", JSON.stringify(v)]];
  }
  const narrow = d.type !== "hash";
  return (
    <Grid head={head}>
      {rows.map(([a, b], i) => {
        const hint = unixHint(b);
        return (
          <tr key={i}>
            <td className={cn("border-b border-rule px-3.5 py-1.5 align-top", narrow ? "w-14 text-ink-3 tnum" : "w-1/3 text-ink-2")}>{a}</td>
            <td className="border-b border-rule px-3.5 py-1.5 break-all whitespace-pre-wrap text-ink">
              {pretty(b)}
              {hint && <span className="ml-2 font-sans text-xs text-ink-3">{hint}</span>}
            </td>
          </tr>
        );
      })}
    </Grid>
  );
}

function Grid({ head, children }: { head: [string, string, string?]; children: ReactNode }) {
  return (
    <div className="max-h-[50vh] overflow-auto">
      <table className="w-full border-separate border-spacing-0 font-mono text-[0.78125rem] leading-[1.1875rem]">
        <thead className="sticky top-0">
          <tr className="text-left">
            {head
              .filter((h) => h !== undefined)
              .map((h, i, a) => (
                <th
                  key={i}
                  className={cn(
                    "border-b border-rule-2 bg-paper-sunk px-3.5 py-1.5 font-sans text-xs font-normal text-ink-3",
                    i === a.length - 1 && a.length === 3 && "text-right",
                  )}
                >
                  {h}
                </th>
              ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

function Connection({ project }: { project: string }) {
  const [url, setUrl] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  return (
    <Section id="kv-connect" label="Connect from this computer" className="mt-12">
      <div className="flex flex-col gap-3 border-t border-rule pt-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-base text-ink-2">
          Apps get <code className="ident text-ink">REDIS_URL</code> already. For valkey-cli or redis-cli, reveal the URL; it carries the password.
        </p>
        {!url && (
          <Button
            size="sm"
            className="self-start sm:self-auto"
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
            Show the URL
          </Button>
        )}
      </div>
      {err ? <ProblemNote className="mt-3" error={err} /> : null}
      {url && <Command className="mt-3" cmd={`valkey-cli -u "${url}"`} />}
    </Section>
  );
}
