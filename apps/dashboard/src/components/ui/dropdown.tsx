import { DropdownMenu as M } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export const Menu = M.Root;

export function MenuTrigger(props: ComponentProps<typeof M.Trigger>) {
  return <M.Trigger data-slot="menu-trigger" {...props} />;
}

export function MenuGroup(props: ComponentProps<typeof M.Group>) {
  return <M.Group data-slot="menu-group" {...props} />;
}

export function MenuRadioGroup(props: ComponentProps<typeof M.RadioGroup>) {
  return <M.RadioGroup data-slot="menu-radio-group" {...props} />;
}

export function MenuContent({ className, sideOffset = 6, ...props }: ComponentProps<typeof M.Content>) {
  return (
    <M.Portal>
      <M.Content
        data-slot="menu-content"
        sideOffset={sideOffset}
        className={cn(
          "z-50 [:where(&)]:min-w-48 overflow-hidden rounded-lg border border-rule bg-paper-raised [:where(&)]:p-1 shadow-raised data-[state=open]:animate-pop",
          className,
        )}
        {...props}
      />
    </M.Portal>
  );
}

// Height, alignment and colours are zero-specificity so a caller's h-auto or
// items-start wins. Destructive items use variant="danger", not colour classes.
const item =
  "relative flex [:where(&)]:h-8 cursor-default select-none [:where(&)]:items-center gap-2 rounded-md px-2 [:where(&)]:text-base outline-hidden data-[disabled]:opacity-50 [&_svg]:size-4";
const tones = {
  default: "[:where(&)]:text-ink-2 [:where(&)]:data-[highlighted]:bg-paper-hover [:where(&)]:data-[highlighted]:text-ink [:where(&)]:[&_svg]:text-ink-3",
  danger: "[:where(&)]:text-danger [:where(&)]:data-[highlighted]:bg-danger-wash [:where(&)]:data-[highlighted]:text-danger [:where(&)]:[&_svg]:text-danger",
} as const;

export function MenuItem({ className, variant = "default", ...props }: ComponentProps<typeof M.Item> & { variant?: keyof typeof tones }) {
  return <M.Item data-slot="menu-item" data-variant={variant} className={cn(item, tones[variant], className)} {...props} />;
}

export function MenuRadioItem({ className, children, ...props }: ComponentProps<typeof M.RadioItem>) {
  return (
    <M.RadioItem data-slot="menu-radio-item" className={cn(item, tones.default, "pr-7", className)} {...props}>
      {children}
      <M.ItemIndicator className="absolute right-2 size-1.5 rounded-full bg-brass" />
    </M.RadioItem>
  );
}

export function MenuLabel({ className, ...props }: ComponentProps<typeof M.Label>) {
  return (
    <M.Label
      data-slot="menu-label"
      className={cn("[:where(&)]:px-2 [:where(&)]:pt-1.5 [:where(&)]:pb-1 text-2xs font-medium tracking-wide [:where(&)]:text-ink-3 uppercase", className)}
      {...props}
    />
  );
}

export function MenuSeparator({ className, ...props }: ComponentProps<typeof M.Separator>) {
  return <M.Separator data-slot="menu-separator" className={cn("-mx-1 [:where(&)]:my-1 h-px bg-rule", className)} {...props} />;
}
