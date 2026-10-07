import type { ReactNode } from "react";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { cn } from "@/lib/cn";

/**
 * A horizontal choice of a few options (periods, ranges): a Radix radio
 * group, so arrow keys move and choose and only the chosen one is a tab
 * stop; the chosen one raised. A short label on phones when one is given.
 */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
  className,
}: {
  label: string;
  value: T | undefined;
  options: Array<{ value: T; label: ReactNode; short?: ReactNode; name?: string }>;
  onChange: (v: T) => void;
  className?: string;
}) {
  return (
    <RadioGroup
      aria-label={label}
      orientation="horizontal"
      value={value ?? ""}
      onValueChange={(v) => onChange(v as T)}
      className={cn("inline-flex shrink-0 rounded-[8px] border border-rule-2 bg-paper-sunk p-0.5", className)}
    >
      {options.map((o) => (
        <RadioItem
          key={o.value}
          value={o.value}
          aria-label={o.name}
          className="h-7 rounded-[6px] px-2.5 text-[0.8125rem] whitespace-nowrap text-ink-3 transition-colors duration-[var(--dur-state)] hover:text-ink sm:px-3 data-[state=checked]:bg-paper-lift data-[state=checked]:font-[550] data-[state=checked]:text-ink data-[state=checked]:shadow-[0_1px_2px_oklch(0.3_0.02_60/0.12)] data-[state=checked]:ring-1 data-[state=checked]:ring-rule-2"
        >
          {o.short ? (
            <>
              <span className="sm:hidden">{o.short}</span>
              <span className="max-sm:hidden">{o.label}</span>
            </>
          ) : (
            o.label
          )}
        </RadioItem>
      ))}
    </RadioGroup>
  );
}
