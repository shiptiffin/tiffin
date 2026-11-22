import { useState } from "react";
import { Working, type SetEdit } from "@/components/project-rows";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { change } from "@/lib/staged";

/** The longest title, in characters (manifest.MaxTitle). */
export const MAX_TITLE = 60;

/** What is wrong with a title as typed (it is trimmed first), or undefined. Empty is fine: it clears it. */
export function titleProblem(raw: string): string | undefined {
  const t = raw.trim();
  if ([...t].length > MAX_TITLE) return `At most ${MAX_TITLE} characters.`;
  // Control characters, line breaks and the marks that reorder text (as the box refuses them).
  if (/[\p{Cc}\u2028\u2029\u200E\u200F\u061C\u202A-\u202E\u2066-\u2069]/u.test(t)) return "One line of plain text.";
  return undefined;
}

/** The edit that sets a project's title: the empty title is stored as absent. */
export function titleEdit(project: string, live: string | undefined, raw: string): SetEdit {
  const to = raw.trim() || undefined;
  return {
    kind: "set",
    path: ["title"],
    from: live,
    to,
    what: to ? `Title ${project} “${to}”` : `Clear the title of ${project}`,
    undo: live ? `${project} is titled “${live}” again` : `${project} has no title again`,
  };
}

/**
 * A project's title: the name people read, shown beside its slug in lists, ⌘K and
 * `tiffin projects list`. `title` in its config; the name itself never changes.
 */
export function ProjectTitle({ project, live, staged, writer }: { project: string; live?: string; staged?: SetEdit; writer: boolean }) {
  const shown = staged ? ((staged.to as string | undefined) ?? "") : (live ?? "");
  const [draft, setDraft] = useState<string | null>(null);
  const value = draft ?? shown;
  const problem = draft === null ? undefined : titleProblem(draft);
  const dirty = draft !== null && draft.trim() !== (live ?? "");
  const save = () => {
    if (!dirty || problem) return;
    change(project, titleEdit(project, live, value), { immediate: true });
    setDraft(null);
  };
  return (
    <form
      className="flex max-w-[32rem] flex-wrap items-start gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <div className="min-w-0 flex-1 basis-60">
        <Input
          aria-label={`Title of ${project}`}
          value={value}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") setDraft(null);
          }}
          placeholder={project}
          maxLength={MAX_TITLE * 2}
          disabled={!writer}
          aria-invalid={!!problem || undefined}
          aria-describedby="project-title-note"
          className="text-[0.875rem]"
        />
        <p id="project-title-note" className={problem ? "mt-1.5 text-xs text-danger" : "mt-1.5 text-xs text-ink-3"}>
          {problem ?? (
            <>
              Its name stays <span className="ident">{project}</span>: addresses, commands and keys don’t change. Leave it empty to show the name alone.
            </>
          )}
        </p>
      </div>
      {dirty && (
        <Button size="md" type="submit" disabled={!!problem}>
          Save
        </Button>
      )}
      {staged && <Working> Saving…</Working>}
    </form>
  );
}
