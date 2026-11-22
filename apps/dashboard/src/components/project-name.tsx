import { useQuery } from "@tanstack/react-query";
import { useCallback } from "react";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";

/**
 * Each project's title (the name people read, `title` in its config) by its
 * name, from the projects list: for lists whose rows come from elsewhere (usage,
 * analytics). The name stays the key everywhere; the title is only shown.
 */
export function useProjectTitles(): (project: string) => string | undefined {
  const projects = useQuery(q.projects);
  const data = projects.data;
  return useCallback((project: string) => data?.find((p) => p.name === project)?.title || undefined, [data]);
}

/**
 * A project as lists show it: its title with its name small beside it, or the
 * name alone when it has no title. Truncates in a flex row.
 */
export function ProjectName({ name, title, className }: { name: string; title?: string; className?: string }) {
  if (!title) return <span className={cn("truncate", className)}>{name}</span>;
  return (
    <span className={cn("flex min-w-0 items-baseline gap-1.5", className)} title={`${title} (${name})`}>
      <span className="truncate">{title}</span>
      <span className="shrink-0 text-[0.75em] font-normal text-ink-4">{name}</span>
    </span>
  );
}
