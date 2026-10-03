import { cn } from "@/lib/cn";
import { bytesParts, NNBSP } from "@/lib/format";

/**
 * A number with a small unit: <Qty value="1.65" unit="GB" />. The value
 * carries the hierarchy; the unit is smaller and quieter, joined by a narrow
 * no-break space so it never wraps away. Tabular figures.
 */
export function Qty({ value, unit, of, className }: { value: string; unit?: string; of?: string; className?: string }) {
  return (
    <span className={cn("tnum whitespace-nowrap", className)}>
      {value}
      {unit && (
        <span className="u">
          {NNBSP}
          {unit}
        </span>
      )}
      {of && <span className="u"> {of}</span>}
    </span>
  );
}

/** Bytes as a Qty: <Bytes n={1771094016} /> → 1.6 GB. */
export function Bytes({ n, digits, className }: { n: number | null | undefined; digits?: number; className?: string }) {
  const p = bytesParts(n, digits);
  return <Qty value={p.value} unit={p.unit} className={className} />;
}
