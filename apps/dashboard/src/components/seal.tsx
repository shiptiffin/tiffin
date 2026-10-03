import { useId, useLayoutEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";
import { MARK_PATHS } from "./logo";

/**
 * The brass seal: the only flourish in the product. It draws itself once
 * (about 600 ms) when a person signs, and is static everywhere else
 * (receipts, the Ledger). With reduced motion it simply appears.
 *
 *   <Seal name="Bilal" date="3 Oct 2026" plan="d746 1a9e" animate />
 */
export function Seal({
  name,
  date,
  plan,
  animate,
  size = 128,
  className,
}: {
  name: string;
  date: string;
  plan?: string;
  /** Draw it now (once). Leave off for a seal that was already signed. */
  animate?: boolean;
  size?: number;
  className?: string;
}) {
  const ref = useRef<SVGSVGElement>(null);
  const id = useId().replace(/:/g, "");
  const [drawing, setDrawing] = useState(false);
  useLayoutEffect(() => {
    if (!animate || !ref.current) return;
    ref.current.querySelectorAll<SVGGeometryElement>(".draw").forEach((p) => {
      p.style.setProperty("--len", String(Math.ceil(p.getTotalLength?.() ?? 400)));
    });
    setDrawing(true);
  }, [animate]);
  const text = ["SIGNED", name.toUpperCase(), date.toUpperCase(), plan ? `PLAN ${plan.toUpperCase()}` : null].filter(Boolean).join(" · ") + " ·";
  return (
    <svg
      ref={ref}
      width={size}
      height={size}
      viewBox="0 0 120 120"
      fill="none"
      stroke="currentColor"
      className={cn("seal", className)}
      data-animate={drawing ? "" : undefined}
      role="img"
      aria-label={`Seal: signed by ${name}, ${date}${plan ? `, plan ${plan}` : ""}`}
    >
      <defs>
        <path id={`ring-${id}`} d="M60 60 m-47 0 a47 47 0 1 1 94 0 a47 47 0 1 1 -94 0" />
      </defs>
      <circle className="draw" cx="60" cy="60" r="56" strokeWidth="1.25" />
      <circle className="draw" cx="60" cy="60" r="38" strokeWidth="1" />
      <text className="fade" fill="currentColor" stroke="none" fontFamily="Instrument Sans, sans-serif" fontSize="7.2" fontWeight="600" letterSpacing="0.6">
        <textPath href={`#ring-${id}`} textLength="292" lengthAdjust="spacing">
          {text}
        </textPath>
      </text>
      <g transform="translate(42 42) scale(1.125)" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
        {MARK_PATHS.map((d) => (
          <path key={d} className="draw" d={d} />
        ))}
      </g>
    </svg>
  );
}
