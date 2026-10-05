import type { Manifest } from "@/api/client";

type Apps = NonNullable<Manifest["apps"]>;

/**
 * The project's main web app, served at the project's own name when it sets
 * no routes: the only web app, else the one named like the project, else
 * "web", else the first in apps' order (the order the plan sends them in).
 * Mirrors internal/manifest/address.go.
 */
export function mainApp(project: string, apps: Apps): string {
  const web = Object.keys(apps).filter((n) => apps[n]?.role !== "worker");
  if (web.length <= 1) return web[0] ?? "";
  return web.find((n) => n === project) ?? web.find((n) => n === "web") ?? web[0];
}

/**
 * Where app answers under the box domain when it sets no routes, beside the
 * other apps' routes: <project> for the main app, <project>-<app> otherwise
 * (and when <project> is already another app's).
 */
export function defaultAddress(project: string, app: string, apps: Apps): string {
  const own = `${project}-${app}`;
  const d = app === project || app === mainApp(project, apps) ? project : own;
  const claimed = Object.entries(apps).some(([n, a]) => n !== app && (a.routes ?? []).some((r) => r.toLowerCase() === d));
  return claimed ? own : d;
}

/** The first labels of every name a project's web apps answer at. */
export function addressesOf(project: string, apps: Apps | undefined): string[] {
  const all = apps ?? {};
  return Object.entries(all).flatMap(([n, a]) =>
    a.role === "worker" ? [] : (a.routes?.length ? a.routes : [defaultAddress(project, n, all)]).map((r) => r.split(".")[0].split("/")[0]),
  );
}
