import { X } from "lucide-react";
import { Dialog as D } from "radix-ui";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

/**
 * A panel from the right edge (the whole screen on a phone) for one row, a
 * new row or a table's columns. Focus stays inside it; Escape closes it.
 */
export function Sheet({
  open,
  onOpenChange,
  title,
  sub,
  children,
  footer,
  wide,
  focusId,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: ReactNode;
  sub?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  /** The element to focus when it opens (else the first field). */
  focusId?: string;
}) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-[var(--scrim)] data-[state=open]:animate-fade" />
        <D.Content
          aria-describedby={undefined}
          onOpenAutoFocus={(e) => {
            const el = focusId ? document.getElementById(focusId) : null;
            if (el) {
              e.preventDefault();
              el.focus();
            }
          }}
          className={cn(
            "fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-rule-2 bg-paper-raised shadow-overlay outline-none data-[state=open]:animate-[sheet-from-right_var(--dur-tray)_var(--ease-tray)_both]",
            wide ? "sm:max-w-[44rem]" : "sm:max-w-[30rem]",
          )}
        >
          <div className="flex items-start gap-3 border-b border-rule px-5 pt-5 pb-4">
            <div className="min-w-0 flex-1">
              <D.Title className="truncate text-lg font-[550] tracking-[-0.01em] text-ink">{title}</D.Title>
              {sub && <div className="mt-0.5 text-sm text-ink-3">{sub}</div>}
            </div>
            <D.Close aria-label="Close" className="grid size-7 shrink-0 place-items-center rounded-md text-ink-3 transition-colors hover:bg-paper-sunk hover:text-ink">
              <X className="size-4" />
            </D.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
          {footer && <div className="flex flex-wrap items-center gap-2 border-t border-rule bg-paper-sunk/50 px-5 py-3">{footer}</div>}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
