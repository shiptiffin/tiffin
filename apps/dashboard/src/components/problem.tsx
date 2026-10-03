import { CircleAlert } from "lucide-react";
import { ApiError } from "@/api/client";
import { cn } from "@/lib/cn";

/** Shows an API problem as words: detail, then the hint. Never raw JSON. */
export function ProblemNote({ error, className, title }: { error: unknown; className?: string; title?: string }) {
  if (!error) return null;
  const p = error instanceof ApiError ? error.problem : undefined;
  const detail = p?.detail ?? (error instanceof Error ? error.message : "Something went wrong.");
  return (
    <div role="alert" className={cn("flex gap-3 rounded-lg border border-danger-rule bg-danger-wash px-4 py-3", className)}>
      <CircleAlert className="mt-0.5 size-4 shrink-0 text-danger" />
      <div className="min-w-0 text-base">
        <p className="font-medium text-ink">{title ?? sentence(detail)}</p>
        {title && <p className="mt-0.5 text-ink-2">{sentence(detail)}</p>}
        {p?.errors && p.errors.length > 0 && (
          <ul className="mt-1 space-y-0.5 text-ink-2">
            {p.errors.map((e, i) => (
              <li key={i}>
                {e.path && <code className="mr-1 font-mono text-sm text-ink-3">{e.path}</code>}
                {e.message}
              </li>
            ))}
          </ul>
        )}
        {p?.hint && !p.approvalUrl && (
          <p className="mt-1 text-ink-3">
            <Code text={sentence(p.hint)} />
          </p>
        )}
        {p?.approvalUrl && (
          <p className="mt-1 text-ink-2">
            This needs a human's approval.{" "}
            <a href={new URL(p.approvalUrl, location.origin).pathname} className="font-medium text-ink underline underline-offset-4">
              Open the approval request
            </a>
            .
          </p>
        )}
      </div>
    </div>
  );
}

export function sentence(s: string) {
  const t = s.trim();
  if (!t) return t;
  return t[0].toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? "" : ".");
}

/** Renders `backticked` spans as code. */
export function Code({ text }: { text: string }) {
  const parts = text.split(/(`[^`]+`)/g);
  return (
    <>
      {parts.map((p, i) =>
        p.startsWith("`") && p.endsWith("`") ? (
          <code key={i} className="rounded-xs bg-paper-sunk px-1 py-px font-mono text-[0.92em] text-ink">
            {p.slice(1, -1)}
          </code>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </>
  );
}
