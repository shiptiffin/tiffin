import { useId } from "react";
import { Select } from "@/components/ui/choice";
import { cn } from "@/lib/cn";

/**
 * How the box builds an app, in one place. Next.js is what Tiffin is tuned
 * for; the rest run, with fewer extras. A new framework is one entry here
 * (and one in the manifest's enum); the forms pick it up.
 */
export const BUILDS = [
  { value: "next", label: "Next.js", as: "a Next.js app" },
  { value: "static", label: "Static site", as: "a static site" },
  { value: "hono", label: "Hono", as: "a Hono server" },
  { value: "bun", label: "Bun or Node server", as: "a Bun or Node server" },
  // Python: Railpack's Python provider (uv); the box starts one Uvicorn process on $PORT, its app entry found for it. No Bun/Node runtime.
  { value: "fastapi", label: "FastAPI", as: "a FastAPI app" },
  { value: "python", label: "Python server", as: "a Python server" },
] as const;

/** "a Next.js app", for sentences. */
export const buildAs = (f: string) => BUILDS.find((b) => b.value === f)?.as ?? f;

/** A word about anything that isn't Next.js; none for Next.js. */
export function buildNote(framework: string): string | null {
  return framework === "next" ? null : "Tiffin is tuned for Next.js today. This runs too; if something’s off, pick another framework.";
}

/** Said when the box can't tell how a repository is built. */
export const UNKNOWN_BUILD =
  "Tiffin couldn’t tell how this is built. It runs Next.js apps best today, plus TanStack Start, Astro, Vite, static sites and Bun, Node or Hono servers. It will try a Bun server; pick the framework if that’s wrong.";

const field =
  "h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.84375rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";

/**
 * The folded-away overrides: which folder, and (where no framework picker sits
 * above) how it's built. Detection fills them; most people never open this.
 */
export function BuildSettings({
  framework,
  onFramework,
  path,
  onPath,
  className,
}: {
  framework?: string;
  onFramework?: (f: string) => void;
  path: string;
  onPath: (p: string) => void;
  className?: string;
}) {
  const uid = useId();
  return (
    <details className={cn("group", className)}>
      <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
        <span className="inline-block transition-transform group-open:rotate-90">›</span> Build settings
      </summary>
      <div className="mt-2 grid max-w-[34rem] gap-3 sm:grid-cols-2">
        {onFramework && (
          <div>
            <span id={`${uid}-as`} className="mb-1 block text-xs text-ink-3">
              Built as
            </span>
            <Select aria-labelledby={`${uid}-as`} value={framework ?? ""} onValueChange={onFramework} options={BUILDS.map((b) => ({ value: b.value, label: b.label }))} />
          </div>
        )}
        <label>
          <span className="mb-1 block text-xs text-ink-3">Folder</span>
          <input value={path} onChange={(e) => onPath(e.target.value.replace(/^\/+/, ""))} placeholder="the top of the repository" spellCheck={false} className={cn(field, "ident")} />
        </label>
      </div>
    </details>
  );
}
