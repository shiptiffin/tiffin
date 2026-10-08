/* The ink illustrations. Pre-sized AVIF with a WebP fallback in public/art,
   keyed from the paper to transparent so they sit on either theme.
   Re-export from the 1024 px masters: trim to the art, then one file per width. */

const ART = {
  "tin-on-server": { widths: [480, 720, 960], aspect: 1036 / 715 },
  "open-tin-parts": { widths: [320, 640], aspect: 663 / 856 },
  "tin-returns-key": { widths: [320, 640], aspect: 805 / 758 },
} as const;

type Name = keyof typeof ART;

export function Art({
  name,
  alt,
  sizes,
  className,
  priority = false,
}: {
  name: Name;
  alt: string;
  sizes: string;
  className?: string;
  /* Only the hero: fetched early instead of lazily. */
  priority?: boolean;
}) {
  const { widths, aspect } = ART[name];
  const set = (ext: string) => widths.map((w) => `/art/${name}-${w}.${ext} ${w}w`).join(", ");
  const w = widths[0];
  return (
    <picture className={className}>
      <source type="image/avif" srcSet={set("avif")} sizes={sizes} />
      <img
        src={`/art/${name}-${w}.webp`}
        srcSet={set("webp")}
        sizes={sizes}
        width={w}
        height={Math.round(w / aspect)}
        alt={alt}
        loading={priority ? "eager" : "lazy"}
        fetchPriority="auto"
        decoding="async"
      />
    </picture>
  );
}
