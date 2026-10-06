import { useInfiniteQuery } from "@tanstack/react-query";
import { ArrowLeft, Pencil, Search, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError } from "@/api/client";
import { mod, type KVValue } from "@/api/modules";
import { CopyButton } from "@/components/copy";
import { MiniSelect, TypeWord } from "@/components/data-parts";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { bytes, count, int } from "@/lib/format";
import { HashEditor, ListEditor, SetEditor, StreamEditor, TextEditor, ZsetEditor, type Items } from "./editors";
import { expiresIn, splitSeconds, toGlob, typeInfo, UNITS } from "./words";
import { useKv } from "./write";

const PAGE = 200;

/** Merges a key's pages into what its editor shows. */
function merge(pages: KVValue[], complete: boolean): Items {
  const t = pages[0].type;
  const all = pages.flatMap((p) => (Array.isArray(p.value) ? p.value : []));
  const byName = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true });
  switch (t) {
    case "string":
      return { type: "string", text: String(pages[0].value ?? ""), truncated: pages[0].truncated };
    case "hash": {
      const pairs = pages.flatMap((p) => Object.entries((p.value ?? {}) as Record<string, string>));
      return { type: "hash", pairs: complete ? pairs.sort(([a], [b]) => byName(a, b)) : pairs };
    }
    case "set": {
      const items = all.map(String);
      return { type: "set", items: complete ? items.sort(byName) : items };
    }
    case "zset":
      return { type: "zset", pairs: (all as Array<[string, number]>).map(([m, s]) => [String(m), Number(s)]) };
    case "stream":
      return { type: "stream", entries: (all as Array<[string, string[]]>).map(([id, f]) => [String(id), (f ?? []).map(String)]) };
    default:
      return { type: "list", items: all.map((x) => (typeof x === "string" ? x : JSON.stringify(x))) };
  }
}

