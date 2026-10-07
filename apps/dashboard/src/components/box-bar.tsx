import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { memWords, type Shares } from "@/lib/usage";

/**
 * The box as one bar: each project's share in a neutral shade (hover one to
 * see its name and size), biggest first, Tiffin itself in a lighter one, the
 * room left dashed. Projects under 1.5% of the box share one segment, so the
 * bar reads the same with 3 projects or 50. The project you're looking at
 * (`focus`) is brass, and its limit shows as a faint outline its share can
 * grow into (only its own: every project's limit at once would overflow).
 *
 *   <BoxBar shares={s} order={["shop", "notes"]} focus="shop" />
 */
export function BoxBar({ shares: s, order, focus, legend, className }: { shares: Shares; order: string[]; focus?: string; legend?: boolean; className?: string }) {
  const w = (mb: number) => `${Math.max(0, (mb / s.totalMB) * 100)}%`;
  const used = (p: string) => s.projects[p] ?? 0;
  const ranked = order.filter((p) => used(p) > 0 || p === focus).sort((a, b) => used(b) - used(a));
  const small = (p: string) => p !== focus && used(p) / s.totalMB < 0.015;
  const shown = ranked.filter((p) => !small(p));
  const rest = ranked.filter(small);
  const restMB = rest.reduce((t, p) => t + used(p), 0);
  const label = `${int(s.usedMB)} of ${int(s.totalMB)} MB in use: ${ranked.map((p) => `${p} ${int(used(p))} MB`).join(", ")}${ranked.length ? ", " : ""}Tiffin ${int(s.tiffinMB)} MB, ${int(s.freeMB)} MB free`;
  // Alternate two ink shades so neighbouring projects stay apart without colour.
  const shade = (i: number) => (i % 2 === 0 ? "bg-ink-3" : "bg-ink-4");
  return (
    <div className={className}>
      <div className="flex h-3 gap-[3px]" role="img" aria-label={label}>
        {shown.map((p, i) => {
          const mb = used(p);
          const cap = p === focus && s.caps[p] ? s.caps[p] * s.totalMB : 0;
          const room = Math.max(0, cap - mb);
          const tip = `${p}: ${memWords(mb)}${cap ? `, may grow to ${memWords(cap)}` : ""}`;
          return (
            <span key={p} title={tip} className="group/seg flex h-full" style={{ width: w(mb + room) }}>
              <span
                className={cn("h-full min-w-[4px] rounded-[3px] transition-[filter] duration-[var(--dur-state)] group-hover/seg:brightness-90", p === focus ? "bg-brass" : shade(i), room > 0 && "rounded-r-none")}
                style={{ width: mb + room > 0 ? `${(mb / (mb + room)) * 100}%` : "100%" }}
              />
              {room > 0 && <span className="h-full flex-1 rounded-r-[3px] border border-l-0 border-dashed border-brass" />}
            </span>
          );
        })}
        {restMB > 0 && (
          <span
            title={`${rest.length === 1 ? rest[0] : `${rest.length} smaller projects`}: ${memWords(restMB)}`}
            className={cn("h-full min-w-[4px] rounded-[3px] transition-[filter] duration-[var(--dur-state)] hover:brightness-90", shade(shown.length))}
            style={{ width: w(restMB) }}
          />
        )}
        {s.tiffinMB > 0 && <span title={`Tiffin itself: ${memWords(s.tiffinMB)}`} className="h-full min-w-[4px] rounded-[3px] bg-[var(--part-4)]" style={{ width: w(s.tiffinMB) }} />}
        <span title={`Free: ${memWords(s.freeMB)}`} className="h-full flex-1 rounded-[3px] border border-dashed border-rule-3" />
      </div>
      {legend && (
        <p className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-2 rounded-[2px] bg-ink-3" />
            Your projects
          </span>
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-2 rounded-[2px] bg-[var(--part-4)]" />
            Tiffin itself
          </span>
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-2 rounded-[2px] border border-dashed border-rule-3" />
            Free
          </span>
        </p>
      )}
    </div>
  );
}
