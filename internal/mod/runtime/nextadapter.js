// SPDX-License-Identifier: Apache-2.0
// The Tiffin box's Next.js adapter (Next.js 16.2+ loads it from
// NEXT_ADAPTER_PATH at build and at `next start`). Written into each build by
// the box; it only fills in what next.config leaves unset.
import { readFileSync } from "node:fs";
import { join } from "node:path";

const box = /*BOX*/ {};
const here = (file) => new URL(file, import.meta.url).pathname;

// atLeast reports whether a Next.js version ("16.3.8", "16.5.0-canary.2") is major.minor or later.
const atLeast = (v, major, minor) => {
  const [a, b] = String(v ?? "").split(".").map((n) => parseInt(n, 10));
  return a > major || (a === major && b >= minor);
};

// builtImmutable is what `next build` decided about immutable assets, read
// from its output (Next.js turns them off for webpack, export and
// standalone builds); undefined without a build.
const builtImmutable = (dir, distDir) => {
  try {
    const rsf = JSON.parse(readFileSync(join(dir ?? process.cwd(), distDir || ".next", "required-server-files.json"), "utf8"));
    return rsf.config?.supportsImmutableAssets === true;
  } catch {
    return undefined;
  }
};

export default {
  name: "tiffin",
  modifyConfig(config, ctx = {}) {
    // The edge compresses responses. No X-Powered-By: Next.js (the config
    // arrives with defaults filled in, so an explicit true looks the same as
    // unset; an app that wants the header sets it with headers()).
    const c = { ...config, compress: false, poweredByHeader: false };
    // Pages and assets of this build carry its id: a browser on an older
    // release reloads instead of mixing builds.
    if (!c.deploymentId) c.deploymentId = box.deploymentId;
    if (box.cache) {
      // One cache in Valkey for every instance (@shiptiffin/sdk/next).
      if (!c.cacheHandler) {
        c.cacheHandler = here("cache-handler.js");
        c.cacheMaxMemorySize = 0;
      }
      const handlers = { ...c.cacheHandlers };
      handlers.default ||= here("use-cache.js");
      handlers.remote ||= here("use-cache.js");
      c.cacheHandlers = handlers;
    }
    // forbidden() and unauthorized() (@shiptiffin/sdk/next/auth) answer 403/401.
    if (c.experimental?.authInterrupts === undefined) {
      c.experimental = { ...c.experimental, authInterrupts: true };
    }
    // Next.js 16.3+: content-hashed chunks go to /_next/static/immutable/
    // and load without ?dpl=<deploy>, so a chunk a deploy left unchanged
    // stays in browsers' caches. The edge serves them from the live release
    // and, for a day, from earlier ones. `next start` must agree with the
    // build (or pages it renders would ask for ?dpl URLs again).
    if (c.supportsImmutableAssets === undefined && c.experimental?.supportsImmutableAssets === undefined && atLeast(ctx.nextVersion, 16, 3)) {
      const on = ctx.phase === "phase-production-build" ? true : ctx.phase === "phase-production-server" ? builtImmutable(ctx.projectDir, c.distDir) : undefined;
      if (on !== undefined) c.supportsImmutableAssets = on;
    }
    // Optimized images live in a directory the box keeps across deploys.
    if (c.images && c.images.maximumDiskCacheSize === undefined) {
      c.images = { ...c.images, maximumDiskCacheSize: box.imageCacheBytes };
    }
    return c;
  },
};
