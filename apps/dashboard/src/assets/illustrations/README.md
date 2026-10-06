# Tiffin illustrations

One family: a small mascot, a stacked steel lunch tin with a face, plus a few scenes around it (a hand cart,
a pantry shelf, open tins with food). Flat risograph prints in three inks on warm paper. Generated 2026-10-04
with Codex's built-in image tool from one mascot reference, picked by hand, then cut out and exported with the
scripts in `apps/dashboard/art/`.

All files except `og-card.png` are transparent WebP, cut out of their paper and sized at 2x (or more) their
largest display size.

## How they work in the dashboard

- **Mark vs. mascot:** the logo (`components/logo.tsx`) is the mascot drawn small and marks the frame: sidebar, tab,
  sign-in. The drawings are for moments (empty pages, a project going live or failing, errors). Never show the tin
  twice on one screen.

- **Light theme:** the drawings sit straight on the paper.
- **Dark theme:** a graphite outline vanishes on the graphite ground, so every drawing sits on a paper plate
  (`styles/art.css`): `.art-plate` puts a paper-tone disc behind it (a rounded tile with
  `data-plate="tile"` for wide scenes), and `.art-well` turns a starter thumbnail's well paper-tone. One
  asset per drawing; no dark variants. The plate colour is `--art-plate`.
- **The mascot** (`components/mascot.tsx`): `<Mascot state="live|idle|deploying|failed|preview|base" />`.
  All six states share one registration (same scale and baseline), so changing `state` cross-fades in place.
  The state shows in the face and a small prop, never in colour. While deploying it bobs gently, except with
  reduced motion.
- **Project colour:** the mascot is plain steel by default. Pass a project's `enamel` and its middle tier takes
  that colour: each mascot file has a `-band` mask (alpha = the band's coverage, with the paper grain let
  through), and the component paints `background-color: var(--enamel-*)` through it with `mask-image`. Any
  project colour works without new art. `<ProjectTin enamel=… />` is the same idea without a face, for project
  cards.

## Files

