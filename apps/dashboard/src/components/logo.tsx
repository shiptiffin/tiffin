import { useId } from "react";
import { cn } from "@/lib/cn";

/**
 * The mark: the mascot, drawn small. A stacked steel tin with a carry handle,
 * three curved tier lines, stub hands and a face on the middle tier. 32-unit
 * grid. Its colours are tokens (--mark-*): ink lines on steel in light; in
 * dark, bright polished steel with a soft outline, so it doesn't glare white.
 *
 * markSvg gives the same drawing with literal colours, for the favicon.
 */
const HANDLE = "M11.5 9V6.5a3 3 0 0 1 3-3h3a3 3 0 0 1 3 3V9";
const BODY = "M7 12q0-3.2 9-3.2t9 3.2v13q0 3.5-9 3.5t-9-3.5z";
const TIERS = "M7 12.6q9 2.6 18 0M7 17.4q9 2.6 18 0M7 24.2q9 2.6 18 0";
const SMILE = "M14.9 22q1.1 1 2.2 0";
const SHINE = [0, 0.28, 0.42, 1];

type Colours = { line: string; handle: string; hand: string; body: [string, string, string, string] };

function parts(c: Colours, id: string) {
  const stops = SHINE.map((o, i) => `<stop offset="${o}" style="stop-color:${c.body[i]}"/>`).join("");
  return `<defs><linearGradient id="${id}">${stops}</linearGradient></defs><g fill="none" stroke="${c.line}" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><path d="${HANDLE}" stroke="${c.handle}"/><circle cx="4.6" cy="20.5" r="1.9" fill="${c.hand}"/><circle cx="27.4" cy="20.5" r="1.9" fill="${c.hand}"/><path d="${BODY}" fill="url(#${id})"/><path d="${TIERS}"/><circle cx="12.6" cy="21" r="1.15" fill="${c.line}" stroke="none"/><circle cx="19.4" cy="21" r="1.15" fill="${c.line}" stroke="none"/><path d="${SMILE}" stroke-width="1.3"/></g>`;
}

/** The mark's inner SVG with literal colours (a favicon can't read CSS variables). */
export const markSvg = parts;

/** The mark's colours as tokens, set per theme in tokens.css. */
const TOKENS: Colours = {
  line: "var(--mark-line)",
  handle: "var(--mark-handle)",
  hand: "var(--mark-hand)",
  body: ["var(--mark-body-1)", "var(--mark-body-2)", "var(--mark-body-3)", "var(--mark-body-4)"],
};

export function Logo({ className, title }: { className?: string; title?: string }) {
  const id = `mark${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  return (
    <svg
      viewBox="0 0 32 32"
      className={cn("shrink-0", className ?? "size-5")}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      dangerouslySetInnerHTML={{ __html: parts(TOKENS, id) }}
    />
  );
}

/** Kept for older imports. */
export const TiffinMark = ({ className }: { className?: string; lid?: boolean }) => <Logo className={className} />;

/** The wordmark: the mark and "tiffin" in Newsreader, lower case. `bare` drops the mark where the mascot is already on screen. */
export function Wordmark({ className, bare }: { className?: string; bare?: boolean }) {
  return (
    <span className={cn("inline-flex items-center gap-2 text-ink", className)}>
      {!bare && <Logo className="size-[26px]" />}
      <span className="font-serif text-[1.375rem] leading-none font-[500] tracking-[-0.03em]">tiffin</span>
    </span>
  );
}
