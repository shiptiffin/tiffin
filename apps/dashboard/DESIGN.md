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
| What the parts are called (Database, KV, Files, Email, Auth, Jobs, Analytics, Health, Shield) | `src/lib/names.ts` |
| Which parts a project has, its standalone part, where switching projects lands | `src/lib/sections.ts`, `src/lib/switch.ts` |
| Keyboard shortcuts and ⌘K commands a page adds (`useShortcut`, `useCommand`) | `src/lib/shortcuts.ts` |
| Connect (env, code, tunnel) for a part's page header | `src/components/connect.tsx` (`<ConnectButton part project>`) |
| Numbers and units | `src/lib/format.ts`; shares of the box in words: `src/lib/usage.ts` (`fullWords`, `shareWords`, `memWords`, `cpuWords`) |
| A project's status line and live address | `src/lib/pulse.ts` (`useProjectPulse`) |
| A project's icon | `src/components/project-icon.tsx` (`<ProjectIcon project size>`) |
| Making a change | `src/lib/staged.ts` (`change`, `usePending`, `undoChange`); the confirm dialog: `src/components/plan-tray.tsx` |
| Page frame, title, crumbs, empty state | `src/components/page.tsx` |

## Information architecture

- **Sidebar** never grows with the number of projects. Top: the project switcher (the current project, or All
  projects; search, five recent, All projects, New project; ⌘K and `g p` reach it). Outside a project:
  Projects, Usage (the box, divided by project, and its default limit), Activity (every project's changes),
  Health, Backups, API keys; Settings at the bottom (Your box, Machine, People, Shield). Inside a project:
  "← All projects", then only that project's sections: Overview, the parts it has (App(s), Database, KV,
  Files, Email, Auth, Analytics, Jobs), then Usage, History, Settings; and a small "Your box" group at the
  bottom (Usage, Activity, Health) so nothing is a dead end. A standalone project (just a database, KV,
  files or schedules; no app) shows only that part and opens on it. Switching projects keeps your section
  when the other project has it, else lands on its Overview, which says why.
- **Account menu** (your name, bottom left): API keys, "Sign in with Touch ID / Face ID", sign out. The word
  "passkey" is never a heading; at most a subtitle. Login offers "Sign in with Touch ID" with "or use a
  sign-in link" as the fallback.
- **Projects (home)**: one line about the box ("Your box is about two-fifths full") over one bar split by
  project, then a card per project: icon, name, live address, one status line, its parts as small glyphs,
  its share of the box. Sort, list view and (past six projects) search. Nothing about the platform here.
- **Project overview**: name, status line, live address; "What's in it" as tiles (only the parts it has) and
  a quiet Add. Each tile opens its own page. **Usage**: memory, CPU and disk, its database, cache and builds
  against their limits, one Limit choice (no limit, or a share of the box that holds all of it) with storage,
  cache and query time under its Advanced, copies and exact numbers under Details. **History**: plain
  sentences, who and when, Undo, and when its limit held it back. **Settings**: name, addresses, settings and secrets, built-in parts (toggles), keys
  that reach it, delete.
- **Settings** (the box): Your box (name, domain, look, moving it, updates), Machine (the carrier with the
  platform's parts), People, Shield.

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
