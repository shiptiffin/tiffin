import { useState } from "react";
import baseBand from "@/assets/illustrations/mascot-base-band.webp";
import base from "@/assets/illustrations/mascot-base.webp";
import deployingBand from "@/assets/illustrations/mascot-deploying-band.webp";
import deploying from "@/assets/illustrations/mascot-deploying.webp";
import failedBand from "@/assets/illustrations/mascot-failed-band.webp";
import failed from "@/assets/illustrations/mascot-failed.webp";
import idleBand from "@/assets/illustrations/mascot-idle-band.webp";
import idle from "@/assets/illustrations/mascot-idle.webp";
import liveBand from "@/assets/illustrations/mascot-live-band.webp";
import live from "@/assets/illustrations/mascot-live.webp";
import previewBand from "@/assets/illustrations/mascot-preview-band.webp";
import preview from "@/assets/illustrations/mascot-preview.webp";
import tinBand from "@/assets/illustrations/tin-plain-band.webp";
import tin from "@/assets/illustrations/tin-plain.webp";
import { cn } from "@/lib/cn";
import { enamelVar, type Enamel } from "@/lib/enamel";

/**
 * The mascot: a little stacked lunch tin with a face. Its expression and a
 * small prop say the state (steam when live, z's when idle, lid up and packing
 * while deploying, a spill when it failed, a tasting spoon for a preview);
 * colour never does. Plain steel by default; pass a project's `enamel` and
 * its middle tier wears that colour (one drawing + a band mask, so any
 * project colour works).
 *
 * Every state shares one registration, so changing `state` cross-fades in
 * place. In dark mode it sits on a paper disc (styles/art.css).
 *
 *   <Mascot state="live" size={64} />
 *   <Mascot state={tone} size={56} enamel={useEnamel(project)} />
 */
export type MascotState = "base" | "live" | "idle" | "deploying" | "failed" | "preview";

const ART: Record<MascotState, { src: string; band: string }> = {
  base: { src: base, band: baseBand },
  live: { src: live, band: liveBand },
  idle: { src: idle, band: idleBand },
  deploying: { src: deploying, band: deployingBand },
  failed: { src: failed, band: failedBand },
  preview: { src: preview, band: previewBand },
};

export function Mascot({
  state = "base",
  size,
  enamel,
  plate = true,
  label,
  className,
}: {
  state?: MascotState;
  /** CSS px (square); or leave it out and size it with classes. */
  size?: number | string;
  /** A project's colour for the middle tier; plain steel without one. */
  enamel?: Enamel;
  /** The paper disc behind it in dark mode (off when it already sits on a paper-tone well). */
  plate?: boolean;
  /** Say what it shows (role="img"); without one it's decoration. */
  label?: string;
  className?: string;
}) {
  // States it has shown stay mounted, so a change cross-fades instead of popping.
  const [shown, setShown] = useState<MascotState[]>([state]);
  if (!shown.includes(state)) setShown([...shown, state]);
  return (
    <span
      className={cn("mascot", plate && "art-plate", className)}
      data-state={state}
      style={size === undefined ? undefined : { width: size, height: size }}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      {shown.map((s) => (
        <Drawing key={s} src={ART[s].src} band={ART[s].band} enamel={enamel} on={s === state} />
      ))}
    </span>
  );
}

/** A project's tin without a face, in the project's colour: one drawing + its band mask. */
export function ProjectTin({
  enamel,
  size = 40,
  plate = true,
  className,
}: {
  enamel?: Enamel;
  size?: number | string;
  plate?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("mascot", plate && "art-plate", className)} style={{ width: size, height: size }} aria-hidden>
      <Drawing src={tin} band={tinBand} enamel={enamel} on />
    </span>
  );
}

function Drawing({ src, band, enamel, on }: { src: string; band: string; enamel?: Enamel; on: boolean }) {
  const hide = on ? undefined : { opacity: 0 };
  return (
    <>
      <img src={src} alt="" draggable={false} decoding="async" style={hide} />
      {enamel && (
        <span
          className="mascot-band"
          style={{ ...hide, backgroundColor: enamelVar(enamel), maskImage: `url(${band})`, WebkitMaskImage: `url(${band})` }}
        />
      )}
    </>
  );
}
