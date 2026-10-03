import { cn } from "@/lib/cn";

/**
 * The mark: a tiffin carrier drawn as one line. A carrying loop, three equal
 * tiers whose rims overhang the rails (the lips of stacked tins), and a base
 * plate. Built on a 32-unit grid with a 2-unit stroke whose centres all sit
 * on odd units, so at 16 px every stroke lands on whole pixels.
 *
 * Exported as plain path data too, for the favicon and the seal.
 */
export const MARK_PATHS = [
  "M11 9V6a3 3 0 0 1 3-3h4a3 3 0 0 1 3 3v3", // the loop
  "M7 27V12a3 3 0 0 1 3-3h12a3 3 0 0 1 3 3v15", // the rails and the lid
  "M3 15h26M3 21h26", // two rims, overhanging
  "M5 27h22", // the base plate
] as const;

export function Logo({ className, title }: { className?: string; title?: string }) {
  return (
    <svg
      viewBox="0 0 32 32"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn("shrink-0", className ?? "size-5")}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      {MARK_PATHS.map((d) => (
        <path key={d} d={d} />
      ))}
    </svg>
  );
}

/** Kept for older imports. */
export const TiffinMark = ({ className }: { className?: string; lid?: boolean }) => <Logo className={className} />;

/** The wordmark: the mark and "tiffin" in Newsreader, lower case. */
export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2 text-ink", className)}>
      <Logo className="size-[22px]" />
      <span className="font-serif text-[1.375rem] leading-none font-[500] tracking-[-0.03em]">tiffin</span>
    </span>
  );
}
