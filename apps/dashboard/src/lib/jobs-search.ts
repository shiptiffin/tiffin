// The Jobs area's search parameters, read by the router (kept out of the
// area's own chunk so the first load stays small). `do` opens a dialog:
// a schedule or queue form (with `name`, editing that one), a test job or a
// workflow run; `id` selects a run in Runs; `kind`, `state`, `queue` and
// `q` (words in a run's name or ID) filter it.
export type JobsDo = "schedule" | "queue" | "send" | "run";
export type JobsSearch = { do?: JobsDo; name?: string; id?: string; kind?: string; state?: string; queue?: string; q?: string };

const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);

export function jobsSearch(s: Record<string, unknown>): JobsSearch {
  const d = str(s.do);
  const out: JobsSearch = {};
  if (d === "schedule" || d === "queue" || d === "send" || d === "run") out.do = d;
  for (const k of ["name", "id", "kind", "state", "queue", "q"] as const) {
    const v = str(s[k]);
    if (v) out[k] = v;
  }
  return out;
}
