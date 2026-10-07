import { ChevronRight, Container, FolderTree } from "lucide-react";
import { useId, useState } from "react";
import { CommandFields, field } from "@/components/build-deploy";
import { FolderPicker } from "@/components/folder-picker";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { detected, draftOf, rowsFor, type BuildOverrides, type Command } from "@/lib/build-config";
import { cn } from "@/lib/cn";
import type { GitHubRepoDetail } from "@/lib/github";

/**
 * The way out when detection gets an import wrong (Vercel's Root Directory
 * and Build and Output Settings): the app's folder, picked from the
 * repository's tree, then the builder and the commands, each the detected
 * default until its Override is on. Folded away unless detection found
 * nothing; the framework picker sits above it.
 */
export function ImportBuild({
  detail,
  path,
  onPath,
  framework,
  value,
  onChange,
  className,
}: {
  detail?: GitHubRepoDetail;
  path: string;
  onPath: (p: string) => void;
  framework: string;
  value?: BuildOverrides;
  onChange: (b: BuildOverrides | undefined) => void;
  className?: string;
}) {
  const uid = useId();
  const roots = detail?.roots ?? [];
  const found = roots.find((r) => r.path === path && !r.workspace);
  const nothing = !!detail && roots.filter((r) => !r.workspace).length === 0;
  // Open by itself when detection found nothing; a click decides after that.
  const [toggled, setToggled] = useState<boolean | null>(null);
  const open = toggled ?? nothing;
  const [picker, setPicker] = useState(false);
  const b = value ?? {};
  const draft = { ...draftOf({ framework, path: ".", role: "web", instances: 1 }), builder: b.builder ?? ("" as const) };
  const rows = rowsFor(draft);
  const def = detected(draft);
  const set = (patch: Partial<BuildOverrides>) => {
    const next = { ...b, ...patch };
    const keep = Object.values(next).some((v) => v !== undefined);
    onChange(keep ? next : undefined);
  };
  const overrides = (["install", "build", "output", "command"] as const).filter((k) => b[k] !== undefined).length + (b.builder ? 1 : 0);
  const cmds: Array<[Command, keyof BuildOverrides]> = [
    ["install", "install"],
    ["build", "build"],
    ["output", "output"],
    ["start", "command"],
  ];

  return (
    <div className={cn("rounded-[10px] border border-rule-2", className)}>
      <button
        type="button"
        onClick={() => setToggled(!open)}
        aria-expanded={open}
        aria-controls={`${uid}-body`}
        className="flex w-full items-center gap-2 px-3.5 py-2.5 text-left text-[0.8125rem] text-ink-2 hover:text-ink"
      >
        <ChevronRight className={cn("size-3.5 text-ink-3 transition-transform duration-[var(--dur-state)]", open && "rotate-90")} aria-hidden />
        <span className="font-[550] text-ink">Build settings</span>
        <span className="ml-auto truncate text-xs text-ink-3">
          {overrides ? `${overrides} overridden` : nothing ? "Nothing detected: set them here" : "Detected"}
          {path ? <> · <span className="ident">{path}</span></> : null}
        </span>
      </button>
      {open && (
        <div id={`${uid}-body`} className="grid gap-4 border-t border-rule px-3.5 pt-3 pb-4">
          <div>
            <label htmlFor={`${uid}-root`} className="mb-1 block text-xs text-ink-3">
              Root directory · the folder with the app’s package.json or Dockerfile
            </label>
            <div className="flex min-w-0 gap-2">
              <div className="relative min-w-0 flex-1">
                <span className="ident pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-[0.8125rem] text-ink-4">/</span>
                <input id={`${uid}-root`} value={path} onChange={(e) => onPath(e.target.value.replace(/^\/+/, ""))} placeholder="the top of the repository" spellCheck={false} className={cn(field, "ident pl-5")} />
              </div>
              <Button type="button" size="md" variant="secondary" className="h-9" onClick={() => setPicker(true)} disabled={!detail}>
                <FolderTree className="size-3.5" aria-hidden /> Browse
              </Button>
            </div>
            {found?.builder === "dockerfile" && !b.builder && (
              <p className="mt-1.5 flex items-center gap-1.5 text-xs text-ink-3">
                <Container className="size-3" aria-hidden /> Found a Dockerfile and nothing else: it builds with that.
              </p>
            )}
          </div>

          {rows.builder && (
            <div>
              <span className="mb-1 block text-xs text-ink-3">Builder</span>
              <div className="flex flex-wrap items-center gap-3">
                <Segmented
                  label="Builder"
                  value={b.builder ?? ""}
                  onChange={(v) => set({ builder: v === "dockerfile" ? "dockerfile" : undefined })}
                  options={[
                    { value: "", label: "Automatic" },
                    { value: "dockerfile", label: "Dockerfile" },
                  ]}
                />
                {b.builder === "dockerfile" && (
                  <input aria-label="Dockerfile path" value={b.dockerfile ?? ""} onChange={(e) => set({ dockerfile: e.target.value || undefined })} placeholder="Dockerfile" spellCheck={false} className={cn(field, "ident max-w-[16rem] flex-1")} />
                )}
              </div>
            </div>
          )}

          <div>
            <span className="mb-1.5 block text-xs text-ink-3">{rows.commandsInImage ? "Commands · the Dockerfile installs and builds" : "Commands · detected unless you override one"}</span>
            <CommandFields
              show={cmds.filter(([c]) => (c === "output" ? rows.output : c === "start" ? rows.start : !rows.commandsInImage)).map(([c]) => c)}
              values={Object.fromEntries(cmds.map(([c, k]) => [c, b[k]]))}
              placeholders={def}
              errors={Object.fromEntries(cmds.map(([c, k]) => [c, b[k] !== undefined && !b[k]!.trim() ? "Type a command, or turn Override off." : undefined]))}
              onChange={(c, v) => set({ [cmds.find(([x]) => x === c)![1]]: v })}
            />
          </div>
          <p className="text-xs text-ink-3">All of these stay editable later in the app’s Settings › Build and deploy.</p>
        </div>
      )}
      {detail && (
        <FolderPicker open={picker} onOpenChange={setPicker} repo={detail.fullName} folders={detail.folders ?? []} roots={roots} value={path} onPick={onPath} />
      )}
    </div>
  );
}

/** Why picked overrides can't be created yet: an Override turned on and left empty. */
export function overridesProblem(b?: BuildOverrides): string | null {
  if (!b) return null;
  const blank = (["install", "build", "output", "command"] as const).find((k) => b[k] !== undefined && !b[k]!.trim());
  return blank ? `The ${blank === "command" ? "start command" : blank === "output" ? "output directory" : `${blank} command`} is empty: type one, or turn its Override off.` : null;
}
