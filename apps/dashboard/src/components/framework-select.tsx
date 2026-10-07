import { FrameworkLogo } from "@/components/framework-logo";
import { Select } from "@/components/ui/choice";
import { cn } from "@/lib/cn";

/** id is the value; logo is the preset whose mark shows (default: the id). */
type Option = { id: string; name: string; logo?: string; disabled?: boolean };

/** A framework's mark and name, as one line. */
export function FrameworkLabel({ id, name, className }: { id: string; name: string; className?: string }) {
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-2", className)}>
      <FrameworkLogo preset={id} className="size-3.5 text-ink" />
      <span className="truncate">{name}</span>
    </span>
  );
}

/**
 * Picks a framework: the dashboard's one dropdown, with each framework's mark
 * beside its name (in the trigger too). With a single option it is a plain
 * label, since there is nothing to choose.
 */
export function FrameworkSelect({
  value,
  onChange,
  options,
  size = "md",
  className,
  "aria-label": ariaLabel,
  "aria-labelledby": labelledBy,
}: {
  value: string;
  onChange: (id: string) => void;
  options: Option[];
  size?: "sm" | "md";
  className?: string;
  "aria-label"?: string;
  "aria-labelledby"?: string;
}) {
  const only = options.length === 1 ? options[0] : undefined;
  if (only)
    return (
      <span className={cn("inline-flex items-center text-ink-2", size === "sm" ? "h-8 text-[0.8125rem]" : "h-9 text-[0.84375rem]", className)}>
        <FrameworkLabel id={only.logo ?? only.id} name={only.name} />
      </span>
    );
  return (
    <Select
      value={value}
      onValueChange={onChange}
      size={size}
      className={className}
      aria-label={ariaLabel}
      aria-labelledby={labelledBy}
      options={options.map((o) => ({ value: o.id, disabled: o.disabled, label: <FrameworkLabel id={o.logo ?? o.id} name={o.name} /> }))}
    />
  );
}
