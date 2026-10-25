import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "shop",
  services: { analytics: { attribution: { signups: false, firstVisitCookie: "outside-eu" } } },
});
