import { defineConfig } from "tiffin-sdk";
import { appDefaults, routeFor } from "./lib/helpers";

export default defineConfig({
  project: "bundled",
  apps: {
    web: { ...appDefaults, routes: [routeFor("www")] },
    admin: { ...appDefaults, routes: [routeFor("admin")], instances: 2 },
  },
});
