import { useNavigate } from "@tanstack/react-router";
import { useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { enamelVar, type Enamel } from "@/lib/enamel";

/**
 * The Stack: the box drawn as a tiffin carrier (handle on top, a lid with the
 * nameplate and vitals, a tier per project with its enamel rim, the platform
 * tier, room left, a base plate). Rows are plain 44 px rows on the page with
 * hairlines; the drawing is only the outline and the rims.
 *
 *   <Carrier>
 *     <Lid>…</Lid>
 *     <Rim />                      steel, between the lid and the parts
 *     <TierHead … />  <TierRow … /> …
 *     <Rim enamel="indigo" />      a project's rim
 *     …
 *     <RoomLeft … />
 *   </Carrier>
 *
 * Columns (lever · part · status · share · MB) come from --cols on .carrier;
 * <TierRow> fills them in order. On a phone rows fold to two lines.
 */
export function Carrier({ children, className, label = "The box" }: { children: ReactNode; className?: string; label?: string }) {
  return (
    <section aria-label={label} className={cn("relative", className)}>
      <div className="carrier">
        <span className="carrier-handle" aria-hidden>
          <i />
          <i />
        </span>
        {children}
      </div>
      <div className="carrier-base" aria-hidden />
    </section>
  );
}

export function Lid({ children }: { children: ReactNode }) {
  return <div className="px-5 pt-4 pb-5 max-sm:px-3.5">{children}</div>;
}

/** The lip of a tier: enamel for a project, steel for the box's own parts. */
export function Rim({ enamel, thin, className }: { enamel?: Enamel; thin?: boolean; className?: string }) {
  return <div className={cn("rim", className)} aria-hidden data-thin={thin ? "" : undefined} style={enamel ? ({ "--rim": enamelVar(enamel) } as CSSProperties) : undefined} />;
}

/** The column labels under the lid. */
export function TierColumns() {
  return (
    <div className="tier-grid h-[30px] max-sm:hidden" aria-hidden>
      <span className="label">Lever</span>
      <span className="label">Part</span>
      <span className="label">Status</span>
      <span className="label flex justify-between">
        <span>Share</span>
        <span className="tracking-normal normal-case">0–512</span>
      </span>
      <span className="label text-right">MB</span>
    </div>
  );
}

/** A tier's heading row: the project (or "Platform"), one line about it, its total. */
export function TierHead({ name, about, total, href }: { name?: ReactNode; about?: ReactNode; total?: ReactNode; href?: ReactNode }) {
  return (
    <div className="tier-grid min-h-[46px] py-2 max-sm:grid-cols-[minmax(0,1fr)_auto]">
      <div className="col-start-2 flex min-w-0 items-baseline gap-2 text-[0.875rem] leading-[1.125rem] font-[550] max-sm:col-start-1">{href ?? name}</div>
      <div className="col-span-2 col-start-3 min-w-0 truncate text-[0.78125rem] text-ink-3 max-sm:hidden">{about}</div>
      <div className="col-start-5 text-right text-[0.875rem] font-[550] tnum max-sm:col-start-2">{total}</div>
    </div>
  );
}

/** Opens a tier's page with the unlatch motion: the row lifts out (≤ 200 ms), then the page opens. */
export function useUnlatch() {
  const navigate = useNavigate();
  const [lifting, setLifting] = useState<string | null>(null);
  const busy = useRef(false);
  const open = (id: string, to: string, params?: Record<string, string>) => {
    if (busy.current) return;
    const go = () => {
      busy.current = false;
      setLifting(null);
      void navigate({ to: to as "/", params: params as never });
    };
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) return go();
    busy.current = true;
    setLifting(id);
    setTimeout(go, 170);
  };
  return { lifting, open };
}

/**
 * One part of the box as a row: lever, name (with a small second line),
 * one status sentence, its share of memory on a 0–512 MB meter, MB.
 * `onOpen` makes the whole row open its page (the name stays the keyboard
 * target); levers inside stop the click.
 */
export function TierRow({
  lever,
  name,
  sub,
  status,
  share,
  amount,
  staged,
  fault,
  bound,
  unlatching,
  onOpen,
  nameLink,
}: {
  lever?: ReactNode;
  name: ReactNode;
  sub?: ReactNode;
  status?: ReactNode;
  share?: ReactNode;
  amount?: ReactNode;
  staged?: boolean;
  fault?: boolean;
  /** Bound to something elsewhere on the page (an approval names this part). */
  bound?: boolean;
  unlatching?: boolean;
  onOpen?: () => void;
  /** The name as a link (keyboard and middle-click); rendered instead of `name`. */
  nameLink?: ReactNode;
}) {
  const onClick = (e: MouseEvent) => {
    if (!onOpen || (e.target as HTMLElement).closest("a, button, [role=slider], input")) return;
    onOpen();
  };
  return (
    <div
      className="tier-row tier-grid py-1 max-sm:grid-cols-[50px_minmax(0,1fr)_auto] max-sm:min-h-10 max-sm:py-1.5"
      data-link={onOpen ? "" : undefined}
      data-staged={staged ? "" : undefined}
      data-fault={fault ? "" : undefined}
      data-bound={bound ? "" : undefined}
      data-unlatching={unlatching ? "" : undefined}
      onClick={onClick}
    >
      <div className="flex items-center justify-start max-sm:row-span-2 max-sm:self-start max-sm:pt-1">{lever}</div>
      <div className="min-w-0">
        <div className="truncate text-[0.875rem] leading-[1.125rem] text-ink">{nameLink ?? name}</div>
        {sub && <div className="truncate text-xs leading-[0.9375rem] text-ink-3">{sub}</div>}
      </div>
      <div className="min-w-0 py-2 text-[0.84375rem] leading-[1.1875rem] text-ink-2 max-sm:col-span-2 max-sm:col-start-2 max-sm:row-start-2 max-sm:py-0 max-sm:text-[0.8125rem]">
        {status}
      </div>
      <div className="max-sm:hidden">{share}</div>
      <div className="text-right text-[0.875rem] tnum max-sm:col-start-3 max-sm:row-start-1">{amount}</div>
    </div>
  );
}
