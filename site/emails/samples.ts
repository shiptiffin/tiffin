// Every email the website sends, with sample values: for the tests
// (lib/emails.test.ts) and for looking at them (render each to a file and
// open it in a browser, light and dark).
import { abuseMail, mails } from "../lib/cloud/emails";
import { stuckMail } from "../lib/cloud/outbox";
import { confirmEmail, ownerEmail } from "../lib/emails";

type Sample = { id: string; build: () => Promise<{ to: string; subject: string; text: string; html?: string }> };

const box = { email: "sam@example.com", name: "shop", id: "box_7f3a9c" };
const unnamed = { email: "sam@example.com", name: null };
const at = new Date("2026-10-08T14:32:00Z");

export const samples: Sample[] = [
  { id: "ready", build: () => mails.ready(box) },
  { id: "paid", build: () => mails.paid(box) },
  { id: "setup-failed", build: () => mails.setup_failed(box, "Hetzner answered 403: the token can't create servers in this project") },
  { id: "setup-failed-unnamed", build: () => mails.setup_failed(unnamed, "") },
  { id: "attention", build: () => mails.attention(box, "The certificate for dashboard.shop.shiptiffin.app hasn't come after two hours.") },
  { id: "server-off", build: () => mails.server_off(box) },
  { id: "duplicate-refunded", build: () => mails.duplicate_refunded(box) },
  { id: "refunded", build: () => mails.refunded(box) },
  { id: "extras-paused", build: () => mails.extras_paused(box, new Date("2026-11-07T00:00:00Z")) },
  { id: "extras-resumed", build: () => mails.extras_resumed(box) },
  { id: "payment-failed", build: () => mails.payment_failed(box) },
  { id: "dns-soon", build: () => mails.dns_soon(box, new Date("2026-11-07T00:00:00Z")) },
  { id: "dns-removed", build: () => mails.dns_removed(box) },
  { id: "down", build: () => mails.down(box, at) },
  { id: "up", build: () => mails.up(box) },
  { id: "silent", build: () => mails.silent(box, at) },
  { id: "silent-never", build: () => mails.silent(unnamed, null) },
  { id: "parked", build: () => mails.parked(box) },
  { id: "deleted", build: () => mails.deleted(box, false, "Subscription cancelled on 9 Oct · last charged $12 on 9 Oct") },
  { id: "deleted-with-data", build: () => mails.deleted(box, true, null) },
  { id: "killed", build: () => mails.killed(box, "Phishing page imitating a bank, reported by the bank's security team.") },
  {
    id: "signup-confirm",
    build: () =>
      confirmEmail("ada@example.com", "Ada Lovelace", {
        confirm: "https://shiptiffin.com/early-access/confirm?t=Zm9vYmFyYmF6cXV4cXV1eGNvcmdlZ3JhdWx0Z2FycGx5",
        remove: "https://shiptiffin.com/early-access/remove?t=Zm9vYmFyYmF6cXV4cXV1eGNvcmdlZ3JhdWx0Z2FycGx5",
        unsubscribe: "https://shiptiffin.com/api/early-access/remove?t=Zm9vYmFyYmF6cXV4cXV1eGNvcmdlZ3JhdWx0Z2FycGx5",
      }),
  },
  {
    id: "signup-owner",
    build: () =>
      ownerEmail("owner@example.com", {
        id: 42,
        email: "ada@example.com",
        name: "Ada Lovelace",
        role: null,
        hostFirst: "A small SaaS",
        projects: null,
        tools: [],
        spend: null,
        agents: [],
        github: "ada",
        x: null,
        linkedin: null,
        site: null,
        note: "Looking forward to it.",
        createdAt: at,
      } as never),
  },
  {
    id: "admin-stuck",
    build: () =>
      stuckMail("ops@example.com", { box_id: "box_7f3a9c" }, {
        kind: "stripe_cancel_refund",
        key: "duplicate:sub_2",
        subscription: "sub_2",
        since: at.toISOString(),
        attempts: 7,
        error: "Stripe answered 500",
      }),
  },
  { id: "admin-abuse", build: () => abuseMail("ops@example.com", { target: "shop.shiptiffin.app", box: "box_7f3a9c", from: null, details: "A login page asking for bank details.\nSeen at https://login.shop.shiptiffin.app" }) },
];
