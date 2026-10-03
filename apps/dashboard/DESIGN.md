# The dashboard's design system (the Fusion)

Ledger's paper, ink and brass; the box drawn as a tiffin carrier with a coloured enamel rim per project;
Instrument's levers and plan tray. Open **`/_kit`** in the running dashboard: every primitive in both
themes, with the line of code that makes it and measured contrast.

## Where things are

| What | Where |
|---|---|
| Tokens (colour, elevation, radii, motion) | `src/styles/tokens.css` |
| Fonts (Newsreader, Instrument Sans, Commit Mono; self-hosted) | `src/styles/fonts.css` |
| Drawings that aren't layout (breaker, throttle, meters, Stack, diff) | `src/styles/fusion.css` |
| Tailwind theme + type roles (`sentence`, `intent`, `entry`, `day`, `title`, `reading`, `label`, `ident`) | `src/styles.css` |
| Numbers and units (`int`, `bytes`, `mb`, `pct`, `duration`, `signed`, `countWords`, `NNBSP`) | `src/lib/format.ts` |
| Project colours (`ENAMELS`, `useEnamel`, `defaultEnamel`) | `src/lib/enamel.ts` |
| Staged changes (`stage`, `useStaged`, `applyEdits`, `openTray`) | `src/lib/staged.ts` |
| Page frame, title, crumbs, tabs, empty state | `src/components/page.tsx` |

## Rules for every page

1. **Frame.** Wrap the page in `<Page>` (`wide` for tables, `full` for the Box). Never centre or add your
   own max-width wrapper: every page starts at the same left edge.
2. **Head.** `PageHeader` with `eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params }, …]} />}`
   and a noun `title` ("Storage"). A page may open with one serif sentence (`className="sentence"`)
   that says the state of things, like the Box does. Never more than two serif lines in a viewport.
3. **Lists are rows on the page**: `divide-y divide-rule` between rows, `border-y border-rule` around the
   group, a `label` above it. No bordered card per item. Boxes (`rounded-[10px] border border-rule-2
   bg-paper-raised`) only for objects: a receipt, a code block, a waiting card, the plan tray.
4. **Colour.** Text `text-ink` / `text-ink-2` / `text-ink-3`; `text-ink-4` only for incidental text.
   Brass (`bg-brass`, `text-brass-ink`) for the one primary action and staged things. `text-danger`,
   `text-warn-ink`, `text-ok` only when status isn't fine. Agents' words in `text-graphite`.
   A project's enamel only via `<EnamelSwatch>` / `<Rim enamel>`.
5. **Numbers** go through `lib/format.ts` (or `<Qty value unit>` for a small unit). No raw `toFixed`,
   no "512MB", no "7m0s". Percent meters always run 0–100 (`<SegMeter scale>`).
6. **Changes.** Anything that edits `tiffin.config.ts` resources stages (`stage(project, edit)`) and goes
   through the plan tray; add a `StagedEdit` kind in `lib/staged.ts` for a new lever. Reversible
   operational actions (restart, retry) apply directly and confirm with `toast({ title, action: Undo })`.
   Irreversible ones keep the existing `HazardDialog` (type the name) and, where there's no passkey,
   `<HoldToCommit>`.
7. **Levers**: services → `<Breaker>`, counts and sizes → `<Throttle>` (with `maxFit` from
   `/v1/box/resources`), budgets and vitals → `<SegMeter>`, something building → `<PilotLight state="busy">`
   (the only blink), risk → `<RiskDots>`. History rows → `<SignedEntry>`.
8. **Copy.** Sentences a person would say, verbs with object and number on buttons ("Apply 3 changes
   to shop"), errors with their fix (`<ProblemNote>` shows the API's hint). Status only when it isn't
   fine: no "Ready" on every row.
9. **Motion** uses the tokens (`duration-[var(--dur-state)] ease-[var(--ease-out)]`); keyboard-driven
   things fade only; nothing animates on frequent actions.
10. **Names.** People and agents through `lib/actors.ts` (`actorName`, `actorWords`): "claude-code" reads
    "Claude Code". Memory through `lib/memory.ts` (`memoryModel`): the one basis every number adds up on.

Check every restyled page at 1440 and 390 px, light and dark (`e2e/screens-fusion.spec.ts` shows how),
against `/_kit` and the anti-slop list in `research/2026-10-03-design/03-craft-and-brand.md` §10.
