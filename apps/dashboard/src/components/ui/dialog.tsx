import { Dialog as D } from "radix-ui";
import { X } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/cn";

export const Dialog = D.Root;

export function DialogTrigger(props: ComponentProps<typeof D.Trigger>) {
  return <D.Trigger data-slot="dialog-trigger" {...props} />;
}

export function DialogClose(props: ComponentProps<typeof D.Close>) {
  return <D.Close data-slot="dialog-close" {...props} />;
}

/**
 * The panel sits 10vh from the top (at least 1rem) and keeps 1rem below, so
 * its height cap subtracts that top offset rather than a flat 2rem.
 * Width (max-w-xl) and height cap are zero-specificity: pass max-w-md,
 * sm:max-w-2xl or max-w-[44rem] in className and it wins.
 */
export const dialogPanel =
  "fixed left-1/2 top-[max(1rem,10vh)] z-50 flex [:where(&)]:max-h-[calc(100dvh-max(1rem,10vh)-1rem)] [:where(&)]:w-[calc(100vw-2rem)] [:where(&)]:max-w-xl -translate-x-1/2 flex-col overflow-hidden rounded-[12px] border bg-paper-raised shadow-raised outline-hidden data-[state=open]:animate-pop";

export const dialogOverlay = "fixed inset-0 z-50 bg-[var(--scrim)] data-[state=open]:animate-fade";

export function DialogContent({
  className,
  children,
  tone = "default",
  hideClose,
  ...props
}: ComponentProps<typeof D.Content> & { tone?: "default" | "danger"; hideClose?: boolean }) {
  return (
    <D.Portal>
      <D.Overlay data-slot="dialog-overlay" className={dialogOverlay} />
      <D.Content
        data-slot="dialog-content"
        className={cn(dialogPanel, tone === "danger" ? "border-danger" : "border-rule-2", className)}
        {...props}
      >
        {children}
        {!hideClose && (
          <D.Close
            data-slot="dialog-close"
            className="absolute right-3 top-3 grid size-7 place-items-center rounded-md text-ink-3 transition-colors hover:bg-paper-hover hover:text-ink"
            aria-label="Close"
          >
            <X className="size-4" />
          </D.Close>
        )}
      </D.Content>
    </D.Portal>
  );
}

export function DialogHeader({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div data-slot="dialog-header" className={cn("[:where(&)]:px-6 [:where(&)]:pt-6 [:where(&)]:pb-4 [:where(&)]:pr-12", className)}>
      {children}
    </div>
  );
}

export function DialogTitle({ className, ...props }: ComponentProps<typeof D.Title>) {
  return <D.Title data-slot="dialog-title" className={cn("[:where(&)]:text-lg font-[550] tracking-[-0.01em] [:where(&)]:text-ink", className)} {...props} />;
}

export function DialogDescription({ className, ...props }: ComponentProps<typeof D.Description>) {
  return <D.Description data-slot="dialog-description" className={cn("[:where(&)]:mt-1.5 [:where(&)]:text-base [:where(&)]:text-ink-2", className)} {...props} />;
}

export function DialogBody({ children, className, ...props }: ComponentProps<"div">) {
  return (
    <div data-slot="dialog-body" className={cn("[:where(&)]:min-h-0 flex-1 overflow-y-auto [:where(&)]:px-6 [:where(&)]:pb-5", className)} {...props}>
      {children}
    </div>
  );
}

export function DialogFooter({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      data-slot="dialog-footer"
      className={cn(
        "flex flex-col-reverse [:where(&)]:gap-2 border-t border-rule bg-paper-sunk/50 [:where(&)]:px-6 [:where(&)]:py-3.5 sm:flex-row sm:items-center sm:justify-end",
        className,
      )}
    >
      {children}
    </div>
  );
}
