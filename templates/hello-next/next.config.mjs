import { fileURLToPath } from "node:url";

/** @type {import("next").NextConfig} */
export default {
  // Every instance shares one cache in Valkey (REDIS_URL), so revalidateTag
  // and revalidatePath reach all of them. See cache-handler.mjs.
  cacheHandler: fileURLToPath(new URL("./cache-handler.mjs", import.meta.url)),
  cacheMaxMemorySize: 0,
};
