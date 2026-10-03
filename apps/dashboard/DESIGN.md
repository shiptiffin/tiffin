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
   and a noun `title` ("Storage"), in sans even when it names an identifier (a table, a bucket, a workflow).
   A section with tabs keeps one title ("Data", "Users") and lets the tabs say which part; never title a page
   with its own active tab. A page may open with one serif sentence (`className="sentence"`)
   that says the state of things, like the Box does, and it must agree with the page below it. Never more
   than two serif lines in a viewport; rails and lists show an intent's first sentence only.
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
7. **Levers**: services → `<Breaker>` (it prints its position, ON / OFF / TRIP, like a panel legend),
   counts and sizes → `<Throttle>` (with `maxFit` from `/v1/box/resources`), budgets and vitals →
   `<SegMeter>` (pass the raw value: a trace under half a segment lights nothing, a value past the scale
   gets a notch), something building → `<PilotLight state="busy">` (the only blink; no `animate-ping`
   anywhere), risk → `<RiskDots>`. History rows → `<SignedEntry>`. A guard asks you to type the name of
   the thing that can't come back, in an empty field.
8. **Copy.** Sentences a person would say, verbs with object and number on buttons ("Apply 3 changes
   to shop"), errors with their fix (`<ProblemNote>` shows the API's hint). Status only when it isn't
   fine: no "Ready" on every row.
9. **Motion** uses the tokens (`duration-[var(--dur-state)] ease-[var(--ease-out)]`); keyboard-driven
   things fade only; nothing animates on frequent actions.
10. **Names.** People and agents through `lib/actors.ts` (`actorName`, `actorWords`): "claude-code" reads
    "Claude Code". Token IDs (a change's actor, an approval's signer, a deploy's author) go through
    `lib/who.ts` (`q.tokenNames` → `.who`, `actorShown`, `tokenWho`): the owner token and every session
    resolve to the person's current name, so nothing prints "Owner" or `tok_…`. Memory through
    `lib/memory.ts` (`memoryModel`): the one basis every number adds up on.
11. **Rails show what stuck.** The Box and project rails drop a change together with the undo that
    cancelled it and say how many in one quiet line; the Ledger keeps both.

Check every restyled page at 1440 and 390 px, light and dark (`e2e/screens-fusion.spec.ts` shows how),
against `/_kit` and the anti-slop list in `research/2026-10-03-design/03-craft-and-brand.md` §10.
