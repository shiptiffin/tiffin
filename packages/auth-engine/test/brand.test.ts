// The button in app emails: the dashboard's brass unless the manifest sets
// auth.emailAccent; the text on it is whichever of white or the dashboard's
// text-on-brass reads better.
import { expect, test } from "bun:test";
import { accentText, templates } from "../src/mail";
import { BRASS, ON_BRASS } from "../src/templates.gen";

const brand = { app: "Larder", primaryUrl: "https://larder.app" };
const button = (html: string) => html.match(/<a [^>]*class="tf-btn[^>]*>/)?.[0] ?? "";

test("no accent: the dashboard's brass button", () => {
  const b = button(templates.verify(brand, "sam@example.com", "https://larder.app/v?t=1").html);
  expect(b).toContain(`background-color:${BRASS}`);
  expect(b).toContain(`color:${ON_BRASS}`);
  expect(BRASS).toBe("#f2b036");
});

test("an explicit accent wins, with readable text", () => {
  const dark = button(templates.verify({ ...brand, accent: "#2f6b4f" }, "sam@example.com", "https://larder.app/v?t=1").html);
  expect(dark).toContain("background-color:#2f6b4f");
  expect(dark).toContain("color:#ffffff");
  const pale = button(templates.verify({ ...brand, accent: "#ffe14d" }, "sam@example.com", "https://larder.app/v?t=1").html);
  expect(pale).toContain(`color:${ON_BRASS}`);
});

test("a malformed accent falls back to brass", () => {
  const b = button(templates.verify({ ...brand, accent: "red" }, "sam@example.com", "https://larder.app/v?t=1").html);
  expect(b).toContain(`background-color:${BRASS}`);
  expect(accentText(BRASS)).toBe(ON_BRASS);
});
