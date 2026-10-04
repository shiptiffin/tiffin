# The dashboard's design system

It's for people. They see their projects, what's in each one, how much of the box each uses, make more, and
give or take resources. The machine's insides are one level down, in Settings. Paper, ink and brass;
Newsreader for the occasional sentence, Instrument Sans for the interface, Commit Mono for identifiers. Open
**`/_kit`** in the running dashboard for every primitive in both themes.

## Where things are

| What | Where |
|---|---|
| Tokens (colour, elevation, radii, motion) | `src/styles/tokens.css` |
| Fonts (self-hosted) | `src/styles/fonts.css` |
| Drawings that aren't layout (toggle, spinner, pilot light, diff, the carrier on Settings › Machine) | `src/styles/fusion.css` |
| Tailwind theme + type roles (`sentence`, `title`, `label`, `ident`, …) | `src/styles.css` |
| What the parts are called (Database, Cache, Files, Email, Auth, Jobs, Analytics, Health, Shield) | `src/lib/names.ts` |
| Numbers and units | `src/lib/format.ts`; shares of the box in words: `src/lib/usage.ts` (`fullWords`, `shareWords`, `memWords`, `cpuWords`) |
| A project's status line and live address | `src/lib/pulse.ts` (`useProjectPulse`) |
| A project's icon | `src/components/project-icon.tsx` (`<ProjectIcon project size>`) |
| Making a change | `src/lib/staged.ts` (`change`, `usePending`, `undoChange`); the confirm dialog: `src/components/plan-tray.tsx` |
| Page frame, title, crumbs, empty state | `src/components/page.tsx` |

## Information architecture

- **Sidebar** never grows with the number of projects. Top: the project switcher (the current project, or All
  projects; search, five recent, All projects, New project; ⌘K and `g p` reach it). Outside a project:
  Projects, and Settings at the bottom (its sections appear under it while you're in them). Inside a project:
  "← All projects", then only that project's sections: Overview, the parts it has (App(s), Database, Cache,
  Files, Email, Auth, Analytics, Jobs), then Usage, History, Settings.
- **Projects (home)**: one line about the box ("Your box is about two-fifths full") over one bar split by
  project, then a card per project: icon, name, live address, one status line, its parts as small glyphs,
  its share of the box. Sort, list view and (past six projects) search. Nothing about the platform here.
- **Project overview**: name, status line, live address; "What's in it" as tiles (only the parts it has) and
  a quiet Add. Each tile opens its own page. **Usage**: memory, CPU and disk, one Resources choice (grows as
  it needs, or a share of the box), copies and exact numbers under Advanced. **History**: plain sentences,
  who and when, Undo. **Settings**: name, addresses, settings and secrets, built-in parts (toggles), keys
  that reach it, delete.
- **Settings** (the box): Your box, Machine (the carrier with the platform's parts), Health, Backups,
  Shield, People, API keys, Passkeys, History of every project.

## Rules for every page

1. **Frame.** Wrap the page in `<Page>` (`wide` for grids and tables). Every page starts at the same left edge.
2. **Head.** A project's sub-page: `PageHeader` with `<Crumbs items={[{ label: project, to: "/projects/$project", params }, …]} />`
   and a noun title ("Usage"). One serif sentence at most, and it must agree with the page below it.
3. **Words.** Plain, warm, short; a person would say them. No jargon on primary surfaces: no tiers, no
   "irreversible/outbound", no `+0 ~1 −0`, no session ids, no plan hashes, no "lever", no MB tables. Parts by
   their names from `lib/names.ts`, the technology only as a small subtitle. Numbers humanised ("about a
   third of your box", "half a CPU"). Errors say the fix (`<ProblemNote>` shows the API's hint).
4. **Changes happen when you click.** A toggle, a stepper, Add, a limit: call `change(project, edit)`. It runs
   plan → apply with the plan's hash (an intent is written for you), so History and Undo work; the toast says
   what happened, with Undo. Rapid clicks on one project go as one change. While it applies, the control shows
   the new value with a spinner (`usePending`), and a tile being added says "Adding a database…". Nothing
   blocks the rest of the page.
5. **Confirm only what can't be undone or reaches outside the box.** `change()` opens the confirm dialog by
   itself when the plan comes back that way, whatever the control expected: what will happen in a line or
   two, exactly what is lost ("18,204 rows will be gone for good"), the project's name typed when real data
   goes, steps/diff/plan id behind Details. Deleting a project plans first, too.
6. **Familiar controls.** On/off: `<Breaker>` renders a horizontal toggle, brass when on, label beside it; a
   crashed service stays on and its row says "Stopped unexpectedly". Counts and sizes: `<Throttle>` renders a
   − 2 + stepper that stops at what fits on the box. Usage: `<SegMeter>` renders a plain rounded bar; put the
   value as text beside it. A share of the box: `<BoxBar>` (neutral shades, the project in view in brass).
   The only blink is the pilot light while something builds.
7. **Lists are rows on the page** (`divide-y divide-rule`, `border-y border-rule`). Cards only for objects: a
   project, a part of a project, a dialog.
8. **Colour.** Text `text-ink` / `-2` / `-3`; `text-ink-4` only for incidental text. Brass for the one
   primary action, the selected choice, a change on its way. `text-danger`, `text-warn-ink`, `text-ok` only
   when status isn't fine. Projects have icons, not colours.
9. **Snappy.** Only Projects ships in the first load; every other page loads on demand, and hovering a link
   runs its route's loader (code and data). Queries share keys, so moving between pages doesn't refetch.
   Skeletons are shaped like the content and appear only after 300 ms.
10. **Names.** People and agents through `lib/actors.ts`; key ids through `lib/who.ts`, so the owner reads as
    their name, not "Owner". History says "You" for you.

Check every page at 1440 and 390 px, light and dark (`e2e/screens-simple.spec.ts`), and ask: would a beginner
know what's going on within five seconds? Is there anything on screen they don't need?
