export default {
  project: "shop",
  apps: {
    web: { routes: ["web", "Example.com"] },
    api: { routes: ["example.com/api"] },
  },
  domains: { "example.com": { www: "redirect" } },
};
