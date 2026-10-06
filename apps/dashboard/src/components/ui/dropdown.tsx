import { DropdownMenu as M } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export const Menu = M.Root;
export const MenuTrigger = M.Trigger;
export const MenuGroup = M.Group;
export const MenuRadioGroup = M.RadioGroup;

export function MenuContent({ className, sideOffset = 6, ...props }: ComponentProps<typeof M.Content>) {
  return (
    <M.Portal>
      <M.Content
        sideOffset={sideOffset}
        className={cn(
          "z-50 min-w-48 overflow-hidden rounded-lg border border-rule bg-paper-raised p-1 shadow-raised data-[state=open]:animate-pop",
          className,
        )}
        {...props}
      />
    </M.Portal>
  );
}

const item =
  "relative flex h-8 cursor-default select-none items-center gap-2 rounded-md px-2 text-base text-ink-2 outline-none data-[highlighted]:bg-paper-hover data-[highlighted]:text-ink data-[disabled]:opacity-50 [&_svg]:size-4 [&_svg]:text-ink-3";

export function MenuItem({ className, ...props }: ComponentProps<typeof M.Item>) {
  return <M.Item className={cn(item, className)} {...props} />;
}

export function MenuRadioItem({ className, children, ...props }: ComponentProps<typeof M.RadioItem>) {
  return (
    <M.RadioItem className={cn(item, "pr-7", className)} {...props}>
      {children}
      <M.ItemIndicator className="absolute right-2 size-1.5 rounded-full bg-brass" />
    </M.RadioItem>
  );
}

export function MenuLabel({ className, ...props }: ComponentProps<typeof M.Label>) {
  return <M.Label className={cn("px-2 pt-1.5 pb-1 text-2xs font-medium tracking-wide text-ink-3 uppercase", className)} {...props} />;
}

export function MenuSeparator({ className, ...props }: ComponentProps<typeof M.Separator>) {
  return <M.Separator className={cn("-mx-1 my-1 h-px bg-rule", className)} {...props} />;
}
