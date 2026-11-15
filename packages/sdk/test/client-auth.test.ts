// @shiptiffin/sdk/client/auth: Better Auth's client set up for the box.
import { expect, test } from "bun:test";
import { createAuthClient } from "../src/client/auth-client";

test("talks to /api/auth on the app's origin, with the box's client plugins", async () => {
  const calls: { url: string; body: unknown }[] = [];
  const customFetchImpl = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : null });
    return Response.json({ status: true });
  }) as typeof fetch;
  const authClient = createAuthClient({ baseURL: "https://shop.example.com", fetchOptions: { customFetchImpl } });

  await authClient.signIn.magicLink({ email: "ana@example.com", callbackURL: "/app/" });
  await authClient.emailOtp.sendVerificationOtp({ email: "ana@example.com", type: "sign-in" });
  await authClient.organization.setActive({ organizationId: "o1" });
  await authClient.twoFactor.sendOtp();
  expect(calls.map((c) => c.url)).toEqual([
    "https://shop.example.com/api/auth/sign-in/magic-link",
    "https://shop.example.com/api/auth/email-otp/send-verification-otp",
    "https://shop.example.com/api/auth/organization/set-active",
    "https://shop.example.com/api/auth/two-factor/send-otp",
  ]);
  expect(calls[0]!.body).toEqual({ email: "ana@example.com", callbackURL: "/app/" });
});

test("takes more plugins after the box's", () => {
  const extra = { id: "extra", getActions: () => ({ hello: () => "hi" }) } as const;
  const authClient = createAuthClient({ baseURL: "https://shop.example.com", plugins: [extra] });
  expect(authClient.hello()).toBe("hi");
  expect(typeof authClient.signIn.magicLink).toBe("function");
});
