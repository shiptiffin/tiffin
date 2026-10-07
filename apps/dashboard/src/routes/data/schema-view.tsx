import { useNavigate } from "@tanstack/react-router";
import { ChevronRight, KeyRound, Link2, Maximize2, Minus, Plus } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as RPointerEvent } from "react";
import type { PgTable } from "@/api/modules";
import { Breaker } from "@/components/breaker";
import { Button } from "@/components/ui/button";
import { slugOf } from "./api";
import { shortType } from "./format";

const W = 216;
const HEAD = 30;
const LINE = 19;
const MAX_COLS = 8;
const GAP_X = 96;
const GAP_Y = 22;

type Node = { t: PgTable; slug: string; x: number; y: number; h: number; cols: Array<{ name: string; type: string; pk: boolean; fk: boolean }>; more: number };
type Edge = { from: Node; to: Node; col: string; key: string };

/**
 * Lays tables out in columns by how they link: tables nothing points from
 * on the left, the tables that link to them to their right. Within a column
 * tables sit near the ones they link to (a few barycentre sweeps). No graph
 * library: this is enough for a few hundred tables.
 */
export function layout(tables: PgTable[]): { nodes: Node[]; edges: Edge[]; width: number; height: number } {
  const bySlug = new Map(tables.map((t) => [slugOf(t), t]));
  const outs = new Map<string, Set<string>>();
  for (const t of tables) {
    const s = slugOf(t);
    outs.set(s, new Set((t.foreignKeys ?? []).map((f) => slugOf({ schema: f.refSchema, name: f.refTable })).filter((r) => r !== s && bySlug.has(r))));
  }
  const layer = new Map<string, number>();
  const visit = (s: string, seen: Set<string>): number => {
    if (layer.has(s)) return layer.get(s)!;
    if (seen.has(s)) return 0; // a cycle: break it here
    seen.add(s);
    let l = 0;
    for (const r of outs.get(s) ?? []) l = Math.max(l, visit(r, seen) + 1);
    seen.delete(s);
    layer.set(s, l);
    return l;
  };
  for (const t of tables) visit(slugOf(t), new Set());
  const layers: string[][] = [];
  for (const [s, l] of [...layer.entries()].sort((a, b) => a[0].localeCompare(b[0]))) (layers[l] ??= []).push(s);
  // Tables with no links at all go in their own column at the end, so linked ones stay together.
  const linked = new Set<string>();
  for (const [s, rs] of outs) if (rs.size) [s, ...rs].forEach((x) => linked.add(x));
  const lonely = (layers[0] ?? []).filter((s) => !linked.has(s));
  if (lonely.length && lonely.length < (layers[0] ?? []).length) {
    layers[0] = layers[0].filter((s) => linked.has(s));
    layers.push(lonely);
  }
  const ins = new Map<string, string[]>();
  for (const [s, rs] of outs) for (const r of rs) ins.set(r, [...(ins.get(r) ?? []), s]);
  const pos = new Map<string, number>();
  const index = () => layers.forEach((l) => l.forEach((s, i) => pos.set(s, i)));
  index();
  for (let sweep = 0; sweep < 4; sweep++) {
    const order = sweep % 2 === 0 ? layers.map((_, i) => i).slice(1) : layers.map((_, i) => i).reverse().slice(1);
    for (const li of order) {
      const nb = (s: string) => [...(sweep % 2 === 0 ? (outs.get(s) ?? []) : (ins.get(s) ?? []))];
      const bary = (s: string) => {
        const n = nb(s).filter((x) => pos.has(x));
        return n.length ? n.reduce((a, x) => a + pos.get(x)!, 0) / n.length : pos.get(s)!;
      };
      layers[li] = [...layers[li]].sort((a, b) => bary(a) - bary(b));
      index();
    }
  }
  const nodes = new Map<string, Node>();
  let height = 0;
  layers.forEach((l, li) => {
    let y = 0;
    for (const s of l) {
      const t = bySlug.get(s)!;
      const fks = new Set((t.foreignKeys ?? []).flatMap((f) => f.columns));
      const all = (t.columns ?? []).map((c) => ({ name: c.name, type: shortType(c.type), pk: !!c.primary, fk: fks.has(c.name) }));
      const keyFirst = [...all.filter((c) => c.pk), ...all.filter((c) => !c.pk && c.fk), ...all.filter((c) => !c.pk && !c.fk)];
      const cols = keyFirst.slice(0, MAX_COLS);
      const more = all.length - cols.length;
      const h = HEAD + cols.length * LINE + (more ? LINE : 0) + 8;
      nodes.set(s, { t, slug: s, x: li * (W + GAP_X), y, h, cols, more });
      y += h + GAP_Y;
    }
    height = Math.max(height, y - GAP_Y);
  });
  // Centre each column against the tallest.
  layers.forEach((l) => {
    const last = nodes.get(l[l.length - 1]);
    if (!last) return;
    const off = (height - (last.y + last.h)) / 2;
    for (const s of l) nodes.get(s)!.y += off;
  });
  const edges: Edge[] = [];
  for (const t of tables) {
    for (const f of t.foreignKeys ?? []) {
      const from = nodes.get(slugOf(t));
      const to = nodes.get(slugOf({ schema: f.refSchema, name: f.refTable }));
      if (from && to && from !== to) edges.push({ from, to, col: (f.columns ?? [])[0], key: `${f.schema}.${f.table}.${f.name}` });
    }
  }
  return { nodes: [...nodes.values()], edges, width: layers.length * (W + GAP_X) - GAP_X, height };
}

