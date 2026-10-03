import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

/**
 * The box's nameplate: name, where it runs, version, uptime, domain. One
 * line on the lid of the Stack; it wraps on a phone.
 *
 *   <Nameplate name="tiffin" where="Mac · Lima" version="Tiffin 0.4.2" uptime="up 6 days" domain="tiffin.localhost" />
 */
export function Nameplate({
  name,
  where,
  version,
  uptime,
  domain,
  className,
}: {
  name: string;
  where?: string;
  version?: string;
  uptime?: string;
  domain?: ReactNode;
  className?: string;
}) {
  const parts = [where, version, uptime].filter(Boolean) as string[];
  return (
    <div className={cn("flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5", className)}>
      <span className="text-[0.9375rem] leading-5 font-[550] text-ink">{name}</span>
      {parts.length > 0 && (
        <span className="text-sm text-ink-3">
          {parts.map((p, i) => (
            <span key={p}>
              {i > 0 && <span aria-hidden> · </span>}
              <span className={i === 0 ? "text-ink-2" : undefined}>{p}</span>
            </span>
          ))}
        </span>
      )}
      {domain && <span className="ident ml-auto text-ink-2 max-sm:ml-0 max-sm:basis-full">{domain}</span>}
    </div>
  );
}
