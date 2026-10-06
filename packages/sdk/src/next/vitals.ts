"use client";
/**
 * `@shiptiffin/sdk/next/vitals`: Web Vitals from a Next.js app to the box.
 *
 * ```tsx
 * // app/layout.tsx
 * import { WebVitals } from "@shiptiffin/sdk/next/vitals";
 *
 * export default function RootLayout({ children }: { children: React.ReactNode }) {
 *   return (
 *     <html lang="en">
 *       <body>
 *         <WebVitals />
 *         {children}
 *       </body>
 *     </html>
 *   );
 * }
 * ```
 *
 * It takes Next's own measurements (`useReportWebVitals`) and reports each
 * page under its route (`/products/[id]`, not `/products/42`), in one beacon
 * to `/_tiffin/vitals` when the page is hidden. Renders nothing.
 */
import { useEffect } from "react";
import { useParams, usePathname } from "next/navigation";
import { useReportWebVitals } from "next/web-vitals";
import { routeOf, VITALS_PATH, VitalsQueue } from "../vitals";

let queue: VitalsQueue | undefined;

// One stable callback for the whole app: Next subscribes again whenever it changes.
const report = (metric: { name: string; value: number }) => queue?.add(metric.name, metric.value);

export interface WebVitalsProps {
  /** Override where beacons go (default /_tiffin/vitals on this origin). */
  endpoint?: string;
}

export function WebVitals({ endpoint = VITALS_PATH }: WebVitalsProps = {}): null {
  const route = routeOf(usePathname() ?? "/", useParams());
  if (!queue && typeof window !== "undefined") queue = new VitalsQueue(endpoint, route);
  useEffect(() => queue?.setPath(route), [route]);
  useReportWebVitals(report);
  return null;
}