function edgePath(e: Edge) {
  const ci = e.from.cols.findIndex((c) => c.name === e.col);
  const y1 = e.from.y + (ci >= 0 ? HEAD + ci * LINE + LINE / 2 + 2 : HEAD / 2);
  const pki = e.to.cols.findIndex((c) => c.pk);
  const y2 = e.to.y + (pki >= 0 ? HEAD + pki * LINE + LINE / 2 + 2 : HEAD / 2);
  const leftToRight = e.to.x < e.from.x;
  const x1 = leftToRight ? e.from.x : e.from.x + W;
  const x2 = leftToRight ? e.to.x + W : e.to.x;
  const dx = Math.max(40, Math.abs(x1 - x2) / 2);
  return `M ${x1} ${y1} C ${leftToRight ? x1 - dx : x1 + dx} ${y1}, ${leftToRight ? x2 + dx : x2 - dx} ${y2}, ${x2} ${y2}`;
}

/** The schema as a diagram you can pan and zoom; click a table to open it. */
export function SchemaMap({ project, branch, tables }: { project: string; branch: string; tables: PgTable[] }) {
  const navigate = useNavigate();
  const [managed, setManaged] = useState(false);
  const shown = useMemo(() => tables.filter((t) => (managed || !t.managed) && t.kind !== "foreign"), [tables, managed]);
  const g = useMemo(() => layout(shown), [shown]);
  const box = useRef<HTMLDivElement>(null);
  const [tf, setTf] = useState({ x: 0, y: 0, k: 1 });
  const [hot, setHot] = useState<string | null>(null);
  const drag = useRef<{ x: number; y: number; moved: boolean } | null>(null);
  const [dragging, setDragging] = useState(false);

  const fit = useCallback(() => {
    const el = box.current;
    if (!el || el.clientWidth === 0) return;
    const pad = 24;
    const k = Math.max(0.15, Math.min(1.1, (el.clientWidth - pad * 2) / Math.max(g.width, 1), (el.clientHeight - pad * 2) / Math.max(g.height, 1)));
    // A phone can't fit a wide diagram legibly: start readable at the top left, and pan.
    if (el.clientWidth < 640 && k < 0.55) return setTf({ k: 0.55, x: 12, y: 12 });
    setTf({ k, x: (el.clientWidth - g.width * k) / 2, y: (el.clientHeight - g.height * k) / 2 });
  }, [g]);
  useLayoutEffect(fit, [fit]);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => fit());
    ro.observe(el);
    return () => ro.disconnect();
  }, [fit]);

  const zoom = (f: number, cx?: number, cy?: number) =>
    setTf((t) => {
      const el = box.current!;
      const px = cx ?? el.clientWidth / 2;
      const py = cy ?? el.clientHeight / 2;
      const k = Math.max(0.15, Math.min(2.5, t.k * f));
      return { k, x: px - ((px - t.x) * k) / t.k, y: py - ((py - t.y) * k) / t.k };
    });
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    // Pinch (ctrl + wheel) zooms; a plain wheel or two fingers pan.
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      if (e.ctrlKey || e.metaKey) zoom(Math.exp(-e.deltaY / 300), e.clientX - r.left, e.clientY - r.top);
      else setTf((t) => ({ ...t, x: t.x - e.deltaX, y: t.y - e.deltaY }));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  });

  const open = (slug: string) =>
    navigate({ to: "/projects/$project/data/tables/$table", params: { project, table: slug }, search: (branch ? { branch } : {}) as never });
  const onDown = (e: RPointerEvent) => {
    drag.current = { x: e.clientX, y: e.clientY, moved: false };
  };
  const onMove = (e: RPointerEvent) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x;
    const dy = e.clientY - d.y;
    if (!d.moved && Math.hypot(dx, dy) < 4) return;
    if (!d.moved) {
      d.moved = true;
      setDragging(true);
      (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    }
    d.x = e.clientX;
    d.y = e.clientY;
    setTf((t) => ({ ...t, x: t.x + dx, y: t.y + dy }));
  };
  const onUp = () => {
    setTimeout(() => (drag.current = null));
    setDragging(false);
  };
  const near = (n: Node) => !!hot && (n.slug === hot || g.edges.some((e) => (e.from.slug === hot && e.to === n) || (e.to.slug === hot && e.from === n)));

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-ink-3">
          {shown.length} {shown.length === 1 ? "table" : "tables"}, {g.edges.length} {g.edges.length === 1 ? "link" : "links"}. Drag to move, pinch or <kbd className="kbd">+</kbd> <kbd className="kbd">−</kbd> to zoom.
        </p>
        <div className="flex items-center gap-3">
          {tables.some((t) => t.managed) && (
            <label className="flex items-center gap-2 text-sm text-ink-2">
              <Breaker label="Show managed tables" state={managed ? "on" : "off"} onFlip={(n) => setManaged(n === "on")} />
              Managed tables
            </label>
          )}
          <span className="flex items-center gap-0.5 rounded-[7px] border border-rule-2 bg-paper-raised p-0.5">
            <Button size="icon-sm" variant="ghost" aria-label="Zoom out" onClick={() => zoom(1 / 1.25)}>
              <Minus />
            </Button>
            <Button size="icon-sm" variant="ghost" aria-label="Zoom in" onClick={() => zoom(1.25)}>
              <Plus />
            </Button>
            <Button size="icon-sm" variant="ghost" aria-label="Fit the diagram" title="Fit (0)" onClick={fit}>
              <Maximize2 />
            </Button>
          </span>
        </div>
      </div>
      <div
        ref={box}
        className="schema-map relative h-[max(24rem,calc(100dvh-20rem))] overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk"
        data-dragging={dragging}
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={onUp}
        onKeyDown={(e) => {
          if (e.key === "+" || e.key === "=") zoom(1.25);
          else if (e.key === "-") zoom(1 / 1.25);
          else if (e.key === "0") fit();
        }}
      >
        <svg width="100%" height="100%" role="group" aria-label={`Diagram of ${shown.length} tables and how they link`}>
          <defs>
            <pattern id="schema-dots" width={16 * tf.k} height={16 * tf.k} patternUnits="userSpaceOnUse" x={tf.x} y={tf.y}>
              <circle cx={1} cy={1} r={0.8} fill="var(--rule-2)" />
            </pattern>
          </defs>
          <rect width="100%" height="100%" fill="url(#schema-dots)" aria-hidden />
          <g transform={`translate(${tf.x} ${tf.y}) scale(${tf.k})`}>
            <g aria-hidden>
              {g.edges.map((e) => (
                <path key={e.key} className="link" d={edgePath(e)} data-on={!!hot && (e.from.slug === hot || e.to.slug === hot)} />
              ))}
            </g>
            {g.nodes.map((n) => {
              const links = (n.t.foreignKeys ?? []).map((f) => f.refTable);
              return (
                <g
                  key={n.slug}
                  className="node cursor-pointer outline-hidden"
                  role="link"
                  tabIndex={0}
                  aria-label={`${n.slug}, ${n.t.columns?.length ?? 0} columns${links.length ? `, links to ${[...new Set(links)].join(", ")}` : ""}. Open it`}
                  transform={`translate(${n.x} ${n.y})`}
                  onMouseEnter={() => setHot(n.slug)}
                  onMouseLeave={() => setHot(null)}
                  onFocus={() => setHot(n.slug)}
                  onBlur={() => setHot(null)}
                  onClick={() => !drag.current?.moved && open(n.slug)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      open(n.slug);
                    }
                  }}
                  opacity={hot && !near(n) ? 0.45 : 1}
                >
                  <rect className="card" width={W} height={n.h} rx={8} fill="var(--paper-raised)" stroke={n.slug === hot ? "var(--ink-3)" : "var(--rule-2)"} />
                  <path d={`M 0 ${HEAD} H ${W}`} stroke="var(--rule)" />
                  <text x={12} y={19} fontSize={12.5} fontWeight={550} fill="var(--ink)" className="font-mono">
                    {n.slug.length > 26 ? `${n.slug.slice(0, 25)}…` : n.slug}
                  </text>
                  {n.t.kind !== "table" && (
                    <text x={W - 10} y={19} fontSize={10.5} textAnchor="end" fill="var(--ink-3)">
                      {n.t.kind === "materialized-view" ? "mat. view" : n.t.kind}
                    </text>
                  )}
                  {n.cols.map((c, i) => (
                    <g key={c.name} transform={`translate(0 ${HEAD + i * LINE + 4})`}>
                      {c.pk && <KeyRound x={10} y={3} width={10} height={10} color="var(--brass-ink)" aria-hidden />}
                      {!c.pk && c.fk && <Link2 x={10} y={3} width={10} height={10} color="var(--ink-3)" aria-hidden />}
                      <text x={26} y={12} fontSize={11.5} fill={c.pk || c.fk ? "var(--ink)" : "var(--ink-2)"} className="font-mono">
                        {c.name.length > 18 ? `${c.name.slice(0, 17)}…` : c.name}
                      </text>
                      <text x={W - 10} y={12} fontSize={10.5} textAnchor="end" fill="var(--ink-3)" className="font-mono">
                        {c.type.length > 12 ? `${c.type.slice(0, 11)}…` : c.type}
                      </text>
                    </g>
                  ))}
                  {n.more > 0 && (
                    <text x={26} y={HEAD + n.cols.length * LINE + 16} fontSize={11} fill="var(--ink-3)">
                      {n.more} more
                    </text>
                  )}
                </g>
              );
            })}
          </g>
        </svg>
        {shown.length === 0 && <p className="absolute inset-0 grid place-items-center text-base text-ink-3">No tables to draw yet.</p>}
      </div>
      <details className="group/links">
        <summary className="flex cursor-pointer list-none items-center gap-1.5 text-sm text-ink-3 hover:text-ink [&::-webkit-details-marker]:hidden">
          <ChevronRight className="size-3.5 transition-transform group-open/links:rotate-90" />
          The links as a list
        </summary>
        <ul className="mt-2 divide-y divide-rule border-y border-rule">
          {g.edges.map((e) => (
            <li key={e.key} className="py-1.5 font-mono text-[0.8125rem] text-ink-2">
              {e.from.slug}.{e.col} <span className="font-sans text-ink-3">links to</span> {e.to.slug}
            </li>
          ))}
          {g.edges.length === 0 && <li className="py-2 text-sm text-ink-3">No links between tables yet.</li>}
        </ul>
      </details>
    </div>
  );
}
