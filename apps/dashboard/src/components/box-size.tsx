import { useState, type ReactNode } from "react";
import type { BoxServer, ServerOffer } from "@/api/client";
import { Command } from "@/components/copy";
import { Segmented } from "@/components/health-kit";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { relative } from "@/lib/time";

/** "€7.99", in the currency Hetzner bills in. */
function money(n: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 2 }).format(n);
  } catch {
    return `${n.toFixed(2)} ${currency}`;
  }
}

/** "4 CPUs, 8 GB memory": what a server type gives, in the words the Machine row uses. */
function specs(o: ServerOffer): string {
  return `${o.cores} ${o.dedicated ? "dedicated " : ""}CPUs, ${o.memoryGB} GB memory`;
}

/**
 * Settings › This box: how to make the box bigger. The dashboard cannot resize a
 * server itself (the box never holds the Hetzner token), so it shows the exact
 * command to run on the computer that set the box up.
 */
export function BoxSize({ server }: { server?: BoxServer }) {
  const [open, setOpen] = useState<"type" | "disk" | null>(null);
  const m = server?.machine;
  const [type, setType] = useState<string | undefined>(m?.upgrades?.find((o) => !o.soldOut)?.name);
  const sizes = m ? [2, 4, 8].map((x) => m.volumeGB * x).filter((gb) => gb <= 10240) : [];
  const [disk, setDisk] = useState<string | undefined>(sizes[0]?.toString());
  if (!server) return null;
  const up = `tiffin up --name ${server.name}`;

  if (server.provider === "ssh" || !m) {
    return (
      <div className="mt-6">
        <h3 className="label mb-2">Size</h3>
        <p className="text-[0.875rem] text-ink-2">
          {server.provider === "ssh" ? "Resize at your host, then run this to retune the box to its new size:" : "Run this once to see the sizes this box can grow to:"}
        </p>
        <Command className="mt-2" cmd={up} />
      </div>
    );
  }

  const upgrades = m.upgrades ?? [];
  const pick = upgrades.find((o) => o.name === type && !o.soldOut) ?? upgrades.find((o) => !o.soldOut);
  const gb = Number(disk ?? sizes[0]);
  return (
    <div className="mt-6">
      <h3 className="label mb-1">Size</h3>
      <p className="text-[0.8125rem] text-ink-3">Upgrading restarts the box for about 2 minutes; growing the disk has no downtime.</p>
      <div className="mt-2 divide-y divide-rule border-y border-rule text-[0.875rem]">
        <Line label="Server" value={`${m.serverType.name} · ${specs(m.serverType)} · ${money(m.serverType.monthlyNet, m.currency)} a month`}>
          {pick && (
            <Button size="sm" aria-expanded={open === "type"} onClick={() => setOpen(open === "type" ? null : "type")}>
              Upgrade
            </Button>
          )}
        </Line>
        {open === "type" && pick && (
          <div className="py-3">
            <div role="radiogroup" aria-label="Server type" className="flex flex-col gap-1">
              {upgrades.map((o) => (
                <button
                  key={o.name}
                  type="button"
                  role="radio"
                  aria-checked={o.name === pick.name}
                  disabled={o.soldOut}
                  onClick={() => setType(o.name)}
                  className={cn(
                    "flex items-baseline justify-between gap-4 rounded-[8px] border px-3 py-2 text-left transition-colors duration-[var(--dur-state)]",
                    o.soldOut ? "border-rule text-ink-3 opacity-70" : o.name === pick.name ? "border-brass bg-paper-select" : "border-rule hover:bg-paper-hover",
                  )}
                >
                  <span className="min-w-0">
                    <span className="ident text-ink">{o.name}</span>
                    <span className="ml-2 text-ink-2">{specs(o)}</span>
                    {o.soldOut && <span className="ml-2 text-ink-3">sold out in {m.location} right now</span>}
                  </span>
                  <span className="shrink-0 text-ink-2 tnum">
                    {money(o.monthlyNet, m.currency)} <span className="text-ink-3">a month</span>
                  </span>
                </button>
              ))}
            </div>
            <p className="mt-3 text-[0.8125rem] text-ink-3">
              Run this on the computer that set the box up. It shows the plan and asks first; the box is back in about 2 minutes.
            </p>
            <Command wrap className="mt-2" cmd={`${up} --type ${pick.name}`} />
          </div>
        )}
        <Line label="Disk" value={`${m.volumeGB} GB for data · ${money(m.volumeGB * m.volumeGBMonthlyNet, m.currency)} a month`}>
          {sizes.length > 0 && (
            <Button size="sm" aria-expanded={open === "disk"} onClick={() => setOpen(open === "disk" ? null : "disk")}>
              Grow disk
            </Button>
          )}
        </Line>
        {open === "disk" && sizes.length > 0 && (
          <div className="py-3">
            <Segmented label="Disk size" value={String(gb)} onChange={setDisk} options={sizes.map((s) => ({ v: String(s), label: `${s} GB` }))} />
            <p className="mt-2 text-[0.8125rem] text-ink-3">
              About {money((gb - m.volumeGB) * m.volumeGBMonthlyNet, m.currency)} a month more. It grows while everything keeps running, and can’t shrink again.
            </p>
            <Command wrap className="mt-2" cmd={`${up} --volume-size ${gb}`} />
          </div>
        )}
      </div>
      <p className="mt-2 text-xs text-ink-3">Prices from Hetzner before VAT, as of {relative(m.pricedAt)}.</p>
    </div>
  );
}

function Line({ label, value, children }: { label: string; value: string; children?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2.5">
      <p className="min-w-0">
        <span className="mr-3 text-ink-3">{label}</span>
        <span className="text-ink">{value}</span>
      </p>
      {children}
    </div>
  );
}
