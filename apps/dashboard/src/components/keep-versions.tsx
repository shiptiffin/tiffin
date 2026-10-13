import { ToggleGroup } from "radix-ui";
import { Working, type SetEdit } from "@/components/project-rows";
import { change } from "@/lib/staged";

/** What a project keeps when its config doesn't say (keepVersions absent). */
export const DEFAULT_KEEP = 5;

const CHOICES = [0, 3, 5, 10, 20];

const note = (n: number) => (n === 0 ? "Only the live one" : `Roll back to the last ${n}${n === DEFAULT_KEEP ? " (default)" : ""}`);

/** The edit that makes a project keep n earlier versions: the default is stored as absent. */
export function keepEdit(project: string, live: number | undefined, n: number): SetEdit {
  const words = (k: number) => (k === 0 ? "only the live version" : `the last ${k} versions`);
  return {
    kind: "set",
    path: ["keepVersions"],
    from: live,
    to: n === DEFAULT_KEEP ? undefined : n,
    what: `Keep ${words(n)} of ${project}’s apps`,
    undo: `${project}’s apps keep ${words(live ?? DEFAULT_KEEP)} again`,
  };
}

/**
 * How many earlier versions each app keeps to roll back to or open at their own
 * address: keepVersions in its config, 0 to 20, 5 unless it says.
 */
export function KeepVersions({ project, live, staged }: { project: string; live?: number; staged?: SetEdit }) {
  const value = (staged ? (staged.to as number | undefined) : live) ?? DEFAULT_KEEP;
  // A number set in tiffin.config.ts that the dashboard doesn't offer stays shown as it is.
  const choices = CHOICES.includes(value) ? CHOICES : [...CHOICES, value].sort((a, b) => a - b);
  return (
    <ToggleGroup.Root
      type="single"
      aria-label={`How many earlier versions ${project}’s apps keep`}
      value={String(value)}
      onValueChange={(t) => {
        const n = Number(t);
        if (t !== "" && n !== value) change(project, keepEdit(project, live, n), { immediate: true });
      }}
      className="flex flex-wrap gap-1.5"
    >
      {choices.map((n) => (
        <ToggleGroup.Item
          key={n}
          value={String(n)}
          className="group flex items-center gap-2.5 rounded-[10px] border border-rule-2 px-3 py-2 text-left transition-colors duration-[var(--dur-state)] hover:border-rule-3 data-[state=on]:border-brass data-[state=on]:bg-brass-wash"
        >
          <span className="grid size-4 shrink-0 place-items-center rounded-full border border-rule-3 group-data-[state=on]:border-brass" aria-hidden>
            <span className="hidden size-2 rounded-full bg-brass group-data-[state=on]:block" />
          </span>
          <span>
            <span className="block text-[0.875rem] font-[550] text-ink">{n === 0 ? "None" : n}</span>
            <span className="block text-xs text-ink-3">{note(n)}</span>
          </span>
        </ToggleGroup.Item>
      ))}
      {staged && <Working> Saving…</Working>}
    </ToggleGroup.Root>
  );
}
