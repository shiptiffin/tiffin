import { Checkbox as C, RadioGroup as R, Select as S } from "radix-ui";
import { Check, ChevronDown } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/cn";

export function Checkbox({ className, ...props }: ComponentProps<typeof C.Root>) {
  return (
    <C.Root
      data-slot="checkbox"
      className={cn(
        "grid size-4 shrink-0 place-items-center rounded-[4px] border border-rule-2 bg-paper transition-colors hover:border-ink-3 data-[state=checked]:border-ink data-[state=checked]:bg-ink",
        className,
      )}
      {...props}
    >
      <C.Indicator data-slot="checkbox-indicator" className="animate-pop text-paper">
        <Check className="size-3" strokeWidth={3} />
      </C.Indicator>
    </C.Root>
  );
}

export function RadioGroup(props: ComponentProps<typeof R.Root>) {
  return <R.Root data-slot="radio-group" {...props} />;
}

/** A bare radio item to style as a tile or row (arrow keys move through its group). */
export function RadioItem(props: ComponentProps<typeof R.Item>) {
  return <R.Item data-slot="radio-group-item" {...props} />;
}

export function Radio({ className, ...props }: ComponentProps<typeof R.Item>) {
  return (
    <R.Item
      data-slot="radio"
      className={cn(
        "grid size-4 shrink-0 place-items-center rounded-full border border-rule-2 bg-paper transition-colors hover:border-ink-3 data-[state=checked]:border-ink data-[state=checked]:bg-ink",
        className,
      )}
      {...props}
    >
      <R.Indicator data-slot="radio-indicator" className="size-1.5 animate-pop rounded-full bg-paper" />
    </R.Item>
  );
}

/**
 * A dropdown: the one used everywhere in the dashboard (never a native
 * <select>). Values are strings; `size` matches the field it sits beside.
 *
 *   <Select value={v} onValueChange={setV} options={[{ value: "a", label: "A" }]} aria-label="Sort" />
 */
export function Select({
  value,
  onValueChange,
  options,
  id,
  size = "md",
  className,
  disabled,
  placeholder,
  "aria-label": ariaLabel,
  "aria-labelledby": labelledBy,
}: {
  value: string;
  onValueChange: (v: string) => void;
  options: Array<{ value: string; label: ReactNode; disabled?: boolean }>;
  id?: string;
  /** sm: 32 px, beside toolbar buttons; md: 36 px, form fields. */
  size?: "sm" | "md";
  /** Width and placement; the trigger fills its container by default. */
  className?: string;
  disabled?: boolean;
  placeholder?: string;
  "aria-label"?: string;
  "aria-labelledby"?: string;
}) {
  return (
    <S.Root value={value} onValueChange={onValueChange} disabled={disabled}>
      {/* Width, min-width and height are zero-specificity (:where) so a caller's w-44, sm:w-36 or min-w-[8rem] wins without tailwind-merge. */}
      <S.Trigger
        data-slot="select-trigger"
        id={id}
        aria-label={ariaLabel}
        aria-labelledby={labelledBy}
        className={cn(
          "flex [:where(&)]:min-w-0 items-center [:where(&)]:w-full justify-between gap-2 rounded-[7px] border border-rule-2 bg-paper-raised pr-2.5 pl-3 text-left text-ink outline-hidden transition-[border-color,box-shadow] hover:border-rule-3 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] disabled:cursor-not-allowed disabled:opacity-55 data-[state=open]:border-rule-3 data-[placeholder]:text-ink-3",
          size === "sm" ? "[:where(&)]:h-8 [:where(&)]:text-[0.8125rem]" : "[:where(&)]:h-9 [:where(&)]:text-[0.84375rem]",
          className,
        )}
      >
        <span className="min-w-0 truncate">
          <S.Value placeholder={placeholder} />
        </span>
        <S.Icon className="shrink-0">
          <ChevronDown className="size-4 text-ink-3" />
        </S.Icon>
      </S.Trigger>
      <S.Portal>
        <S.Content
          data-slot="select-content"
          position="popper"
          sideOffset={6}
          collisionPadding={12}
          className="z-50 max-h-[min(22rem,var(--radix-select-content-available-height))] min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised p-1 shadow-overlay data-[state=open]:animate-pop"
        >
          <S.Viewport>
            {options.map((o) => (
              <S.Item
                data-slot="select-item"
                key={o.value}
                value={o.value}
                disabled={o.disabled}
                className={cn(
                  "relative flex cursor-default items-center rounded-[6px] pr-8 pl-2.5 text-ink-2 outline-hidden select-none data-[disabled]:opacity-45 data-[highlighted]:bg-paper-hover data-[highlighted]:text-ink data-[state=checked]:text-ink",
                  size === "sm" ? "h-8 text-[0.8125rem]" : "h-9 text-[0.84375rem]",
                )}
              >
                <S.ItemText>{o.label}</S.ItemText>
                <S.ItemIndicator className="absolute right-2.5">
                  <Check className="size-3.5 text-ink" />
                </S.ItemIndicator>
              </S.Item>
            ))}
          </S.Viewport>
        </S.Content>
      </S.Portal>
    </S.Root>
  );
}
