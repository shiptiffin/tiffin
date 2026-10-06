// The components against the real engine (in-process, on a throwaway
// Postgres): what a person sees and does, end to end.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { adminHandler } from "../../auth-engine/src/admin";
import { outbox } from "../../auth-engine/src/mail";
import { Registry } from "../../auth-engine/src/registry";
import { publicHandler } from "../../auth-engine/src/server";
import { projectConfig } from "../../auth-engine/test/helpers";
import { freshDatabase, stopCluster } from "../../auth-engine/test/pg";
import { AcceptInvite, CaptchaField, Invite, OrgSwitcher, SignIn, SignUp, TiffinAuthProvider, UserButton, createTiffinAuth } from "../src";

const ORIGIN = "https://shop.tiffin.localhost:8443";
let reg: Registry;
let handle: (r: Request) => Promise<Response>;

/** A browser's cookie jar in front of the engine. */
class Browser {
  jar = new Map<string, string>();
  fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(input as string, init);
    const u = new URL(req.url);
    const h = new Headers(req.headers);
    h.set("host", u.host);
    h.set("origin", ORIGIN);
    if (this.jar.size) h.set("cookie", [...this.jar].map(([k, v]) => `${k}=${v}`).join("; "));
    const res = await handle(new Request(req.url, { method: req.method, headers: h, body: req.method === "GET" || req.method === "HEAD" ? undefined : await req.arrayBuffer(), redirect: "manual" }));
    if (res.status >= 500) console.log("5xx", req.method, u.pathname, await res.clone().text());
    for (const c of res.headers.getSetCookie()) {
      const [pair] = c.split(";");
      const i = pair!.indexOf("=");
      const k = pair!.slice(0, i).trim();
      const v = pair!.slice(i + 1).trim();
      if (!v || /max-age=0/i.test(c)) this.jar.delete(k);
      else this.jar.set(k, v);
    }
    return res;
  }) as typeof fetch;
}

let browser = new Browser();
const use = (b: Browser) => {
  browser = b;
  (globalThis as any).fetch = (i: any, n: any) => browser.fetch(i, n);
};
const mailTo = (to: string, kind: string) => {
  const m = [...outbox].reverse().find((x) => x.to === to && x.kind === kind);
  if (!m) throw new Error(`no ${kind} mail to ${to}`);
  return m;
};
const linkIn = (text: string) => text.match(/https?:\/\/\S+/)![0];
// Each test gets a fresh client so session caches don't leak between people.
const withClient = (ui: React.ReactNode) => <TiffinAuthProvider client={createTiffinAuth({ baseURL: ORIGIN })} baseURL={ORIGIN}>{ui}</TiffinAuthProvider>;

beforeAll(async () => {
  const db = await freshDatabase("react_test");
  reg = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(db, { methods: ["email", "magic-link", "otp", "passkey", "github"], social: { github: { clientId: "x", clientSecret: "y" } } }) } });
  handle = publicHandler(reg);
  await adminHandler(reg)(new Request("http://admin/projects/shop/migrate", { method: "POST" }));
  use(new Browser());
}, 60_000);

afterAll(async () => {
  cleanup();
  await reg.closeAll();
  await stopCluster();
});

async function signUpAndVerify(email: string, name: string) {
  const b = new Browser();
  use(b);
  render(withClient(<SignUp signInUrl="/sign-in" />));
  await screen.findByRole("heading", { name: "Create your Shop account" });
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: name } });
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: email } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
  fireEvent.click(screen.getByRole("button", { name: "Create account" }));
  await screen.findByRole("heading", { name: "Check your inbox" }, { timeout: 10_000 });
  await b.fetch(linkIn(mailTo(email, "verify").text));
  cleanup();
  return b;
}

