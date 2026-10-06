import { Dialog as D } from "radix-ui";
import { X } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/cn";

export const Dialog = D.Root;
export const DialogTrigger = D.Trigger;
export const DialogClose = D.Close;

export function DialogContent({
  className,
  children,
  tone = "default",
  hideClose,
  ...props
}: ComponentProps<typeof D.Content> & { tone?: "default" | "danger"; hideClose?: boolean }) {
  return (
    <D.Portal>
      <D.Overlay className="fixed inset-0 z-50 bg-[var(--scrim)] data-[state=open]:animate-fade" />
      <D.Content
        className={cn(
          "fixed left-1/2 top-[max(1rem,10vh)] z-50 flex max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] max-w-xl -translate-x-1/2 flex-col overflow-hidden rounded-[12px] border bg-paper-raised shadow-raised outline-none data-[state=open]:animate-pop",
          tone === "danger" ? "border-danger" : "border-rule-2",
          className,
        )}
        {...props}
      >
        {children}
        {!hideClose && (
          <D.Close
            className="absolute right-3 top-3 grid size-7 place-items-center rounded-md text-ink-3 transition-colors hover:bg-paper-sunk hover:text-ink"
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
  return <div className={cn("px-6 pt-6 pb-4 pr-12", className)}>{children}</div>;
}

export function DialogTitle({ className, ...props }: ComponentProps<typeof D.Title>) {
  return <D.Title className={cn("text-lg font-[550] tracking-[-0.01em] text-ink", className)} {...props} />;
}

export function DialogDescription({ className, ...props }: ComponentProps<typeof D.Description>) {
  return <D.Description className={cn("mt-1.5 text-base text-ink-2", className)} {...props} />;
}

export function DialogBody({ children, className, ...props }: ComponentProps<"div">) {
  return (
    <div className={cn("min-h-0 flex-1 overflow-y-auto px-6 pb-5", className)} {...props}>
      {children}
    </div>
  );
}

export function DialogFooter({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        "flex flex-col-reverse gap-2 border-t border-rule bg-paper-sunk/50 px-6 py-3.5 sm:flex-row sm:items-center sm:justify-end",
        className,
      )}
    >
      {children}
    </div>
  );
}
