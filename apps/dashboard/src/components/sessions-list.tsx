import { useQueryClient } from "@tanstack/react-query";
import { Laptop, Smartphone, Tablet } from "lucide-react";
import { Fragment, useState } from "react";
import { methodWords, sessions, type Session } from "@/api/sessions";
import { Confirm } from "@/components/confirm";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { full, relative } from "@/lib/time";

// Dashboard sessions as rows (Settings › Sign-ins, and People › End sessions):
// the browser, where and how it signed in, when it was last used, Sign out.

/** A phone, a tablet or a computer, from the browser's name ("Safari on iPhone"). */
export function DeviceIcon({ device, className }: { device: string; className?: string }) {
  const Icon = /iPhone|Android/.test(device) ? Smartphone : /iPad/.test(device) ? Tablet : Laptop;
  return <Icon aria-hidden className={cn("size-4 text-ink-3", className)} strokeWidth={1.75} />;
}

/** "Canada · 198.51.100.7", the address alone, or "". */
export const whereWords = (s: Pick<Session, "country" | "ip">) => [s.country, s.ip].filter(Boolean).join(" · ");

/** Used in the last two minutes (the box notes use once a minute). */
const activeNow = (s: Session) => s.current || (!!s.lastSeenAt && Date.now() - new Date(s.lastSeenAt).getTime() < 2 * 60_000);

/** When it was last used, in words. */
export function seenWords(s: Session) {
  if (activeNow(s)) return "Active now";
  return `Last active ${relative(s.lastSeenAt ?? s.createdAt)}`;
}

/** Facts joined by " · ", each kept whole, so a line never wraps inside one or starts with a dot. */
export function Dots({ parts, className }: { parts: Array<string | undefined | false>; className?: string }) {
  const kept = parts.filter(Boolean) as string[];
  return (
    <p className={className}>
      {kept.map((x, i) => (
        <Fragment key={i}>
          <span className="whitespace-nowrap">{i < kept.length - 1 ? `${x} ·` : x}</span>{" "}
        </Fragment>
      ))}
    </p>
  );
}

/** Open sessions as rows, with Sign out on each (except this browser's). */
export function SessionRows({ list, onEnded }: { list: Session[]; onEnded?: () => void }) {
  const qc = useQueryClient();
  const [ending, setEnding] = useState<Session | null>(null);
  return (
    <>
      <ul className="divide-y divide-rule border-y border-rule">
        {list.map((s) => (
          <li key={s.id} className="grid grid-cols-[1.5rem_minmax(0,1fr)_auto] items-start gap-x-3 py-3.5">
            <span className="flex h-5 items-center">
              <DeviceIcon device={s.device} />
            </span>
            <div className="min-w-0">
              <p className="flex flex-wrap items-baseline gap-x-2 text-[0.875rem] leading-5 text-ink">
                <span className="font-[550]">{s.device}</span>
                {s.current && <span className="text-[0.8125rem] font-[550] text-brass-ink">This browser</span>}
              </p>
              <Dots className="mt-0.5 text-[0.8125rem] text-ink-3" parts={[whereWords(s), methodWords[s.method], `signed in ${relative(s.createdAt)}`]} />
              <p className={cn("mt-0.5 text-[0.8125rem] sm:hidden", activeNow(s) ? "text-ink" : "text-ink-3")}>{seenWords(s)}</p>
            </div>
            <div className="flex items-center gap-4 self-center">
              <span
                className={cn("hidden text-[0.8125rem] whitespace-nowrap sm:inline", activeNow(s) ? "text-ink" : "text-ink-3")}
                title={s.lastSeenAt ? `Last used ${full(s.lastSeenAt)}` : undefined}
              >
                {seenWords(s)}
              </span>
              {s.current ? (
                <span className="hidden w-[4.75rem] sm:block" aria-hidden />
              ) : (
                <Button variant="secondary" size="sm" className="w-[4.75rem] justify-center" onClick={() => setEnding(s)}>
                  Sign out
                </Button>
              )}
            </div>
          </li>
        ))}
      </ul>
      <Confirm
        open={!!ending}
        onClose={() => setEnding(null)}
        title={`Sign out ${ending?.device ?? "this session"}?`}
        body={`That browser is signed out at once${ending && whereWords(ending) ? ` (${whereWords(ending)})` : ""}. Any API keys made while signed in there stop working too.`}
        action="Sign out"
        tone="normal"
        cancel="Cancel"
        run={() => sessions.end(ending!.id)}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["sessions"] });
          onEnded?.();
        }}
      />
    </>
  );
}
