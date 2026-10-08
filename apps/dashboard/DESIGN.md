# The dashboard's design system

It's for people. They see their projects, what's in each one, how much of the box each uses, make more, and
give or take resources. The machine's insides are one level down, in Settings. Warm white, ink and brass;
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
| Which parts a project has (Database, KV, Files, Email, Analytics and Jobs always; Auth when added), where switching projects lands | `src/lib/sections.ts`, `src/lib/switch.ts` |
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
  Health, Backups, API keys, Settings. Settings swaps the sidebar for its own (← Back, then
  Your box: General, Machine, Git, DNS, Shield, People; You: Sign-ins, Touch ID / Face ID), so the sidebar
  never goes three levels deep. Inside a project: "← All projects", then only that project's sections:
  Overview, Deployments, Logs, Analytics, Observability, Domains, Environment Variables, its services,
  then Activity (the same changes as the box's Activity, for this project) and Settings. Every project has a Database, KV,
  Files, Email, Analytics and Jobs, so there is no Add for them and no empty "add it" page; Auth is the one part that is
  added. A project with no apps shows only Overview, Database, KV, Files, Jobs, Activity and Settings. Switching projects keeps your section
  when the other project has it, else lands on its Overview, which says why.
- **Frame.** On a desktop the sidebar is a tinted ground (`--side`) and the page a white panel inset 8 px
  on it (Linear, Supabase); a phone gets the plain page.
- **Account menu** (your name, bottom left): Theme (system, light, dark as three icons),
  API keys, Sign-ins, "Sign in with Touch ID / Face ID", sign out. The word
  "passkey" is never a heading; at most a subtitle. Login offers "Sign in with Touch ID" with "or use a
  sign-in link" as the fallback.
- **Projects (home)**: the box's memory, CPU and disk in one quiet line under the title (linking to
  Usage), then a card per project: icon, name, live address, one status line, its parts as small glyphs. Sort, list view and (past six projects) search. Nothing about the platform here.
- **Project overview**: name, status line, live address (its own domain once that's live, the automatic
  address listed under it as "also at"); "What's in it" as tiles (only the parts it has) and
  a quiet Add. Each tile opens its own page. **Usage**: memory, CPU and disk, its database, cache and builds
  against their limits, one Limit choice (no limit, or a share of the box that holds all of it) with storage,
  cache and query time under its Advanced, copies and exact numbers under Details. **History**: plain
  sentences, who and when, Undo, and when its limit held it back. **Settings**: name, addresses, settings and secrets, sign-in on or off, keys
  that reach it, and the Danger zone: one bordered card of rows (title and what happens, the button on the right): Delete all data
  in Database, KV or Files (restorable for 7 days, with a Restore row while it is), then Delete project.
- **Settings** (the box): Your box (name, domain, look, moving it, updates), Machine (the carrier with the
  platform's parts), People, Sign-ins (where you're signed in, sign out, the last 30 days), Shield.

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
   value as text beside it. The box's memory: `<BoxBar>`, read like a phone's storage bar: each project in its own colour (its icon's), the five biggest by name, then the rest together, then System (Linux and the shared services) in grey, with a legend of names and sizes. It lives on Usage only.
   The only blink is the pilot light while something builds.
7. **Lists are rows on the page** (`divide-y divide-rule`, `border-y border-rule`). Cards only for objects: a
   project, a part of a project, a dialog.
8. **Colour.** Text `text-ink` / `-2` / `-3`; `text-ink-4` only for incidental text. Brass for the one
   primary action, the selected choice, a change on its way. Charts use the data colours: `--data` (warm
   orange) for the series a chart is about, `--data-2` (blue) beside it, then `--chart-3…5`; bars in lists
   are `bg-data-wash`; a comparison period is a dashed `--ink-4` line. `text-danger`, `text-warn-ink`, `text-ok` only
   when status isn't fine. A project's colour is its icon's, and appears only where it stands for that project (its icon, its share of the box).
9. **Snappy.** Only Projects ships in the first load; every other page loads on demand, and hovering a link
   runs its route's loader (code and data). Queries share keys, so moving between pages doesn't refetch.
   Skeletons are shaped like the content and appear only after 300 ms.
10. **Names.** People and agents through `lib/actors.ts`; key ids through `lib/who.ts`, so the owner reads as
    their name, not "Owner". History says "You" for you.

11. **Disclose progressively.** The first view holds what most people need for the task; the rest is one
    step away (a closed disclosure whose label says what's inside, a link, or the empty state), never two.
    Say each fact once per screen. A number earns its place only if it changes what someone does next
    (the box's numbers sit quietly under Projects and take a colour only when one runs short; usage
    lives on Usage, never on each card). A lede only when it adds something the title doesn't. Terminal
    and agent instructions sit behind "From a terminal" or "Other ways to …", except where they are an
    empty state's one action. An empty state: the status, one line that teaches, one action. Tooltips only
    for optional detail, never for something a task needs. Sources: NN/g on progressive disclosure, empty
    states and tooltips; GOV.UK Details; Linear's 2026 refresh.

Check every page at 1440 and 390 px, light and dark (`e2e/screens-simple.spec.ts`), and ask: would a beginner
know what's going on within five seconds? Is there anything on screen they don't need?
