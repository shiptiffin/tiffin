import { useEffect, useMemo, useState } from "react";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { useWidth } from "./core";
import type { Shape } from "./world-geo";

/**
 * Visitors by country on a map: one hue (ink), darker for more, in five
 * steps on a log scale so one big country doesn't wash out the rest; the
 * chosen country in brass. Hover says the country and its count; a click
 * filters by it. The list beside it is the keyboard and screen-reader way
 * in, so the map itself is hidden from them.
 */
export function WorldMap({
  values,
  name,
  selected,
  onSelect,
  className,
}: {
  values: Record<string, number>;
  name: (code: string) => string;
  selected?: string;
  onSelect?: (code: string) => void;
  className?: string;
}) {
  const [wrap, width] = useWidth<HTMLDivElement>();
  const [shapes, setShapes] = useState<Shape[] | null>(null);
  const [hover, setHover] = useState<{ code: string; x: number; y: number } | null>(null);
  useEffect(() => {
    if (!width) return;
    let on = true;
    void import("./world-geo").then((m) => on && setShapes(m.worldShapes(width)));
    return () => {
      on = false;
    };
  }, [width]);
  const most = Math.max(1, ...Object.values(values));
  const step = useMemo(() => (v: number) => (v > 0 ? Math.min(5, 1 + Math.floor((Math.log(v) / Math.log(most + 1)) * 5)) : 0), [most]);
  const fills = ["var(--paper-sunk)", "0.16", "0.3", "0.46", "0.64", "0.84"];
  const height = width * 0.47;
  return (
    <div ref={wrap} className={cn("relative", className)} style={{ height: height || undefined, aspectRatio: height ? undefined : "1 / 0.47" }} aria-hidden>
      {shapes && (
        <svg width={width} height={height} className="block" onPointerLeave={() => setHover(null)}>
          {shapes.map((s, i) => {
            const v = values[s.code] ?? 0;
            const k = step(v);
            const on = selected === s.code;
            return (
              <path
                key={`${s.code}${i}`}
                d={s.d}
                fill={on ? "var(--brass)" : k === 0 ? fills[0] : "var(--data)"}
                fillOpacity={on || k === 0 ? 1 : Number(fills[k])}
                stroke={hover?.code === s.code && s.code ? "var(--ink)" : "var(--paper)"}
                strokeWidth={hover?.code === s.code && s.code ? 1 : 0.6}
                className={cn(v > 0 && onSelect && "cursor-pointer")}
                onPointerMove={(e) => {
                  const r = e.currentTarget.ownerSVGElement!.getBoundingClientRect();
                  setHover({ code: s.code, x: e.clientX - r.left, y: e.clientY - r.top });
                }}
                onClick={() => v > 0 && s.code && onSelect?.(s.code)}
              />
            );
          })}
        </svg>
      )}
      {hover && hover.code && (
        <div
          className="pointer-events-none absolute z-10 rounded-[8px] border border-rule-2 bg-paper-raised px-2.5 py-1.5 shadow-raised"
          style={{ left: Math.min(hover.x + 12, Math.max(0, width - 170)), top: Math.max(0, hover.y - 44) }}
        >
          <p className="text-[0.75rem] text-ink-3">{name(hover.code)}</p>
          <p className="text-[0.8125rem] font-[550] text-ink tnum">
            {int(values[hover.code] ?? 0)} {(values[hover.code] ?? 0) === 1 ? "visitor" : "visitors"}
          </p>
        </div>
      )}
    </div>
  );
}
