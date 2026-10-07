import type { Config } from "@react-router/dev/config";

export default {
  // Pages render on the server; /about is also written to HTML at build time.
  ssr: true,
  prerender: ["/about"],
  // Every route's manifest goes out with the first page, so navigating
  // doesn't ask the server which route matches.
  routeDiscovery: { mode: "initial" },
} satisfies Config;
