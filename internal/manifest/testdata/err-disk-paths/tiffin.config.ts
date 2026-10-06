export default {
  project: "media",
  apps: {
    web: { disk: { "../up": "1GB", data: "2GB", "data/x": "1GB" } },
    site: { framework: "static", timeoutSeconds: 60 },
  },
};
