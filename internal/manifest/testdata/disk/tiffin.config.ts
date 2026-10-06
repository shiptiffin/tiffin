export default {
  project: "media",
  apps: {
    web: { packages: ["ffmpeg", "chromium"], disk: ["band-data", "data", ".podframes-renders", "public/clips"] },
  },
};
