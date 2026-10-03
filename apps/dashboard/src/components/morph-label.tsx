import { useLayoutEffect, useRef, useState } from "react";

/**
 * A button label that changes words in place (Family-style): the old words
 * lift away, the button's width follows, the new words settle. Use it where
 * the same button moves the same commitment forward ("Review 3 changes" →
 * "Apply 3 changes to shop"). Put it inside any button; it animates width.
 */
export function MorphLabel({ text }: { text: string }) {
  const outer = useRef<HTMLSpanElement>(null);
  const measure = useRef<HTMLSpanElement>(null);
  const [shown, setShown] = useState(text);
  const [phase, setPhase] = useState<"idle" | "out" | "in">("idle");
  const [width, setWidth] = useState<number | undefined>(undefined);

  useLayoutEffect(() => {
    if (text === shown) return;
    const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (reduce || !outer.current || !measure.current) {
      setShown(text);
      return;
    }
    const from = outer.current.getBoundingClientRect().width;
    measure.current.textContent = text;
    const to = measure.current.getBoundingClientRect().width;
    setWidth(from);
    setPhase("out");
    const r = requestAnimationFrame(() => setWidth(to));
    const t1 = setTimeout(() => {
      setShown(text);
      setPhase("in");
    }, 110);
    const t2 = setTimeout(() => {
      setPhase("idle");
      setWidth(undefined);
    }, 400);
    return () => {
      cancelAnimationFrame(r);
      clearTimeout(t1);
      clearTimeout(t2);
    };
  }, [text, shown]);

  return (
    <span
      ref={outer}
      className="relative inline-block overflow-hidden whitespace-nowrap transition-[width] duration-[260ms] ease-[var(--ease-out)]"
      style={{ width }}
    >
      <span
        className="inline-block transition-[opacity,transform] duration-[140ms] ease-[var(--ease-out)]"
        style={{
          opacity: phase === "out" ? 0 : 1,
          transform: phase === "out" ? "translateY(-4px)" : "none",
        }}
        key={shown}
      >
        <span className={phase === "in" ? "inline-block animate-[rise_160ms_var(--ease-out)_both]" : undefined}>{shown}</span>
      </span>
      <span ref={measure} aria-hidden className="pointer-events-none invisible absolute top-0 left-0 whitespace-nowrap" />
    </span>
  );
}