| File | Pixels | Shown at (CSS px) | Used on |
|---|---|---|---|
| `mascot-base.webp` (+ `-band`) | 800x800 (mask 400) | up to 416 | Login (waiting) |
| `mascot-live.webp` (+ `-band`) | 800x800 | 112–416 | Login once the link works; New project when it's live; Errors, none open |
| `mascot-deploying.webp` (+ `-band`) | 800x800 | 112–212 | New project the moment it starts (lid up, packing) |
| `mascot-failed.webp` (+ `-band`) | 800x800 | 112–212 | New project when the app didn't start; the route error page |
| `mascot-idle.webp` (+ `-band`) | 800x800 | — | In `<Mascot>` (asleep); not placed now |
| `mascot-preview.webp` (+ `-band`) | 800x800 | — | In `<Mascot>` for previews (tasting spoon); not placed yet |
| `mascot-night.webp` | 800x800 | 128 | Jobs, nothing scheduled (asleep under a moon) |
| `tin-plain.webp` (+ `-band`) | 800x800 | 40 | Project cards on Projects, in the project's colour |
| `starter-web.webp` | 640x400 | 320x200 | New project and starter pickers, "Web app" (a browser window with a small database beside it) |
| `starter-api.webp` | 640x400 | 320x200 | "API" (a plug meeting its socket, one arrow in and one out) |
| `starter-static.webp` | 640x400 | 320x200 | "Static site" (a single page with a folded corner) |
| `empty-projects.webp` | 640x320 | — | Not placed now (an empty box opens on New project); the mascot alone on an empty hand cart |
| `empty-inbox.webp` | 400x400 | 160 | Email dev inbox, nothing caught; the 404 page (lid up, an empty dashed slot above) |
| `empty-backups.webp` | 480x280 | 240x140 | Backups, none yet (the mascot beside a pantry shelf of jars) |
| `og-card.png` | 1200x630 | social card | README / social preview: flat paper (#F3EDE1), the cart with five tins on the right, the left ~50 % empty for a headline. No text. 96-colour PNG. |

The guestbook demo starter reuses `starter-web.webp`.

## Style bible

Every prompt ended with the mascot and style bibles (`apps/dashboard/art/mascot.txt`, `style.txt`):

> **MASCOT** (match the attached reference image exactly; same character every time): a small cute stacked
> steel lunch tin character. Body = 3 short stacked cylindrical steel-grey tiers with graphite outlines,
> slightly taller than wide; flat steel-grey lid with a thin rim; a simple steel-grey arched carry handle on
> top attached by two small posts. The middle tier is plain steel-grey like the others; the face is drawn
> directly on it: two small solid graphite dot eyes set wide apart and one tiny simple curved mouth, nothing
> else on the face. Two tiny round steel-grey mitten stub hands at the sides of the middle tier; no legs, no
> feet. Flat fills, no shine. No coloured band anywhere on the body.
>
> **STYLE:** flat two-to-three ink risograph / screenprint illustration on warm off-white paper (#F3EDE1).
> Inks only: warm graphite (#2E2A26) for confident slightly uneven outlines and dark fills, steel grey
> (#8C9196) flat fills, ONE ochre accent (#C8962E), optional muted brick red (#A5543F) used very sparingly.
> Slight riso grain and tiny misregistration are fine. Absolutely NO gradients, NO glossy metal highlights,
> NO 3D shading, NO photorealism, NO drop shadows beyond a flat offset shape, NO text, NO letters, NO numbers,
> NO logos, NO people or human faces. Generous empty paper margin around the subject, subject centered,
> simple readable silhouette that works small (64px) and large.

The first generation gave the mascot an ochre middle band; the owner then made the default plain steel (the
band is only ever a project's colour). `cut.py` repaints that band as steel (same lightness and grain, steel's
tint) and keeps its coverage as the `-band` mask, so the shipped art matches the bible above. New generations
should use the bible as written and need no repainting; to make a band mask for a new pose, generate it with
the ochre band (or paint the mask by hand).

Subject lines, in short (each was 2–3 variants; the best kept):

- **States** (square, with the reference): live, a content smile and three small steam curls; idle, eyes
  closed as lines, tilted, a few "z" strokes (drawn shapes, not type); deploying, lid hovering above, a
  determined face, hands pressing a cloth-wrapped parcel into the open top; failed, friendly not scary:
  spiral eyes, a wavy mouth, a sweat drop, lid knocked askew, a small ochre spill on the ground; preview,
  happy closed eyes, holding up a long-handled tasting spoon, one sparkle; night, asleep with a small crescent
  moon above.
- **Starters** (2026-10-06, not tins: they say what you make): a browser window with a slim title bar and a
  page of flat blocks, a small database cylinder at its corner (web app); a plug about to meet its socket, an
  ochre arrow in and a graphite arrow out (API); a single sheet with a folded corner (static site). Prompted with
  "NO mascot and NO lunch tins", cut with `keep='all'` so the page inside each stays paper.
- **Scenes:** a wooden hand cart with large spoked wheels carrying five tins, the mascot among them (og card);
  the same cart empty but for the mascot (first run); the mascot beside a two-shelf pantry of jars (backups);
  the mascot lifting its own lid under an empty dashed slot (empty inbox, 404).

## Regenerate

Sources stay out of git (they are about 2 MB each). In a work folder outside the repo:

1. **Generate:** `ART_SRC=<work> HEAVY=<path>/research/heavy.sh apps/dashboard/art/gen.sh <batch> <subjects.txt> <mascot-base.png>`
   (Codex image tool; run at most two at once). Pick the best of each into `<work>/picks/`.
2. **Cut out:** `ART_SRC=<work> python apps/dashboard/art/cut.py <work>/cut` (numpy, scipy, pillow,
   opencv-python-headless). It keys the paper out by flood fill from the edges (paper-coloured pockets enclosed
   by the drawing, like the inside of a handle, are background too, except the ones listed as `keep`), drops
   grain specks, unmixes the anti-aliased edge against the paper (alpha from the projection onto the nearest
   solid colour, the colour decontaminated), repaints the band and writes its mask. Edit `JOBS` for new
   files.
3. **Export:** `python apps/dashboard/art/export.py <work>/cut <work>/out` (pillow, `cwebp`). Mascot files
   share one 1100 px window on the 1254 px canvas, scaled to 800 (masks to 400); others are trimmed and
   centred in their canvas. WebP quality 84, alpha 90; masks quality 70. Copy `out/*` here.
4. Check each on paper and on graphite (with the plate) before shipping.
