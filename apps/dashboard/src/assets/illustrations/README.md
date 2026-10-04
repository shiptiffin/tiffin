# Tiffin illustrations

One family: a stacked Indian tiffin carrier drawn as a precise, flat product illustration (steel tiers,
brass handle and latch, project enamel only on a tier's rim). Generated 2026-10-03 with Codex's built-in
`image_gen` tool (imagegen skill, built-in mode, no API fallback), then curated and post-processed.

All files except `og-card.png` are transparent WebP, sized at 2x their display size. They work on both the
paper ground (`oklch(0.975 0.008 85)`) and the graphite ground (`oklch(0.18 0.01 60)`) without a dark
variant: the objects carry their own steel tones and outline.

## Files

| File | Pixels | Display (CSS px) | Used on |
|---|---|---|---|
| `carrier-hero.webp` | 1440x1440 | 720x720 | Login page; first run, "Your tiffin is packed" |
| `carrier-hero-open.webp` | 1440x1440 | 720x720 | The moment a project starts (lid and top tier lifted, latch pin out) |
| `starter-static.webp` | 640x400 | 320x200 | New project, "Static site" starter (teal) |
| `starter-api.webp` | 640x400 | 320x200 | New project, "API on Postgres" starter (leaf) |
| `starter-guestbook.webp` | 640x400 | 320x200 | New project, "Full-stack guestbook" starter (kokum) |
| `starter-next.webp` | 640x400 | 320x200 | New project, "Next.js" starter (indigo) |
| `empty-projects.webp` | 400x320 | 200x160 | Box, no projects yet |
| `empty-inbox.webp` | 400x320 | 200x160 | Email dev inbox, nothing caught |
| `empty-jobs.webp` | 400x320 | 200x160 | Jobs, nothing scheduled |
| `empty-errors.webp` | 400x320 | 200x160 | Errors, none (calm, nothing wrong) |
| `empty-backups.webp` | 400x320 | 200x160 | Backups, none yet |
| `empty-approvals.webp` | 400x320 | 200x160 | Approvals, nothing waiting |
| `og-card.png` | 1200x630 | social card | README / social preview. Opaque paper ground; the left ~55% is empty for a headline the site overlays. No text in the image. |

`carrier-hero.webp` and `carrier-hero-open.webp` share the same registration: same scale (base plate the
same width) and the same baseline and centre, so they can be cross-faded or swapped in place.

The enamel colours are fixed in the art (teal / leaf / kokum on the hero; one per starter). They are
decoration, not a project's chosen colour.

## Post-processing

Each pick was trimmed to its alpha bounds, scaled with Lanczos into the target canvas (object about 80–85 %
of the box, centred), and saved as WebP (quality 88, alpha quality 90). The generated soft contact shadows
were mid-grey, which glowed on the graphite ground, so every cast-shadow pixel (semi-transparent pixels and
anything under the object's lowest outline) was recoloured to warm graphite `#221b16`, with its alpha scaled
so it looks the same on paper and simply vanishes on graphite.

## Prompts

Every prompt began with this style bible:

> **SUBJECT FAMILY:** a stacked Indian tiffin carrier (dabba) treated as a beautifully engineered desk object.
> Squat round stainless-steel tiers (each tier much wider than tall, about 2.5:1), stacked vertically, each
> with a slightly overhanging rolled rim; a gently domed lid; two flat vertical steel side rails (clamp bars)
> running up both sides through little lugs on each tier; a simple arched brass carry handle on top with a
> small brass latch pin; a flat round steel base plate a touch wider than the tiers. Enamel colour appears
> ONLY as a narrow painted band on a tier's rolled rim (no more than one fifth of the tier height); the tier
> walls, lid, rails and base stay steel, the handle and latch stay brass. Tiers are clearly separate, squat
> and wide, with a visible shadow line under each overhanging rim.
> **MEDIUM:** a FLAT, DRAWN illustration, NOT a 3D render, NOT photoreal, NOT an app icon. Like a precise
> technical illustration printed in a few flat inks (industrial-design presentation drawing meets Stripe
> Press book art). Crisp, confident, uniform warm-graphite outline (#2a231d, medium weight), crisp
> ellipses, true verticals. Every surface is a flat fill; metal is suggested by at most two HARD-EDGED flat
> tonal steps per surface (one narrow pale highlight strip, one shade side), never smooth gradients, never
> specular glare, never glossy reflections. Matte. Small honest construction details (rolled rims, rivets,
> hinge pins) drawn sparingly. The only soft thing is one low, diffuse warm-grey contact shadow directly
> under the object.
> **VIEW:** calm three-quarter view from slightly above (about 20 degrees), no exaggerated perspective,
> object centred, generous empty space around it.
> **PALETTE:** warm steel greys (#e4dfd6 highlight, #cbc4b8 body, #a39b8f shade), brass #ad7c2f with
> highlight #cfa55e, outline #2a231d. Enamels (muted, all equal lightness): leaf #519160, teal #2b9191,
> indigo #607ec2, kokum #b05e74, chilli #c06143, turmeric #a8862a.
> **BACKGROUND:** fully transparent background (real alpha), no backdrop, no frame, no floor other than the
> soft contact shadow.
> **AVOID:** text, letters, numbers, logos, people, hands, food, steam, sparkles, glows, lens flares,
> gradient backgrounds, glassmorphism, purple, neon, isometric tech cubes, server racks, clouds, circuit
> patterns, glossy plastic, chrome reflections, skeuomorphic app-icon rendering, cartoon faces, noise
> texture, watermark.

The subject lines used for the kept images follow. Each was generated as 2–3 variants and the best one kept.

- **carrier-hero** (square): "HERO: the closed, latched tiffin carrier with four squat tiers stacked on a
  base plate. Rim enamel bands: top tier teal, second leaf, third kokum, lowest tier plain steel with no
  colour. Domed lid with a small brass knob, brass arched handle standing upright between the two side
  rails, brass latch pin engaged at the top. Soft contact shadow. Object height about 65% of the canvas,
  centred, generous margin all around."
- **carrier-hero-open**: a built-in *edit* of the kept hero, so the two match: "Open the tiffin carrier:
  pull the brass latch pin out so it floats just to the right of the top of the right rail; lift the domed
  lid together with the top tier (teal rim band) straight up so they float level, a clear gap of about half
  a tier height above the rest of the stack. Keep the exact same drawing style, linework, colours,
  proportions, rails, rivets, handle, base plate, the leaf and kokum tiers and the plain bottom tier
  unchanged. No motion lines, no text. Keep the soft contact shadow only under the base. Transparent
  background."
- **starter-static** (landscape): "one squat tiffin tier with a narrow teal rim band and two small side
  lugs, lid off and resting flat beside it; standing upright in the open tier, a single blank sheet of paper
  held in a slim brass clip, like a page on display. No writing, no marks on the paper."
- **starter-api**: "one squat tiffin tier with a narrow leaf rim band, lid off and resting beside it;
  sitting inside the open tier and rising just above its rim, a small squat steel drum made of three
  stacked short discs (a database drum drawn as a physical object, no symbols)."
- **starter-guestbook**: "one squat tiffin tier with a narrow kokum rim band, lid off and leaning against
  its side; standing inside the open tier, a few blank paper cards like a small card file, and a small open
  notebook with blank pages resting on the rim. No writing on anything."
- **starter-next**: "one squat tiffin tier with a narrow indigo rim band; resting on its lid, three thin
  flat rectangular steel plates stacked with small even gaps between them, like layered panels or trays,
  each slightly offset. No markings." (The model put the panels under the tier; kept, it reads as layers.)
- Empty states all began: "Quiet, minimal EMPTY-STATE SPOT illustration, steel and brass only (no enamel
  colour), small object centred, occupying about 40% of the canvas width, lots of empty space around it."
  - **empty-projects**: "an empty tiffin carrier frame: base plate, the two side rails and the brass arched
    handle standing up, with one empty plain steel tier on the base; the domed lid lies on the ground beside
    it." (Kept image came from an earlier pass of the bible with the same subject.)
  - **empty-inbox**: "a single shallow tiffin tier seen slightly from above so its clean, empty interior is
    visible; its domed lid rests leaning against its side; a small brass spring clip on the rim holds
    nothing."
  - **empty-jobs**: "a single closed tiffin tier with a small brass winding key lying at rest on its lid, as
    if the clockwork is wound down and waiting."
  - **empty-errors**: "a short closed two-tier tiffin carrier standing perfectly upright, and lying beside it
    a small brass-and-steel spirit level whose single bubble sits exactly centred. Serene, nothing wrong."
  - **empty-backups**: "two empty tiffin tiers nested one inside the other for storage, with a blank small
    brass tag on a loop of string hanging from the rim (the tag is blank, no writing)."
  - **empty-approvals**: "a small round brass seal stamp (a short turned steel handle on a plain round brass
    disc, face blank) standing upright at rest beside a single closed tiffin tier."
- **og-card**: no separate generation. The kept `carrier-hero` drawing is placed on the paper ground,
  centred at x = 880 px, with the left side left empty for the overlaid headline.
