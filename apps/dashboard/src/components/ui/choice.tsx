import { Checkbox as C, RadioGroup as R, Select as S } from "radix-ui";
import { Check, ChevronDown } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/cn";

export function Checkbox({ className, ...props }: ComponentProps<typeof C.Root>) {
  return (
    <C.Root
      className={cn(
        "grid size-4 shrink-0 place-items-center rounded-[4px] border border-rule-2 bg-paper transition-colors hover:border-ink-3 data-[state=checked]:border-ink data-[state=checked]:bg-ink",
        className,
      )}
      {...props}
    >
      <C.Indicator className="animate-pop text-paper">
        <Check className="size-3" strokeWidth={3} />
      </C.Indicator>
    </C.Root>
  );
}

export const RadioGroup = R.Root;
/** A bare radio item to style as a tile or row (arrow keys move through its group). */
export const RadioItem = R.Item;

export function Radio({ className, ...props }: ComponentProps<typeof R.Item>) {
  return (
    <R.Item
      className={cn(
        "grid size-4 shrink-0 place-items-center rounded-full border border-rule-2 bg-paper transition-colors hover:border-ink-3 data-[state=checked]:border-ink data-[state=checked]:bg-ink",
        className,
      )}
      {...props}
    >
      <R.Indicator className="size-1.5 animate-pop rounded-full bg-paper" />
    </R.Item>
  );
}

export function Select({
  value,
  onValueChange,
  options,
  id,
}: {
  value: string;
  onValueChange: (v: string) => void;
  options: Array<{ value: string; label: ReactNode }>;
  id?: string;
}) {
  return (
    <S.Root value={value} onValueChange={onValueChange}>
      <S.Trigger
        id={id}
        className="flex h-9 w-full items-center justify-between gap-2 rounded-md border border-rule bg-paper px-3 text-base text-ink outline-none transition-colors hover:border-rule-2 focus-visible:border-brass data-[state=open]:border-rule-2"
      >
        <S.Value />
        <S.Icon>
          <ChevronDown className="size-4 text-ink-3" />
        </S.Icon>
      </S.Trigger>
      <S.Portal>
        <S.Content
          position="popper"
          sideOffset={6}
          className="z-50 min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-lg border border-rule bg-paper-raised p-1 shadow-raised data-[state=open]:animate-pop"
        >
          <S.Viewport>
            {options.map((o) => (
              <S.Item
                key={o.value}
                value={o.value}
                className="relative flex h-8 cursor-default items-center rounded-md pr-8 pl-2 text-base text-ink-2 outline-none select-none data-[highlighted]:bg-paper-sunk data-[highlighted]:text-ink"
              >
                <S.ItemText>{o.label}</S.ItemText>
                <S.ItemIndicator className="absolute right-2">
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
