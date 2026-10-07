import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { iconQuery, type IconImage } from "@/api/modules";
import { cn } from "@/lib/cn";
import { useEnamel, type Enamel } from "@/lib/enamel";

/** The sizes the icon is drawn for: rows and menus (16, 20), headers (24, 32), cards and settings (40). */
export type ProjectIconSize = 16 | 20 | 24 | 32 | 40;

/**
 * A project's icon, the way a hosting dashboard shows one:
 *   1. the image uploaded for it, or
 *   2. its app's own icon (the box finds it after each production deploy), or
 *   3. its initials on a tint of its colour ("S" for shop, "MS" for my-shop;
 *      one letter below 24 px, where two would blur).
 * A 1 px inner ring keeps a white icon visible on white paper, and a mark
 * drawn for the other theme (black on clear, white on clear) gets a plate.
 *
 *   <ProjectIcon project="shop" size={20} />
 */
export function ProjectIcon({ project, size = 16, className }: { project?: string; size?: ProjectIconSize | number; className?: string }) {
  const info = useQuery({ ...iconQuery(project ?? ""), enabled: !!project });
  const enamel = useEnamel(project);
  const [broken, setBroken] = useState<string>();
  const style = { width: size, height: size, borderRadius: radius(size) };
  if (!project) return <span className={cn("inline-block shrink-0", className)} style={style} aria-hidden />;
  const image = info.data?.image;
  if (image && broken !== image.hash) return <IconTile image={image} size={size} className={className} onError={() => setBroken(image.hash)} />;
  if (info.isPending) return <span className={cn("relative inline-block shrink-0 bg-paper-sunk", ring, className)} style={style} aria-hidden />;
  return <Monogram letters={info.data?.letters ?? letters(project)} enamel={enamel} size={size} className={className} />;
}

/** An image icon, sized and ringed like ProjectIcon (the settings preview uses it for the app's own icon too). */
export function IconTile({ image, size, className, onError }: { image: IconImage; size: number; className?: string; onError?: () => void }) {
  return (
    <span
      className={cn(
        "relative inline-block shrink-0 overflow-hidden",
        // A mark made for the other theme sits on a plate.
        image.tone === "light" && "bg-ink dark:bg-transparent",
        image.tone === "dark" && "dark:bg-ink-2",
        ring,
        className,
      )}
      style={{ width: size, height: size, borderRadius: radius(size) }}
      aria-hidden
    >
      <img src={image.url} alt="" width={size} height={size} decoding="async" draggable={false} onError={onError} className="size-full object-contain" />
    </span>
  );
}

/** Initials on the project's colour: a pale tint with deep letters (≥ 4.5:1 in both themes, measured on /_kit). */
export function Monogram({ letters: text, enamel, size, className }: { letters: string; enamel: Enamel; size: number; className?: string }) {
  const shown = size < 24 ? [...text][0] : text;
  const two = [...shown].length > 1;
  return (
    <span
      className={cn("relative inline-grid shrink-0 place-items-center font-sans leading-none font-[620] select-none", ring, className)}
      style={{
        width: size,
        height: size,
        borderRadius: radius(size),
        background: `color-mix(in oklab, var(--enamel-${enamel}) 24%, var(--paper-raised))`,
        color: `color-mix(in oklab, var(--enamel-${enamel}) 55%, var(--ink))`,
        fontSize: Math.round(size * (two ? 0.42 : 0.54) * 2) / 2,
        letterSpacing: two ? "-0.02em" : undefined,
      }}
      aria-hidden
    >
      {shown}
    </span>
  );
}

// The ring is drawn inside the tile (an inset shadow over the image), so it never changes the layout.
const ring = "after:pointer-events-none after:absolute after:inset-0 after:rounded-[inherit] after:shadow-[inset_0_0_0_1px_var(--rule)] after:content-['']";

const radius = (size: number) => Math.max(3, Math.round(size * 0.25));

/** Same rule as the box (projicon.Letters): the first letters of the first two words, else of the name. */
export function letters(project: string) {
  const words = project.split(/[-_ .]+/).filter(Boolean);
  const out = words.slice(0, 2).map((w) => [...w][0].toUpperCase());
  return out.join("") || "?";
}

/**
 * The tiffin, drawn from what's in a project: a brass handle, a lid, one tier
 * per part (up to five shown), and the base. Line-drawn in the ink colour.
 */
export function TiffinGlyph({ tiers, className }: { tiers: number; className?: string }) {
  const n = Math.max(0, Math.min(5, tiers));
  const top = 5.4; // under the lid
  const bottom = 13.6; // above the base
  const h = n > 0 ? Math.min(2.8, (bottom - top) / n) : 0;
  const start = n > 0 ? bottom - h * n : bottom;
  return (
    <svg viewBox="0 0 16 16" className={cn("size-full", className)} fill="none" strokeLinejoin="round">
      <path d="M5.6 3.3c0-1.3 1.1-2.1 2.4-2.1s2.4.8 2.4 2.1" stroke="var(--brass)" strokeWidth={1.3} strokeLinecap="round" />
      <path d={`M3.6 ${start}V4.8c0-.5.4-.9.9-.9h7c.5 0 .9.4.9.9V${start}`} stroke="currentColor" strokeWidth={1.2} />
      {Array.from({ length: n }, (_, i) => (
        <rect key={i} x={3} y={start + i * h + 0.35} width={10} height={Math.max(0.9, h - 0.7)} rx={0.6} stroke="currentColor" strokeWidth={1.1} />
      ))}
      <path d={`M2.8 14.4h10.4`} stroke="currentColor" strokeWidth={1.3} strokeLinecap="round" />
    </svg>
  );
}
