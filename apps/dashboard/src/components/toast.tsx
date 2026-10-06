import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { X } from "lucide-react";
import { cn } from "@/lib/cn";

export type ToastAction = { label: string; run: () => void | Promise<unknown> };
export type ToastInput = {
  /** One sentence: "Scaled web to 3 instances in shop." */
  title: ReactNode;
  /** A second, quieter line. */
  detail?: ReactNode;
  /** Usually Undo. Shown as a button; Esc doesn't trigger it. */
  action?: ToastAction;
  tone?: "default" | "danger";
  /** ms; 8 s by default, long enough to reach Undo. */
  duration?: number;
};
type Item = ToastInput & { id: number; leaving?: boolean };

let items: Item[] = [];
let seq = 0;
const subs = new Set<() => void>();
const emit = () => subs.forEach((f) => f());

/**
 * Shows a toast, Sonner-style: "Deployed v42 to web · Undo" for 8 s.
 * Returns a function that dismisses it.
 *
 *   toast({ title: "Scaled web to 3 instances.", action: { label: "Undo", run: undo } })
 */
export function toast(t: ToastInput): () => void {
  const id = ++seq;
  items = [...items.slice(-2), { ...t, id }];
  emit();
  return () => dismiss(id);
}

export function dismiss(id: number) {
  if (!items.some((i) => i.id === id)) return;
  items = items.map((i) => (i.id === id ? { ...i, leaving: true } : i));
  emit();
  setTimeout(() => {
    items = items.filter((i) => i.id !== id);
    emit();
  }, 150);
}

/** Mount once (the Shell does). Bottom right on a desktop, bottom on a phone. */
export function Toaster() {
  const list = useSyncExternalStore(
    (f) => (subs.add(f), () => subs.delete(f)),
    () => items,
  );
  return (
    <section
      aria-label="Notifications"
      aria-live="polite"
      className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(400px,calc(100vw-2rem))] flex-col gap-2 max-sm:right-2 max-sm:bottom-2 max-sm:w-[calc(100vw-1rem)]"
    >
      {list.map((t) => (
        <ToastCard key={t.id} t={t} />
      ))}
    </section>
  );
}

function ToastCard({ t }: { t: Item }) {
  const [busy, setBusy] = useState(false);
  const [hover, setHover] = useState(false);
  useEffect(() => {
    if (hover || busy) return;
    const h = setTimeout(() => dismiss(t.id), t.duration ?? 8000);
    return () => clearTimeout(h);
  }, [t.id, t.duration, hover, busy]);
  return (
    <div
      role="status"
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      className={cn(
        "pointer-events-auto flex items-start gap-3 rounded-[10px] border bg-paper-raised py-2.5 pr-2 pl-3.5 shadow-raised",
        "animate-[rise_var(--dur-enter)_var(--ease-out)_both]",
        t.leaving && "animate-[fade-out_var(--dur-exit)_var(--ease-out)_both]",
        t.tone === "danger" ? "border-danger-rule" : "border-rule-2",
      )}
    >
      <div className="min-w-0 flex-1 py-0.5">
        <p className="text-[0.84375rem] leading-5 text-ink">{t.title}</p>
        {t.detail && <p className="mt-0.5 text-sm text-ink-3">{t.detail}</p>}
      </div>
      {t.action && (
        <button
          type="button"
          disabled={busy}
          className="h-7 shrink-0 rounded-[6px] px-2.5 text-[0.8125rem] font-[550] text-brass-ink transition-colors hover:bg-brass-wash disabled:opacity-50"
          onClick={async () => {
            setBusy(true);
            try {
              await t.action!.run();
            } finally {
              setBusy(false);
              dismiss(t.id);
            }
          }}
        >
          {busy ? "Working…" : t.action.label}
        </button>
      )}
      <button
        type="button"
        aria-label="Dismiss"
        className="grid size-7 shrink-0 place-items-center rounded-[6px] text-ink-3 transition-colors hover:bg-paper-hover hover:text-ink"
        onClick={() => dismiss(t.id)}
      >
        <X className="size-3.5" />
      </button>
    </div>
  );
}
