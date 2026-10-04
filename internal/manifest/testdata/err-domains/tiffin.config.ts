export default {
  project: "shop",
  apps: { web: { routes: ["web", "example.com", "www.shop.test", "shop.test"] } },
  domains: {
    "nobody.example.com": {},
    "shop.test": { www: "redirect" },
    "www.example.com": { www: "redirect" },
  },
};
