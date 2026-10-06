import { type AuthOptions } from "./auth.js";
/** Fetches and solves one challenge: the x-captcha-response header value. Each works once. */
export declare function solveCaptcha(o?: AuthOptions): Promise<string>;
/**
 * Starts solving now (when the app has captcha on) and returns a function
 * that gives the headers for one protected request, then starts on the next.
 * With captcha off it gives {}.
 *
 *   const captcha = prepareCaptcha();
 *   await authClient.signUp.email({ name, email, password }, { headers: await captcha() });
 */
export declare function prepareCaptcha(o?: AuthOptions): () => Promise<Record<string, string>>;
/**
 * The bot check for a plain form that posts to a Server Action (signIn /
 * signUp from @shiptiffin/sdk/next/auth) or anywhere else: keeps a hidden
 * `captcha` field filled. Submitting before it is ready waits for it; each
 * submit gets a fresh one for the next try. Returns a function that detaches.
 *
 *   useEffect(() => attachCaptcha(formRef.current!), []);   // React
 *   attachCaptcha(document.querySelector("form")!);          // anywhere else
 */
export declare function attachCaptcha(form: HTMLFormElement, o?: AuthOptions & {
    name?: string;
}): () => void;
