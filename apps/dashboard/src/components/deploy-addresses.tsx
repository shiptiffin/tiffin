import { ToggleGroup } from "radix-ui";
import { Working, type SetEdit } from "@/components/project-rows";
import { change } from "@/lib/staged";

const CHOICES: Array<{ value?: string; title: string; note: string }> = [
  { title: "Signed-in only", note: "People signed in to this box" },
  { value: "public", title: "Public", note: "Anyone with the address" },
];

/**
 * Who may open the address each version gets (d-<id>--<app>): deployAddresses
 * in its config, signed-in only unless it says "public".
 */
export function DeployAddresses({ project, live, staged }: { project: string; live?: string; staged?: SetEdit }) {
  const value = (staged ? (staged.to as string | undefined) : live) === "public" ? "public" : undefined;
  const pick = (to?: string) =>
    change(
      project,
      {
        kind: "set",
        path: ["deployAddresses"],
        from: live,
        to,
        what: to ? `Let anyone with the address open ${project}’s versions` : `Only people signed in here can open ${project}’s versions`,
        undo:
          live === "public"
            ? `Anyone with the address can open ${project}’s versions again`
            : `Only signed-in people can open ${project}’s versions again`,
      },
      { immediate: true },
    );
  return (
    <ToggleGroup.Root
      type="single"
      aria-label={`Who can open ${project}’s version addresses`}
      value={CHOICES.find((c) => c.value === value)?.title ?? ""}
      onValueChange={(t) => {
        const c = CHOICES.find((x) => x.title === t);
        if (c && c.value !== value) pick(c.value);
      }}
      className="flex flex-wrap gap-1.5"
    >
      {CHOICES.map((c) => (
        <ToggleGroup.Item
          key={c.title}
          value={c.title}
          className="group flex items-center gap-2.5 rounded-[10px] border border-rule-2 px-3 py-2 text-left transition-colors duration-[var(--dur-state)] hover:border-rule-3 data-[state=on]:border-brass data-[state=on]:bg-brass-wash"
        >
          <span className="grid size-4 shrink-0 place-items-center rounded-full border border-rule-3 group-data-[state=on]:border-brass" aria-hidden>
            <span className="hidden size-2 rounded-full bg-brass group-data-[state=on]:block" />
          </span>
          <span>
            <span className="block text-[0.875rem] font-[550] text-ink">{c.title}</span>
            <span className="block text-xs text-ink-3">{c.note}</span>
          </span>
        </ToggleGroup.Item>
      ))}
      {staged && <Working> Saving…</Working>}
    </ToggleGroup.Root>
  );
}
