import { useEffect, useEffectEvent, useRef } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

/** What ShowMore needs from a useInfiniteQuery result. */
export type MoreQuery = {
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  isFetchNextPageError: boolean;
  fetchNextPage: () => Promise<unknown>;
};

/**
 * The end of a paged list. A "Show more" button (a real button: Tab reaches
 * it, Enter or Space loads the next page; rows arrive below it, so the page
 * doesn't jump). With auto, for feeds (Activity, the inbox), the next page
 * also loads as the end comes near; the button stays for keyboards and for
 * trying again after an error. Nothing when the list is complete, unless
 * `end` says something.
 */
export function ShowMore({
  query,
  auto = false,
  label = "Show more",
  end,
  className,
}: {
  query: MoreQuery;
  auto?: boolean;
  label?: string;
  end?: React.ReactNode;
  className?: string;
}) {
  const { hasNextPage: next, isFetchingNextPage: busy, isFetchNextPageError: failed, fetchNextPage } = query;
  const tail = useRef<HTMLDivElement>(null);
  const load = useEffectEvent(() => void fetchNextPage());

  useEffect(() => {
    const el = tail.current;
    if (!auto || !el || !next || busy || failed || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && load(), { rootMargin: "600px 0px" });
    io.observe(el);
    return () => io.disconnect();
  }, [auto, next, busy, failed]);

  if (!next && !end) return null;
  return (
    <div ref={tail} className={cn("[:where(&)]:mt-4 flex min-h-8 justify-center text-center text-sm text-ink-3", className)}>
      {next ? (
        // Not disabled while it loads: a disabled button would drop keyboard focus.
        <Button variant="ghost" size="sm" onClick={() => !busy && void fetchNextPage()} aria-disabled={busy} aria-busy={busy}>
          {busy ? "Loading…" : failed ? "Couldn’t load more. Try again" : label}
        </Button>
      ) : (
        end
      )}
    </div>
  );
}
