export default {
  project: "shop",
  apps: { web: { routes: ["web", "example.com"] } },
  domains: { "https://bad": {}, "Example.com": {}, "example.com": { www: "always" } },
};
