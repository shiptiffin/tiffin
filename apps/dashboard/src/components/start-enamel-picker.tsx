import { cn } from "@/lib/cn";
import { ENAMELS, enamelNames, type Enamel } from "@/lib/enamel";
import { EnamelSwatch } from "./enamel-swatch";

/** The six enamels as a radio group: a project's colour, picked when it starts and changed on its page. */
export function EnamelPicker({ value, onChange, size = 26 }: { value: Enamel; onChange: (e: Enamel) => void; size?: number }) {
  return (
    <div role="radiogroup" aria-label="Colour" className="flex items-center gap-1.5">
      {ENAMELS.map((e) => (
        <button
          key={e}
          type="button"
          role="radio"
          aria-checked={value === e}
          aria-label={enamelNames[e]}
          title={enamelNames[e]}
          onClick={() => onChange(e)}
          className={cn(
            "grid place-items-center rounded-[7px] border transition-[border-color,transform] duration-[var(--dur-state)] active:scale-95",
            value === e ? "border-ink" : "border-transparent hover:border-rule-3",
          )}
          style={{ width: size + 8, height: size + 8 }}
        >
          <EnamelSwatch enamel={e} size={size} className="rounded-[4px]" />
        </button>
      ))}
    </div>
  );
}
