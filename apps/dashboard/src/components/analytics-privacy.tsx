import { Check, Copy } from "lucide-react";
import { Fragment, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Segmented } from "@/components/segmented";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";

/**
 * The privacy policy text the box writes for a project's analytics, as it
 * will read on the person's site (a preview) or as the Markdown they paste.
 * Only the little Markdown the box writes: ### headings, paragraphs, `code`
 * and [links](/path); everything is rendered as React text, never as HTML.
 */

function inline(s: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /`([^`]+)`|\[([^\]]+)\]\(([^)]+)\)/g;
  let at = 0;
  for (let m = re.exec(s); m; m = re.exec(s)) {
    if (m.index > at) out.push(s.slice(at, m.index));
    if (m[1] !== undefined)
      out.push(
        <code key={m.index} className="rounded-[4px] bg-paper-sunk px-1 py-px font-mono text-[0.8em] text-ink">
          {m[1]}
        </code>,
      );
    else
      out.push(
        // A link on the person's own site (/_tiffin/opt-out): shown, not followed from here.
        <span key={m.index} title={m[3]} className="text-ink underline decoration-rule-3 underline-offset-[3px]">
          {m[2]}
        </span>,
      );
    at = m.index + m[0].length;
  }
  if (at < s.length) out.push(s.slice(at));
  return out;
}

const blocksOf = (md: string) => md.trim().split(/\n{2,}/);

/** A brass rule beside text that is new against `before`. */
const mark = "relative before:absolute before:top-1 before:bottom-1 before:-left-3 before:w-[2px] before:rounded-full before:bg-brass";

/** The text as it reads; with `before`, what is new against it is marked. */
export function PrivacyPreview({ markdown, before, className }: { markdown: string; before?: string; className?: string }) {
  const blocks = blocksOf(markdown);
  const old = before === undefined ? null : new Set(blocksOf(before));
  return (
    <div className={cn("font-serif text-[0.96875rem] leading-[1.5rem] text-ink-2 [font-optical-sizing:auto]", className)}>
      {blocks.map((b, i) => {
        const isNew = old !== null && !old.has(b);
        const h = /^#{1,6}\s+(.*)$/.exec(b);
        if (h)
          return (
            <h3 key={i} data-new={isNew || undefined} className={cn("font-sans text-[0.875rem] leading-5 font-[550] text-ink", i > 0 && "mt-5", isNew && mark)}>
              {h[1]}
            </h3>
          );
        return (
          <p key={i} data-new={isNew || undefined} className={cn("mt-1.5 text-pretty", isNew && mark)}>
            {b.split("\n").map((line, k) => (
              <Fragment key={k}>
                {k > 0 && " "}
                {inline(line)}
              </Fragment>
            ))}
          </p>
        );
      })}
    </div>
  );
}

/** Copy, with "Copied" for a moment; onCopied runs once the clipboard has it. */
export function CopyText({ value, label, onCopied, className }: { value: string; label: string; onCopied?: () => void; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        if (await copyText(value)) {
          setDone(true);
          onCopied?.();
          setTimeout(() => setDone(false), 1600);
        }
      }}
      className={cn(
        "inline-flex h-7 shrink-0 items-center gap-1.5 rounded-[6px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] font-[550] text-ink transition-colors hover:bg-paper-hover",
        className,
      )}
    >
      {done ? <Check aria-hidden className="size-3.5 animate-pop text-ok" /> : <Copy aria-hidden className="size-3.5 text-ink-3" />}
      <span aria-live="polite">{done ? "Copied" : label}</span>
    </button>
  );
}

/**
 * The text in a box: Preview (as it reads) or Markdown (what is copied),
 * with Copy in its header. `aside` sits beside the header's switch (the version).
 */
export function PrivacyBox({
  markdown,
  before,
  onCopied,
  aside,
  maxHeight,
  className,
}: {
  markdown: string;
  /** The text as it is now: what's new in `markdown` is marked, and scrolled to. */
  before?: string;
  onCopied?: () => void;
  aside?: ReactNode;
  maxHeight?: string;
  className?: string;
}) {
  const [view, setView] = useState<"preview" | "markdown">("preview");
  const body = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = body.current;
    const first = el?.querySelector<HTMLElement>("[data-new]");
    if (!el || !first || view !== "preview") return;
    // From its section's heading when that is unchanged; the box is relative, so offsetTop is within it.
    const prev = first.previousElementSibling as HTMLElement | null;
    const at = prev?.tagName === "H3" ? prev : first;
    el.scrollTop = Math.max(0, at.offsetTop - 14);
  }, [view, markdown]);
  return (
    <div className={cn("min-w-0 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised", className)}>
      <div className="flex items-center gap-2 border-b border-rule bg-paper-sunk/60 py-1.5 pr-1.5 pl-1.5">
        <Segmented
          label="Show the text as"
          value={view}
          onChange={setView}
          options={[
            { value: "preview", label: "Preview" },
            { value: "markdown", label: "Markdown" },
          ]}
        />
        {before !== undefined && view === "preview" ? (
          <span className="flex min-w-0 items-center gap-1.5 truncate text-[0.75rem] text-ink-3 max-sm:hidden">
            <span aria-hidden className="h-3 w-[2px] shrink-0 rounded-full bg-brass" />
            New text
          </span>
        ) : (
          aside && <span className="min-w-0 truncate text-[0.75rem] text-ink-3 max-sm:hidden">{aside}</span>
        )}
        <CopyText value={markdown} label="Copy text" onCopied={onCopied} className="ml-auto" />
      </div>
      <div ref={body} tabIndex={0} aria-label="Privacy text" className="relative overflow-y-auto px-4 py-3.5 sm:px-5" style={{ maxHeight }}>
        {view === "preview" ? (
          <PrivacyPreview markdown={markdown} before={before} />
        ) : (
          <pre className="font-mono text-[0.75rem] leading-5 whitespace-pre-wrap text-ink-2 [overflow-wrap:anywhere]">{markdown}</pre>
        )}
      </div>
    </div>
  );
}
