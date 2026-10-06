// The box's bot check (ALTCHA proof of work, verified on the box): sign-up,
// password sign-in, magic links, email codes and password resets need a
// solved challenge in the x-captcha-response header when the app's auth
// config has captcha on. Solving takes a fraction of a second, so these
// helpers start in the background and keep one solution ready.
import { solveChallenge } from "altcha-lib";
import { deriveKey } from "altcha-lib/algorithms/web/sha";
import { authConfig, baseOf } from "./auth.js";
/** Fetches and solves one challenge: the x-captcha-response header value. Each works once. */
export async function solveCaptcha(o = {}) {
    const r = await (o.fetch ?? fetch)(`${baseOf(o)}/api/auth/altcha/challenge`, { credentials: "include" });
    if (!r.ok)
        throw new Error("Couldn't load the bot check. Check your connection and try again.");
    const challenge = (await r.json());
    const solution = await solveChallenge({ challenge, deriveKey, timeout: 60_000 });
    if (!solution)
        throw new Error("The bot check took too long. Try again.");
    return btoa(JSON.stringify({ challenge, solution }));
}
/**
 * Starts solving now (when the app has captcha on) and returns a function
 * that gives the headers for one protected request, then starts on the next.
 * With captcha off it gives {}.
 *
 *   const captcha = prepareCaptcha();
 *   await authClient.signUp.email({ name, email, password }, { headers: await captcha() });
 */
export function prepareCaptcha(o = {}) {
    let next = null;
    const start = () => {
        const p = solveCaptcha(o);
        p.catch(() => {
            if (next === p)
                next = null;
        });
        return p;
    };
    const on = authConfig(o).then((c) => c.captcha, () => false);
    void on.then((yes) => {
        if (yes && !next)
            next = start();
    });
    return async () => {
        if (!(await on))
            return {};
        const p = next ?? start();
        next = start();
        return { "x-captcha-response": await p };
    };
}
/**
 * The bot check for a plain form that posts to a Server Action (signIn /
 * signUp from @shiptiffin/sdk/next/auth) or anywhere else: keeps a hidden
 * `captcha` field filled. Submitting before it is ready waits for it; each
 * submit gets a fresh one for the next try. Returns a function that detaches.
 *
 *   useEffect(() => attachCaptcha(formRef.current!), []);   // React
 *   attachCaptcha(document.querySelector("form")!);          // anywhere else
 */
export function attachCaptcha(form, o = {}) {
    const name = o.name ?? "captcha";
    const found = form.elements.namedItem(name);
    const el = found instanceof HTMLInputElement ? found : form.appendChild(Object.assign(document.createElement("input"), { type: "hidden", name }));
    let state = "waiting";
    let held = null; // a submit waiting for the check
    let pending = Promise.resolve();
    let take = null;
    let stopped = false;
    const resubmit = () => {
        const h = held;
        held = null;
        if (h && !stopped)
            form.requestSubmit(h.by ?? undefined);
    };
    const fill = () => {
        state = "solving";
        el.value = "";
        pending = take().then((h) => {
            el.value = h["x-captcha-response"] ?? "";
            state = "ready";
        }, () => {
            state = "failed";
        });
    };
    const whenReady = () => void pending.then(() => state === "ready" && resubmit());
    authConfig(o).then((c) => {
        if (stopped)
            return;
        if (!c.captcha) {
            state = "off";
            return resubmit();
        }
        take = prepareCaptcha(o);
        fill();
        whenReady();
    }, () => {
        state = "off"; // let the server say what's wrong
        resubmit();
    });
    const onSubmit = (e) => {
        if (state === "off")
            return;
        if (state === "ready") {
            setTimeout(fill); // after the form's data is read
            return;
        }
        e.preventDefault();
        e.stopPropagation(); // React handles submits at the root: it never sees this one
        held = { by: e.submitter };
        if (state === "failed")
            fill();
        if (state !== "waiting")
            whenReady();
    };
    form.addEventListener("submit", onSubmit, true);
    return () => {
        stopped = true;
        form.removeEventListener("submit", onSubmit, true);
    };
}
