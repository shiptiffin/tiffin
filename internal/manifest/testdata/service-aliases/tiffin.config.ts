import { defineConfig } from "tiffin-sdk";

// The dashboard's names (Database, Cache, Files) work too; they are stored as postgres, valkey and storage.
export default defineConfig({
  project: "aliases",
  apps: { web: { framework: "next" } },
  services: {
    database: { extensions: ["vector"] },
    cache: { maxMemoryMB: 32 },
    files: { buckets: { photos: {} } },
  },
});
