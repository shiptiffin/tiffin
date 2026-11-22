import { expect, test } from "bun:test";
import { MAX_TITLE, titleEdit, titleProblem } from "@/components/project-title";

test("titles: one line of text up to 60 characters, trimmed; empty is fine", () => {
  for (const ok of ["", "  ", "ShipTiffin website", "  padded  ", "Café · 東京 ✨", "é".repeat(MAX_TITLE)]) expect(titleProblem(ok)).toBeUndefined();
  for (const bad of ["x".repeat(MAX_TITLE + 1), "a\nb", "a\tb", "a\u0000b", "a\u202Eb", "a\u2066b", "a\u2028b"]) expect(titleProblem(bad)).toBeDefined();
});

test("titles: the edit sets title, an empty one removes it, and both undo in words", () => {
  expect(titleEdit("website", undefined, "  ShipTiffin website ")).toEqual({
    kind: "set",
    path: ["title"],
    from: undefined,
    to: "ShipTiffin website",
    what: "Title website “ShipTiffin website”",
    undo: "website has no title again",
  });
  const clear = titleEdit("website", "ShipTiffin website", " ");
  expect(clear.to).toBeUndefined();
  expect(clear.what).toBe("Clear the title of website");
  expect(clear.undo).toBe("website is titled “ShipTiffin website” again");
});
