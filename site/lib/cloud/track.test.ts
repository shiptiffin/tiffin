import { describe, expect, test } from "bun:test";
import { clientIP, track } from "@shiptiffin/sdk/analytics";
import { safeNext } from "./names";
import { cancelHow, openedVia, requestFrom, signInCallbacks } from "./track";

describe("signInCallbacks", () => {
  test("lands on /sign-in/done, which goes on to the same next", () => {
    for (const next of ["/start", "/account", "/api/cloud/boxes/box_1/open", "/start?new=1"]) {
      const { callbackURL, newUserCallbackURL } = signInCallbacks(next);
      for (const to of [callbackURL, newUserCallbackURL]) {
        const u = new URL(to, "https://shiptiffin.com");
        expect(u.pathname).toBe("/sign-in/done");
        expect(safeNext(u.searchParams.get("next") ?? undefined)).toBe(next);
      }
      expect(new URL(callbackURL, "https://shiptiffin.com").searchParams.get("new")).toBeNull();
      expect(new URL(newUserCallbackURL, "https://shiptiffin.com").searchParams.get("new")).toBe("1");
    }
  });

  test("survives the email link decoding it once more (Better Auth's magic-link verify)", () => {
    const { callbackURL } = signInCallbacks("/api/cloud/boxes/box_1/open");
    const back = decodeURIComponent(callbackURL);
    expect(new URL(back, "https://shiptiffin.com").searchParams.get("next")).toBe("/api/cloud/boxes/box_1/open");
  });
});

describe("requestFrom", () => {
  test("carries what joins the session, and nothing else", async () => {
    const h = new Headers({
      "x-forwarded-for": "203.0.113.9",
      "user-agent": "Mozilla/5.0",
      referer: "https://checkout.stripe.com/",
      "sec-gpc": "1",
      cookie: "session=secret",
    });
    const req = requestFrom(h, "https://shiptiffin.com/start");
    expect(clientIP(req)).toBe("203.0.113.9");
    expect(req.headers.get("cookie")).toBeNull();
    let sent: any;
    const ok = await track("checkout_completed", undefined, {
      request: req,
      endpoint: "https://t.example",
      key: "k",
      fetch: (async (_url: string, init: RequestInit) => {
        sent = JSON.parse(String(init.body));
        return Response.json({ ok: true }, { status: 202 });
      }) as any,
    });
    expect(ok).toBe(true);
    expect(sent).toEqual({ name: "checkout_completed", url: "https://shiptiffin.com/start", ip: "203.0.113.9", ua: "Mozilla/5.0", referrer: "https://checkout.stripe.com/", gpc: true });
  });
});

describe("event props", () => {
  test("Open your dashboard: link, asked or direct", () => {
    expect(openedVia("https://acme.shiptiffin.app/login#tfl_abc")).toBe("link");
    expect(openedVia("/account?signin=asked#box_1")).toBe("asked");
    expect(openedVia("https://acme.shiptiffin.app/")).toBe("direct");
  });

  test("only the ways out count as cancellations", () => {
    expect(cancelHow("cancel")).toBe("period_end");
    expect(cancelHow("release")).toBe("keep_server");
    expect(cancelHow("delete-server")).toBe("delete_server");
    for (const a of ["resume", "resize", "new-signin", "forget-signin"]) expect(cancelHow(a)).toBeNull();
  });
});
