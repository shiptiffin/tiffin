// The Tiffin box's Next.js adapter (Next.js 16.2+ loads it from
// NEXT_ADAPTER_PATH at build and at `next start`). Written into each build by
// the box; it only fills in what next.config leaves unset.
const box = /*BOX*/ {};
const here = (file) => new URL(file, import.meta.url).pathname;

export default {
  name: "tiffin",
  modifyConfig(config) {
    // The edge compresses responses. No X-Powered-By: Next.js (the config
    // arrives with defaults filled in, so an explicit true looks the same as
    // unset; an app that wants the header sets it with headers()).
    const c = { ...config, compress: false, poweredByHeader: false };
    // Pages and assets of this build carry its id: a browser on an older
    // release reloads instead of mixing builds.
    if (!c.deploymentId) c.deploymentId = box.deploymentId;
    if (box.cache) {
      // One cache in Valkey for every instance (tiffin-sdk/next).
      if (!c.cacheHandler) {
        c.cacheHandler = here("cache-handler.js");
        c.cacheMaxMemorySize = 0;
      }
      const handlers = { ...c.cacheHandlers };
      handlers.default ||= here("use-cache.js");
      handlers.remote ||= here("use-cache.js");
      c.cacheHandlers = handlers;
    }
    // forbidden() and unauthorized() (tiffin-sdk/next/auth) answer 403/401.
    if (c.experimental?.authInterrupts === undefined) {
      c.experimental = { ...c.experimental, authInterrupts: true };
    }
    // Optimized images live in a directory the box keeps across deploys.
    if (c.images && c.images.maximumDiskCacheSize === undefined) {
      c.images = { ...c.images, maximumDiskCacheSize: box.imageCacheBytes };
    }
    return c;
  },
};
