export default {
  project: "media",
  apps: {
    web: { disk: { data: "5GB", "renders/out": "2048MB", cache: "1GB", tmp: "500MB" }, timeoutSeconds: 7200 },
    same: { disk: { data: "1024MB" } },
  },
};
