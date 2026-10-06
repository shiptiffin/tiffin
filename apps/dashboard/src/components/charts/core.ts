import { useEffect, useLayoutEffect, useRef, useState } from "react";

/**
 * The chart layer's shared parts: one scale for the value axis (its ticks
 * are the gridlines and the printed numbers), time ticks on round hours and
 * days, the plot's measured width, and reduced motion. Charts draw with
 * d3-shape on these; nothing here knows about a library.
 */

export type XY = [number, number]; // [ms, value]

/**
 * A round top for the axis and evenly spaced round ticks from 0 (0 / 200 /
 * 400 / 600, 0 / 2.5 / 5): the lowest top that takes at most `ticks` + 1
 * steps, so the data fills the height.
 */
export function niceScale(max: number, ticks = 3): { top: number; ticks: number[] } {
  if (!(max > 0) || !Number.isFinite(max)) return { top: 1, ticks: [0, 1] };
  const p = Math.pow(10, Math.floor(Math.log10(max / ticks)));
  let best = { top: Infinity, step: 0, n: 0 };
  for (const m of [1, 2, 2.5, 3, 4, 5, 10, 20]) {
    const step = m * p;
    const n = Math.max(1, Math.ceil(max / step - 1e-9));
    if (n >= 2 && n <= ticks + 1 && step * n < best.top) best = { top: step * n, step, n };
  }
  if (!best.n) best = { top: 10 * p * Math.ceil(max / (10 * p)), step: 10 * p * Math.ceil(max / (10 * p)), n: 1 };
  return { top: best.top, ticks: Array.from({ length: best.n + 1 }, (_, i) => best.step * i) };
}

const HOUR = 3_600_000;
const DAY = 86_400_000;

/**
 * Where to print time labels: at most `want` of them, on round hours (every
 * 1, 2, 3, 6 or 12) or days (every 1, 2, 7 or 14), in UTC or local time.
 */
export function timeTicks(t0: number, t1: number, want: number, utc: boolean): number[] {
  const span = t1 - t0;
  if (!(span > 0)) return [];
  const off = utc ? 0 : new Date(t0).getTimezoneOffset() * 60_000;
  const steps = [5 * 60_000, 15 * 60_000, 30 * 60_000, HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, DAY, 2 * DAY, 7 * DAY, 14 * DAY, 30 * DAY];
  const step = steps.find((s) => span / s <= want) ?? 30 * DAY;
  const out: number[] = [];
  // Local midnight-aligned: shift into local time, round, shift back.
  let t = Math.ceil((t0 - off) / step) * step + off;
  if (step === 7 * DAY || step === 14 * DAY) t = Math.ceil((t0 - off) / DAY) * DAY + off; // weeks start where the data does
  for (; t <= t1; t += step) out.push(t);
  return out;
}

/** The width an element was laid out at, kept up to date. */
export function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.getBoundingClientRect().width);
    const ro = new ResizeObserver(([e]) => setW(e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w] as const;
}

/** True when the person asked for less motion. */
export function useReducedMotion() {
  const [r, setR] = useState(() => typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches);
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const m = matchMedia("(prefers-reduced-motion: reduce)");
    const on = () => setR(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return r;
}

/** Index of the point nearest x in a sorted array (binary search). */
export function nearest(xs: ArrayLike<number>, x: number): number {
  let lo = 0;
  let hi = xs.length - 1;
  if (hi < 0) return -1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (xs[mid] < x) lo = mid;
    else hi = mid;
  }
  return Math.abs(xs[lo] - x) <= Math.abs(xs[hi] - x) ? lo : hi;
}

const fmt = (o: Intl.DateTimeFormatOptions, utc: boolean) => new Intl.DateTimeFormat("en-GB", { ...o, ...(utc ? { timeZone: "UTC" } : {}) });
const F = {
  true: {
    hm: fmt({ hour: "2-digit", minute: "2-digit", hourCycle: "h23" }, true),
    dm: fmt({ day: "numeric", month: "short" }, true),
    wdm: fmt({ weekday: "short", day: "numeric", month: "short" }, true),
    whm: fmt({ weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" }, true),
  },
  false: {
    hm: fmt({ hour: "2-digit", minute: "2-digit", hourCycle: "h23" }, false),
    dm: fmt({ day: "numeric", month: "short" }, false),
    wdm: fmt({ weekday: "short", day: "numeric", month: "short" }, false),
    whm: fmt({ weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" }, false),
  },
};

/** A tick label: "14:00" within a day or two, "3 Oct" beyond. */
export function tickLabel(t: number, span: number, utc: boolean) {
  const f = F[`${utc}`];
  return span <= 2 * DAY ? f.hm.format(t) : f.dm.format(t);
}

/** A hover label for a step starting at t: "Fri 3 Oct" for days, "Fri 3 Oct, 14:00–15:00" for hours. */
export function stepLabel(t: number, step: number, utc: boolean) {
  const f = F[`${utc}`];
  if (step >= DAY) return f.wdm.format(t);
  return `${f.whm.format(t)}–${f.hm.format(t + step)}`;
}
