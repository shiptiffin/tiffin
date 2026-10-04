import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Breaker } from "@/components/breaker";
import { useTitle } from "@/components/favicon";
import { Logo, Wordmark } from "@/components/logo";
import { MorphLabel } from "@/components/morph-label";
import { Nameplate } from "@/components/nameplate";
import { PilotLight } from "@/components/pilot";
import { Qty } from "@/components/qty";
import { SegMeter } from "@/components/seg-meter";
import { SignedEntry } from "@/components/signed-entry";
import { Carrier, Lid, Rim, TierColumns, TierHead, TierRow } from "@/components/stack";
import { INSTANCE_STOPS, Throttle } from "@/components/throttle";
import { TiffinGlyph } from "@/components/project-icon";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { contrast } from "@/lib/contrast";

/**
 * /_kit: the living style guide. Every primitive of the Fusion in both
 * themes, with the line of code that makes it. Not linked from the
 * interface; it is how the next person learns the system. Sample data on
 * this page is labelled as such and never leaves it.
 */
export function KitPage() {
  useTitle("Kit");
  return (
    <div className="w-full max-w-[84rem] px-4 pt-8 pb-24 sm:px-8 sm:pt-10 lg:px-12">
      <p className="label">Tiffin · the Fusion · /_kit</p>
      <h1 className="sentence mt-2 max-w-[44rem] text-ink">Paper, ink and brass; a carrier for the box; levers whose shape tells you the consequence.</h1>
      <p className="mt-3 max-w-[44rem] text-md text-ink-2">
        Every piece below is a component in <code className="ident">src/components</code> or a token in <code className="ident">src/styles/tokens.css</code>. Use
        them; don’t restyle them per page. Left is light, right is dark. Sample values on this page are made up for the demo.
      </p>
      <nav className="mt-6 flex flex-wrap gap-x-4 gap-y-1 text-sm text-brass-ink">
        {["Colour", "Type", "Numbers", "Levers", "Buttons", "Ledger", "Stack", "Mark", "Motion", "Rules"].map((s) => (
          <a key={s} href={`#${s.toLowerCase()}`} className="hover:underline hover:underline-offset-4">
            {s}
          </a>
        ))}
      </nav>

      <Section id="colour" title="Colour" note="About 90 % neutrals. Brass under 5 %: the one primary action, focus, the active marker, a change on its way, the project you’re looking at in a usage bar. Status colours only when status isn't fine. Projects have icons, not colours.">
        <Both>{(theme) => <Tokens theme={theme} />}</Both>
      </Section>

      <Section id="type" title="Type" note="Newsreader only for sentences (two serif lines per viewport at most). Instrument Sans for the interface. Commit Mono only for identifiers.">
        <Both>
          {() => (
            <div className="grid gap-4">
              <Spec code='className="sentence"'>
                <span className="sentence">Everything is running.</span>
              </Spec>
              <Spec code='className="intent"'>
                <span className="intent">Drop the imports table.</span>
              </Spec>
              <Spec code='className="entry"'>
                <span className="entry">Deployed web to shop.</span>
              </Spec>
              <Spec code='className="day"'>
                <span className="day">Today</span>
              </Spec>
              <Spec code='className="title"'>
                <span className="title">Storage</span>
              </Spec>
              <Spec code='<Qty className="reading" value="1.65" unit="GB" />'>
                <Qty className="reading" value="1.65" unit="GB" />
              </Spec>
              <Spec code="text-base text-ink / text-sm text-ink-2">
                <span className="text-base text-ink">Serving 41 requests a minute.</span>{" "}
                <span className="text-sm text-ink-2">Idle. The last job finished 4 min ago.</span>
              </Spec>
              <Spec code='className="label"'>
                <span className="label">Room left in the box</span>
              </Spec>
              <Spec code='className="ident"'>
                <span className="ident">plan d746 1a9e · shop.tiffin.localhost</span>
              </Spec>
            </div>
          )}
        </Both>
      </Section>

      <Section id="numbers" title="Numbers" note="Everything goes through src/lib/format.ts: thousands separators, a narrow no-break space before units, a real minus, humanised bytes and durations. <Qty> makes the unit small.">
        <div className="grid gap-2 text-sm text-ink-2 sm:grid-cols-2">
          {[
            ["int(1284)", "1,284"],
            ["bytes(1771094016)", "1.6 GB"],
            ["mb(1771094016)", "1,689"],
            ["signed(-24)", "−24"],
            ["duration(540000)", "6 days"],
            ["pct(0.313)", "31 %"],
            ["countWords(2, 'app')", "two apps"],
            ["plainWords('2 ban(s)')", "2 bans"],
          ].map(([c, r]) => (
            <p key={c} className="flex justify-between gap-4 border-b border-rule py-1.5">
              <code className="ident text-ink">{c}</code>
              <span className="tnum">{r}</span>
            </p>
          ))}
        </div>
      </Section>

      <Section id="controls" title="Controls" note="Familiar controls only: a toggle, a stepper, a progress bar. A click makes the change at once (with Undo in the toast); only what deletes data or reaches outside the box asks first.">
        <Both>{() => <Levers />}</Both>
      </Section>

      <Section id="buttons" title="Buttons" note="Verb and object. One primary (brass) per screen. Danger red only on the confirm that deletes data.">
        <Both>{() => <Buttons />}</Both>
      </Section>

      <Section id="ledger" title="History entries" note="The all-projects History (Settings › History) and a change's own page. People in ink, agents in graphite.">
        <Both>{() => <LedgerDemo />}</Both>
      </Section>

      <Section id="stack" title="Stack" note="The box as a tiffin carrier: loop, lid with nameplate and vitals, a tier per project on its enamel rim, the platform, room left, a base plate. Rows sit on the page; only objects get boxes.">
        <Both stacked>{() => <StackDemo />}</Both>
      </Section>

      <Section id="mark" title="Mark" note="The carrier as one line on a 32-unit grid: every stroke centre on an odd unit, so it is crisp at 16 px. The favicon carries the box's state (brass dot: waiting; red dot: down).">
        <Both>
          {() => (
            <div className="flex flex-wrap items-end gap-8 text-ink">
              {[64, 40, 32, 24, 16].map((s) => (
                <figure key={s} className="m-0 grid justify-items-center gap-2">
                  <span className="block" style={{ width: s, height: s }}>
                    <Logo className="size-full" title={`Mark at ${s} px`} />
                  </span>
                  <figcaption className="text-xs text-ink-3">{s}</figcaption>
                </figure>
              ))}
              <Wordmark />
            </div>
          )}
        </Both>
      </Section>

      <Section id="motion" title="Motion" note="Motion only for real state changes. One ease-out, nothing eases in. Everything can be interrupted. Reduced motion: no movement, the pilot holds steady, the seal just appears.">
        <table className="w-full text-sm">
          <tbody className="divide-y divide-rule">
            {[
              ["--ease-out", "cubic-bezier(.23,1,.32,1)", "Every system response."],
              ["--dur-press", "100 ms · scale .97", "Buttons and levers under the finger."],
              ["--dur-state", "150 ms", "Hover, breaker throw, guard lifting to red."],
              ["--dur-detent", "120 ms", "Throttle snapping to a stop."],
              ["--dur-enter", "220 ms · opacity + 4 px rise", "Something new: a Ledger entry, a toast."],
              ["--dur-exit", "150 ms", "Leaving is faster than arriving."],
              ["--dur-tray", "320 ms · spring, no bounce", "The plan tray and sheets."],
              ["--dur-unlatch", "200 ms", "A tier lifting out of the Stack to open its page."],
              ["--dur-seal", "600 ms · once", "The brass seal."],
              ["pilot", "1 Hz steps", "The only blink: something is building."],
              ["keyboard", "0–100 ms · fade only", "⌘K, arrow keys."],
            ].map(([a, b, c]) => (
              <tr key={a} className="max-sm:grid max-sm:grid-cols-2 max-sm:gap-x-3 max-sm:py-2">
                <td className="ident py-2 text-ink max-sm:py-0 sm:w-40">{a}</td>
                <td className="ident py-2 text-ink-3 max-sm:py-0 sm:w-56">{b}</td>
                <td className="py-2 text-ink-2 max-sm:col-span-2 max-sm:py-0">{c}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      <Section id="rules" title="Rules" note="The ones that keep it from turning into a costume. Full list: research/2026-10-03-design/03-craft-and-brand.md §10.">
        <ul className="grid gap-x-10 gap-y-2 text-sm text-ink-2 md:grid-cols-2">
          {[
            "Rows on the page with hairlines. Boxes only for objects: a receipt, code, the plan tray, a waiting card.",
            "One left edge: every page starts 48 px from the sidebar (Page component).",
            "Serif for sentences only; never for labels, numbers or nouns. Two serif lines per viewport at most.",
            "No gradients, glass, texture, hazard tape, emoji, purple or pulsing badges. Only the pilot light blinks.",
            "Status colour only when status isn't fine. ‘Ready’ is the default and says nothing.",
            "Numbers through lib/format.ts; no delta without a real baseline (‘nothing to compare yet’).",
            "Plain sentences a person would say. At most one food pun per page, never in nouns or buttons.",
            "Every change is staged and planned; nothing applies from a lever directly.",
          ].map((r) => (
            <li key={r} className="border-b border-rule py-2">
              {r}
            </li>
          ))}
        </ul>
      </Section>
    </div>
  );
}

function Section({ id, title, note, children }: { id: string; title: string; note: string; children: ReactNode }) {
  return (
    <section id={id} className="mt-14 scroll-mt-6 border-t border-rule-2 pt-6">
      <div className="mb-5 max-w-[46rem]">
        <h2 className="text-[1.0625rem] font-[550] text-ink">{title}</h2>
        <p className="mt-1 text-sm text-ink-2">{note}</p>
      </div>
      {children}
    </section>
  );
}

/** The same thing in light and dark, side by side. */
function Both({ children, stacked }: { children: (theme: "light" | "dark") => ReactNode; stacked?: boolean }) {
  return (
    <div className={cn("grid overflow-hidden rounded-[14px] border border-rule-2", !stacked && "xl:grid-cols-2")}>
      {(["light", "dark"] as const).map((t) => (
        <div key={t} data-theme={t} className="min-w-0 bg-paper p-6 text-ink">
          <p className="label mb-4">{t === "light" ? "Light, the hero" : "Dark, authored"}</p>
          {children(t)}
        </div>
      ))}
    </div>
  );
}

function Spec({ code, children }: { code: string; children: ReactNode }) {
  return (
    <div className="grid gap-1">
      <div className="min-w-0">{children}</div>
      <code className="ident text-[0.6875rem] text-ink-3">{code}</code>
    </div>
  );
}

const tokenJobs: Array<[string, string, string?]> = [
  ["paper", "Ground: the page."],
  ["paper-sunk", "Hover, wells, code."],
  ["paper-raised", "Menus, trays, objects."],
  ["rule", "Hairline between rows."],
  ["rule-3", "The Stack outline, rims."],
  ["ink", "Text; people.", "paper"],
  ["ink-2", "Status sentences.", "paper"],
  ["ink-3", "Labels, margins, units.", "paper-sunk"],
  ["ink-4", "Incidental only (3:1).", "paper-sunk"],
  ["graphite", "Agents, until signed.", "paper"],
  ["brass", "The one primary action, focus fill."],
  ["brass-ink", "Brass words on paper.", "paper"],
  ["danger", "Irreversible, tripped, down.", "paper"],
  ["warn-ink", "Past a printed threshold.", "paper"],
  ["ok", "Pilot steady, nothing else.", "paper"],
];

function Tokens({ theme }: { theme: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [vals, setVals] = useState<Record<string, string>>({});
  useLayoutEffect(() => {
    const cs = getComputedStyle(ref.current!);
    const names = [...tokenJobs.map((t) => t[0]), "paper", "paper-sunk", "on-brass"];
    setVals(Object.fromEntries(names.map((n) => [n, cs.getPropertyValue(`--${n}`).trim()])));
  }, [theme]);
  const ratio = (fg: string, bg: string) => {
    const r = vals[fg] && vals[bg] ? contrast(vals[fg], vals[bg], vals.paper) : null;
    return r ? r.toFixed(2) : "…";
  };
  return (
    <div ref={ref}>
      <div className="divide-y divide-rule">
        {tokenJobs.map(([n, job, on]) => (
          <div key={n} className="grid grid-cols-[22px_96px_minmax(0,1fr)_auto] items-center gap-x-3 py-1.5 text-[0.78125rem]">
            <i className="size-[22px] rounded-[5px] shadow-[inset_0_0_0_1px_var(--rule)]" style={{ background: `var(--${n})` }} />
            <span className="ident text-ink">{n}</span>
            <span className="truncate text-ink-2">{job}</span>
            <span className="text-right text-ink-3 tnum">{on ? `${ratio(n, on)} on ${on}` : ""}</span>
          </div>
        ))}
        <div className="grid grid-cols-[22px_96px_minmax(0,1fr)_auto] items-center gap-x-3 py-1.5 text-[0.78125rem]">
          <i className="size-[22px] rounded-[5px] bg-brass" />
          <span className="ident text-ink">on-brass</span>
          <span className="text-ink-2">Text on a brass button.</span>
          <span className="text-ink-3 tnum">{ratio("on-brass", "brass")} on brass</span>
        </div>
      </div>
    </div>
  );
}

function Levers() {
  const [svc, setSvc] = useState<"on" | "off" | undefined>(undefined);
  const [inst, setInst] = useState(2);
  return (
    <div className="grid gap-8">
      <div>
        <p className="label mb-3">Toggle (on/off)</p>
        <div className="flex flex-wrap gap-8">
          {(
            [
              ["On", <Breaker key="a" size="md" label="Database" state="on" />],
              ["Off", <Breaker key="b" size="md" label="Email" state="off" />],
              ["Applying (click)", <Breaker key="d" size="md" label="Cache" state="on" staged={svc} onFlip={(x) => setSvc(x === "on" ? undefined : x)} />],
              ["Label beside", <Breaker key="e" label="Analytics" state="on" printed="beside" />],
            ] as Array<[string, ReactNode]>
          ).map(([l, b]) => (
            <figure key={l} className="m-0 grid justify-items-start gap-2">
              {b}
              <figcaption className="text-xs text-ink-3">{l}</figcaption>
            </figure>
          ))}
        </div>
        <p className="mt-3 text-xs text-ink-3">A crashed service stays “on”; the row says “Stopped unexpectedly”.</p>
        <code className="ident mt-1 block text-[0.6875rem] text-ink-3">{'<Breaker label="Database" state="on" staged="off" onFlip={…} />'}</code>
      </div>
      <div>
        <p className="label mb-2">Stepper (click, or focus and use ↑/↓)</p>
        <div className="flex flex-wrap items-start gap-8">
          <Throttle label="web copies" stops={INSTANCE_STOPS} value={inst} applied={inst} maxFit={8} onCommit={setInst} />
          <Throttle label="worker copies" stops={INSTANCE_STOPS} value={3} applied={2} maxFit={6} printed={(n) => `${n} copies, applying`} />
          <Throttle label="api copies" stops={INSTANCE_STOPS} value={4} applied={4} maxFit={4} />
        </div>
        <code className="ident mt-3 block text-[0.6875rem] text-ink-3">{"<Throttle label stops={INSTANCE_STOPS} value applied maxFit onCommit />"}</code>
      </div>
      <div>
        <p className="label mb-3">Progress bar</p>
        <div className="grid gap-4">
          {(
            [
              ["CPU", <SegMeter key="c" label="CPU" value={9} />, "9 %"],
              ["Memory", <SegMeter key="m" label="Memory" value={420} max={1900} add={256} />, "420 MB, +256"],
              ["Disk, nearly full", <SegMeter key="d" label="Disk" value={97} warnAt={0.8} fullAt={0.95} />, "39.8 GB"],
            ] as Array<[string, ReactNode, string]>
          ).map(([l, m, v]) => (
            <div key={l} className="grid grid-cols-[120px_minmax(0,1fr)_96px] items-center gap-3 text-sm">
              <span className="text-ink-2">{l}</span>
              {m}
              <span className="text-right tnum">{v}</span>
            </div>
          ))}
        </div>
      </div>
      <div className="grid gap-6 sm:grid-cols-2">
        <div>
          <p className="label mb-3">Something happening</p>
          <div className="grid gap-2 text-sm text-ink-2">
            <span className="flex items-center gap-2 text-brass-ink">
              <span className="spinner" /> Adding a database…
            </span>
            <span className="flex items-center gap-2">
              <PilotLight state="busy" /> Building (the only blink)
            </span>
          </div>
        </div>
        <div>
          <p className="label mb-3">Project icons</p>
          <div className="flex items-center gap-4 text-sm text-ink-2">
            {[0, 1, 3, 5].map((n) => (
              <span key={n} className="grid justify-items-center gap-1">
                <span className="size-6 text-ink-2">
                  <TiffinGlyph tiers={n} />
                </span>
                <span className="text-xs text-ink-3">{n} parts</span>
              </span>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function Buttons() {
  const [reviewed, setReviewed] = useState(false);
  return (
    <div className="grid gap-6">
      <div className="flex flex-wrap items-center gap-2.5">
        <Button variant="primary">New project</Button>
        <Button>Add a setting…</Button>
        <Button variant="ghost">Cancel</Button>
        <Button variant="danger">Delete for good</Button>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" size="lg" onClick={() => setReviewed((r) => !r)}>
          <MorphLabel text={reviewed ? "Creating…" : "Create shop"} />
        </Button>
        <span className="text-sm text-ink-3">MorphLabel: click to morph</span>
      </div>
      <div>
        <Button
          onClick={() =>
            toast({ title: "Added a database to shop.", action: { label: "Undo", run: () => void toast({ title: "Undone (demo)." }) } })
          }
        >
          Show a toast with Undo
        </Button>
        <code className="ident mt-2 block text-[0.6875rem] text-ink-3">{"toast({ title, detail, action: { label: 'Undo', run } })"}</code>
      </div>
    </div>
  );
}

function LedgerDemo() {
  return (
    <div className="grid items-center gap-6">
      <div className="divide-y divide-rule border-y border-rule">
        <SignedEntry time="10:31" actor={{ kind: "human", name: "Bilal" }} intent="Deployed web v42 to shop." counts={{ create: 0, update: 1, delete: 0 }} tier="reversible" />
        <SignedEntry
          time="09:58"
          actor={{ kind: "agent", name: "Claude Code", model: "claude-opus-5-5", session: "51c0a2" }}
          intent="Raised Valkey’s memory cap for shop to 64 MB."
          counts={{ create: 0, update: 1, delete: 0 }}
          tier="reversible"
          signature="within its grant"
        />
        <SignedEntry
          time="10:46"
          timeNote="signed"
          actor={{ kind: "agent", name: "Claude Code", session: "7f3a90" }}
          intent="Dropped the imports table from shop’s database."
          counts={{ create: 0, update: 0, delete: 1 }}
          tier="irreversible"
          signature="can’t be undone"
        />
      </div>
    </div>
  );
}

function StackDemo() {
  return (
    <div className={cn("pt-2")}>
      <Carrier label="Sample box">
        <Lid>
          <Nameplate name="tiffin" where="Mac · Lima" version="Tiffin 0.4.2" uptime="up 6 days" domain="tiffin.localhost" />
        </Lid>
        <Rim />
        <TierColumns />
        <Rim />
        <TierHead name="shop" about="three apps, six services" total="826" />
        <TierRow
          lever={<Throttle size="mini" label="web instances" stops={INSTANCE_STOPS} value={2} applied={2} maxFit={8} />}
          name="web"
          sub="Next.js · 2 instances"
          status="41 requests a minute. Went live 13 min ago."
          share={<SegMeter size="row" segments={16} max={512} value={296} label="web memory" />}
          amount="296"
        />
        <TierRow
          lever={<Breaker label="Postgres" state="on" staged="off" />}
          name={
            <>
              Postgres <span className="label ml-1 text-brass-ink">staged</span>
            </>
          }
          sub="Database"
          status="Comes out of shop when you apply."
          staged
        />
        <TierRow lever={<Breaker label="Valkey" state="tripped" />} name="Valkey" sub="Cache" status={<span className="text-danger">Tripped: out of memory.</span>} fault />
        <Rim />
        <div className="room tier-grid m-3 min-h-14 py-3 max-sm:mx-2 max-sm:flex max-sm:flex-col max-sm:items-start max-sm:gap-1.5 max-sm:px-3.5">
          <div className="col-start-2 text-sm font-[550]">Room left</div>
          <p className="col-span-2 col-start-3 text-sm text-ink-2">Enough for about four more apps the size of web.</p>
          <div className="col-start-5 text-right font-[550] tnum">
            2,407<span className="u">&#8239;MB</span>
          </div>
        </div>
      </Carrier>
    </div>
  );
}
