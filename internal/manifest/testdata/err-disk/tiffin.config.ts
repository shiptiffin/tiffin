export default {
  project: "disks",
  apps: {
    site: { framework: "static", packages: ["ffmpeg"], disk: ["data"] },
    api: { disk: ["data", "data/db", "../up", "a/./b", "./x"] },
  },
};
