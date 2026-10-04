import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";
import { deploysQuery } from "@/lib/pulse";

/**
 * A project's icon, in three states:
 *   1. its live app's own favicon (GET /v1/projects/{p}/icon, when the box has one);
 *   2. live without one: the framework's mark (Next.js, Hono, Bun, static), in ink;
 *   3. new or not live yet: a small tiffin carrier drawn from what's in it,
 *      one tier per part (an empty project is just lid and base).
 *
 *   <ProjectIcon project="shop" size={20} />
 */
export function ProjectIcon({ project, size = 16, className }: { project?: string; size?: number; className?: string }) {
  const id = useProjectIdentity(project);
  const [broken, setBroken] = useState(false);
  const box = cn("inline-grid shrink-0 place-items-center overflow-hidden", className);
  if (!project) return <span className={box} style={{ width: size, height: size }} aria-hidden />;
  if (id.icon && !broken)
    return (
      <span className={cn(box, "rounded-[4px]")} style={{ width: size, height: size }} aria-hidden>
        <img src={id.icon} alt="" width={size} height={size} className="size-full object-contain" onError={() => setBroken(true)} />
      </span>
    );
  return (
    <span className={cn(box, "text-ink-2")} style={{ width: size, height: size }} aria-hidden>
      {id.live && id.framework && MARKS[id.framework] ? MARKS[id.framework] : <TiffinGlyph tiers={id.parts} />}
    </span>
  );
}

/** What a project's icon is made from: its parts, its main app's framework, whether it's live, and its favicon. */
export function useProjectIdentity(project?: string) {
  const p = useQuery({ ...q.project(project ?? ""), enabled: !!project });
  const res = p.data?.resources ?? [];
  const apps = res.filter((r) => r.address.startsWith("app/")).map((r) => ({ name: r.address.slice(4), spec: (r.spec ?? {}) as { role?: string; framework?: string } }));
  // The main app: a web app with a server first (shop's "web", not its "docs"), then any web app, then any.
  const main = apps.find((a) => a.spec.role !== "worker" && a.spec.framework !== "static") ?? apps.find((a) => a.spec.role !== "worker") ?? apps[0];
  const d = useQuery({ ...deploysQuery(project ?? "", main?.name ?? ""), enabled: !!project && !!main });
  const live = !!d.data?.some((x) => !x.preview && x.status === "live");
  const parts = res.filter((r) => r.address.startsWith("app/") || r.address.startsWith("service/")).length;
  const icon = useQuery({
    queryKey: ["project-icon", project],
    queryFn: async () => {
      const url = `/v1/projects/${encodeURIComponent(project!)}/icon`;
      const r = await fetch(url, { method: "HEAD", credentials: "same-origin" });
      return r.ok ? url : null;
    },
    enabled: !!project && live,
    staleTime: 10 * 60_000,
    retry: false,
    refetchOnWindowFocus: false,
  });
  return { parts, framework: main?.spec.framework, live, icon: icon.data ?? undefined };
}

/**
 * The tiffin, drawn from what's in a project: a brass handle, a lid, one tier
 * per part (up to five shown), and the base. Line-drawn in the ink colour.
 */
export function TiffinGlyph({ tiers, className }: { tiers: number; className?: string }) {
  const n = Math.max(0, Math.min(5, tiers));
  const top = 5.4; // under the lid
  const bottom = 13.6; // above the base
  const h = n > 0 ? Math.min(2.8, (bottom - top) / n) : 0;
  const start = n > 0 ? bottom - h * n : bottom;
  return (
    <svg viewBox="0 0 16 16" className={cn("size-full", className)} fill="none" strokeLinejoin="round">
      <path d="M5.6 3.3c0-1.3 1.1-2.1 2.4-2.1s2.4.8 2.4 2.1" stroke="var(--brass)" strokeWidth={1.3} strokeLinecap="round" />
      <path d={`M3.6 ${start}V4.8c0-.5.4-.9.9-.9h7c.5 0 .9.4.9.9V${start}`} stroke="currentColor" strokeWidth={1.2} />
      {Array.from({ length: n }, (_, i) => (
        <rect key={i} x={3} y={start + i * h + 0.35} width={10} height={Math.max(0.9, h - 0.7)} rx={0.6} stroke="currentColor" strokeWidth={1.1} />
      ))}
      <path d={`M2.8 14.4h10.4`} stroke="currentColor" strokeWidth={1.3} strokeLinecap="round" />
    </svg>
  );
}

const MARKS: Record<string, React.ReactNode> = {
  next: (
    <svg viewBox="0 0 16 16" className="size-full">
      <circle cx="8" cy="8" r="7.2" fill="currentColor" />
      <path d="M5.6 11.2V4.8l5.2 6.9M10.6 4.8v4.1" stroke="var(--paper)" strokeWidth="1.3" fill="none" strokeLinecap="round" />
    </svg>
  ),
  hono: (
    <svg viewBox="0 0 16 16" className="size-full">
      <path d="M8.3 1.3c1.8 2.6 4.6 4.7 4.6 8.1a4.9 4.9 0 0 1-9.8 0c0-2.1 1.1-3.5 2.2-4.6.2 1.3.9 2.2 1.8 2.6C6.9 5.3 7.4 3.2 8.3 1.3Z" fill="currentColor" />
    </svg>
  ),
  bun: (
    <svg viewBox="0 0 16 16" className="size-full" fill="none">
      <ellipse cx="8" cy="9" rx="6.6" ry="5" stroke="currentColor" strokeWidth="1.3" />
      <path d="M4.8 6.2c.9-1 2-1.5 3.2-1.5" stroke="currentColor" strokeWidth="1.1" strokeLinecap="round" />
      <circle cx="6.2" cy="9.4" r=".8" fill="currentColor" />
      <circle cx="9.8" cy="9.4" r=".8" fill="currentColor" />
    </svg>
  ),
  static: (
    <svg viewBox="0 0 16 16" className="size-full" fill="none" stroke="currentColor" strokeWidth="1.2" strokeLinejoin="round">
      <path d="M4 1.8h5.6L12.4 4.6v9.6H4z" />
      <path d="M9.4 1.8v3h3M6 8h4.4M6 10.6h4.4" strokeLinecap="round" />
    </svg>
  ),
};
