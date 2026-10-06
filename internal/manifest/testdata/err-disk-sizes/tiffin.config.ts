export default {
  project: "media",
  apps: {
    web: { disk: { data: "5gb", "../up": "1GB", big: "0GB" }, timeoutSeconds: 90000 },
    site: { framework: "static", timeoutSeconds: 60 },
  },
};
