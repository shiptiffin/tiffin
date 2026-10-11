import { describe, expect, test } from "bun:test";
import { fromGitHub, githubUserOf, preselect } from "./github-keys";

const ED = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINbgit9Y8WPU/QBdrxDAtouyK+piPxWmFbJ3gKgMj4Po";

/** A stand-in for GitHub: answers by path, and records what was asked (never the network). */
function github(answers: Record<string, [number, unknown]>) {
  const asked: { url: string; init?: RequestInit }[] = [];
  const f = (async (url: string, init?: RequestInit) => {
    asked.push({ url, init });
    const [status, body] = answers[new URL(url).pathname + new URL(url).search] ?? [404, { message: "Not Found" }];
    return new Response(JSON.stringify(body), { status });
  }) as typeof fetch;
  return { f, asked };
}

describe("from GitHub", () => {
  test("usernames as people type them", () => {
    expect(githubUserOf(" sam ")).toBe("sam");
    expect(githubUserOf("@sam-k")).toBe("sam-k");
    expect(githubUserOf("https://github.com/sam?tab=keys")).toBe("sam");
    expect(githubUserOf("github.com/sam/")).toBe("sam");
  });

  test("the profile and its keys, asked of api.github.com without cookies", async () => {
    const { f, asked } = github({
      "/users/sam": [200, { login: "Sam", name: "Sam Kay", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4" }],
      "/users/sam/keys?per_page=100": [200, [{ id: 1, key: ED, created_at: "2025-04-12T22:05:33Z", last_used: null }, { bogus: true }]],
    });
    const r = await fromGitHub("sam", f);
    expect(r).toEqual({
      ok: true,
      profile: { login: "Sam", name: "Sam Kay", avatar: "https://avatars.githubusercontent.com/u/1?v=4" },
      keys: [{ id: 1, key: ED, created_at: "2025-04-12T22:05:33Z", last_used: null }],
    });
    expect(asked.map((a) => a.url)).toEqual(["https://api.github.com/users/sam", "https://api.github.com/users/sam/keys?per_page=100"]);
    expect(asked.every((a) => a.init?.credentials === "omit")).toBe(true);
  });

  test("an avatar from anywhere else, or a strange login, isn't used", async () => {
    const { f } = github({
      "/users/sam": [200, { login: "../evil", avatar_url: "https://evil.example/a.png" }],
      "/users/sam/keys?per_page=100": [200, []],
    });
    const r = await fromGitHub("sam", f);
    expect(r).toMatchObject({ ok: true, profile: { login: "sam", name: null, avatar: "" }, keys: [] });
  });

  test("failures say what happened, so the page can offer Paste", async () => {
    expect(await fromGitHub("nobody", github({}).f)).toEqual({ ok: false, message: "There's no GitHub account called @nobody." });
    const limited = github({ "/users/sam": [403, {}], "/users/sam/keys?per_page=100": [403, {}] });
    expect(await fromGitHub("sam", limited.f)).toMatchObject({ ok: false, message: expect.stringContaining("60 lookups an hour") });
    const down = (async () => {
      throw new TypeError("Failed to fetch");
    }) as unknown as typeof fetch;
    expect(await fromGitHub("sam", down)).toEqual({ ok: false, message: "We couldn't reach GitHub from this browser." });
    const odd = github({ "/users/sam": [500, {}], "/users/sam/keys?per_page=100": [200, []] });
    expect(await fromGitHub("sam", odd.f)).toEqual({ ok: false, message: "GitHub answered 500." });
    const never = github({});
    expect(await fromGitHub("../x", never.f)).toMatchObject({ ok: false });
    expect(never.asked).toHaveLength(0);
  });

  test("the key ticked first: the only one, else the latest used, else the newest, else none", () => {
    const k = (id: string, added: string | null, lastUsed?: string | null, problem?: string) => ({ id, added, lastUsed, problem });
    expect(preselect([k("a", null)])).toBe("a");
    expect(preselect([k("a", null), k("b", null, null, "too short")])).toBe("a");
    expect(preselect([k("a", "2024-01-01", "2026-10-01"), k("b", "2025-01-01", "2026-10-04"), k("c", "2026-01-01", null)])).toBe("b");
    expect(preselect([k("a", "2024-01-01", null), k("b", "2025-06-01", null)])).toBe("b");
    expect(preselect([k("a", null), k("b", null)])).toBeNull();
    expect(preselect([])).toBeNull();
  });
});
