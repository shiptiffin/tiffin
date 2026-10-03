export default {
  project: "badauth",
  services: {
    auth: { methods: ["email", "sms", "Google"], organizations: "yes", sso: true },
  },
};
