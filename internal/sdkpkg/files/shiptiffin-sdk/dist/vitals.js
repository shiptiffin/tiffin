/**
 * `@shiptiffin/sdk/vitals`: send Web Vitals (LCP, INP, CLS, FCP, TTFB) from real
 * visitors' browsers to the box. They go to `/_tiffin/vitals` on the page's
 * own origin (the box answers it on every app host), once per page load,
 * when the page is hidden, so they cost no request while the visitor is on
 * the page. The project needs `services: { analytics: {} }`; read them with
 * `tiffin analytics vitals` or on the dashboard's Analytics page.
 *
 * ```ts
 * import { reportWebVitals } from "@shiptiffin/sdk/vitals";
 * reportWebVitals();                                    // any framework, in browser code
 * reportWebVitals({ path: () => "/products/[id]" });    // report a route instead of the path
 * ```
 *
 * In Next.js, render `<WebVitals />` from `@shiptiffin/sdk/next/vitals` in the root
 * layout instead: it uses Next's own measurements and reports routes such as
 * `/products/[id]`.
 */
/** Where the box takes Web Vitals, on every app host. */
export const VITALS_PATH = "/_tiffin/vitals";
const NAMES = new Set(["LCP", "INP", "CLS", "FCP", "TTFB"]);
/**
 * The route a path belongs to: each segment that is a route parameter's
 * value becomes `[name]`, a catch-all's segments `[...name]`.
 */
export function routeOf(pathname, params) {
    let route = pathname || "/";
    for (const [name, value] of Object.entries(params ?? {})) {
        if (Array.isArray(value)) {
            const joined = "/" + value.map(encodeURIComponent).join("/");
            if (value.length > 0 && route.includes(joined))
                route = route.replace(joined, `/[...${name}]`);
        }
        else if (value) {
            route = route
                .split("/")
                .map((s) => (s === value || safeDecode(s) === value ? `[${name}]` : s))
                .join("/");
        }
    }
    return route;
}
function safeDecode(s) {
    try {
        return decodeURIComponent(s);
    }
    catch {
        return s;
    }
}
/**
 * Collects one page's metrics and sends them in one beacon when the page
 * is hidden, or when the path changes (a client-side navigation).
 */
export class VitalsQueue {
    endpoint;
    metrics = {};
    path;
    constructor(endpoint = VITALS_PATH, path = typeof location !== "undefined" ? location.pathname : "/") {
        this.endpoint = endpoint;
        this.path = path;
        if (typeof addEventListener === "function") {
            addEventListener("visibilitychange", () => document.visibilityState === "hidden" && this.flush(), true);
            addEventListener("pagehide", () => this.flush(), true);
        }
    }
    setPath(path) {
        if (path === this.path)
            return;
        this.flush();
        this.path = path;
    }
    add(name, value) {
        if (!NAMES.has(name) || !Number.isFinite(value) || value < 0)
            return;
        this.metrics[name] = Math.round(name === "CLS" ? value * 10000 : value * 10) / (name === "CLS" ? 10000 : 10);
        // Metrics that become final as the page hides arrive after our listener ran.
        if (typeof document !== "undefined" && document.visibilityState === "hidden")
            queueMicrotask(() => this.flush());
    }
    flush() {
        if (Object.keys(this.metrics).length === 0)
            return;
        const body = JSON.stringify({ path: this.path, metrics: this.metrics });
        this.metrics = {};
        // The visitor asked not to be tracked (Global Privacy Control): send nothing.
        if (typeof navigator !== "undefined" && navigator.globalPrivacyControl)
            return;
        try {
            if (typeof navigator !== "undefined" && navigator.sendBeacon?.(this.endpoint, body))
                return;
            void fetch(this.endpoint, { method: "POST", body, keepalive: true }).catch(() => { });
        }
        catch {
            // Vitals must never break a page.
        }
    }
}
/**
 * Measure this page load's Web Vitals with the browser's performance
 * observers and send them once, when the page is hidden. Returns a function
 * that stops measuring. Does nothing outside a browser.
 */
export function reportWebVitals(opts = {}) {
    if (typeof window === "undefined" || typeof PerformanceObserver === "undefined")
        return () => { };
    const path = () => (typeof opts.path === "function" ? opts.path() : (opts.path ?? location.pathname));
    const q = new VitalsQueue(opts.endpoint ?? VITALS_PATH, path());
    const nav = performance.getEntriesByType("navigation")[0];
    const start = nav?.activationStart ?? 0;
    let lcp = -1;
    let lcpDone = false;
    let fcp = -1;
    let cls = 0;
    let session = 0;
    let sessionFirst = 0;
    let sessionLast = 0;
    const interactions = new Map();
    const observers = [];
    const observe = (type, f, extra = {}) => {
        try {
            const o = new PerformanceObserver((l) => l.getEntries().forEach(f));
            o.observe({ type, buffered: true, ...extra });
            observers.push(o);
        }
        catch {
            // This browser does not report this entry type.
        }
    };
    observe("paint", (e) => {
        if (e.name === "first-contentful-paint")
            fcp = Math.max(e.startTime - start, 0);
    });
    observe("largest-contentful-paint", (e) => {
        if (!lcpDone)
            lcp = Math.max(e.startTime - start, 0);
    });
    const stopLCP = () => (lcpDone = lcp >= 0);
    for (const t of ["keydown", "pointerdown"])
        addEventListener(t, stopLCP, { once: true, capture: true });
    // CLS: the largest session window of shifts (at most 5 s, gaps under 1 s).
    observe("layout-shift", (e) => {
        const s = e;
        if (s.hadRecentInput)
            return;
        if (session && s.startTime - sessionLast < 1000 && s.startTime - sessionFirst < 5000)
            session += s.value;
        else
            [session, sessionFirst] = [s.value, s.startTime];
        sessionLast = s.startTime;
        cls = Math.max(cls, session);
    });
    // INP: the longest interaction, ignoring one in every 50 as an outlier.
    const onEvent = (e) => {
        const t = e;
        if (t.interactionId)
            interactions.set(t.interactionId, Math.max(interactions.get(t.interactionId) ?? 0, t.duration));
    };
    observe("event", onEvent, { durationThreshold: 40 });
    observe("first-input", onEvent);
    const send = (e) => {
        if (e.type === "visibilitychange" && document.visibilityState !== "hidden")
            return;
        if (nav && nav.responseStart > 0)
            q.add("TTFB", Math.max(nav.responseStart - start, 0));
        if (fcp >= 0)
            q.add("FCP", fcp);
        if (lcp >= 0)
            q.add("LCP", lcp);
        q.add("CLS", cls);
        if (interactions.size > 0) {
            const d = [...interactions.values()].sort((a, b) => b - a);
            q.add("INP", d[Math.min(Math.floor(interactions.size / 50), d.length - 1)] ?? 0);
        }
        q.flush();
        stop();
    };
    const stop = () => {
        observers.forEach((o) => o.disconnect());
        removeEventListener("visibilitychange", send, true);
        removeEventListener("pagehide", send, true);
    };
    addEventListener("visibilitychange", send, true);
    addEventListener("pagehide", send, true);
    return stop;
}
