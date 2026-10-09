import { expect, test } from "bun:test";
import type { StatusReport } from "@/api/client";
import { shipTiffinAccount, splitAccountMention } from "@/lib/box";

const base = { ok: true, version: "v0.21.0", started: "", uptime: "1m", host: { hostname: "box", os: "linux", arch: "arm64" }, checks: [] } satisfies StatusReport;

test("only a managed box has an account link", () => {
  expect(shipTiffinAccount(undefined)).toBeUndefined();
  expect(shipTiffinAccount(base)).toBeUndefined();
  expect(shipTiffinAccount({ ...base, managed: { account: "https://shiptiffin.com/account" } })).toBe("https://shiptiffin.com/account");
});

test("the paused message links its mention of the account page", () => {
  const msg = "Automatic updates are paused: this box's ShipTiffin subscription is not active. Your apps keep running. Renew at shiptiffin.com/account.";
  expect(splitAccountMention(msg, "https://shiptiffin.com/account")).toEqual([
    "Automatic updates are paused: this box's ShipTiffin subscription is not active. Your apps keep running. Renew at ",
    "shiptiffin.com/account",
    ".",
  ]);
  expect(splitAccountMention("Updates are paused.", "https://shiptiffin.com/account")).toEqual(["Updates are paused.", "", ""]);
});
