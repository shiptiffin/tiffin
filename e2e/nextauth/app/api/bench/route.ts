import { getSession } from "@shiptiffin/sdk/auth";

// How long getSession takes inside the app, per path: the signed cookie
// (no request), a remembered engine answer, and the engine itself.
export async function GET(req: Request) {
  const cookie = req.headers.get("cookie") ?? "";
  const tokenOnly = cookie
    .split(";")
    .filter((c) => !c.includes("session_data"))
    .join(";");
  const withoutCache = new Request(req.url, { headers: { ...Object.fromEntries(req.headers), cookie: tokenOnly } });
  const time = async (f: () => Promise<unknown>, n = 200) => {
    await f();
    const t = performance.now();
    for (let i = 0; i < n; i++) await f();
    return +((performance.now() - t) / n).toFixed(3);
  };
  return Response.json({
    signedCookieMs: await time(() => getSession(req)),
    rememberedMs: await time(() => getSession(withoutCache)),
    engineMs: await time(() => getSession(req, { fresh: true })),
    via: (await getSession(req))?.user.email ?? null,
  });
}
