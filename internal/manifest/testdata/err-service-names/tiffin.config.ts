import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "guesses",
  services: { postgres: {}, database: {}, redis: {}, jobs: {} },
});
