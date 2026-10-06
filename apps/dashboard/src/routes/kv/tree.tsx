import { useQueries } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ChevronRight, Search, Trash2, X } from "lucide-react";
import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { mod, type KVTree } from "@/api/modules";
import { MiniSelect } from "@/components/data-parts";
import { ProblemNote } from "@/components/problem";
import { Skeleton } from "@/components/page";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { Segmented } from "./parts";
import { expiresIn, kvPrefix, toGlob, TYPES } from "./words";
import { useKv } from "./write";

const PAGE = 500;

type Row =
  | { kind: "group"; prefix: string; depth: number; count: number; open: boolean }
  | { kind: "key"; key: string; type: string; ttlMs: number; depth: number }
  | { kind: "more"; prefix: string; depth: number; left: number }
  | { kind: "loading"; prefix: string; depth: number };

export type Filters = { search: string; type: string; expiry: "" | "kept" | "cache" };

export type BrowserHandle = { focusSearch: () => void };

/**
 * The key browser: keys grouped by ":" like folders, with counts, a level at
 * a time from the API (kv-tree), in one virtualized list so 50,000 keys
 * scroll as smoothly as 50. A search shows matching keys flat. Keyboard: the
 * ARIA tree pattern (arrows move, → opens a group, ← closes it, Enter opens
 * a key, Delete deletes the key or everything under the group).
 */
