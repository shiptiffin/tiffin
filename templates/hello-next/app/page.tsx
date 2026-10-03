import { unstable_cache } from "next/cache";

// Rendered per request, but the expensive part is cached in Valkey under the
// tag "time" and shared by every instance until something revalidates it.
export const dynamic = "force-dynamic";

const cachedTime = unstable_cache(async () => new Date().toISOString(), ["cached-time"], { tags: ["time"] });

export default async function Home() {
  const cached = await cachedTime();
  return (
    <main>
      <h1>Hello from Next.js on Tiffin</h1>
      <p>
        Cached at <code id="cached">{cached}</code> (shared by all instances)
      </p>
      <p>
        Served by instance <code id="instance">{process.env.TIFFIN_INSTANCE ?? "local"}</code> of deploy{" "}
        <code id="deploy">{process.env.TIFFIN_DEPLOY ?? "local"}</code>, runtime{" "}
        <code id="runtime">{typeof (globalThis as { Bun?: unknown }).Bun === "undefined" ? "node" : "bun"}</code>
      </p>
      <p>
        Refresh the cache: <code>curl -X POST {"<url>"}/api/revalidate</code>
      </p>
    </main>
  );
}
