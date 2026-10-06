/**
 * `tiffin-sdk/vitals`: send Web Vitals (LCP, INP, CLS, FCP, TTFB) from real
 * visitors' browsers to the box. They go to `/_tiffin/vitals` on the page's
 * own origin (the box answers it on every app host), once per page load,
 * when the page is hidden, so they cost no request while the visitor is on
 * the page. The project needs `services: { analytics: {} }`; read them with
 * `tiffin analytics vitals` or on the dashboard's Analytics page.
 *
 * ```ts
 * import { reportWebVitals } from "tiffin-sdk/vitals";
 * reportWebVitals();                                    // any framework, in browser code
 * reportWebVitals({ path: () => "/products/[id]" });    // report a route instead of the path
 * ```
 *
 * In Next.js, render `<WebVitals />` from `tiffin-sdk/next/vitals` in the root
 * layout instead: it uses Next's own measurements and reports routes such as
 * `/products/[id]`.
 */
/** Where the box takes Web Vitals, on every app host. */
export declare const VITALS_PATH = "/_tiffin/vitals";
/**
 * The route a path belongs to: each segment that is a route parameter's
 * value becomes `[name]`, a catch-all's segments `[...name]`.
 */
export declare function routeOf(pathname: string, params?: Record<string, string | string[] | undefined> | null): string;
/**
 * Collects one page's metrics and sends them in one beacon when the page
 * is hidden, or when the path changes (a client-side navigation).
 */
export declare class VitalsQueue {
    private endpoint;
    private metrics;
    private path;
    constructor(endpoint?: string, path?: string);
    setPath(path: string): void;
    add(name: string, value: number): void;
    flush(): void;
}
export interface ReportOptions {
    /** Override where beacons go (default /_tiffin/vitals on this origin). */
    endpoint?: string;
    /** The page or route to report under (default location.pathname). */
    path?: string | (() => string);
}
/**
 * Measure this page load's Web Vitals with the browser's performance
 * observers and send them once, when the page is hidden. Returns a function
 * that stops measuring. Does nothing outside a browser.
 */
export declare function reportWebVitals(opts?: ReportOptions): () => void;