/** One key: name, type, size, expiry, and the editor for its type. */
export function KeyPanel({ k, onBack, onGone, onRenamed }: { k: string; onBack: () => void; onGone: () => void; onRenamed: (to: string) => void }) {
  const { project, run, canWrite } = useKv();
  const [find, setFind] = useState("");
  const [match, setMatch] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setMatch(toGlob(find)), 250);
    return () => clearTimeout(t);
  }, [find]);
  const q = useInfiniteQuery({
    queryKey: ["kv-key", project, k, match],
    queryFn: ({ pageParam }) => mod.kvKey(project, k, { cursor: pageParam || undefined, match: match || undefined, count: PAGE }),
    initialPageParam: "",
    getNextPageParam: (last) => last.cursor || undefined,
    placeholderData: (prev) => prev,
  });
  const [renaming, setRenaming] = useState<string | null>(null);

  if (q.isPending)
    return (
      <div className="space-y-3" aria-busy>
        <Skeleton className="h-6 w-64 max-w-full" />
        <Skeleton className="h-4 w-48" />
        <Skeleton className="h-48" />
      </div>
    );
  if (q.isError) {
    if (q.error instanceof ApiError && q.error.status === 404)
      return (
        <div className="grid min-h-48 place-items-center rounded-[10px] border border-dashed border-rule-3 p-8 text-center">
          <div>
            <p className="text-md text-ink">
              <span className="font-mono">{k}</span> is gone.
            </p>
            <p className="mt-1 text-base text-ink-3">It expired or was deleted.</p>
            <Button size="sm" variant="ghost" className="mt-3" onClick={onGone}>
              Back to the keys
            </Button>
          </div>
        </div>
      );
    return <ProblemNote error={q.error} />;
  }
  const first = q.data.pages[0];
  const info = typeInfo(first.type);
  const items = merge(q.data.pages, !q.hasNextPage);
  const paging = { more: !!q.hasNextPage, loadMore: () => !q.isFetchingNextPage && void q.fetchNextPage() };
  const searchable = (first.type === "hash" || first.type === "set" || first.type === "zset") && (first.length > 50 || !!find);

  const rename = async () => {
    const to = renaming?.trim();
    if (!to || to === k) return setRenaming(null);
    try {
      await run("rename", { key: k, to }, `Renamed ${k} to ${to}`);
      setRenaming(null);
      onRenamed(to);
    } catch {
      // the toast said why
    }
  };

  return (
    <section aria-labelledby="kv-key" className="min-w-0">
      <button type="button" onClick={onBack} className="mb-3 inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink lg:hidden">
        <ArrowLeft className="size-3.5" /> Keys
      </button>
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0 flex-1">
          {renaming === null ? (
            <div className="flex min-w-0 items-center gap-1">
              <h2 id="kv-key" className="min-w-0 font-mono text-[0.9375rem] break-all text-ink">
                {k}
              </h2>
              <CopyButton value={k} label="Copy the key's name" className="size-6" />
            </div>
          ) : (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                void rename();
              }}
              className="flex items-center gap-2"
            >
              <input
                autoFocus
                aria-label="New name"
                value={renaming}
                spellCheck={false}
                onChange={(e) => setRenaming(e.target.value)}
                onKeyDown={(e) => e.key === "Escape" && setRenaming(null)}
                className="h-8 min-w-0 flex-1 rounded-[7px] border border-brass bg-paper-raised px-2 font-mono text-[0.875rem] text-ink shadow-[0_0_0_3px_var(--brass-wash)] outline-none"
              />
              <Button size="sm" type="submit">
                Rename
              </Button>
              <Button size="sm" variant="ghost" type="button" onClick={() => setRenaming(null)}>
                Cancel
              </Button>
            </form>
          )}
          <p className="mt-1 flex flex-wrap items-baseline gap-x-2 text-sm text-ink-3 tnum">
            <span className="text-ink-2">{info.name}</span>
            <TypeWord>{first.type}</TypeWord>
            <span aria-hidden>·</span>
            <span>
              {count(first.length, info.one, info.many)}, {bytes(first.memoryBytes)} in memory
            </span>
          </p>
        </div>
        {canWrite && renaming === null && (
          <div className="flex shrink-0 gap-1">
            <Button size="sm" variant="ghost" onClick={() => setRenaming(k)}>
              <Pencil />
              Rename
            </Button>
            <Button
              size="sm"
              variant="danger-quiet"
              onClick={() =>
                void run("delete", { keys: [k] }, `Deleted ${k}`)
                  .then((r) => r && onGone())
                  .catch(() => undefined)
              }
            >
              <Trash2 />
              Delete key
            </Button>
          </div>
        )}
      </div>

      <Expiry k={k} ttlMs={first.ttlMs} at={q.dataUpdatedAt} />

      {searchable && (
        <label className="mb-2 flex h-8 max-w-sm items-center gap-2 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 focus-within:border-brass">
          <Search className="size-3.5 text-ink-3" aria-hidden />
          <input
            value={find}
            onChange={(e) => setFind(e.target.value)}
            placeholder={first.type === "hash" ? "Find fields" : "Find members"}
            aria-label={first.type === "hash" ? "Find fields" : "Find members"}
            spellCheck={false}
            className="min-w-0 flex-1 bg-transparent font-mono text-[0.78125rem] text-ink outline-none placeholder:font-sans placeholder:text-ink-4"
          />
        </label>
      )}
      {items.type === "string" && <TextEditor key={k} k={k} text={items.text} truncated={items.truncated} />}
      {items.type === "hash" && <HashEditor k={k} pairs={items.pairs} {...paging} />}
      {items.type === "list" && <ListEditor k={k} items={items.items} {...paging} />}
      {items.type === "set" && <SetEditor k={k} items={items.items} {...paging} />}
      {items.type === "zset" && <ZsetEditor k={k} pairs={items.pairs} ranked={!match} {...paging} />}
      {items.type === "stream" && <StreamEditor k={k} entries={items.entries} length={first.length} {...paging} />}
      {(items.type === "hash" || items.type === "set") && q.hasNextPage && (
        <p className="mt-2 text-xs text-ink-3">Showing {int(first.length > 0 ? q.data.pages.reduce((n, p) => n + (Array.isArray(p.value) ? p.value.length : Object.keys(p.value ?? {}).length), 0) : 0)} of {int(first.length)} so far.</p>
      )}
    </section>
  );
}

