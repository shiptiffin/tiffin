import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";

/** The spec of a project's "readonly" resource: the box holds its writes. */
type Hold = { reason?: "disk" | "limit"; message?: string };

const link = "font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink";

/**
 * "shop is read-only": the box stopped its database writes and uploads
 * (the data disk nearly full, or the project at its storage limit). Says
 * which, and how to fix it; writes come back by themselves once it's fixed.
 */
export function ReadOnlyBanner({ project, className }: { project: string; className?: string }) {
  const p = useQuery(q.project(project));
  const h = p.data?.resources?.find((r) => r.address === "readonly")?.spec as Hold | undefined;
  if (!h) return null;
  return (
    <div role="status" className={cn("max-w-[46rem] rounded-[10px] bg-danger-wash px-4 py-3 text-[0.9375rem] text-ink", className)}>
      <p className="font-[550]">{project} is read-only: its database refuses writes and its files refuse uploads.</p>
      {h.message && <p className="mt-1 text-sm text-ink-2">{h.message}</p>}
      <p className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-sm">
        {h.reason === "limit" ? (
          <Link to="/projects/$project/usage" params={{ project }} hash="storage" className={link}>
            Raise its storage limit
          </Link>
        ) : (
          <Link to="/usage" className={link}>
            See the box’s disk
          </Link>
        )}
        <Link to="/projects/$project/history" params={{ project }} className={link}>
          History
        </Link>
      </p>
    </div>
  );
}
