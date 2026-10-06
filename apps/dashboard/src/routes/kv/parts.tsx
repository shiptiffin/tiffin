import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { cn } from "@/lib/cn";

/** A small horizontal choice ("All · Kept · Cache"), arrow keys move through it. */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
  className,
}: {
  label: string;
  value: T;
  options: Array<{ value: T; label: string }>;
  onChange: (v: T) => void;
  className?: string;
}) {
  return (
    <RadioGroup
      aria-label={label}
      value={value}
      onValueChange={(v) => onChange(v as T)}
      orientation="horizontal"
      className={cn("inline-flex h-7 shrink-0 items-center rounded-[7px] bg-paper-sunk p-0.5", className)}
    >
      {options.map((o) => (
        <RadioItem
          key={o.value}
          value={o.value}
          className="h-6 rounded-[5px] px-2.5 text-[0.8125rem] text-ink-2 transition-colors hover:text-ink data-[state=checked]:bg-paper-raised data-[state=checked]:text-ink data-[state=checked]:shadow-[0_1px_2px_oklch(0.3_0.02_60/0.12)]"
        >
          {o.label}
        </RadioItem>
      ))}
    </RadioGroup>
  );
}

/** Typing in a field shouldn't trigger page shortcuts. */
export function typing(e: KeyboardEvent): boolean {
  const t = e.target;
  return e.metaKey || e.ctrlKey || e.altKey || (t instanceof Element && !!t.closest("input, textarea, select, [contenteditable], [role=dialog]"));
}
