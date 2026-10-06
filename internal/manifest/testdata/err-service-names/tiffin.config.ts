import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "guesses",
  services: { postgres: {}, database: {}, redis: {}, jobs: {} },
});
