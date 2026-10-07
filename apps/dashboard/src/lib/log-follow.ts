/**
 * Follows an app's runtime logs over server-sent events, from `since` on.
 *
 * EventSource reconnects on its own with the URL it was opened with, which
 * replays everything after the original `since` (the server sends recent
 * lines first). So this reconnects itself instead: after an error or the
 * server's "timeout", it opens a new stream from the newest line it has
 * passed on. Lines it has already passed on (a replay, or the overlap
 * between the server's catch-up read and its live tail) are dropped, and
 * lines are handed over in batches rather than one render each. (A line
 * from one instance still in flight when the stream drops, and older than
 * another instance's newest line, can be missed; reloading shows it.)
 */

export type FollowedLine = { time: string; instance: string; stream: string; text: string; deploy: string };
type Source = { addEventListener(type: string, f: (e: MessageEvent) => void): void; onerror: unknown; close(): void };

const keyOf = (l: FollowedLine) => `${l.time}\u0000${l.instance}\u0000${l.stream}\u0000${l.text}`;

/** RFC 3339 times compared to the nanosecond (Date.parse stops at milliseconds). */
export function timeKey(t: string): [number, number] {
  const ms = Date.parse(t);
  const frac = /\.(\d+)/.exec(t)?.[1] ?? "";
  return [ms, Number(frac.slice(3, 9).padEnd(6, "0"))];
}
const before = (a: [number, number], b: [number, number]) => a[0] < b[0] || (a[0] === b[0] && a[1] < b[1]);

export function followLogLines<L extends FollowedLine>(opts: {
  open: (since: string | undefined) => Source;
  since?: string;
  onLines: (lines: L[]) => void;
  /** How long lines collect before onLines (ms). */
  batch?: number;
  /** The first wait before reconnecting (ms); it doubles up to 15 s. */
  retry?: number;
}): () => void {
  const batch = opts.batch ?? 150;
  const firstRetry = opts.retry ?? 1000;
  let since = opts.since;
  let newest: [number, number] | null = since ? timeKey(since) : null;
  const seen = new Set<string>();
  const order: string[] = [];
  let pending: L[] = [];
  let timer: ReturnType<typeof setTimeout> | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  let backoff = firstRetry;
  let stopped = false;
  let es: Source | null = null;

  const flush = () => {
    timer = null;
    if (stopped || !pending.length) return;
    const out = pending;
    pending = [];
    opts.onLines(out);
  };
  const take = (l: L) => {
    const t = timeKey(l.time);
    const k = keyOf(l);
    // Already passed on (a replay). Not "older than the newest": instances' lines interleave out of order.
    if (seen.has(k)) return;
    seen.add(k);
    order.push(k);
    if (order.length > 5000) seen.delete(order.shift()!);
    if (!newest || before(newest, t)) {
      newest = t;
      since = l.time;
    }
    pending.push(l);
    timer ??= setTimeout(flush, batch);
  };

  const connect = () => {
    retry = null;
    if (stopped) return;
    const s = opts.open(since);
    es = s;
    const again = () => {
      if (es !== s) return;
      s.close();
      es = null;
      if (!stopped) retry = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 15_000);
    };
    s.addEventListener("log", (e) => {
      backoff = firstRetry;
      take(JSON.parse(e.data) as L);
    });
    s.addEventListener("timeout", again);
    s.onerror = again;
  };
  connect();

  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    if (retry) clearTimeout(retry);
    es?.close();
  };
}
