export default {
  project: "shop",
  apps: {
    web: { routes: ["web", "Example.com", "old-shop.com"] },
    api: { routes: ["example.com/api"] },
  },
  domains: { "example.com": { www: "redirect", primary: true }, "old-shop.com": { www: "redirect" } },
};
