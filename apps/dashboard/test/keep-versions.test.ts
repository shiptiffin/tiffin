import { describe, expect, test } from "bun:test";
import type { Manifest } from "@/api/client";
import { keepEdit } from "@/components/keep-versions";
import { applyEdits } from "@/lib/staged";

const base = { project: "shop", apps: { web: {} } } as unknown as Manifest;

describe("earlier versions kept", () => {
  test("0 is stored as 0, not dropped as unset", () => {
    const e = keepEdit("shop", undefined, 0);
    expect(applyEdits(base, [e]).keepVersions).toBe(0);
    expect(e.what).toBe("Keep only the live version of shop’s apps");
    expect(e.undo).toBe("shop’s apps keep the last 5 versions again");
  });
  test("another number is stored; the default 5 removes the field", () => {
    expect(applyEdits(base, [keepEdit("shop", undefined, 10)]).keepVersions).toBe(10);
    const back = applyEdits({ ...base, keepVersions: 10 }, [keepEdit("shop", 10, 5)]);
    expect("keepVersions" in back).toBe(false);
  });
});
