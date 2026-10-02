export default {
  project: "jobs",
  apps: { worker: { role: "worker", routes: ["jobs", "jobs.example.com/x"] } },
};
