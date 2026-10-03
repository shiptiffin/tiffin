export default {
  project: "badtopics",
  apps: { jobs: { role: "worker" } },
  queues: { emails: { app: "jobs" } },
  topics: {
    "Order.Created": {},
    ".dots": {},
    "extra": { subscribers: ["emails"], filter: "x" },
    "ok.topic": { subscribers: [] },
    "wrong-type": { subscribers: "emails" },
  },
};
