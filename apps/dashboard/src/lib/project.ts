import { useParams, useSearch } from "@tanstack/react-router";

/** The project in view: from /projects/$project or the activity filter. */
export function useCurrentProject() {
  const fromPath = useParams({ strict: false, select: (p: { project?: string }) => p.project });
  const fromSearch = useSearch({ strict: false, select: (s: { project?: string }) => s.project });
  return fromPath ?? fromSearch;
}
