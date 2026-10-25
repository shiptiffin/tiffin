import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "shop",
  services: { analytics: { attribution: { firstVisitCookie: "always" } } },
});
