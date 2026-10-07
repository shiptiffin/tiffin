/**
 * Framework presets: the framework as people know it (Next.js, Astro…),
 * mapped to how the box builds and runs it (the manifest's framework: next,
 * static, bun…). The GitHub picker shows the preset the box detected and
 * offers every one here, like Vercel's Framework Preset. Starters carry the
 * same ids (GET /v1/templates `preset`), so a new starter's framework only
 * needs an entry here for its name and mark.
 */
export type Preset = {
  id: string;
  name: string;
  /** The manifest framework it builds as. */
  build: string;
};

export const PRESETS: Preset[] = [
  { id: "nextjs", name: "Next.js", build: "next" },
  { id: "tanstack-start", name: "TanStack Start", build: "bun" },
  { id: "sveltekit", name: "SvelteKit", build: "bun" },
  { id: "react-router", name: "React Router", build: "bun" },
  { id: "nuxt", name: "Nuxt", build: "bun" },
  { id: "astro", name: "Astro", build: "static" },
  { id: "vite-react", name: "Vite + React", build: "static" },
  { id: "vite", name: "Vite", build: "static" },
  { id: "hono", name: "Hono", build: "hono" },
  { id: "html", name: "HTML", build: "static" },
  { id: "fastapi", name: "FastAPI", build: "fastapi" },
  { id: "server-other", name: "Other Bun or Node server", build: "bun" },
  { id: "python-other", name: "Other Python server", build: "python" },
  { id: "static-other", name: "Other static site", build: "static" },
];

/** Frameworks with a starter that builds and deploys on the box: no "tuned for Next.js" caveat for these. */
const TESTED = new Set(["nextjs", "tanstack-start", "sveltekit", "react-router", "nuxt", "astro", "vite-react", "vite", "hono", "html", "fastapi"]);
export const isTested = (id: string) => TESTED.has(id);

const byBuild: Record<string, string> = { next: "nextjs", hono: "hono", bun: "server-other", static: "static-other", fastapi: "fastapi", python: "python-other" };

/** The preset a detected folder shows: its own, else the catch-all for how it builds. */
export const presetOf = (r: { preset?: string; framework: string }) =>
  r.preset && PRESETS.some((p) => p.id === r.preset) ? r.preset : (byBuild[r.framework] ?? "server-other");

export const presetName = (id: string) => PRESETS.find((p) => p.id === id)?.name ?? id;

/**
 * How a picked preset builds. The detected preset keeps the detected build
 * (Astro with @astrojs/node runs as a server, not as files); any other uses
 * the preset's own.
 */
export function buildFor(id: string, detected?: { preset?: string; framework: string }): string {
  if (detected && presetOf(detected) === id) return detected.framework;
  return PRESETS.find((p) => p.id === id)?.build ?? "bun";
}
