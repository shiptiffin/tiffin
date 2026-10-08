import { useQueries } from "@tanstack/react-query";
import { cn } from "@/lib/cn";
import { appearanceQuery, defaultEnamel, ENAMELS, enamelVar, type Enamel } from "@/lib/enamel";
import { int } from "@/lib/format";
import { memWords, type Shares } from "@/lib/usage";

/** Projects shown by name; the rest share one segment, so the bar reads the same with 3 projects or 50. */
const NAMED = 5;

/**
 * The box's memory as one bar, the way a phone shows its storage: each
 * project in its own colour (the one its icon wears), biggest first, then
 * one segment for the rest, then System (Linux and the shared services every
 * project uses) in grey; the empty part of the track is free. A legend under
 * it names each one with its size, so nothing needs a hover. On a project's
 * own page (`focus`) only that project is in colour, and its limit shows as
 * a dashed outline its share can grow into.
 *
 *   <BoxBar shares={s} order={["shop", "notes"]} focus="shop" legend />
 */
export function BoxBar({ shares: s, order, focus, legend, className }: { shares: Shares; order: string[]; focus?: string; legend?: boolean; className?: string }) {
  const w = (mb: number) => `${Math.max(0, (mb / s.totalMB) * 100)}%`;
  const used = (p: string) => s.projects[p] ?? 0;
  const ranked = order.filter((p) => used(p) > 0 || p === focus).sort((a, b) => used(b) - used(a));
  const named = focus ? ranked.filter((p) => p === focus) : ranked.slice(0, NAMED);
  const rest = ranked.filter((p) => !named.includes(p));
  const restMB = rest.reduce((t, p) => t + used(p), 0);
  const colour = useColours(named);
  const label = `${int(s.usedMB)} of ${int(s.totalMB)} MB in use: ${ranked.map((p) => `${p} ${int(used(p))} MB`).join(", ")}${ranked.length ? ", " : ""}system ${int(s.tiffinMB)} MB, ${int(s.freeMB)} MB free`;

  const segs: Array<{ key: string; label: string; mb: number; fill: string; room?: number }> = [
    ...named.map((p) => {
      const cap = p === focus && s.caps[p] ? s.caps[p] * s.totalMB : 0;
      return { key: p, label: p, mb: used(p), fill: colour[p], room: Math.max(0, cap - used(p)) };
    }),
    ...(restMB > 0 ? [{ key: "_rest", label: focus ? "Other projects" : rest.length === 1 ? rest[0] : `${rest.length} more`, mb: restMB, fill: "var(--part-3)" }] : []),
    ...(s.tiffinMB > 0 ? [{ key: "_system", label: "System", mb: s.tiffinMB, fill: "var(--part-4)" }] : []),
  ];

  return (
    <div className={className}>
      <div className="flex h-3 overflow-hidden rounded-full bg-paper-sunk shadow-[inset_0_0_0_1px_var(--rule)]" role="img" aria-label={label}>
        {segs.map((g, i) => (
          <span key={g.key} title={`${g.label}: ${memWords(g.mb)}`} className="flex h-full" style={{ width: w(g.mb + (g.room ?? 0)) }}>
            <span
              className={cn("h-full min-w-[3px]", i > 0 && "border-l-2 border-paper")}
              style={{ background: g.fill, width: g.room ? `${(g.mb / (g.mb + g.room)) * 100}%` : "100%" }}
            />
            {!!g.room && <span className="h-full flex-1 border-y border-r border-dashed" style={{ borderColor: g.fill }} />}
          </span>
        ))}
      </div>
      {legend && (
        <ul className="mt-2.5 flex flex-wrap gap-x-5 gap-y-1.5 text-[0.8125rem] text-ink-2">
          {segs.map((g) => (
            <li key={g.key} className="inline-flex items-center gap-1.5">
              <i aria-hidden className="inline-block size-2.5 rounded-full" style={{ background: g.fill }} />
              <span className="text-ink">{g.label}</span>
              <span className="text-ink-3 tnum">{memWords(g.mb)}</span>
            </li>
          ))}
          <li className="inline-flex items-center gap-1.5">
            <i aria-hidden className="inline-block size-2.5 rounded-full bg-paper-sunk shadow-[inset_0_0_0_1px_var(--rule-2)]" />
            <span className="text-ink">Free</span>
            <span className="text-ink-3 tnum">{memWords(s.freeMB)}</span>
          </li>
        </ul>
      )}
    </div>
  );
}

/** Each named project's colour: its own enamel, or the next unused one when two would match side by side. */
function useColours(projects: string[]): Record<string, string> {
  const looks = useQueries({ queries: projects.map((p) => appearanceQuery(p)) });
  const taken = new Set<Enamel>();
  const out: Record<string, string> = {};
  projects.forEach((p, i) => {
    const own = looks[i]?.data?.enamel as Enamel | undefined;
    let e: Enamel = own && (ENAMELS as readonly string[]).includes(own) ? own : defaultEnamel(p);
    if (taken.has(e)) e = ENAMELS.find((x) => !taken.has(x)) ?? e;
    taken.add(e);
    out[p] = enamelVar(e);
  });
  return out;
}
