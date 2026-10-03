import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";

export function CopyButton({ value, label = "Copy", className }: { value: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      aria-label={done ? "Copied" : label}
      onClick={async () => {
        if (await copyText(value)) {
          setDone(true);
          setTimeout(() => setDone(false), 1400);
        }
      }}
      className={cn("grid size-7 shrink-0 place-items-center rounded-md text-ink-3 transition-colors hover:bg-hover hover:text-ink", className)}
    >
      {done ? <Check className="size-3.5 animate-pop text-rev" /> : <Copy className="size-3.5" />}
    </button>
  );
}

/** A mono value with a copy button, e.g. a plan hash. Truncates in the middle-ish by CSS. */
export function CopyValue({ value, display, className }: { value: string; display?: string; className?: string }) {
  return (
    <span className={cn("group inline-flex min-w-0 items-center gap-1", className)}>
      <code className="truncate font-mono text-sm text-ink-2" title={value}>
        {display ?? value}
      </code>
      <CopyButton value={value} className="size-6 opacity-70 group-hover:opacity-100" />
    </span>
  );
}

/** A terminal line with a prompt and copy button. */
export function Command({ cmd, className }: { cmd: string; className?: string }) {
  return (
    <div className={cn("flex items-center gap-2 rounded-lg border border-rule bg-paper-sunk py-1.5 pr-1.5 pl-3", className)}>
      <span aria-hidden className="font-mono text-sm text-ink-4 select-none">
        $
      </span>
      <code className="min-w-0 flex-1 overflow-x-auto font-mono text-sm whitespace-nowrap text-ink [scrollbar-width:none]">{cmd}</code>
      <CopyButton value={cmd} label={`Copy: ${cmd}`} />
    </div>
  );
}