describe("SignUp", () => {
  test("creates the account, sends the confirmation, and the link signs in", async () => {
    const b = await signUpAndVerify("ada@example.com", "Ada Lovelace");
    expect(b.jar.size).toBeGreaterThan(0);
    render(withClient(<UserButton />));
    const trigger = await screen.findByRole("button", { name: "Account: Ada Lovelace" });
    expect(trigger.textContent).toBe("AL");
    fireEvent.click(trigger);
    const menu = await screen.findByRole("menu", { name: "Account" });
    expect(within(menu).getByText("ada@example.com")).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: "Add a passkey" })).toBeTruthy();
    fireEvent.keyDown(menu, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    cleanup();
  }, 30_000);

  test("shows the social buttons the project set up", async () => {
    use(new Browser());
    render(withClient(<SignUp />));
    await screen.findByRole("button", { name: "Continue with GitHub" });
    expect(screen.queryByRole("button", { name: /Google/ })).toBeNull();
    cleanup();
  });
});

describe("SignIn", () => {
  test("password: wrong one explains, right one signs in", async () => {
    use(new Browser());
    let signedIn = 0;
    render(withClient(<SignIn onSignedIn={() => signedIn++} signUpUrl="/sign-up" />));
    await screen.findByRole("heading", { name: "Sign in to Shop" });
    expect(screen.getByRole("link", { name: "Create an account" }).getAttribute("href")).toBe("/sign-up");
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "not my password" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect((await screen.findByRole("alert", {}, { timeout: 10_000 })).textContent).toContain("don't match");
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(signedIn).toBe(1), { timeout: 10_000 });
    cleanup();
  }, 30_000);

  test("magic link: check your inbox", async () => {
    use(new Browser());
    render(withClient(<SignIn onSignedIn={() => {}} />));
    await screen.findByRole("heading", { name: "Sign in to Shop" });
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "linky@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Email me a link instead" }));
    await screen.findByRole("heading", { name: "Check your inbox" }, { timeout: 10_000 });
    expect(mailTo("linky@example.com", "magic-link").subject).toBe("Your sign-in link for Shop");
    cleanup();
  }, 30_000);

  test("one-time code: six boxes, signs in when complete", async () => {
    use(new Browser());
    let signedIn = 0;
    render(withClient(<SignIn onSignedIn={() => signedIn++} />));
    await screen.findByRole("heading", { name: "Sign in to Shop" });
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "coda@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Use a one-time code" }));
    await screen.findByRole("heading", { name: "Enter your code" }, { timeout: 10_000 });
    const code = mailTo("coda@example.com", "otp").subject.split(" ")[0]!;
    fireEvent.change(screen.getByLabelText("Digit 1"), { target: { value: code } }); // paste/autofill
    await waitFor(() => expect(signedIn).toBe(1), { timeout: 10_000 });
    cleanup();
  }, 30_000);
});

