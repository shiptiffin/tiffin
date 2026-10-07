// Why a job or a step failed: the message first, in full, then the stack
// folded under it (most people need the first frame, not all forty). The
// SDK's "don't retry" protocol shows as a sentence, not "HTTP 489".
import { ChevronRight } from "lucide-react";
import { Collapsible } from "radix-ui";
import { CopyButton } from "@/components/copy";
import { jobError } from "@/components/jobs-words";
import { cn } from "@/lib/cn";

const FRAME = /^\s*(at\s|[\w$.<>]+@|File ")/;

/** "Error: x\n    at f (a.ts:1)" → the message and the frames. */
export function splitStack(text: string): { message: string; frames: string[] } {
  const lines = text.replace(/\r/g, "").split("\n");
  const first = lines.findIndex((l) => FRAME.test(l));
  if (first <= 0) return { message: text.trim(), frames: [] };
  return { message: lines.slice(0, first).join("\n").trim(), frames: lines.slice(first).map((l) => l.trim()).filter(Boolean) };
}

export function ErrorBlock({ error, status, className, title = "Error" }: { error: string; status?: number; className?: string; title?: string }) {
  const e = jobError(error);
  if (!e) return null;
  const { message, frames } = splitStack(e.text);
  return (
    <section className={cn("min-w-0 overflow-hidden rounded-[10px] border border-danger-rule bg-danger-wash", className)} aria-label={title}>
      <header className="flex items-center gap-2 py-1 pr-1 pl-3.5">
        <h3 className="text-[0.8125rem] font-[550] text-danger">{title}</h3>
        {status !== undefined && status > 0 && status !== 489 && <span className="font-mono text-xs text-ink-3">HTTP {status}</span>}
        {e.gaveUp && <span className="text-xs text-ink-2">The app said not to retry, so Tiffin stopped there.</span>}
        <span className="ml-auto" />
        <CopyButton value={e.text} label="Copy the error" />
      </header>
      <p className="px-3.5 pb-3 font-mono text-[0.78rem] leading-5 break-words whitespace-pre-wrap text-ink">{message}</p>
      {frames.length > 0 && (
        <Collapsible.Root className="group border-t border-danger-rule">
          <Collapsible.Trigger className="flex h-8 w-full items-center gap-1.5 px-3.5 text-left text-xs font-[550] text-ink-2 outline-hidden select-none hover:text-ink focus-visible:text-ink">
            <ChevronRight className="size-3.5 transition-transform group-data-[state=open]:rotate-90" aria-hidden />
            Stack trace <span className="font-normal text-ink-3">{frames.length} frames</span>
          </Collapsible.Trigger>
          <Collapsible.Content>
            <pre className="max-h-72 overflow-auto px-3.5 pb-3 font-mono text-[0.72rem] leading-5 text-ink-2" tabIndex={0}>
              {frames.map((f, i) => (
                <span key={i} className={cn("block", i > 0 && /node_modules|node:internal/.test(f) && "text-ink-3")}>
                  {f}
                </span>
              ))}
            </pre>
          </Collapsible.Content>
        </Collapsible.Root>
      )}
    </section>
  );
}