export const KeyBrowser = forwardRef<BrowserHandle, {
  selected?: string;
  onOpen: (key: string) => void;
  filters: Filters;
  onFilters: (f: Filters) => void;
  className?: string;
}>(function KeyBrowser({ selected, onOpen, filters, onFilters, className }, ref) {
  const { project, run, canWrite } = useKv();
  const [open, setOpen] = useState<Set<string>>(() => new Set(selected?.includes(":") ? parents(selected) : []));
  const [pages, setPages] = useState<Record<string, number>>({});
  const [search, setSearch] = useState(filters.search);
  const searchRef = useRef<HTMLInputElement>(null);
  useImperativeHandle(ref, () => ({ focusSearch: () => searchRef.current?.focus() }));
  useEffect(() => {
    const t = setTimeout(() => search !== filters.search && onFilters({ ...filters, search }), 250);
    return () => clearTimeout(t);
  }, [search, filters, onFilters]);

  const flat = !!filters.search;
  const glob = toGlob(filters.search);
  // The levels to load: the top, and every open group whose parents are open.
  const levels = useMemo(() => {
    if (flat) return [""];
    const out = [""];
    for (const p of [...open].sort()) if (parents(p).every((q) => open.has(q))) out.push(p);
    return out;
  }, [open, flat]);
  const specs = levels.flatMap((prefix) => Array.from({ length: pages[prefix] ?? 1 }, (_, i) => ({ prefix, offset: i * PAGE })));
  const results = useQueries({
    queries: specs.map(({ prefix, offset }) => ({
      queryKey: ["kv-tree", project, glob, filters.type, filters.expiry, prefix, offset],
      queryFn: () =>
        mod.kvTree(project, { prefix, offset, limit: PAGE, delimiter: flat ? "none" : ":", match: glob, type: filters.type, expiry: filters.expiry }),
      staleTime: 15_000,
      // While a filter changes, keep showing the same level's last answer.
      placeholderData: (prev: KVTree | undefined, q?: { queryKey: readonly unknown[] }) =>
        q?.queryKey[5] === prefix && q.queryKey[6] === offset ? prev : undefined,
    })),
  });
  const byLevel = new Map<string, Array<KVTree | undefined>>();
  specs.forEach((s, i) => byLevel.set(s.prefix, [...(byLevel.get(s.prefix) ?? []), results[i].data]));
  const top = results[0];
  const pre = kvPrefix(project);

  const rows: Row[] = [];
  const build = (prefix: string, depth: number) => {
    const got = byLevel.get(prefix) ?? [];
    const first = got[0];
    if (!first) {
      rows.push({ kind: "loading", prefix, depth });
      return;
    }
    for (const g of first.groups ?? []) {
      const isOpen = !flat && open.has(g.prefix);
      rows.push({ kind: "group", prefix: g.prefix, depth, count: g.keys, open: isOpen });
      if (isOpen) build(g.prefix, depth + 1);
    }
    for (const page of got) for (const k of page?.keys ?? []) rows.push({ kind: "key", key: k.key.slice(pre.length), type: k.type, ttlMs: k.ttlMs, depth });
    const last = got[got.length - 1];
    if (last?.next) rows.push({ kind: "more", prefix, depth, left: first.total - last.next });
  };
  build("", 0);

  const scroller = useRef<HTMLDivElement>(null);
  const v = useVirtualizer({ count: rows.length, getScrollElement: () => scroller.current, estimateSize: () => 32, overscan: 12 });
  const items = v.getVirtualItems();
  // A "more" row scrolled into view loads the level's next page.
  const shownMore = items.map((it) => rows[it.index]).find((r) => r?.kind === "more")?.prefix;
  const fetching = results.some((x) => x.isFetching);
  useEffect(() => {
    if (shownMore !== undefined && !fetching) setPages((p) => ({ ...p, [shownMore]: (p[shownMore] ?? 1) + 1 }));
  }, [shownMore, fetching]);

  const [focus, setFocus] = useState(0);
  const at = Math.min(focus, Math.max(0, rows.length - 1));
  const toggle = (prefix: string, to?: boolean) =>
    setOpen((o) => {
      const n = new Set(o);
      if (to ?? !n.has(prefix)) n.add(prefix);
      else n.delete(prefix);
      return n;
    });
  const move = (i: number) => {
    const n = Math.max(0, Math.min(rows.length - 1, i));
    setFocus(n);
    v.scrollToIndex(n);
    requestAnimationFrame(() => scroller.current?.querySelector<HTMLElement>(`[data-row="${n}"]`)?.focus());
  };
  const removeRow = (r: Row) => {
    if (!canWrite) return;
    if (r.kind === "group") void run("delete-prefix", { prefix: r.prefix }, `Deleted every key starting with ${r.prefix}`).catch(() => undefined);
    if (r.kind === "key") void run("delete", { keys: [r.key] }, `Deleted ${r.key}`).catch(() => undefined);
  };
  const onKey = (e: React.KeyboardEvent, i: number) => {
    const r = rows[i];
    switch (e.key) {
      case "ArrowDown":
        move(i + 1);
        break;
      case "ArrowUp":
        move(i - 1);
        break;
      case "Home":
        move(0);
        break;
      case "End":
        move(rows.length - 1);
        break;
      case "ArrowRight":
        if (r.kind === "group" && !r.open) toggle(r.prefix, true);
        else if (r.kind === "group") move(i + 1);
        else return;
        break;
      case "ArrowLeft":
        if (r.kind === "group" && r.open) toggle(r.prefix, false);
        else {
          const parent = rows.findLastIndex((x, j) => j < i && x.kind === "group" && x.depth === r.depth - 1);
          if (parent < 0) return;
          move(parent);
        }
        break;
      case "Enter":
      case " ":
        if (r.kind === "group") toggle(r.prefix);
        else if (r.kind === "key") onOpen(r.key);
        else return;
        break;
      case "Delete":
      case "Backspace":
        removeRow(r);
        break;
      default:
        return;
    }
    e.preventDefault();
  };

  const total = top?.data ? (flat ? top.data.total : (top.data.groups ?? []).reduce((n, g) => n + g.keys, 0) + top.data.total) : undefined;

  return (
    <div className={cn("flex min-h-0 flex-col", className)}>
      <label className="flex h-9 items-center gap-2.5 rounded-[8px] border border-rule-2 bg-paper-raised px-2.5 focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)]">
        <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
        <input
          ref={searchRef}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              move(0);
            }
            if (e.key === "Escape" && search) setSearch("");
          }}
          placeholder="Find keys, or a pattern like cart:*"
          aria-label="Find keys"
          spellCheck={false}
          className="h-8 min-w-0 flex-1 bg-transparent font-mono text-[0.8125rem] text-ink outline-none placeholder:font-sans placeholder:text-ink-4"
        />
        {search ? (
          <button type="button" onClick={() => setSearch("")} aria-label="Clear search" className="grid size-6 place-items-center rounded-[5px] text-ink-3 hover:bg-paper-sunk">
            <X className="size-3.5" />
          </button>
        ) : (
          <kbd className="kbd" aria-hidden>
            /
          </kbd>
        )}
      </label>
      <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
        <MiniSelect aria-label="Type" value={filters.type} onChange={(e) => onFilters({ ...filters, type: e.target.value })} className="w-32">
          <option value="">All types</option>
          {TYPES.map((t) => (
            <option key={t.type} value={t.type}>
              {t.name}
            </option>
          ))}
        </MiniSelect>
        <Segmented
          label="Expiry"
          value={filters.expiry}
          onChange={(expiry) => onFilters({ ...filters, expiry })}
          options={[
            { value: "", label: "All" },
            { value: "kept", label: "Kept" },
            { value: "cache", label: "Cache" },
          ]}
        />
      </div>
      <p className="mt-3 mb-1.5 flex h-4 items-baseline justify-between text-xs text-ink-3" aria-live="polite">
        <span>
          {total === undefined ? "" : flat ? `${int(total)} ${total === 1 ? "key matches" : "keys match"}` : `${int(total)} ${total === 1 ? "key" : "keys"}`}
          {top?.data?.partial && " at least"}
        </span>
        {top?.isFetching && !top.isPending && <span>Updating…</span>}
      </p>
      {top?.isError && <ProblemNote error={top.error} />}
      {top?.isPending && (
        <div className="space-y-2 border-t border-rule pt-2">
          {["w-[70%]", "w-[55%]", "w-[80%]", "w-[45%]", "w-[62%]"].map((w) => (
            <Skeleton key={w} className={cn("h-5", w)} />
          ))}
        </div>
      )}
      {top?.isSuccess && rows.length === 0 && (
        <p className="border-y border-rule py-10 text-center text-base text-ink-3">
          {filters.search || filters.type || filters.expiry ? "No keys match." : "No keys yet. Your apps' keys show up here, or make one with New key."}
        </p>
      )}
      <div
        ref={scroller}
        role="tree"
        aria-label="Keys"
        className={cn("relative min-h-0 flex-1 overflow-y-auto overscroll-contain border-t border-rule", rows.length === 0 && "hidden")}
      >
        <div style={{ height: v.getTotalSize() }} className="relative w-full">
          {items.map((it) => {
            const r = rows[it.index];
            const i = it.index;
            const common = {
              "data-row": i,
              tabIndex: i === at ? 0 : -1,
              onKeyDown: (e: React.KeyboardEvent) => onKey(e, i),
              onFocus: () => setFocus(i),
              style: { transform: `translateY(${it.start}px)`, paddingLeft: `${r.depth * 14 + 6}px` },
              className:
                "group absolute inset-x-0 top-0 flex h-8 items-center gap-2 rounded-[6px] pr-1.5 text-left outline-none transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk focus-visible:bg-paper-sunk focus-visible:shadow-[inset_0_0_0_2px_var(--focus)]",
            };
            if (r.kind === "loading" || r.kind === "more")
              return (
                <div key={`m${r.prefix}${i}`} {...common} role="treeitem" aria-level={r.depth + 1} aria-selected={false} aria-busy>
                  <span className="text-xs text-ink-3">{r.kind === "more" ? `Loading ${int(r.left)} more…` : "Loading…"}</span>
                </div>
              );
            if (r.kind === "group")
              return (
                <div
                  key={`g${r.prefix}`}
                  {...common}
                  role="treeitem"
                  aria-level={r.depth + 1}
                  aria-expanded={r.open}
                  aria-selected={false}
                  onClick={() => {
                    setFocus(i);
                    toggle(r.prefix);
                  }}
                >
                  <ChevronRight aria-hidden className={cn("size-3.5 shrink-0 text-ink-3 transition-transform duration-[var(--dur-state)]", r.open && "rotate-90")} />
                  <span className="min-w-0 flex-1 truncate font-mono text-[0.8125rem] text-ink">{r.prefix.slice(r.prefix.lastIndexOf(":", r.prefix.length - 2) + 1)}</span>
                  <span className="w-16 shrink-0 text-right text-xs text-ink-3 tnum">{int(r.count)}</span>
                  {canWrite && (
                    <button
                      type="button"
                      tabIndex={-1}
                      aria-label={`Delete every key starting with ${r.prefix}`}
                      title={`Delete every key starting with ${r.prefix} (Delete)`}
                      onClick={(e) => {
                        e.stopPropagation();
                        removeRow(r);
                      }}
                      className="absolute right-1 grid size-6 place-items-center rounded-[5px] bg-paper-sunk text-ink-3 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100 hover:bg-danger-wash hover:text-danger"
                    >
                      <Trash2 className="size-3.5" />
                    </button>
                  )}
                </div>
              );
            const active = r.key === selected;
            const ttl = expiresIn(r.ttlMs);
            const name = flat ? r.key : r.key.slice(r.key.lastIndexOf(":", r.key.length - 2) + 1) || r.key;
            return (
              <div
                key={`k${r.key}`}
                {...common}
                role="treeitem"
                aria-level={r.depth + 1}
                aria-selected={active}
                title={r.key}
                onClick={() => {
                  setFocus(i);
                  onOpen(r.key);
                }}
                className={cn(common.className, active && "bg-paper-sunk")}
              >
                {active && <span aria-hidden className="absolute inset-y-1.5 left-0 w-[2px] rounded-full bg-brass" />}
                <span aria-hidden className="w-3.5 shrink-0" />
                <span className="min-w-0 flex-1 truncate font-mono text-[0.8125rem] text-ink">{name}</span>
                {ttl && (
                  <span className="shrink-0 text-xs text-ink-3 tnum" title={`Cache: expires ${ttl}`}>
                    {ttl.replace(/^in /, "")}
                  </span>
                )}
                <span className="w-11 shrink-0 text-right font-mono text-[0.71875rem] text-ink-3">{r.type}</span>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
});

/** "a:b:c" → ["a:", "a:b:"]: the groups a key sits in. */
function parents(key: string): string[] {
  const out: string[] = [];
  let i = key.indexOf(":");
  while (i >= 0 && i < key.length - 1) {
    out.push(key.slice(0, i + 1));
    i = key.indexOf(":", i + 1);
  }
  return out;
}