describe("organizations", () => {
  let ada: Browser;
  let link = "";

  test("OrgSwitcher shows the personal org and creates a team", async () => {
    ada = new Browser();
    use(ada);
    await ada.fetch(`${ORIGIN}/api/auth/sign-in/email`, {
      method: "POST",
      headers: { "content-type": "application/json", "x-captcha-response": await solve() },
      body: JSON.stringify({ email: "ada@example.com", password: "correct horse battery" }),
    });
    let switched = "";
    render(withClient(<OrgSwitcher onSwitch={(id) => (switched = id)} />));
    const trigger = await screen.findByRole("button", { name: /Organization: Ada Lovelace/ }, { timeout: 10_000 });
    fireEvent.click(trigger);
    fireEvent.click(await screen.findByRole("menuitem", { name: "Create organization" }));
    fireEvent.change(screen.getByLabelText("Organization name"), { target: { value: "Analytical Engines" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(switched).not.toBe(""), { timeout: 10_000 });
    cleanup();
  }, 30_000);

  test("Invite: email invitation, roles above yours aren't offered to admins, link", async () => {
    use(ada);
    render(withClient(<Invite />));
    await screen.findByRole("heading", { name: "Invite people" }, { timeout: 10_000 });
    await screen.findByText("Analytical Engines", {}, { timeout: 10_000 });
    const roles = screen.getByRole("radiogroup");
    expect(within(roles).getByRole("radio", { name: "owner" }).hasAttribute("disabled")).toBe(false); // ada owns it
    fireEvent.click(within(roles).getByRole("radio", { name: "admin" }));
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "babbage@example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Send invitation" }));
    await screen.findByText(/Invitation sent to babbage@example.com/, {}, { timeout: 10_000 });
    expect(mailTo("babbage@example.com", "invitation").subject).toBe("Ada Lovelace invited you to Analytical Engines");
    await screen.findByText("babbage@example.com");
    fireEvent.click(within(roles).getByRole("radio", { name: "viewer" }));
    fireEvent.click(screen.getByRole("button", { name: "Create a viewer invite link" }));
    const code = await screen.findByText(/accept-invite\?link=/, {}, { timeout: 10_000 });
    link = code.textContent!;
    expect(link.startsWith(`${ORIGIN}/accept-invite?link=tfi_`)).toBe(true);
    cleanup();
  }, 30_000);

  test("AcceptInvite: someone else joins with the link", async () => {
    const grace = await signUpAndVerify("grace@example.com", "Grace");
    use(grace);
    const token = new URL(link).searchParams.get("link")!;
    let joined = "";
    render(withClient(<AcceptInvite token={token} onAccepted={(id) => (joined = id)} />));
    await screen.findByRole("heading", { name: "Join Analytical Engines" }, { timeout: 10_000 });
    expect(screen.getByText("viewer")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Accept invitation" }));
    await waitFor(() => expect(joined).not.toBe(""), { timeout: 10_000 });
    cleanup();
    // As a viewer, Invite explains instead of offering a form.
    render(withClient(<Invite />));
    await screen.findByText(/Only owners and admins can invite people/, {}, { timeout: 10_000 });
    cleanup();
  }, 30_000);
});

describe("CaptchaField", () => {
  test("a form posting to a Server Action waits for the bot check, and each submit gets a fresh one", async () => {
    use(new Browser());
    const sent: string[] = [];
    render(
      withClient(
        <form
          aria-label="sign in"
          onSubmit={(e) => {
            e.preventDefault();
            sent.push((e.currentTarget.elements.namedItem("captcha") as HTMLInputElement).value); // what the browser posts
          }}
        >
          <CaptchaField />
          <button type="submit">Go</button>
        </form>,
      ),
    );
    const form = screen.getByRole("form", { name: "sign in" }) as HTMLFormElement;
    await waitFor(() => expect(form.elements.namedItem("captcha")).toBeTruthy());
    fireEvent.submit(form); // likely before it is solved: held, then sent
    await waitFor(() => expect(sent.length).toBe(1), { timeout: 10_000 });
    expect(sent[0]).not.toBe("");
    const tried = await browser.fetch(`${ORIGIN}/api/auth/sign-in/email`, {
      method: "POST",
      headers: { "content-type": "application/json", "x-captcha-response": sent[0]! },
      body: JSON.stringify({ email: "nobody@example.com", password: "wrong password!" }),
    });
    expect(tried.status).toBe(401); // the check passed; the password didn't
    await waitFor(() => expect((form.elements.namedItem("captcha") as HTMLInputElement).value).not.toBe(""), { timeout: 10_000 });
    fireEvent.submit(form);
    await waitFor(() => expect(sent.length).toBe(2));
    expect(sent[1]).not.toBe(sent[0]);
    cleanup();
  }, 30_000);
});

async function solve(): Promise<string> {
  const { solveChallenge } = await import("altcha-lib");
  const { deriveKey } = await import("altcha-lib/algorithms/web/sha");
  const r = await browser.fetch(`${ORIGIN}/api/auth/altcha/challenge`);
  const challenge = await r.json();
  const solution = await solveChallenge({ challenge, deriveKey });
  return btoa(JSON.stringify({ challenge, solution }));
}

// keep act() warnings meaningful
void act;