/**
 * The key's expiry: kept until deleted, or cache that expires (counting down),
 * with Change and Keep forever.
 */
function Expiry({ k, ttlMs, at }: { k: string; ttlMs: number; at: number }) {
  const { run, canWrite, refresh } = useKv();
  const [now, setNow] = useState(() => Date.now());
  const [edit, setEdit] = useState<{ n: string; unit: string } | null>(null);
  const kept = ttlMs < 0;
  useEffect(() => {
    if (kept) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [kept]);
  const left = ttlMs - (now - at);
  useEffect(() => {
    if (!kept && left <= 0) refresh();
  }, [kept, left <= 0, refresh]); // eslint-disable-line react-hooks/exhaustive-deps
  const start = () => {
    const [n, unit] = kept ? [1, "hours"] : splitSeconds(Math.max(60, Math.round(left / 1000)));
    setEdit({ n: String(n), unit });
  };
  const save = async () => {
    if (!edit) return;
    const s = Number(edit.n) * (UNITS.find((u) => u.unit === edit.unit)?.s ?? 60);
    if (!(s > 0)) return;
    try {
      await run("expire", { key: k, ttlSeconds: Math.round(s) }, `${k} now expires in ${edit.n} ${edit.unit}`);
      setEdit(null);
    } catch {
      // the toast said why
    }
  };
  return (
    <div className="my-4 flex min-h-9 flex-wrap items-center justify-between gap-x-4 gap-y-2 border-y border-rule py-2">
      {edit ? (
        <form
          className="flex flex-wrap items-center gap-2 text-base text-ink-2"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <label htmlFor="kv-ttl">Expire in</label>
          <input
            id="kv-ttl"
            autoFocus
            inputMode="numeric"
            value={edit.n}
            onChange={(e) => setEdit({ ...edit, n: e.target.value.replace(/[^\d.]/g, "") })}
            onKeyDown={(e) => e.key === "Escape" && setEdit(null)}
            className="h-7 w-16 rounded-[6px] border border-rule-2 bg-paper-raised px-2 text-right text-ink tnum outline-none focus-visible:border-brass"
          />
          <MiniSelect aria-label="Unit" value={edit.unit} onChange={(e) => setEdit({ ...edit, unit: e.target.value })} className="w-24">
            {UNITS.map((u) => (
              <option key={u.unit} value={u.unit}>
                {u.unit}
              </option>
            ))}
          </MiniSelect>
          <Button size="sm" type="submit" disabled={!(Number(edit.n) > 0)}>
            Save
          </Button>
          <Button size="sm" variant="ghost" type="button" onClick={() => setEdit(null)}>
            Cancel
          </Button>
        </form>
      ) : (
        <p className="text-base text-ink-2">
          <span className="label mr-2.5">Expiry</span>
          {kept ? (
            <>
              Kept until deleted <span className="text-ink-3">(never dropped to make room)</span>
            </>
          ) : (
            <>
              <span className="text-ink tnum">{expiresIn(Math.max(0, left))}</span> <span className="text-ink-3">(cache: dropped first when memory runs short)</span>
            </>
          )}
        </p>
      )}
      {canWrite && !edit && (
        <div className="flex gap-1">
          <Button size="sm" variant="ghost" onClick={start}>
            {kept ? "Add an expiry" : "Change"}
          </Button>
          {!kept && (
            <Button size="sm" variant="ghost" onClick={() => void run("expire", { key: k, ttlSeconds: 0 }, `${k} is kept until deleted`).catch(() => undefined)}>
              Keep forever
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
