import { cn } from "@/lib/cn";

/** The tiffin: three stacked tins under a carry handle. A line mark that holds up at 16 px. */
export function TiffinMark({ className, lid }: { className?: string; lid?: boolean }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={cn("size-5", className)}
      aria-hidden
    >
      <g className={cn("origin-[6px_8px]", lid && "animate-lid")}>
        <path d="M8.5 7.5V5.25a3.5 3.5 0 0 1 7 0V7.5" />
        <path d="M5 8.25h14" />
      </g>
      <rect x="5.5" y="8.25" width="13" height="13" rx="2.25" />
      <path d="M5.5 12.6h13M5.5 16.9h13" />
      <path d="M3.75 10.4v8.8M20.25 10.4v8.8" opacity={0.55} />
    </svg>
  );
}

export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2 text-ink", className)}>
      <TiffinMark className="size-[22px] text-brass" />
      <span className="display text-[1.3rem] leading-none font-[520] tracking-[-0.02em]">Tiffin</span>
    </span>
  );
}
